//! The security parsers must handle any message from any sender without panicking: log lines are
//! written by whoever runs on the host (KONZEPT §11). Input: one byte choosing identifier and
//! sender, then the message (lossy UTF-8; journal lines are decoded the same way).
#![no_main]

use libfuzzer_sys::fuzz_target;
use sermon_security::{LogLine, Parser, ParserConfig, redact};

const IDENTIFIERS: [&str; 7] = [
    "sshd",
    "sshd-session",
    "sshd-auth",
    "sudo",
    "useradd",
    "userdel",
    "kernel",
];

fuzz_target!(|data: &[u8]| {
    let Some((&choice, rest)) = data.split_first() else {
        return;
    };
    let identifier = IDENTIFIERS[usize::from(choice) % IDENTIFIERS.len()];
    let message = String::from_utf8_lossy(rest);
    let mut parser = Parser::new(
        ParserConfig {
            sudo_commands: true,
        },
        Some(("a".into(), Some(1))),
    );
    let mut out = Vec::new();
    for line in message.split('\n') {
        parser.parse(
            &LogLine {
                at_us: 1,
                identifier: Some(identifier),
                transport: Some(if choice & 0x80 != 0 {
                    "kernel"
                } else {
                    "syslog"
                }),
                uid: Some(u32::from(choice & 0x40 == 0)),
                cap_effective: None,
                boot_id: Some(if choice & 0x20 != 0 { "b" } else { "a" }),
                monotonic_us: Some(2),
                message: line,
            },
            &mut out,
        );
        let _ = redact(line);
    }
});
