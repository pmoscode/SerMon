//! What the module docker reads of a container (M5-16): its cgroup's keyed files and io.stat,
//! the name of its first process – chosen by whoever runs the container – and its
//! /proc/<pid>/net/dev. No input may panic; a name is a label without control characters of at
//! most 64 bytes; a cgroup name gives an ID of 64 hex digits or none.
#![no_main]

use libfuzzer_sys::fuzz_target;
use sermon_modules::docker::cgroups::{field, id_of, interfaces, io, name};

fuzz_target!(|data: &[u8]| {
    let text = String::from_utf8_lossy(data);
    let _ = field(&text, "usage_usec");
    let _ = io(&text);
    let _ = interfaces(&text);
    let n = name(&text);
    assert!(n.len() <= 64 && !n.chars().any(char::is_control));
    if let Some((_, id)) = id_of(&text) {
        assert!(id.len() == 64 && id.bytes().all(|b| b.is_ascii_hexdigit()));
    }
});
