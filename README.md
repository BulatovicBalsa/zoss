# KT1 – Konceptualni i arhitekturni opis (Temu-like marketplace)

## 1) Domen problema i poslovni kontekst

### 1.1. Kratak opis domena
Sistem predstavlja **online marketplace platformu** (Temu-like) koja omogućava da **prodavci (Sellers)** objavljuju proizvode, a **kupci (Customers)** pretražuju katalog, naručuju proizvode, plaćaju i prate isporuku. Pored toga, platforma obuhvata operativne funkcije administracije, podrške korisnicima i integracije sa eksternim provajderima plaćanja i isporuke.

### 1.2. Učesnici (akteri) i njihove uloge
- **Customers (Kupci)**  
  Pretražuju proizvode, upravljaju korpom, poručuju, plaćaju, prate pošiljke, podnose reklamacije/vrate robu, komuniciraju sa podrškom.
- **Sellers (Prodavci)**  
  Onboarduju se na platformu, upravljaju katalogom i cenama, prate porudžbine, ažuriraju dostupnost zaliha, obrađuju povrate i sporne slučajeve.
- **Administrators (Administratori)**  
  Upravljaju pravilima platforme (moderacija ponuda, blokade naloga, pravila isporuke/povrata), upravljaju konfiguracijom integracija, pregledaju audit logove i izveštaje.
- **C-Support (Customer Support / Operateri podrške)**  
  Obrađuju tikete, reklamacije, upite kupaca i prodavaca, eskalacije, preglede statusa narudžbina i isporuka.
- **Payment Provider (Eksterni platni provajder)**  
  Autorizacija/naplata, refundacije, chargeback procesi.
- **Logistics & Shipping Provider (Eksterni logistički provajder)**  
  Kreiranje pošiljki, tracking, statusi isporuke, povratnice.

### 1.3. Poslovni procesi koje softver podržava (high-level)
1. **Registracija i autentifikacija korisnika** (kupci, prodavci, admin/support) i upravljanje sesijama.
2. **Onboarding prodavca**: verifikacija, kreiranje profila prodavca, podešavanje isporuke/povrata.
3. **Upravljanje katalogom proizvoda**: unos/izmena proizvoda, kategorije, slike, varijante, cene, akcije.
4. **Pretraga i pregled proizvoda**: listanje kataloga, filtriranje, preporuke, detalji proizvoda.
5. **Kreiranje porudžbine**: korpa, obračun cene, dostupnost zaliha, rezervacija zaliha.
6. **Plaćanje**: iniciranje plaćanja preko payment adaptera, potvrda naplate, refundacije.
7. **Isporuka i logistika**: kreiranje pošiljki, tracking, promene statusa, povrat.
8. **Post-prodajni procesi**: reklamacije, povrati, sporovi, podrška (ticketing).
9. **Administracija i nadzor**: moderacija ponuda i naloga, izveštavanje, audit, upravljanje integracijama.

---

## 2) Arhitektura zamišljenog softvera

### 2.1. Arhitekturalne karakteristike
Sistem je projektovan kao **mikroservisna, event-driven** arhitektura sa jasnim razdvajanjem domena:
- **WebAPP sloj** (klijentske aplikacije) za kupce, prodavce, administratore/podršku.
- **Domen-servisi** (npr. Catalog, Ordering, Shipping/Logistics, Customer/Seller internal).
- **Identity & Access** servis (Auth).
- **Integracioni adapteri** za eksterno plaćanje i logistiku.
- **Poliglotna perzistencija** (više tipova skladišta po domenu: relacione, NoSQL, pretraga, cache, objekat skladište).
- **Asinhrona obrada** kritičnih tokova preko message broker-a (npr. statusi pošiljki, promene zaliha, notifikacije, refundacije).

Ovakav pristup omogućava:
- skaliranje po domenu (npr. Catalog i Search skaliraju nezavisno od Ordering),
- izolaciju integracija (payment/logistics) kroz adaptere,
- jasnije bezbednosne granice.

### 2.2. Predložene tehnologije (minimum “interesantnih” komponenti)
U nastavku su tehnologije i njihova uloga (primer jedne realistične kombinacije):

**A) Klijentske aplikacije**
- **Customer WebAPP (Next.js / React, TypeScript)**  
  UI za kupce; SSR/CSR za performanse i SEO za pretragu proizvoda.
- **Seller WebAPP (React, TypeScript)**  
  UI za prodavce; unos kataloga, porudžbine, zalihe.
- **Admin & Support WebAPP (Angular ili React, TypeScript)**  
  UI za admin i support; rad sa tiketima, moderacija, pregledi naloga i porudžbina.

**B) Backend servisi (primer poliglotnog pristupa)**
- **Auth Service (Keycloak ili custom OIDC servis; Java/Kotlin ili Go)**  
  Centralizovana autentifikacija/autorizacija (OIDC/OAuth2), izdavanje tokena, MFA, RBAC.
- **Product Catalog Service (Go + gRPC/REST)**  
  Upravljanje katalogom; pogodna platforma za visoki throughput i jednostavno skaliranje.
- **Ordering Service (Go ili Java/Kotlin)**  
  Porudžbine, korpa, obračun, orkestracija toka narudžbine.
- **Shipping & Logistics Service (Go)**  
  Integracija sa eksternim logističkim provajderima; asinhrono rukovanje statusima pošiljki.
- **Customer Internal Service / Seller Internal Service (Go)**  
  Domen logika za kupce/prodavce, profili, preferencije, onboarding, podešavanja.
- **Payment Adapter (Rust)**  
  Izolovan adapter ka payment provajderu (autorizacija, capture, refund, chargeback), fokus na sigurnost (memory safety) i jasne granice.

**C) Skladišta i infrastruktura**
- **MongoDB (Catalog/Offer data)**  
  Fleksibilna šema za proizvode/varijante, brze izmene strukture podataka.
- **PostgreSQL (Orders/Transactions, gde je potrebno ACID)**  
  Za porudžbine i finansijske evidencije (stroga konzistentnost).
- **Redis (Cache + rate limiting + session pomoćni sloj)**  
  Ubrzava čitanja kataloga i štiti servise (rate limiting).
- **Elasticsearch (Search indeks)**  
  Full-text pretraga, filtriranje i rangiranje proizvoda.
- **Kafka (event bus)**  
  Event-driven tokovi (OrderCreated, PaymentConfirmed, ShipmentStatusUpdated, InventoryAdjusted).
- **Object Storage (S3 kompatibilno) za slike i dokumente**  
  Slike proizvoda, prilozi za reklamacije, dokumenti verifikacije prodavca.
- **API Gateway (npr. Kong / Envoy)**  
  Jedinstvena ulazna tačka, throttling, WAF integracija, routing.
- **Observability (Prometheus + Grafana + OpenTelemetry)**  
  Metrike, tracing, detekcija anomalija, audit.

> Ovim se ispunjava uslov “interesantnijih” tehnologija: Go/Rust, MongoDB, Kafka, Elasticsearch, Redis, S3, Keycloak, itd. (nisu sve klasične web app + SQL).

---

## 3) Grupe slučajeva korišćenja (Use-case groups)

### 3.1. Customer (Kupac) – Customer WebAPP + Customer Internal Service + Ordering
- **Pretraga i pregled kataloga**: pretraga, filtriranje, preporuke, detalji proizvoda.
- **Korpa i checkout**: dodavanje/uklanjanje artikala, obračun ukupne cene, unos adrese, izbor isporuke.
- **Narudžbina i praćenje**: kreiranje narudžbine, pregled statusa, tracking pošiljke.
- **Plaćanje i refundacije**: iniciranje plaćanja, potvrda, pregled transakcija, refund (kroz podršku/automatizovano).
- **Podrška i reklamacije**: otvaranje tiketa, povrat robe.

### 3.2. Seller (Prodavac) – Seller WebAPP + Seller Internal Service + Catalog/Ordering
- **Onboarding i profil prodavca**: registracija, verifikacija, podešavanja isporuke/povrata.
- **Upravljanje katalogom**: kreiranje i izmena proizvoda, slike, varijante, cene, promocije.
- **Zalihe i dostupnost**: ažuriranje zaliha, rezervacije, sinhronizacija sa Inventory Data.
- **Obrada porudžbina**: prihvat, priprema, statusi, povrati, komunikacija sa podrškom.

### 3.3. Admin & Support – Admin & Support WebAPP
- **Moderacija i kontrola sadržaja**: pregled/listing ponuda, uklanjanje zabranjenog sadržaja, sankcije.
- **Upravljanje korisnicima i ulogama**: blokade, reset, prava, pregled prijava.
- **Rad sa tiketima i eskalacijama**: reklamacije, sporovi, refund workflow.
- **Nadzor i audit**: pregled logova, izveštaji, detekcija zloupotreba, pregled integracija.

### 3.4. Sistemske integracije – Payment/Logistics
- **Payment orkestracija**: authorize/capture/refund/chargeback, idempotency, stanje transakcije.
- **Logistics orkestracija**: kreiranje shipment-a, label, tracking, status update eventi.

---

## 4) Osetljivi resursi (Sensitive assets) i bezbednosni ciljevi

> Cilj: imati dovoljno resursa za kasnije modelovanje pretnji. Lista ispod sadrži više od minimuma.

### 4.1. Resursi vezani za identitet i pristup
1. **Auth Data (nalozi, lozinke hash, MFA tajne, recovery tokeni)**  
   - Ciljevi: *Poverljivost* (nema curenja), *Integritet* (nema neovlašćene izmene), *Dostupnost* (login ne sme padati).
2. **Access/Refresh tokeni i sesije korisnika**  
   - Ciljevi: poverljivost, integritet (sprečiti hijacking/replay), dostupnost.
1. **RBAC politika i admin privilegije**  
   - Ciljevi: integritet (nema eskalacije privilegija), auditabilnost.

### 4.2. Lični podaci i podaci o kupcima/prodavcima
4. **Customer Data (PII: ime, adresa, telefon, istorija porudžbina, preferencije)**  
   - Ciljevi: poverljivost i minimizacija; integritet (tačne adrese/isporuke); dostupnost.  
   - Regulativa: **GDPR** (ako posluje sa EU korisnicima) i/ili lokalni zakoni o zaštiti podataka.
5. **Seller Data (PII + poslovni podaci: bankovni račun za isplatu, poreski/identifikacioni podaci, dokumenti verifikacije)**  
   - Ciljevi: poverljivost, integritet, audit; posebno zaštititi dokumente verifikacije.

### 4.3. Plaćanja i finansije (regulatorno najkritičnije)
6. **Payment transakcioni podaci + tokeni kartica (ako se čuvaju) / payment reference IDs**  
   - Ciljevi: poverljivost i integritet (sprečiti preusmeravanje uplata/refund), dostupnost (naplata).  
   - Standard: **PCI DSS** direktno nameće bezbednosne zahteve za rad sa cardholder data (idealno: ne čuvati PAN, koristiti tokenizaciju).
7. **Refund/chargeback evidencije i odluke**  
   - Ciljevi: integritet (sprečiti lažne refundacije), audit (neporicanje), dostupnost.

### 4.4. Porudžbine, logistika i integritet poslovnih tokova
8. **Ordering podaci (Order state machine: kreiranje, plaćanje, isporuka, otkazivanje)**  
   - Ciljevi: integritet (sprečiti manipulaciju statusima), dostupnost (SLA), neporicanje (audit trail).
9. **Shipping/tracking podaci (statusi pošiljke, brojevi pošiljke, adrese isporuke)**  
   - Ciljevi: integritet (lažni statusi), poverljivost (curenje adresa), dostupnost.

### 4.5. Katalog, zalihe i anti-fraud
10. **Inventory Data (stanje zaliha, rezervacije, skladišta)**  
   - Ciljevi: integritet (sprečiti “oversell” i manipulaciju), dostupnost.
11. **Product Catalog (ponude, cene, promocije, kuponi)**  
   - Ciljevi: integritet (sprečiti neovlašćene promene cena/kupona), dostupnost (pretraga), reputacioni rizik.
12. **Fraud signals / risk scoring / pravila**  
   - Ciljevi: poverljivost (da napadač ne nauči pravila), integritet (sprečiti sabotiranje), dostupnost.

### 4.6. Operativa i evidencije
13. **Customer Support tiketi i chat logovi (često sadrže PII i dispute detalje)**  
   - Ciljevi: poverljivost (PII), integritet (tačan trag), audit.
14. **Audit logovi admin/support aktivnosti**  
   - Ciljevi: integritet (tamper-proof), dostupnost, neporicanje.
15. **Secrets/Keys/Certificates (API ključevi ka payment/logistics, DB kredencijali, signing keys)**  
   - Ciljevi: poverljivost (najkritičnije), integritet (sprečiti zamenu ključeva), dostupnost (rotacija bez prekida).
