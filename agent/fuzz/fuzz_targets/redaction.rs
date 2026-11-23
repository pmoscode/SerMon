//! Redaction runs over every log message and command line, and the rules of an organization are
//! regexes from the server (KONZEPT §10.3, M3-02): neither may panic or blow up the text. Input:
//! one byte with the number of rules (0..=3), the rules separated by NUL, then the text (lossy
//! UTF-8).
#![no_main]

use libfuzzer_sys::fuzz_target;
use sermon_security::redact::{MASK, Redactor, RuleSpec};

fuzz_target!(|data: &[u8]| {
    let Some((&count, rest)) = data.split_first() else {
        return;
    };
    let mut parts: Vec<&[u8]> = rest
        .splitn(usize::from(count % 4) + 1, |b| *b == 0)
        .collect();
    let text = String::from_utf8_lossy(parts.pop().unwrap_or_default());
    let rules: Vec<RuleSpec> = parts
        .into_iter()
        .map(|p| RuleSpec {
            pattern: String::from_utf8_lossy(p).into_owned(),
        })
        .collect();
    let redactor = Redactor::with_rules(&rules).unwrap_or_else(|_| Redactor::builtin());
    let out = redactor.redact(&text);
    // Every mask replaces at least one character.
    assert!(out.len() <= text.len() * MASK.len() + MASK.len());
});
