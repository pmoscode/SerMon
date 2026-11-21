//! The parsers of `journalctl -o json` lines and of classic log file lines must handle any input
//! without panicking: the journal and the files hold whatever local processes write (ADR 0013,
//! ADR 0025). Input: lines separated by newlines.
#![no_main]

use libfuzzer_sys::fuzz_target;
use sermon_logs::file::{Format, LocalTime, parse};
use sermon_logs::journal::{Seqnum, parse_line};

fuzz_target!(|data: &[u8]| {
    let time = LocalTime::new(None, 1_795_291_200_000_000);
    for line in data.split(|b| *b == b'\n') {
        let text = String::from_utf8_lossy(line);
        let _ = parse(Format::Syslog, &text, &time);
        let _ = parse(Format::Trusted, &text, &time);
        if let Ok(entry) = parse_line(line) {
            assert!(entry.message.len() <= sermon_logs::journal::MAX_MESSAGE_BYTES);
            let _ = Seqnum::from_cursor(&entry.cursor);
        }
    }
});
