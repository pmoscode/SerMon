//! The parser of `journalctl -o json` lines must handle any input without panicking: the journal
//! holds whatever local processes write (ADR 0013). Input: lines separated by newlines.
#![no_main]

use libfuzzer_sys::fuzz_target;
use sermon_logs::journal::{Seqnum, parse_line};

fuzz_target!(|data: &[u8]| {
    for line in data.split(|b| *b == b'\n') {
        if let Ok(entry) = parse_line(line) {
            assert!(entry.message.len() <= sermon_logs::journal::MAX_MESSAGE_BYTES);
            let _ = Seqnum::from_cursor(&entry.cursor);
        }
    }
});
