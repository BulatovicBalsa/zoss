# Rustup OpenSSL Use-After-Free
## 1. Uvod
Rustup je toolchain menadžer za instalaciju rustc, cargo i komponenti u Payment Adapteru. Sadrži rust-openssl crate za crypto operacije.

CVE-2025-3416 dozvoljava use-after-free u ***Md::fetch*** i ***Cipher::fetch***, dovodeći do crypto bypass-a u webhook  verifikaciji.

Ovo ugrožava integritet Payment Adaptera jer maliciozni webhook-ovi mogu zaobići signature provere.

## 2. Definicija pretnje: 
U kontekstu Payment Adaptera:
- Malformed properties argument u openssl pozivu oslobađa memoriju
- Rust bindings pristupaju oslobođenoj memoriji
- Webhook HMAC validacija fail-uje, dozvoljavajući lažne payment_intent.succeeded evente

***Rezultat***: lažni payment eventi

## Zašto ovo nije klasičan bezbedonosni napad?

Use-after-free je vrsta memory corruption napada koja sama po sebi ne omogućava izvršavanje proizvoljnog koda (RCE), već uzrokuje neispravno ponašanje programa.

U kriptografskom kontekstu (kao što je OpenSSL FFI), to vodi do logičkih grešaka poput bypass-a provera (npr. invalid hostname prolazi validaciju jer bindings čita prazni string sa oslobođene memorije).

## 4. Generički obrazac ekploatacije u rust-openssl

```
1. Craft payload sa invalid properties: b"properties\0fakedata"
2. Pozovi openssl::Md::fetch(payload) u rustup ili app crypto
3. UAF -> empty parse -> HMAC("empty") == true na fake webhook
4. Procesiraj maliciozan Stripe event
```

## 5. Početna konfiguracija rustup-a

Rustup instalira rust-openssl < 0.10.72. U Payment Adapteru CI koristi ***rustup component add rust-src*** -> vulnerable chain.

```rust
// Webhook handler
let signature = openssl::sign::verify_hmac(payload)?; //UAF trigger
```

## 6. Zašto je rustup ranjiv?

Bindings ne handluju životni ciklus OpenSSL memorije, omogućavajući UAF u ***properties*** parsingu.

## 7. Mitigacija pretnje UAF

1. ***Inicijalna mitigacija***: Update rustup

***rustup self update*** i ***cargo update -p rust-openssl***. 