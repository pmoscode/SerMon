//! What a PostgreSQL server answers (M5-13), parsed by the module's own client: the server is
//! foreign. No input may panic; the bounds of an answer hold; SCRAM takes no server message
//! that doesn't extend the client's nonce and never accepts a server signature it didn't derive.
#![no_main]

use libfuzzer_sys::fuzz_target;
use sermon_modules::postgres::wire::{self, Query, Scram};

fuzz_target!(|data: &[u8]| {
    let Ok((messages, _)) = wire::split(data) else {
        return;
    };
    let mut q = Query::default();
    for m in &messages {
        let _ = wire::error_fields(&m.body);
        let _ = wire::row_description(&m.body);
        let _ = wire::data_row(&m.body, 2);
        match q.feed(m) {
            Ok(Some(Ok(sets))) => {
                let rows: usize = sets.iter().map(|s| s.rows.len()).sum();
                assert!(rows <= wire::MAX_ROWS);
                break;
            }
            Ok(Some(Err(_)) | None) => {}
            Err(_) => break,
        }
    }
    let text = String::from_utf8_lossy(data);
    let mut scram = Scram::new("pencil", "rOprNGfwEbeRWgbNEkqO");
    if scram.client_final(&text).is_ok() {
        assert!(text.contains("r=rOprNGfwEbeRWgbNEkqO"));
    }
    assert!(scram.verify(&text).is_err() || text.starts_with("v="));
});
