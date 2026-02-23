### Analiza bezbjednosnih ranjivosti Rust komponenti

###### Teodor Vidaković, R213/2025

---

## Pregled

Ovaj repozitorijum sadrži istraživanje i analizu **5 bezbjednosnih ranjivosti** u Rust bibliotekama i komponentama standardnog toolchain-a, sprovedenu u kontekstu **Payment Adapter** mikroservisa. Za svaku ranjivost definisana je pretnja, model napada, opis eksploatacije i koraci mitigacije.

Dvije ranjivosti demonstrirane su praktičnim napadom sa funkcionalnim PoC-om.

---

## Ranjivosti

### 1. `smallvec` - Memory Corruption (Double-Free / Heap Buffer Overflow)
**CVE**: RUSTSEC-2019-0012 | **CVSS**: 9.8 CRITICAL

`SmallVec::grow()` u verzijama `< 0.6.10` sadrži logičku grešku pri realokaciji interne memorije koja uzrokuje **double-free** i **heap buffer overflow**. Memorijska korupcija detektovana Valgrind alatom.

[Dokumentacija](Documentation/Rust%20smallvec%20memory%20corruption.md) | [PoC](PoCs/smallvec-poc/)

---

### 2. `yaml-rust` - Uncontrolled Recursion (Stack Overflow DoS)
**CVE**: RUSTSEC-2018-0006 / CVE-2018-20993 | **CVSS**: HIGH

`yaml-rust < 0.4.1` implementira rekurzivni descent parser bez ograničenja dubine rekurzije. Duboko ugniježdeni YAML dokument (npr. `{a: {a: {a: ...}}}` sa 100 000 nivoa) prekoračuje call stack i terminira proces putem `abort()` bez mogućnosti oporavka. Napadački payload je veličine samo ~600 KB.

[Dokumentacija](Documentation/Uncontrolled%20Recursion%20DoS%20Yaml-Rust%20Attack.md) | [PoC](PoCs/yaml-poc/)

---

### 3. `rust-openssl` - Use-After-Free u kriptografskom FFI bindingu
**CVE**: RUSTSEC-2025-0022 / CVE-2025-3416 | **CVSS**: 3.7 LOW

`rust-openssl < 0.10.72` ne osigurava ispravan životni vijek `CString` objekta pri prelasku FFI granice prema OpenSSL C biblioteci u funkcijama `Md::fetch()` i `Cipher::fetch()`. Rust dealocira `CString` dok OpenSSL još drži pokazivač na tu memoriju, uzrokujući Use-After-Free koji tiho ignorira proslijeđene kriptografske parametre; potencijalni webhook HMAC bypass.

[Dokumentacija](Documentation/Rustup%20OpenSSL%20Use-After-Free.md)

---

### 4. `cargo` - Zip Bomb Disk Exhaustion
**CVE**: CVE-2022-36114 | **CVSS**: 6.5 MEDIUM

Cargo upravljač zavisnostima u verzijama `< Rust 1.64` ne ograničava količinu podataka pri ekstrakciji `.crate` arhiva. Napadač koji može objaviti paket na alternativni registar može kreirati zip bombu (~1 MB kompresovano, 10+ GB expandovano) koja iscrpljuje disk prostor build servera, blokirajući sve naredne `cargo build` operacije i deployment Payment Adapter-a.

[Dokumentacija](Documentation/Cargo%20Zip%20Bomb.md)

---

### 5. `std::process::Command` - Command Injection na Windows-u
**CVE**: CVE-2024-24576 | **CVSS**: 10.0 CRITICAL

Rust standardna biblioteka u verzijama `< 1.77.2` ne provodi ispravno escaping argumenata pri pozivu Windows batch fajlova (`.bat`, `.cmd`). API garantuje da argumenti neće biti evaluirani od strane shell-a, ali ta garancija se ne ispunjava zbog specifičnog ponašanja `cmd.exe` escapinga. Napadač koji kontroliše argumente može injektovati proizvoljne shell komande i postići **Remote Code Execution** sa privilegijama Payment Adapter procesa.

[Dokumentacija](Documentation/Command%20Injection%20attack.md)