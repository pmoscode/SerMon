//! What a MySQL or MariaDB server sends (M5-14), parsed by the module's own client: the server
//! is foreign. No input may panic; a handshake gives a 20-byte scramble; the bounds of an
//! answer hold.
#![no_main]

use libfuzzer_sys::fuzz_target;
use sermon_modules::mysql::wire::{self, Handshake, Query};
use sermon_modules::mysql::{replica, values};

fuzz_target!(|data: &[u8]| {
    if let Ok(h) = Handshake::parse(data) {
        assert_eq!(h.scramble.len(), 20);
    }
    let _ = wire::err_packet(data);
    let Ok((packets, _)) = wire::split(data) else {
        return;
    };
    let mut q = Query::default();
    for p in &packets {
        match q.feed(p) {
            Ok(Some(set)) => {
                assert!(set.rows.len() <= wire::MAX_ROWS);
                let _ = values(&set);
                let _ = replica(&set);
                break;
            }
            Ok(None) => {}
            Err(_) => break,
        }
    }
});
