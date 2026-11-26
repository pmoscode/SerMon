//! The parsers of the modules read text of other programs and services (ADR 0039): the
//! listening sockets of /proc/net for the discovery (M5-04), the answers of the services the
//! modules ask. The first byte picks the parser. No input may panic, and what is taken stays in
//! its bounds.
#![no_main]

use libfuzzer_sys::fuzz_target;
use sermon_modules::discovery::{self, MAX_FACTS};

fuzz_target!(|data: &[u8]| {
    let Some((&pick, rest)) = data.split_first() else {
        return;
    };
    let text = String::from_utf8_lossy(rest);
    match pick % 2 {
        0 => {
            let listeners = discovery::parse_tcp(&text);
            assert!(listeners.len() <= MAX_FACTS);
        }
        _ => {
            let sockets = discovery::parse_unix(&text);
            assert!(sockets.len() <= MAX_FACTS);
            for s in &sockets {
                assert!(s.starts_with('/') && s.len() <= 108);
                assert!(!s.chars().any(char::is_control));
            }
        }
    }
});
