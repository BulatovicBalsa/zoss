# 1. Cargo Zip Bomb Disk Exhaustion

## 1. Uvod
Payment Adapter je Rust mikroservis koji koristi Cargo za upravljanje zavisnostima iz crates.io ili alternativnih registy-ja. Cargo komponenta toolchain-a je ključna za build i dependency resolution procese.

Ranjivost u Cargo komponenti omogućava disk exhaustion napad preko specijalno kreiranih paketa. To dovodi do DoS napada na mašinama.

U nastavku je definisana eksploatacija CVE-2022-36114 u kontekstu Payment Adapter-a i pokazano je zašto je ranjiva na jednostavne napade.

## 2. Definicija pretnje: Zip Bomb Disk Exhaustion

U kontekstu Payment Adapter-a, Zip Bomb Disk Exhaustion označava situaciju u kojoj:
- napadač upload-uje maliciozan crate na alternate registry
- developer pokrene ***cargo build*** ili ***cargo update*** koji skida paket
- Cargo ekstraktuje arhivu koja "eksplodira" u gigabajte podataka, iscrpljujući disk prostor

Zip Bomb je napad koji:
- ne zahteva autentifikaciju ili eskalaciju privilegija
- koristi legitimni Cargo workflow
- manifestuje se kao OOM (Out of memory) ili disk full, bez logova o malveru

## 3. Generički obrazac eksploatacije u Cargo-u
Napad se manifestuje kroz sledeći obrazac:
1. Napadač kreira zip bombu: mali tar.gz (kb) koji ekstraktuje više od 10GB fajlova (npr. rekurzivno duplirani fajlovi)
2. Objavljuje kao crate v1.0.0 na alternate registry
3. Modifikuje Cargo.toml Payment Adapter-
4. Pokretanje ***cargo build***

## 4. Početna konfiguracija Payment Adapter-a
Payment Adapter koristi standardni Cargo.toml sa zavisnostima puput ***async-stripe***, ***tokio***. Ako se doda nesigurna zavisnost:
```
[source.crates-io-proxy]
replace-with = 'vendored-sources'

[source.vendored-sources]
directory = 'vendor-sources'  # Napadač kontrolisani dir
```

## 5. Zašto je Cargo ranjiv?
Cargo ne limitira ekstrakciju iz tar.gz, dozvoljavajući "bomb" pakete. Pazment Adapter sa ograničenim diskom je idealna meta.

## 6. Mitigacija pretnje Zip Bomb-a
1. ***Inicijalna mitigacija***: Disk quota i safe registries
```
Uvedi disk quota u Docker CI (npr. --storage-opt size=5GB)
```
```
Koristi samo crates.io sa cargo audit
```
---
2. ***Dodatna mitigacija***: Vendor + tar extraction limits
Koristi ***cargo vendor*** sa custom patch-om za tar ekstrakciju:
```rust
use tar::Archive;
archive.unpack()?;  // Dodaj size check: if unpacked_size > 100MB { panic!() }
```
---
3. ***Glavna mitigacija***: Nadograditi na Rust 1.64+
## 7. Zaključak
Zip Bomb u Cargo-u predstavlja lak DoS napad za Rust projekte poput Payment Adapter-a, eksploatišući poverenje u registries.

