//! Grouping the lines of a log file into entries (M3-05) must handle any input: the files hold
//! whatever local processes write. Input: a rule byte (0: by indentation, else: an entry starts
//! with that byte), then lines separated by newlines.
#![no_main]

use libfuzzer_sys::fuzz_target;
use sermon_logs::file::{Assembler, Line, MAX_ENTRY_BYTES, MAX_ENTRY_LINES, Position, indented};

fuzz_target!(|data: &[u8]| {
    let Some((&rule, rest)) = data.split_first() else {
        return;
    };
    let starts = |text: &str| {
        if rule == 0 {
            !indented(text)
        } else {
            text.as_bytes().first() == Some(&rule)
        }
    };
    let mut assembler = Assembler::default();
    let mut entries = Vec::new();
    let mut offset = 0u64;
    let mut settled = 0u64;
    let mut count = 0usize;
    for (i, text) in rest.split(|b| *b == b'\n').enumerate() {
        offset += text.len() as u64 + 1;
        let line = Line {
            text: text.to_vec(),
            truncated: i % 7 == 3,
            end: Position {
                dev: 1,
                inode: 1,
                offset,
            },
        };
        assembler.push(&line, starts, &mut entries);
        count += 1;
        let now = assembler.settled().map_or(0, |p| p.offset);
        assert!(
            now >= settled && now <= offset,
            "settled moves forward within the file"
        );
        settled = now;
    }
    assembler.flush(&mut entries);
    let lines: usize = entries.iter().map(|e| e.lines).sum();
    assert!(lines <= count);
    for entry in &entries {
        assert!(entry.text.len() <= MAX_ENTRY_BYTES);
        assert!(entry.text.split('\n').count() <= MAX_ENTRY_LINES);
        assert!(entry.end.offset <= offset);
    }
    assert!(!assembler.is_open());
    if count > 0 {
        assert_eq!(assembler.settled().map_or(0, |p| p.offset), offset);
    }
});
