//! The parsers of the modules read text of other programs and services (ADR 0039): the
//! listening sockets of /proc/net for the discovery (M5-04), the URLs of the configuration and of
//! redirects and the certificates of services (M5-08), the answers of the services the modules
//! ask. The first byte picks the parser. No input may panic, and what is taken stays in its
//! bounds.
#![no_main]

use libfuzzer_sys::fuzz_target;
use sermon_modules::discovery::{self, MAX_FACTS};
use sermon_modules::http::Url;
use sermon_modules::x509;

fuzz_target!(|data: &[u8]| {
    let Some((&pick, rest)) = data.split_first() else {
        return;
    };
    let text = String::from_utf8_lossy(rest);
    match pick % 4 {
        0 => {
            let listeners = discovery::parse_tcp(&text);
            assert!(listeners.len() <= MAX_FACTS);
        }
        2 => {
            if let Some(v) = x509::validity(rest) {
                // Years 0 to 9999 at most.
                assert!(v.not_before >= -62_167_219_200 && v.not_after <= 253_402_300_799);
            }
        }
        3 => {
            let (base, location) = text.split_at(text.floor_char_boundary(text.len() / 2));
            if let Ok(url) = Url::parse(base) {
                assert!(
                    url.host()
                        .bytes()
                        .all(|b| b.is_ascii_alphanumeric() || b".-:".contains(&b))
                );
                let label = url.label();
                let authority = label.split('/').nth(2).unwrap_or_default();
                assert!(label.len() <= 512 && !label.contains('?') && !authority.contains('@'));
                if let Ok(next) = url.join(location) {
                    assert!(!next.host().is_empty());
                }
            }
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
