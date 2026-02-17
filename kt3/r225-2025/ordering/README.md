# KT3 — Race Condition napad na Ordering State Machine

###### Danilo Cvijetić R225/2025

---

## 1. Uvod

Ordering sistem predstavlja centralnu komponentu Temu marketplace platforme koja upravlja životnim ciklusom porudžbina. Svaka porudžbina prolazi kroz niz stanja definisanih konačnim automatom (state machine):

```
PENDING_PAYMENT → PAID → SHIPPING → DELIVERED
       ↓                     ↓
   CANCELLED            SHIP_FAILED
```

Tranzicije stanja se pokreću iz dva izvora:
- **HTTP zahtjevi korisnika** — npr. kupac otkazuje porudžbinu (`PENDING_PAYMENT → CANCELLED`)
- **Asinhroni događaji** — npr. Payment servis javlja uspješno plaćanje putem Kafka eventa (`PENDING_PAYMENT → PAID`)

Originalna implementacija koristi Redis distribuirani lock baziran na `SETNX` i `DEL` operacijama. Međutim, ovakav pristup ne garantuje vlasništvo nad lock-om, jer Redis ne razlikuje klijente koji su ga postavili. U slučaju isteka TTL-a (slučaj nepredviđeno duge obrade) i preuzimanja lock-a od strane druge instance servisa, originalni vlasnik može nenamjerno obrisati novi lock pozivom `DEL` operacije. Ova situacija dovodi do paralelnog izvršavanja checkout operacija nad istom narudžbinom.

Ovaj dokument opisuje ranjivost non-owner-aware Redis lock-a, demonstrira eksploataciju putem konkurentnih zahtjeva, i prikazuje mitigaciju uvođenjem owner-aware lock mehanizma.

---

## 2. Definicija pretnje

### 2.1 STRIDE klasifikacija

| STRIDE kategorija | Primjenljivost | Obrazloženje |
|---|---|---|
| **Tampering** |  Da | Napadač manipuliše stanjem porudžbine, dovodeći sistem u nekonzistentno stanje. |
| **Elevation of Privilege** |  Da | Napadač postiže ishod koji ne bi smio biti moguć (npr. otkazana porudžbina za koju je plaćanje izvršeno). |
| **Repudiation** |  Da | Status historija bilježi dvije tranzicije iz istog izvornog stanja, što je nemoguća sekvenca u ispravnom sistemu. Forenzika ne može pouzdano utvrditi koji zahtjev je bio "pravi". |
| **Information Disclosure** |  Ne | Napad ne dovodi do curenja podataka. |
| **Denial of Service** |  Ne | Sistem nastavlja da funkcioniše — problem je u integritetu, ne u dostupnosti. |
| **Spoofing** |  Ne | Napadač koristi sopstveni legitimni nalog. |

### 2.2 CWE referenca

- **CWE-362: Concurrent Execution using Shared Resource with Improper Synchronization ('Race Condition')**
- **CWE-367: Time-of-Check Time-of-Use (TOCTOU) Race Condition**

### 2.3 Opis pretnje

Ordering servis koristi Redis `SETNX` (Set if Not eXists) sa TTL-om kao distribuirani lock za serijalizaciju tranzicija stanja. Međutim, lock vrijednost je statički string `"1"` — Redis ne može razlikovati koji klijent je postavio lock.

Kada lock TTL istekne dok je holder još uvijek u fazi obrade (processing), drugi klijent može zauzeti lock putem `SETNX`. Originalni holder tada nenamjerno briše NOVI lock pozivom bezuslovne `DEL` operacije, jer `DEL` ne provjerava vlasništvo. Ovo poništava garanciju međusobnog isključivanja (mutual exclusion) i omogućava paralelne tranzicije stanja na istoj porudžbini.

Ključni problem: **lock vrijednost ne nosi informaciju o vlasništvu**, pa klijent ne može detektovati da je lock koji pokušava obrisati zapravo tuđi.

---

## 3. Afektovani resursi


### 3.1 Ordering podaci — INTEGRITET

Primarni afektovani resurs. Race condition direktno narušava integritet podataka o porudžbini:

- **Status porudžbine** postaje nekonzistentan sa stvarnim stanjem poslovnog procesa. Porudžbina može istovremeno biti i "plaćena" (Payment servis dobio potvrdu) i "otkazana" (kupac dobio potvrdu otkazivanja), dok baza čuva samo jedan od ta dva statusa — koji zavisi od toga čiji upis je stigao posljednji.
- **Status istorije** bilježi nemoguću sekvencu: dva zapisa sa istim izvornim stanjem (`PENDING_PAYMENT → PAID` i `PENDING_PAYMENT → CANCELLED`), što ukazuje da je state machine prekršen.
- **Životni ciklus porudžbine** postaje nepredvidiv — dalji procesi (shipping, refund) se mogu pokrenuti na osnovu pogrešnog stanja.

**CIA triada**: Integritet je kompromitovan. Dostupnost nije ugrožena. Poverljivost nije afektovana.

### 3.2 Payment transakcioni podaci — INTEGRITET

Sekundarni afektovani resurs. Ako Payment servis primi potvrdu da je tranzicija `PENDING_PAYMENT → PAID` uspjela (HTTP 200), izvršiće capture sredstava sa platne kartice kupca. Međutim, ako je istovremeno i cancel zahtjev uspjeo, nastaje situacija u kojoj:

- Novac je naplaćen kupcu
- Porudžbina prikazuje status `CANCELLED`
- Automatski refund se ne pokreće jer sistem ne prepoznaje nekonzistentnost

**Poslovni uticaj**: Finansijski gubitak za kupca i potencijalna regulatorna neusaglašenost (PCI DSS zahtijeva integritet transakcija).

### 3.3 Audit logovi — NEPORICANJE

Status historija (tabela `order_status_history`) bilježi obje tranzicije kao uspješne. Sa stanovišta forenzike, sistem prikazuje nemoguću putanju. Dva prelaza iz istog izvornog stanja narušavaju pouzdanost audit trail-a i otežavaju dispute resolution.

### 3.4 Customer Data — INTEGRITET

Kupac dobija HTTP 200 odgovor na cancel zahtjev sa porukom "order cancelled", ali porudžbina može završiti u stanju `PAID`. Kupac vidi pogrešan status ili dobija neočekivanu isporuku. Ovo narušava povjerenje u platformu i može rezultirati disputima i chargeback-ovima.

---

## 4. Model napada

### 4.1 Akter napada

Napadač je **autentifikovani kupac** koji:

- Posjeduje legitimni korisnički nalog
- Ima aktivnu porudžbinu u stanju `PENDING_PAYMENT`
- Razumije da Redis lock nije owner-aware i da se TTL može iskoristiti

Napadač ne mora eskalirati privilegije niti zaobilaziti autentifikaciju. Sve akcije prolaze standardne bezbjednosne kontrole (JWT validacija, RBAC provjera).

### 4.2 Preduslovi

- Ordering servis koristi Redis lock sa `SETNX` + `DEL` bez provjere vlasništva
- `DEL` operacija je bezuslovna — briše lock bez obzira na to ko ga posjeduje
- Processing delay je varijabilan (random 0 do `MAX_PROCESSING_DELAY_MS`) — kada premaši `LOCK_TTL_MS`, lock ističe tokom obrade

### 4.3 Tok napada

Svaki zahtjev dobija random processing delay (0 do `MAX_PROCESSING_DELAY_MS`), čime se simuliraju realni uslovi u produkciji. Kada delay premaši `LOCK_TTL_MS`, lock istekne tokom obrade i race condition postaje moguć:

![Sekvencni dijagram napada](attack-sequence.png)

Kada je delay < TTL, prvi zahtjev završi i oslobodi lock prije isteka, pa drugi zahtjev pročita ažurirano stanje i bude odbijen — race condition se ne dešava. Zato je ishod napada nedeterministički.

---

## 5. Ranjiva arhitektura

### 5.1 Ranjivi kod — `state.go`

Ključna ranjivost je u `Transition()` metodi. Lock koristi statičku vrijednost `"1"` i bezuslovnu `DEL` operaciju:

```go
func (sm *StateMachine) Transition(ctx context.Context, orderID, targetState, reason string) error {
    lockKey := fmt.Sprintf("order_lock:%s", orderID)

    // SETNX sa statičkom vrijednošću "1" — nema informacije o vlasništvu
    acquired, err := sm.rdb.SetNX(ctx, lockKey, "1", sm.lockTTL).Result()
    // ...

    // RANJIVO: bezuslovni DEL — briše BILO ČIJI lock
    defer func() {
        sm.rdb.Del(ctx, lockKey)
    }()

    // Random processing delay — ponekad premaši lock TTL
    delay := time.Duration(rand.Int63n(int64(sm.maxProcessingDelay)))
    time.Sleep(delay)

    // Upis bez provjere da li još uvijek posjedujemo lock
    err = sm.store.UpdateOrderStatus(ctx, orderID, targetState, reason)
    // ...
}
```

Problemi:

1. **Lock vrijednost je statička (`"1"`)** — klijent ne može razlikovati svoj lock od tuđeg.
2. **`DEL` je bezuslovan** — kada lock istekne i drugi klijent ga preuzme, originalni klijent briše tuđi lock.
3. **Nema provjere vlasništva prije upisa** — upis se izvršava čak i ako je lock istekao.

---

## 6. Demonstracija napada

```
chmod +x attack.sh
./attack.sh http://localhost:8080 20
```

Skripta za svaki pokušaj kreira porudžbinu, šalje istovremeni PAY i CANCEL, i provjerava ishod. Zbog random delay-a, ishod varira:

- `RACE` — oba zahtjeva vratila 200 (delay > TTL, lock istekao, oba pisala u bazu)
- `OK` — samo jedan uspio (delay < TTL, lock držao do kraja, drugi pročitao ažurirano stanje)

```
=== Race Condition Attack ===
Target:   http://localhost:8080
Attempts: 20

  #01  OK     pay=409 cancel=200 final=CANCELLED
  #02  RACE   pay=200 cancel=200 final=PAID
  #03  OK     pay=409 cancel=200 final=CANCELLED
  #04  OK     pay=200 cancel=409 final=PAID
  #05  OK     pay=200 cancel=409 final=PAID
  #06  OK     pay=409 cancel=200 final=CANCELLED
  #07  OK     pay=200 cancel=409 final=PAID
  #08  RACE   pay=200 cancel=200 final=PAID
  #09  RACE   pay=200 cancel=200 final=CANCELLED
  #10  RACE   pay=200 cancel=200 final=CANCELLED
  ...

--- Results ---
  Total:  20
  RACE:   7  (both succeeded — vulnerable!)
  OK:     13  (one succeeded, one rejected)
  SAFE:   0  (both rejected — fail-safe)
  ERRORS: 0

VULNERABLE: 7/20 resulted in race condition.
```

Svaki `RACE` predstavlja narušavanje state machine-a — oba zahtjeva su dobila HTTP 200, a konačno stanje zavisi od last-write-wins. Istorija takve porudžbine bilježi dva zapisa (`PENDING_PAYMENT → PAID` i `PENDING_PAYMENT → CANCELLED`), što je nemoguća sekvenca u ispravnom sistemu.

---

## 7. Mitigacija

Mitigacija se postiže uvođenjem **owner-aware lock mehanizma**. Patch se nalazi u `fix.patch` i mijenja isključivo `state.go`. Tri ključne promjene:

**1. UUID kao lock vrijednost** — umjesto statičkog `"1"`, svaki zahtjev čuva `uuid.New().String()` kao lock vrijednost. Ovo omogućava identifikaciju vlasnika.

**2. Lua skripta za release** — umjesto bezuslovnog `DEL`, lock se otpušta atomskom Lua skriptom koja provjerava da li je lock vrijednost još uvijek naša prije brisanja:

```lua
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("DEL", KEYS[1])
end
return 0
```

**3. Ownership check prije upisa** — prije pisanja u Cassandru, `GET` lock i provjera da li je vrijednost jednaka našem UUID-u. Ako nije (lock istekao ili ga preuzeo drugi klijent), tranzicija se odbija sa `ErrLockExpired`.

### Primjena i revert

```bash
# primjena
patch -p0 < fix.patch
docker compose build ordering-service --no-cache
docker compose up -d ordering-service

# revert
patch -R -p0 < fix.patch
docker compose build ordering-service --no-cache
docker compose up -d ordering-service
```

---

## 8. Demonstracija mitigacije

```bash
./attack.sh http://localhost:8080 20
```

Očekivani rezultat — mješavina `OK` (delay < TTL, jedan uspije) i `SAFE` (delay > TTL, oba odbijena). Ne postoji race ishod (`RACE`):

```
=== Race Condition Attack ===
Target:   http://localhost:8080
Attempts: 20

  #01  SAFE   pay=409 cancel=409 final=PENDING_PAYMENT
  #02  OK     pay=200 cancel=409 final=PAID
  #03  OK     pay=409 cancel=200 final=CANCELLED
  #04  OK     pay=409 cancel=200 final=CANCELLED
  #05  SAFE   pay=409 cancel=409 final=PENDING_PAYMENT
  #06  SAFE   pay=409 cancel=409 final=PENDING_PAYMENT
  #07  OK     pay=200 cancel=409 final=PAID
  #08  OK     pay=200 cancel=409 final=PAID
  #09  OK     pay=409 cancel=200 final=CANCELLED
  #10  OK     pay=200 cancel=409 final=PAID
  ...

--- Results ---
  Total:  20
  RACE:   0  (both succeeded — vulnerable!)
  OK:     16  (one succeeded, one rejected)
  SAFE:   4  (both rejected — fail-safe)
  ERRORS: 0

NO RACE CONDITIONS DETECTED.
```

`SAFE` ishodi se dešavaju kada oba zahtjeva dobiju delay > TTL — oba detektuju gubitak vlasništva i odbiju upis. Ovo je ispravno fail-safe ponašanje.
