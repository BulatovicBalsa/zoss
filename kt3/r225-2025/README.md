# KT3 — Bezbjednosna analiza Ordering i Shipping & Logistics podsistema

###### Danilo Cvijetić R225/2025

---

## Pregled

Ovaj direktorijum sadrži bezbjednosnu analizu i demonstraciju dva napada na Temu marketplace platformu:

| # | Podsistem | Napad | Folder |
|---|-----------|-------|--------|
| 1 | **Ordering** | Race Condition na State Machine (non-owner-aware Redis lock) | [`ordering/`](ordering/) |
| 2 | **Shipping & Logistics** | Webhook Signature Bypass (JSON canonicalization flaw) | [`shipping/`](shipping/) |

Zajednička demo aplikacija se nalazi u [`demo/`](demo/) folderu.

---

## Arhitektura demo aplikacije

Demo aplikacija je Go servis koji implementira Ordering state machine sa Redis distribuiranim lock-om i Shipping webhook endpoint-om sa HMAC verifikacijom. Koristi Cassandru za perzistenciju i Redis za distribuirano zaključavanje.

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

**Mitigacija**: Raw-byte HMAC verifikacija nad kompletnim payload-om + konstantno-vremensko poređenje. Detalji u [`shipping/README.md`](shipping/README.md).

---

## Ostali napadi na Ordering podsistem

U nastavku su teorijski opisani ostali napadi koji se mogu izvršiti na Ordering podsistem, zajedno sa odgovarajućim mitigacijama.

### 1. Price Manipulation — TOCTOU (Time-of-Check Time-of-Use)

**Pretnja**: Napadač manipuliše cijenama artikala između trenutka kada su cijene prikazane u korpi i trenutka kada se kreira porudžbina.

**Tok napada**: Kupac dodaje artikal u korpu po cijeni od 100 EUR. U trenutku checkout-a, Ordering servis dohvata aktuelne cijene iz Catalog servisa. Ako napadač ima pristup Catalog servisu (npr. kompromitovan prodavac ili insider threat), može promijeniti cijenu artikla na 1 EUR između prikaza i checkout-a. Ordering servis koristi novu cijenu, pa kupac plaća znatno manje nego što bi trebao.

**Afektovani resursi**: Ordering podaci (integritet), Payment transakcije (integritet). Finansijski gubitak za prodavce ili platformu.

**Mitigacija**:
- **Price Snapshot pri checkout-u** — Ordering servis kreira immutable snapshot cijena u trenutku checkout-a (tabela `order_item_snapshots`). Sve dalje operacije koriste snapshot cijene, ne aktuelne.
- **Price deviation check** — Ako se cijena promijenila za više od konfigurisanog praga (npr. 5%) između prikaza i checkout-a, sistem traži ponovnu potvrdu od kupca.
- **Write-once semantika** — Snapshot je immutable nakon kreiranja; naknadne izmjene cijena u katalogu ne utiču na već kreirane porudžbine.

---

### 2. Kafka Event Replay / Injection

**Pretnja**: Napadač ponovo šalje `PaymentSucceeded` Kafka event za istu porudžbinu, pokušavajući da dobije dupli fulfillment (dva puta isporuku za jedno plaćanje).

**Tok napada**: Napadač koji ima pristup Kafka klasteru (kompromitovan broker, insider threat, ili man-in-the-middle na internoj mreži) hvata `PaymentSucceeded` event i ponovo ga šalje na topic. Ordering servis prima event i ponovo pokreće shipping proces.

**Afektovani resursi**: Ordering podaci (integritet — dupli fulfillment), Shipment Data (integritet — kreirana dupla pošiljka), Payment podaci (integritet — jedna uplata, dva slanja).

**Mitigacija**:
- **Idempotency check** — Svaki Kafka event nosi jedinstveni `event_id`. Ordering servis čuva `event_id` u Redis deduplication cache-u (`SET dedup:{event_id} 1 NX EX 3600`). Ako event sa istim ID-em stigne ponovo, preskače se.
- **State machine zaštita** — State machine ne dozvoljava tranziciju `PAID → PAID`, pa čak i ako deduplication promaši, tranzicija se odbija.
- **Kafka ACL** — Ograničiti ko može pisati na payment topic-e. Samo Payment servis smije producirati `PaymentSucceeded` evente.
- **Event potpis** — Payment servis potpisuje svaki event HMAC-om. Ordering servis verifikuje potpis prije obrade.

---

### 3. IDOR (Insecure Direct Object Reference) na porudžbinama

**Pretnja**: Napadač pristupa ili modificira porudžbine drugih korisnika pogađanjem ili enumeracijom `order_id` vrijednosti.

**Tok napada**: Napadač šalje `GET /orders/{orderID}` ili `POST /orders/{orderID}/cancel` sa tuđim `order_id`. Ako servis ne provjerava da li `order_id` pripada autentifikovanom korisniku, napadač može vidjeti podatke o tuđim porudžbinama (adresa isporuke, stavke, iznos) ili otkazati tuđu porudžbinu.

**Afektovani resursi**: Customer Data (poverljivost — PII curenje), Ordering podaci (integritet — neautorizovana modifikacija).

**Mitigacija**:
- **Ownership check** — Svaki handler provjerava da `order.customer_id == authenticated_user_id`. Ako ne, vraća HTTP 403 Forbidden.
- **UUID format** — Korišćenje UUID-a (v4, random) kao `order_id` čini brute-force enumeraciju nepraktičnom (128-bit prostor).
- **Rate limiting** — Ograničenje broja GET zahtjeva po korisniku sprečava masovnu enumeraciju.
- **Audit logging** — Logovanje svih pristupa sa neslaganjem ownership-a radi detekcije pokušaja IDOR napada.

---

### 4. Denial of Service — Mass Order Creation

**Pretnja**: Napadač kreira ogroman broj porudžbina radi preopterećenja sistema (Cassandra, Redis, Kafka).

**Tok napada**: Automatizovanom skriptom, napadač šalje hiljade `POST /orders` zahtjeva u kratkom periodu. Svaki zahtjev rezultira upisom u Cassandru, alokacijom Redis checkout cache-a, i potencijalno Kafka eventom. Sistem postaje spor ili nedostupan za legitimne korisnike.

**Afektovani resursi**: Ordering podaci (dostupnost), Cassandra klaster (dostupnost — storage i compaction preopterećenje), Redis (dostupnost — memorija).

**Mitigacija**:
- **Rate limiting po korisniku** — Redis sliding window rate limiter ograničava broj kreiranih porudžbina po korisniku (npr. max 10 porudžbina po minutu).
- **Rate limiting po IP adresi** — API Gateway nivo zaštite od distribuiranih napada.
- **CAPTCHA na checkout** — Za korisnike koji prekorače normalne obrasce ponašanja.
- **Resource quotas** — Ograničenje ukupnog broja `PENDING_PAYMENT` porudžbina po korisniku (npr. max 5 istovremeno).

---

### 5. Cassandra CQL Injection

**Pretnja**: Napadač ubacuje zlonamjerne CQL fragmente kroz korisnički kontrolisane parametre (npr. `reason` polje u cancel zahtjevu).

**Tok napada**: Napadač šalje cancel zahtjev sa `reason` poljem koje sadrži CQL fragment, npr.: `reason: "test'; DROP TABLE ordering.orders; --"`. Ako servis koristi string interpolaciju za pravljenje CQL upita, napad može obrisati tabelu ili izmijeniti podatke.

**Afektovani resursi**: Ordering podaci (integritet i dostupnost — brisanje ili izmjena podataka), Cassandra (integritet šeme).

**Mitigacija**:
- **Prepared statements** — Go aplikacija koristi parametrizovane upite (`?` placeholder-e) za sve CQL operacije. GoCQL driver automatski eskejpuje parametre, čime se CQL injection potpuno eliminiše.
- **Input validation** — Validacija dužine i dozvoljenih karaktera za string polja na handler nivou.
- **Cassandra RBAC** — Aplikacioni korisnik nema `DROP` privilegiju na produkcijskim tabelama.
