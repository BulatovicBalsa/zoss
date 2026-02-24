# KT3 — Bezbjednosna analiza Ordering i Shipping & Logistics podsistema

###### Danilo Cvijetić R225/2025

---

## Pregled

Ovaj direktorijum sadrži bezbjednosnu analizu i demonstraciju tri napada na Temu marketplace platformu:

| # | Podsistem | Napad | Folder |
|---|-----------|-------|--------|
| 1 | **Ordering** | Race Condition na State Machine (non-owner-aware Redis lock) | [`ordering/`](ordering/) |
| 2 | **Shipping & Logistics** | Webhook Signature Bypass (JSON canonicalization flaw) | [`shipping/`](shipping/) |
| 3 | **Shipping & Logistics (v2)** | Protobuf JSON DoS (CVE-2024-24786, `protojson.Unmarshal`) | [`shipping-v2-protobuf/`](shipping-v2-protobuf/) |

Zajednička demo aplikacija se nalazi u [`demo/`](demo/) folderu.

---

## Arhitektura demo aplikacije

Demo aplikacija je Go servis koji implementira Ordering state machine sa Redis distribuiranim lock-om i Shipping webhook endpoint-om sa HMAC verifikacijom. Koristi Cassandru za perzistenciju i Redis za distribuirano zaključavanje.

U praksi Ordering i Shipping bi bili odvojeni servisi, ali su ovde spojeni zbog jednostavnosti demonstracije napada. Arhitektura je pojednostavljena i fokusirana na ključne komponente relevantne za napade.

![Arhitektura demo aplikacije](demo/architecture.png)

### Pokretanje

```bash
cd demo
docker compose up --build -d
```

Servis je dostupan na `http://localhost:8080`.

### API endpointi

| Metod | Endpoint | Opis |
|-------|----------|------|
| `POST` | `/orders` | Kreiranje porudžbine |
| `GET` | `/orders/{orderID}` | Pregled porudžbine |
| `POST` | `/orders/{orderID}/pay` | Plaćanje (PENDING_PAYMENT → PAID) |
| `POST` | `/orders/{orderID}/cancel` | Otkazivanje (PENDING_PAYMENT → CANCELLED) |
| `POST` | `/orders/{orderID}/ship` | Iniciranje slanja (PAID → SHIPPING) |
| `GET` | `/orders/{orderID}/history` | Istorija statusa |
| `POST` | `/webhooks/shipping` | Webhook za status pošiljke |
| `POST` | `/webhooks/shipping/v2` | Webhook v2 (protobuf-json envelope) |
| `GET` | `/health` | Health check |

---

## Napad 1: Race Condition na Ordering State Machine

**Folder**: [`ordering/`](ordering/)

**Ranjivost**: Non-owner-aware Redis lock koristi statičku vrijednost i bezuslovnu `DEL` operaciju. Kada lock TTL istekne tokom obrade, drugi klijent preuzima lock, a originalni klijent briše tuđi lock pozivom `DEL`, omogućavajući paralelne tranzicije stanja.

**Demonstracija**:
```bash
cd ordering
chmod +x attack.sh
./attack.sh http://localhost:8080 20
```
Demo video nije postavljen zbog nedeterminističke prirode napada.

**Mitigacija**: Owner-aware lock sa UUID vrijednošću i Lua skriptom za atomski release. Detalji u [`ordering/README.md`](ordering/README.md).

---

## Napad 2: Webhook Signature Bypass na Shipping podsistem

**Folder**: [`shipping/`](shipping/)

**Ranjivost**: Webhook HMAC verifikacija koristi JSON deserializaciju u struct koji ne sadrži `status` polje, pa re-serializacija proizvodi kanonički JSON bez tog polja. HMAC se izračunava nad nepotpunim podacima, omogućavajući napadaču da promijeni `status` (npr. iz `IN_TRANSIT` u `LOST`) bez invalidacije potpisa, čime pokreće neovlašteni refund.

**Demonstracija**:
```bash
cd shipping
chmod +x attack.sh
./attack.sh http://localhost:8080
```
Demo video je dostupan: [`shipping/webhook_attack.mp4`](shipping/webhook_attack.mp4).

**Mitigacija**: Raw-byte HMAC verifikacija nad kompletnim payload-om + konstantno-vremensko poređenje. Detalji u [`shipping/README.md`](shipping/README.md).

---

## Napad 3: Protobuf JSON DoS na Shipping v2 webhook (CVE-2024-24786)

**Folder**: [`shipping-v2-protobuf/`](shipping-v2-protobuf/)

**Ranjivost**: Interni JSON dekoder u `google.golang.org/protobuf` (`protojson.Unmarshal`) u verzijama `< 1.33.0` sadrži bug u obradi malformed JSON-a: payload `{"":}` (objekat sa praznim ključem i nedostajućom vrijednošću) izaziva beskonačnu petlju u `skipJSONValue()` funkciji kada je `DiscardUnknown=true`. Svaki napadački zahtjev trajno zarobljava goroutinu na 100% CPU — 5 konkurentnih zahtjeva zasićuje servis na ~600% CPU. Endpoint `/webhooks/shipping/v2` koristi Protobuf JSON (`google.protobuf.Any` envelope) za standardizovanu integraciju sa eksternim logističkim provajderima, a upravo `DiscardUnknown` mehanizam (ključan za kompatibilnost sa raznolikim provajderima) aktivira ranjivu granu izvrsavanja.

**Demonstracija**:
```bash
cd shipping-v2-protobuf
chmod +x attack.sh
./attack.sh http://localhost:8080
```
Demo video je dostupan: [`shipping-v2-protobuf/protobuff_attack.mp4`](shipping-v2-protobuf/protobuff_attack.mp4).

**Mitigacija**: Upgrade protobuf biblioteke na `v1.33.0+` (fix u dekoderu i `skipJSONValue()`) i defense-in-depth postavljanje `DiscardUnknown=false`. Detalji u [`shipping-v2-protobuf/README.md`](shipping-v2-protobuf/README.md).

---

## Teorijski napadi (bez demo)

| # | Podsistem / Komponenta | Napad | Folder |
|---|------------------------|-------|--------|
| A | **Ordering** (Cassandra 4.0.x) | Remote Code Execution putem User Defined Functions (CVE-2021-44521) | [`cassandra-udf-rce/`](cassandra-udf-rce/) |
| B | **Ordering** (Redis — Debian paket) | Lua Sandbox Escape — RCE (CVE-2022-0543) | [`redis-lua-rce/`](redis-lua-rce/) |
| C | **Ordering** | IDOR — Neovlašteni pristup i izmjena tuđih porudžbina | [`idor/`](idor/) |

---

### Napad A: CVE-2021-44521 — Cassandra UDF Remote Code Execution

**Folder**: [`cassandra-udf-rce/`](cassandra-udf-rce/)

**Ranjive verzije**: Apache Cassandra 3.0.x < 3.0.26, 3.11.x < 3.11.12, 4.0.x < 4.0.2

**Ranjivost**: Kada je `enable_user_defined_functions_threads: false` u `cassandra.yaml`, Cassandra izvršava UDF-ove u glavnom JVM threadu bez aktivnog Java SecurityManager-a. Napadač sa `CREATE FUNCTION` privilegijom može kreirati UDF koji poziva `Runtime.getRuntime().exec()` i izvršiti proizvoljne OS komande na serveru baze podataka. U kontekstu Ordering podsistema, kompromitovanje Cassandre znači potpunu kompromitaciju svih porudžbina, istorije stanja i korisničkih podataka.

**Mitigacija**: Upgrade na Cassandra 4.0.2+ / 3.11.12+, postavljanje `enable_user_defined_functions: false` ako UDF-ovi nisu potrebni, i restrikcija `CREATE FUNCTION` privilegija na aplikacioni korisnik. Detalji u [`cassandra-udf-rce/README.md`](cassandra-udf-rce/README.md).

---

### Napad B: CVE-2022-0543 — Redis Lua Sandbox Escape

**Folder**: [`redis-lua-rce/`](redis-lua-rce/)

**Ranjive verzije**: Redis kao Debian/Ubuntu sistemski paket (`apt-get install redis-server`):
- Debian 10 Buster: < `5:5.0.14-1+deb10u4`
- Debian 11 Bullseye: < `5:6.0.16-1+deb11u2`

**Ranjivost**: Debian/Ubuntu maintaineri kompajliraju Redis sa dinamičkim linkovanjem sistemske `liblua` biblioteke, koja uključuje `package` modul koji upstream Redis namjerno izostavlja. Redis sandbox ne blokira `package.loadlib()`, pa napadač može učitati libc i dobiti pristup `io.popen()` → RCE na Redis hostu jednom `EVAL` komandom. Ranjivost je posebno relevantna jer mitigirani owner-aware lock (Napad 1) koristi upravo `EVALSHA` Lua skriptu — isti mehanizam koji CVE-2022-0543 eksploatiše.

**Mitigacija**: Koristiti upstream Redis (zvanični Docker image `redis:7-alpine` ili binary sa redis.io) umjesto Debian paketa. Detalji u [`redis-lua-rce/README.md`](redis-lua-rce/README.md).

---

### Napad C: IDOR — Neovlašteni pristup tuđim porudžbinama

**Folder**: [`idor/`](idor/)

**Ranjivost**: `GET /orders/{orderID}` i ostali endpointi (`/pay`, `/cancel`, `/ship`, `/history`) ne provjeravaju da li autentifikovani korisnik posjeduje traženu porudžbinu. Sistem razlikuje autentifikaciju od autorizacije na nivou resursa, ali implementira samo prvu. Napadač sa validnim tokenom može pristupiti ili modifikovati porudžbine bilo kojeg drugog korisnika uz poznavanje UUID-a, koji se tipično eksponira putem email potvrde, referral linka ili customer support kanala.

**Mitigacija**: Ownership check u svakom handleru (`order.CustomerID != authenticatedUserID → HTTP 403`), rate limiting na GET endpointima i audit logging za mismatch događaje. Detalji u [`idor/README.md`](idor/README.md).

---

## Ostali napadi na Ordering i Shipping podsisteme

U nastavku su teorijski opisani ostali napadi koji se mogu izvršiti na definisani sistem, zajedno sa odgovarajućim mitigacijama.

### 1. Price Manipulation — TOCTOU (Time-of-Check Time-of-Use)

**Pretnja**: Napadač manipuliše cijenama artikala između trenutka kada su cijene prikazane u korpi i trenutka kada se kreira porudžbina.

**CWE referenca**: CWE-367 (Time-of-Check Time-of-Use Race Condition)

**STRIDE**: Tampering, Elevation of Privilege

**Akter napada**: Kompromitovani prodavac (seller) sa pristupom Catalog servisu, ili insider threat sa write pristupom na Catalog API/bazu.

**Preduslovi**:
- Ordering servis dohvata aktuelne cijene iz Catalog servisa u realnom vremenu prilikom checkout-a (nema lokalni snapshot cijena)
- Napadač ima mogućnost izmjene cijena artikala u Catalog servisu (seller portal, kompromitovan API ključ, ili direktan pristup bazi)
- Ne postoji mehanizam za poređenje prikazane cijene sa checkout cijenom (price deviation check)
- Vremenski prozor između prikaza cijene u korpi i izvršenja checkout-a je dovoljno velik (tipično 1–30 sekundi) da napadač može izvršiti izmjenu
- Catalog servis nema rate limiting ili audit trail na promjene cijena koji bi detektovao brze oscilacije

**Tok napada**:
1. Kupac (ili napadačev saučesnik) dodaje artikal u korpu — Catalog servis vraća cijenu od 100 EUR
2. Frontend prikazuje artikal po cijeni od 100 EUR u korpi
3. Napadač (kompromitovani prodavac) mijenja cijenu artikla na 1 EUR putem Catalog API-ja
4. Kupac klikne "Checkout" — Ordering servis šalje `GET /catalog/products/{id}/price` ka Catalog servisu
5. Catalog servis vraća novu cijenu od 1 EUR
6. Ordering servis kreira porudžbinu sa cijenom od 1 EUR — kupac plaća 99 EUR manje
7. Napadač vraća cijenu na 100 EUR — sljedeći kupci vide normalnu cijenu

**Afektovani resursi**: Ordering podaci (integritet), Payment transakcije (integritet). Finansijski gubitak za prodavce ili platformu.

**Mitigacija**:
- **Price Snapshot pri checkout-u** — Ordering servis kreira immutable snapshot cijena u trenutku checkout-a (tabela `order_item_snapshots`). Sve dalje operacije koriste snapshot cijene, ne aktuelne.
- **Price deviation check** — Ako se cijena promijenila za više od konfigurisanog praga (npr. 5%) između prikaza i checkout-a, sistem traži ponovnu potvrdu od kupca.
- **Write-once semantika** — Snapshot je immutable nakon kreiranja; naknadne izmjene cijena u katalogu ne utiču na već kreirane porudžbine.
- **Audit trail na Catalog servisu** — Logovanje svih promjena cijena sa timestamp-om, seller ID-em i IP adresom. Detekcija brzih oscilacija cijena (npr. promjena > 50% u roku od 1 minute) sa automatskim alertom.

---

### 2. Kafka Event Replay / Injection

**Pretnja**: Napadač ponovo šalje `PaymentSucceeded` Kafka event za istu porudžbinu, pokušavajući da dobije dupli fulfillment (dva puta isporuku za jedno plaćanje).

**CWE referenca**: CWE-294 (Authentication Bypass by Capture-replay), CWE-345 (Insufficient Verification of Data Authenticity)

**STRIDE**: Tampering, Spoofing, Elevation of Privilege

**Akter napada**: Insider threat sa pristupom internoj mreži (DevOps, SRE, kompromitovani zaposleni), ili eksterni napadač koji je ostvario pristup Kafka klasteru putem kompromitovanih kredencijala ili mrežnog pivotiranja.

**Preduslovi**:
- Napadač ima mrežni pristup Kafka klasteru (broker-i su dostupni sa napadačeve pozicije — ista mreža, VPN, ili kompromitovan bastion host)
- Kafka klaster nema konfigurisane ACL-ove (Access Control Lists) ili su postavljeni previše permisivno — napadač može slati poruke na `payment.events` topic
- Kafka eventi nemaju kriptografski potpis (HMAC ili digitalni potpis) koji bi omogućio verifikaciju pošiljaoca
- Ordering servis ne implementira idempotency provjeru (deduplication) za `event_id` polje Kafka evenata
- Napadač može presresti ili pročitati postojeće Kafka evente (npr. putem `kafka-console-consumer`, kompromitovanog monitoring alata poput Kafka UI, ili log aggregation sistema)
- TLS između Kafka brokera i klijenata nije konfigurisan ili su certifikati kompromitovani

**Tok napada**:
1. Kupac kreira porudžbinu i izvrši plaćanje — Payment servis salje `PaymentSucceeded` event na Kafka topic `payment.events`
2. Ordering servis konzumira event, izvršava tranziciju `PENDING_PAYMENT → PAID`, i pokreće shipping proces
3. Napadač presretne originalni `PaymentSucceeded` event (koristeći `kafka-console-consumer --topic payment.events --from-beginning` ili sličan alat)
4. Napadač ponovo salje isti event na `payment.events` topic koristeći `kafka-console-producer` ili Kafka client biblioteku
5. Ordering servis prima duplirani event — bez idempotency provjere, ponovo pokreće shipping proces
6. Rezultat: dva fulfillment procesa za jedno plaćanje — dupla pošiljka, finansijski gubitak

**Afektovani resursi**: Ordering podaci (integritet — dupli fulfillment), Shipment Data (integritet — kreirana dupla pošiljka), Payment podaci (integritet — jedna uplata, dva slanja).

**Mitigacija**:
- **Idempotency check** — Svaki Kafka event nosi jedinstveni `event_id`. Ordering servis čuva `event_id` u Redis deduplication cache-u (`SET dedup:{event_id} 1 NX EX 3600`). Ako event sa istim ID-em stigne ponovo, preskače se.
- **State machine zaštita** — State machine ne dozvoljava tranziciju `PAID → PAID`, pa čak i ako deduplication promaši, tranzicija se odbija.
- **Kafka ACL** — Ograničiti ko može pisati na payment topic-e. Samo Payment servis smije slati `PaymentSucceeded` evente. Konfiguracija: `kafka-acls --add --allow-principal User:payment-service --producer --topic payment.events`.
- **Event potpis** — Payment servis potpisuje svaki event HMAC-om. Ordering servis verifikuje potpis prije obrade.
- **mTLS između Kafka klijenata i brokera** — Obostrana TLS autentifikacija sprečava neautorizovane klijente da se povežu na klaster.

---

### 3. Denial of Service — Mass Order Creation

**Pretnja**: Napadač kreira ogroman broj porudžbina radi preopterećenja sistema (Cassandra, Redis, Kafka).

**CWE referenca**: CWE-770 (Allocation of Resources Without Limits or Throttling), CWE-400 (Uncontrolled Resource Consumption)

**STRIDE**: Denial of Service

**Akter napada**: Eksterni napadač (anonimni ili autentifikovani korisnik) sa pristupom javnom API-ju. Može koristiti botnet, distribuirane proxy-je ili skripte za generisanje zahtjeva.

**Preduslovi**:
- `POST /orders` endpoint nema rate limiting po korisniku ili po IP adresi
- Ne postoji ograničenje na broj istovremenih `PENDING_PAYMENT` porudžbina po korisniku (resource quota)
- API Gateway ne implementira request throttling ili burst limiting
- Napadač ima mrežni pristup API-ju (javno dostupan endpoint)
- Autentifikacija je prisutna ali ne sprečava masovno kreiranje — napadač može registrovati više naloga ili koristiti kompromitovane kredencijale
- Ne postoji CAPTCHA ili proof-of-work mehanizam na checkout flow-u
- Cassandra klaster nema konfigurisane per-table storage quotas ili compaction throttling

**Tok napada**:
1. Napadač registruje jedan ili više korisničkih naloga na platformi
2. Koristeći automatizovanu skriptu (npr. `for i in $(seq 1 100000); do curl -X POST /orders ...`), napadač šalje hiljade `POST /orders` zahtjeva po sekundi
3. Svaki zahtjev rezultira: upisom reda u `ordering.orders` i `ordering.order_status_history` tabele u Cassandri, alokacijom Redis cache-a za checkout sesiju, i Kafka eventom `OrderCreated`
4. Cassandra SSTable-ovi rastu, compaction troši CPU i disk I/O — latencija čitanja raste
5. Redis memorija se puni checkout sesijama — `maxmemory-policy allkeys-lru` počinje evictovati legitimne cache zapise
6. Kafka topic se puni `OrderCreated` eventima — downstream servisi (inventory, notification) su preopterećeni
7. Legitimni korisnici doživljavaju timeout-e na checkout-u, spora učitavanja porudžbina, i neuspjela plaćanja

**Afektovani resursi**: Ordering podaci (dostupnost), Cassandra klaster (dostupnost — storage i compaction preopterećenje), Redis (dostupnost — memorija), Kafka (dostupnost — topic preopterećenje). Cassandra ne podrzava JOIN i UNION statemente, pa zbog toga je broj ranjivih resursa ogranicen.

**Mitigacija**:
- **Rate limiting po korisniku** — Redis sliding window rate limiter ograničava broj kreiranih porudžbina po korisniku (npr. max 10 porudžbina po minutu). Implementacija: `INCR rate:{user_id}:{minute}` sa `EXPIRE 60`.
- **Rate limiting po IP adresi** — API Gateway nivo zaštite od distribuiranih napada (npr. nginx `limit_req_zone`, AWS WAF rate-based rules).
- **CAPTCHA na checkout** — Za korisnike koji prekorače normalne obrasce ponašanja. Progressive CAPTCHA: prvi checkout bez CAPTCHA, nakon 5. u roku od sat vremena — reCAPTCHA v3.
- **Resource quotas** — Ograničenje ukupnog broja `PENDING_PAYMENT` porudžbina po korisniku (npr. max 5 istovremeno). Provjera: `SELECT COUNT(*) FROM ordering.orders WHERE customer_id = ? AND status = 'PENDING_PAYMENT'` prije kreiranja.
