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
use sermon_modules::{apache, nginx, redis, systemd_units};

fuzz_target!(|data: &[u8]| {
    let Some((&pick, rest)) = data.split_first() else {
        return;
    };
    let text = String::from_utf8_lossy(rest);
    match pick % 8 {
        0 => {
            let listeners = discovery::parse_tcp(&text);
            assert!(listeners.len() <= MAX_FACTS);
        }
        4 => {
            let units = systemd_units::parse_show(&text);
            assert!(units.len() <= systemd_units::MAX_UNITS);
            for u in &units {
                for v in [&u.id, &u.load, &u.active, &u.sub] {
                    assert!(v.len() <= 255 && !v.chars().any(char::is_control));
                }
            }
        }
        5 => {
            if let Ok(s) = nginx::parse_stub_status(&text) {
                assert!(text.len() <= 4096 && s.active < u64::MAX);
            }
        }
        6 => {
            if let Ok(s) = apache::parse_server_status(&text) {
                assert!(text.len() <= 64 * 1024 && s.slots > 0);
            }
        }
        7 => {
            if let Ok(info) = redis::parse_info(&text) {
                assert!(text.len() <= redis::MAX_INFO && info.used_memory.is_some());
            }
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
