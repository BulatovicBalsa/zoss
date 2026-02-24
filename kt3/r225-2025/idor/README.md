# IDOR — Neovlašteni pristup i izmjena tuđih porudžbina

###### Danilo Cvijetić R225/2025

---

## 1. Uvod

Insecure Direct Object Reference (IDOR) je klasa ranjivosti u kojoj aplikacija koristi korisnički kontrolisani identifikator (ID resursa) za direktan pristup objektu u bazi podataka, bez provjere da li autentifikovani korisnik ima pravo na taj resurs.

U Ordering podsistemu, svaka porudžbina je identifikovana `order_id` vrijednošću (UUID v4). API endpointi za pregled, plaćanje, otkazivanje i iniciranje slanja prihvataju ovaj identifikator iz URL putanje. Ranjivost nastaje jer **nijedan od ovih handlera ne provjerava da li je `order.CustomerID` isti korisnik koji je autentifikovan** — provjera identiteta (autentifikacija) postoji, ali provjera vlasništva (autorizacija na nivou resursa) ne.

Posljedica: svaki autentifikovani korisnik platforme može pregledati ili modifikovati porudžbinu bilo kojeg drugog korisnika, uz uslov da zna ili pogodi njen `order_id`.

---

## 2. Definicija pretnje

### 2.1 STRIDE klasifikacija

| STRIDE kategorija | Primjenljivost | Obrazloženje |
|---|---|---|
| **Spoofing** | Ne | Napadač koristi vlastiti validan nalog — ne lažira identitet. |
| **Tampering** | Da | Napadač može otkazati, platiti ili inicirati slanje tuđe porudžbine bez vlasnikove saglasnosti. |
| **Repudiation** | Da | Akcije se bilježe pod napadačevim `order_id` zahtjevima, ali sistem ne biljezi da je pristupljeno tuđem resursu — nema kontekstualne anomalije u logu. |
| **Information Disclosure** | Da | Napadač može pročitati kompletne podatke tuđe porudžbine: ime kupca, adresu isporuke, stavke, iznos i status plaćanja. |
| **Denial of Service** | Djelimično | Masovno otkazivanje tuđih porudžbina narušava dostupnost servisa za legitimne korisnike. |
| **Elevation of Privilege** | Da | Napadač djeluje na resursima za koje nema autorizaciju — de facto eskalacija privilegija na nivou podataka. |

### 2.2 CWE referenca

- **CWE-639** — Authorization Bypass Through User-Controlled Key: aplikacija koristi `order_id` iz URL-a bez provjere vlasništva.
- **CWE-284** — Improper Access Control: nedostaje autorizacijska provjera na nivou resursa (object-level authorization).
- **CWE-862** — Missing Authorization: handler izvršava akciju nad resursom bez verifikacije da li je pozivalac ovlašten.

### 2.3 Opis pretnje

Razlika između autentifikacije i autorizacije na nivou resursa je ključna:

- **Autentifikacija** (ko si?) — potvrda identiteta korisnika putem JWT tokena. Prisutna je u sistemu.
- **Autorizacija na nivou resursa** (smiješ li pristupiti *ovom* objektu?) — provjera da `order.CustomerID == authenticatedUserID`. **Nedostaje** u ranjivoj implementaciji.

Ova klasa ranjivosti posebno je opasna jer je teška za automatsku detekciju: svaki individualni zahtjev je tehnički validan (ispravan token, ispravna sintaksa), samo logički neovlašten.

---

## 3. Afektovani resursi

### 3.1 Customer Data (PII) — POVJERLJIVOST

Svaka porudžbina sadrži `customer_id`, listu artikala, ukupni iznos i podatke o plaćanju. Napadač može enumeracijom prikupiti lične podatke kupaca (adresa isporuke, naručeni artikli, payment ID) za phishing ili social engineering napade.

**CIA**: Povjerljivost kompromitovana.

### 3.2 Ordering podaci — INTEGRITET

Napadač može otkazati (`PENDING_PAYMENT → CANCELLED`) ili inicirati slanje (`PAID → SHIPPING`) tuđe porudžbine bez znanja vlasnika. State machine tranzicija je ireverzibilna — otkazana porudžbina ne može biti reaktivovana kroz normalni tok.

**CIA**: Integritet kompromitovan.

### 3.3 Order status history — POVJERLJIVOST / INTEGRITET

`GET /orders/{orderID}/history` vraća kompletnu historiju stanja porudžbine sa timestamp-ovima i reason string-ovima. Ovi podaci mogu sadržati interne napomene ili informacije o payment provajderu koje ne bi trebale biti javno dostupne.

**CIA**: Povjerljivost i integritet kompromitovani.

### 3.4 Payment integritet — INTEGRITET

Napadač može pozvati `POST /orders/{orderID}/pay` na tuđoj porudžbini sa proizvoljnim `payment_id` vrijednošću, što uzrokuje lažnu tranziciju `PENDING_PAYMENT → PAID` bez stvarnog plaćanja. Ovo je najozbiljnija posljedica — direktni finansijski gubitak za platformu.

**CIA**: Integritet kompromitovan.

---

## 4. Ranjivi kod

### 4.1 Ranjivi handler — `GetOrder`

```go
// demo/handlers.go — ranjiva implementacija
func (h *Handlers) GetOrder(w http.ResponseWriter, r *http.Request) {
    orderID := chi.URLParam(r, "orderID")

    // RANJIVOST: order se dohvata bez provjere vlasnistva.
    // Bilo koji autentifikovani korisnik moze proslijediti
    // tuđi order_id i dobiti kompletne podatke porudzbine.
    order, err := h.store.GetOrder(r.Context(), orderID)
    if err != nil {
        if errors.Is(err, ErrOrderNotFound) {
            writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "order not found"})
            return
        }
        writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to get order"})
        return
    }

    // Nema provjere: order.CustomerID == authenticatedUserID
    writeJSON(w, http.StatusOK, order)
}
```

Isti problem postoji u `CancelOrder`, `PayOrder`, `ShipOrder` i `GetOrderHistory` handlerima — nijedan ne provjerava vlasništvo.

### 4.2 Ranjivi handler — `CancelOrder`

```go
// demo/handlers.go — ranjiva implementacija
func (h *Handlers) CancelOrder(w http.ResponseWriter, r *http.Request) {
    orderID := chi.URLParam(r, "orderID")

    var req CancelOrderRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
        return
    }

    reason := req.Reason
    if reason == "" {
        reason = "cancelled by customer"
    }

    // RANJIVOST: state machine tranzicija se izvrsava bez
    // provjere da li je orderID u vlasnistvu pozivaca.
    // Napadac moze otkazati tuđu porudzbinu samo poznavajuci njen UUID.
    err := h.sm.Transition(r.Context(), orderID, StatusCancelled, reason)
    // ...
}
```

---

## 5. Model napada

### 5.1 Akter napada

**Autentifikovani korisnik platforme** — napadač koji posjeduje validan korisnički nalog i može generisati HTTP zahtjeve sa ispravnim Bearer tokenom. Napadač ne mora imati posebne privilegije — ranjivost je dostupna svim registrovanim korisnicima.

### 5.2 Preduslovi

- Napadač posjeduje validan korisnički nalog i JWT token
- Napadač zna ili može pogoditi `order_id` ciljne porudžbine
  - UUID v4 čini brute-force nepraktičnim (`~3.4×10³⁸` mogućih vrijednosti), ali `order_id` se tipično eksponira putem: email potvrde narudžbe, referral linkova, customer support transkripata, URL-a u browseru ili logova dijeljenih na zahtjev korisnika
- API Gateway ne implementira autorizaciju na nivou resursa (oslanja se samo na autentifikaciju)
- Ordering servis ne provjerava `order.CustomerID == authenticatedUserID` u handlerima

### 5.3 Tok napada — Scenario A: Neovlašteni pristup podacima

```
1. Napadac se autentifikuje i dobija JWT token
   POST /auth/login  →  { "token": "eyJ..." }

2. Napadac saznaje order_id ciljne porudzbine
   (putem dijeljenog linka, emaila, customer support interakcije)
   order_id = "a3f2c1d0-7e8b-4f9a-b2c3-1d4e5f6a7b8c"

3. Napadac salje GET zahtjev sa tuđim order_id
   GET /orders/a3f2c1d0-7e8b-4f9a-b2c3-1d4e5f6a7b8c
   Authorization: Bearer eyJ...   ← napadacev token

4. Servis vraca kompletne podatke porudzbine bez provjere vlasnistva:
   {
     "order_id": "a3f2c1d0-...",
     "customer_id": "victim-user-id",
     "status": "PENDING_PAYMENT",
     "items": [...],
     "total": 249.99,
     "payment_id": ""
   }

5. Napadac prikuplja PII zrtve i koristi ga za phishing
```

### 5.4 Tok napada — Scenario B: Neovlašteno otkazivanje tuđe porudžbine

```
1. Napadac salje POST /orders/{victim-order-id}/cancel
   Authorization: Bearer eyJ...   ← napadacev token
   Body: { "reason": "I changed my mind" }

2. Servis izvrsava state machine tranziciju bez provjere vlasnistva:
   PENDING_PAYMENT → CANCELLED

3. Zrtva pokusava platiti svoju porudzbinu:
   POST /orders/{victim-order-id}/pay
   → HTTP 409: "transition PENDING_PAYMENT→PAID not allowed from CANCELLED"

4. Porudzbina je trajno otkazana — zrtva mora kreirati novu
```

### 5.5 Tok napada — Scenario C: Lažna uplata tuđe porudžbine

```
1. Napadac pronalazi order_id porudzbine u stanju PENDING_PAYMENT

2. Salje POST /orders/{victim-order-id}/pay bez stvarnog placanja:
   Authorization: Bearer eyJ...   ← napadacev token
   Body: { "payment_id": "FAKE-PAYMENT-12345" }

3. Servis izvrsava tranziciju PENDING_PAYMENT → PAID
   i upisuje lazni payment_id u bazu

4. Napadac zatim inicira slanje:
   POST /orders/{victim-order-id}/ship

5. Roba je poslana, platforma trpi finansijski gubitak jer
   stvarna uplata nikad nije izvrsena
```

---

## 6. Mitigacija

### 6.1 Primarna mitigacija — Ownership check u svakom handleru

Svaki handler koji pristupa ili modifikuje porudžbinu mora provjeriti da je autentifikovani korisnik vlasnik resursa. Pretpostavlja se da JWT middleware ubacuje `customer_id` u context zahtjeva:

```go
// demo/handlers.go — mitigirana implementacija GetOrder
func (h *Handlers) GetOrder(w http.ResponseWriter, r *http.Request) {
    orderID := chi.URLParam(r, "orderID")

    order, err := h.store.GetOrder(r.Context(), orderID)
    if err != nil {
        if errors.Is(err, ErrOrderNotFound) {
            writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "order not found"})
            return
        }
        writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to get order"})
        return
    }

    // MITIGACIJA: provjera vlasnistva
    authenticatedUserID, _ := r.Context().Value(ctxKeyUserID).(string)
    if order.CustomerID != authenticatedUserID {
        // HTTP 403, a ne 404 — napadac ne smije znati da resurs postoji
        writeJSON(w, http.StatusForbidden, ErrorResponse{Error: "access denied"})
        return
    }

    writeJSON(w, http.StatusOK, order)
}
```

Ista provjera se dodaje u sve handlere koji pristupaju porudžbini:

```go
// Pomocna funkcija za DRY ownership provjeru
func (h *Handlers) getOrderWithOwnerCheck(
    ctx context.Context,
    orderID string,
    authenticatedUserID string,
) (*Order, error) {
    order, err := h.store.GetOrder(ctx, orderID)
    if err != nil {
        return nil, err
    }
    if order.CustomerID != authenticatedUserID {
        return nil, ErrForbidden
    }
    return order, nil
}
```

### 6.2 UUID v4 kao `order_id`

Demo aplikacija već koristi UUID v4 (`github.com/google/uuid`) što čini brute-force enumeraciju nepraktičnom. Ovo je neophodan, ali **ne dovoljan** uslov:

- UUID v4 sprečava sekvencijalnu enumeraciju
- Ne sprečava napad kada napadač legitimno sazna tuđi UUID (email, link, support)
- Ownership check je **uvijek neophodan** bez obzira na format ID-a

### 6.3 Rate limiting na GET endpoint

Čak i uz UUID v4, rate limiting na `GET /orders/{orderID}` sprečava automatizovane pokušaje pogađanja:

```go
// Primjer chi rate limiter middleware-a
r.With(rateLimiter(60, time.Minute)).Get("/orders/{orderID}", h.GetOrder)
```

### 6.4 Audit logging za ownership mismatch

Svaki pokušaj pristupa tuđem resursu treba biti logovan i praćen:

```go
if order.CustomerID != authenticatedUserID {
    log.Printf("[SECURITY] IDOR attempt: user=%s tried to access order=%s owned by=%s ip=%s",
        authenticatedUserID, orderID, order.CustomerID,
        r.RemoteAddr,
    )
    // Alert ako isti korisnik pokusa pristupiti >10 tuđih resursa u 5 minuta
    writeJSON(w, http.StatusForbidden, ErrorResponse{Error: "access denied"})
    return
}
```

### 6.5 Preporuka: admin rola za operativne akcije

Akcije poput `ShipOrder` i `PayOrder` ne bi trebale biti dostupne krajnjim korisnicima u produkciji — to su privilegovane operacije rezervisane za interne servise (Payment servis, Shipping servis). Razdvajanje API-ja na korisničke i interne endpointe uklanja napadnu površinu za ove scenarije.

### 6.6 Sažetak mitigacionih mjera

| Mjera | Efikasnost | Implementaciona složenost |
|---|---|---|
| Ownership check u svim handlerima | Potpuna eliminacija IDOR ranjivosti | Niska |
| UUID v4 `order_id` format | Smanjuje brute-force rizik | Trivijalna (već implementovano) |
| Rate limiting na GET endpointima | Ograničava enumeraciju | Niska |
| Audit logging za mismatch | Detekcija i forenzika | Niska |
| Razdvajanje korisničkog i internog API-ja | Uklanja napadnu površinu | Srednja |

---

## 7. Zaključak

IDOR na Ordering podsistemu nastaje zbog fundamentalne greške u dizajnu: sistem razlikuje autentifikaciju od autorizacije na nivou objekta, ali implementira samo prvu. Ranjivost je tihog karaktera — ne generiše greške, ne uzrokuje padove sistema, i teška je za automatsku detekciju jer su svi zahtjevi tehnički ispravni.

Posljedice su ozbiljne: od curenja PII podataka i narušavanja privatnosti korisnika, do neovlaštenog otkazivanja ili lažnog plaćanja porudžbina s direktnim finansijskim implikacijama. Primarna mitigacija je jednostavna i ne zahtijeva arhitekturalne izmjene: dodavanje jedne provjere (`order.CustomerID != authenticatedUserID → HTTP 403`) u svaki handler koji pristupa porudžbini.