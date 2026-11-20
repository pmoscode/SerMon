//! One line of `journalctl -o json` (ADR 0013).
//!
//! The output is read defensively: invalid UTF-8 (raw bytes on systemd 239) is replaced before the
//! JSON parser sees it; a value is a string, an array of bytes (binary data), an array of values
//! (a field set more than once; the first counts) or `null`. Only the fields of [`OUTPUT_FIELDS`]
//! are taken over – command lines (`_CMDLINE`) and the environment never leave the parser.

use serde_json::{Map, Value};

/// The fields asked for with `--output-fields`; `__CURSOR` and the timestamps always come along.
/// `_CAP_EFFECTIVE` (set by journald from `/proc`) tells real sshd or sudo lines from forged ones.
pub const OUTPUT_FIELDS: [&str; 11] = [
    "MESSAGE",
    "PRIORITY",
    "SYSLOG_IDENTIFIER",
    "SYSLOG_FACILITY",
    "_SYSTEMD_UNIT",
    "_PID",
    "_COMM",
    "_BOOT_ID",
    "_TRANSPORT",
    "_UID",
    "_CAP_EFFECTIVE",
];

/// A message is cut to this many bytes (KONZEPT §10.2).
pub const MAX_MESSAGE_BYTES: usize = 16 * 1024;
/// Other text fields (unit, identifier, …) are cut to this many bytes.
pub const MAX_FIELD_BYTES: usize = 256;
/// Longest accepted cursor; real ones have about 130 characters.
pub const MAX_CURSOR_BYTES: usize = 512;

/// Position of an entry in the journal's sequence: entries of one `id` are numbered without gaps,
/// so a jump means entries were lost (vacuum, rotation while the agent was stopped).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Seqnum {
    /// The sequence the number belongs to (`__SEQNUM_ID`, `s=` in the cursor).
    pub id: String,
    /// The number within it (`__SEQNUM`, `i=` in the cursor, hexadecimal there).
    pub number: u64,
}

impl Seqnum {
    /// Reads the sequence number from a cursor (`s=<id>;i=<hex>;…`). Cursors are officially
    /// opaque, so this is only a hint.
    #[must_use]
    pub fn from_cursor(cursor: &str) -> Option<Self> {
        let mut id = None;
        let mut number = None;
        for part in cursor.split(';') {
            if let Some(value) = part.strip_prefix("s=") {
                id = Some(value);
            } else if let Some(value) = part.strip_prefix("i=") {
                number = u64::from_str_radix(value, 16).ok();
            }
        }
        Some(Self {
            id: id.filter(|id| !id.is_empty())?.to_owned(),
            number: number?,
        })
    }

    /// Entries missing between `self` and the following `next`; none across sequences.
    #[must_use]
    pub fn lost_before(&self, next: &Self) -> u64 {
        if self.id == next.id && next.number > self.number {
            next.number - self.number - 1
        } else {
            0
        }
    }
}

/// The boot an entry belongs to and its time since that boot.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Boot {
    /// `_BOOT_ID`, `b=` in the cursor.
    pub id: String,
    /// Microseconds since the boot (`__MONOTONIC_TIMESTAMP`, `m=` in the cursor, hexadecimal there).
    pub monotonic_us: Option<u64>,
}

impl Boot {
    /// Reads the boot from a cursor (`…;b=<id>;m=<hex>;…`); only a hint, like [`Seqnum::from_cursor`].
    #[must_use]
    pub fn from_cursor(cursor: &str) -> Option<Self> {
        let mut id = None;
        let mut monotonic_us = None;
        for part in cursor.split(';') {
            if let Some(value) = part.strip_prefix("b=") {
                id = Some(value);
            } else if let Some(value) = part.strip_prefix("m=") {
                monotonic_us = u64::from_str_radix(value, 16).ok();
            }
        }
        Some(Self {
            id: id.filter(|id| !id.is_empty())?.to_owned(),
            monotonic_us,
        })
    }
}

/// A journal entry with the fields the agent uses.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Entry {
    /// Position for `--after-cursor`.
    pub cursor: String,
    /// Time of the entry in microseconds since the epoch (`__REALTIME_TIMESTAMP`).
    pub realtime_us: u64,
    /// Microseconds since the boot (`__MONOTONIC_TIMESTAMP`).
    pub monotonic_us: Option<u64>,
    /// Sequence number, if known.
    pub seqnum: Option<Seqnum>,
    /// The message, at most [`MAX_MESSAGE_BYTES`].
    pub message: String,
    /// The message was longer and has been cut.
    pub message_truncated: bool,
    /// Syslog priority 0 (emergency) to 7 (debug).
    pub priority: Option<u8>,
    /// Syslog facility.
    pub facility: Option<u8>,
    /// `SYSLOG_IDENTIFIER`, e.g. `sshd-session`.
    pub identifier: Option<String>,
    /// `_SYSTEMD_UNIT`.
    pub unit: Option<String>,
    /// `_COMM`, the process name.
    pub comm: Option<String>,
    /// `_TRANSPORT` (journal, syslog, stdout, kernel, …).
    pub transport: Option<String>,
    /// `_BOOT_ID`.
    pub boot_id: Option<String>,
    /// `_PID`.
    pub pid: Option<u32>,
    /// `_UID`.
    pub uid: Option<u32>,
    /// `_CAP_EFFECTIVE`: the sender's effective capabilities; non-zero for root and setuid
    /// programs like sudo, zero for processes of normal users.
    pub cap_effective: Option<u64>,
}

/// Why a line is no entry.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum LineError {
    /// Not a JSON object.
    #[error("not a JSON object: {0}")]
    Json(String),
    /// Without a usable `__CURSOR` the agent could not resume after the entry.
    #[error("missing or invalid __CURSOR")]
    Cursor,
    /// Without `__REALTIME_TIMESTAMP` the entry has no time.
    #[error("missing or invalid __REALTIME_TIMESTAMP")]
    Timestamp,
}

/// Whether `cursor` can be passed to `--after-cursor`: printable ASCII without spaces.
#[must_use]
pub fn valid_cursor(cursor: &str) -> bool {
    !cursor.is_empty()
        && cursor.len() <= MAX_CURSOR_BYTES
        && cursor.bytes().all(|b| b.is_ascii_graphic())
}

/// Parses one line of `journalctl -o json`.
///
/// # Errors
///
/// [`LineError`] if the line is no JSON object or lacks cursor or timestamp; every other oddity
/// (binary values, `null`, unknown fields, invalid UTF-8) is tolerated.
pub fn parse_line(line: &[u8]) -> Result<Entry, LineError> {
    let text = String::from_utf8_lossy(line);
    let object: Map<String, Value> =
        serde_json::from_str(&text).map_err(|error| LineError::Json(error.to_string()))?;
    let field = |name: &str| field(&object, name);
    let cursor = field("__CURSOR")
        .filter(|c| valid_cursor(c))
        .ok_or(LineError::Cursor)?;
    let realtime_us = field("__REALTIME_TIMESTAMP")
        .and_then(|t| t.parse().ok())
        .ok_or(LineError::Timestamp)?;
    let seqnum = match (field("__SEQNUM_ID"), field("__SEQNUM")) {
        (Some(id), Some(number)) if !id.is_empty() => number
            .parse()
            .ok()
            .map(|number| Seqnum { id, number })
            .or_else(|| Seqnum::from_cursor(&cursor)),
        _ => Seqnum::from_cursor(&cursor),
    };
    let (message, message_truncated) =
        truncate(field("MESSAGE").unwrap_or_default(), MAX_MESSAGE_BYTES);
    let short = |name: &str| field(name).map(|value| truncate(value, MAX_FIELD_BYTES).0);
    Ok(Entry {
        cursor,
        realtime_us,
        monotonic_us: number(&object, "__MONOTONIC_TIMESTAMP"),
        seqnum,
        message,
        message_truncated,
        priority: number::<u8>(&object, "PRIORITY").filter(|p| *p <= 7),
        facility: number(&object, "SYSLOG_FACILITY"),
        identifier: short("SYSLOG_IDENTIFIER"),
        unit: short("_SYSTEMD_UNIT"),
        comm: short("_COMM"),
        transport: short("_TRANSPORT"),
        boot_id: short("_BOOT_ID"),
        pid: number(&object, "_PID"),
        uid: number(&object, "_UID"),
        cap_effective: field("_CAP_EFFECTIVE")
            .and_then(|hex| u64::from_str_radix(hex.trim(), 16).ok()),
    })
}

fn field(object: &Map<String, Value>, name: &str) -> Option<String> {
    object.get(name).and_then(text_of)
}

fn number<T: std::str::FromStr>(object: &Map<String, Value>, name: &str) -> Option<T> {
    field(object, name).and_then(|value| value.trim().parse().ok())
}

/// The text of a value: strings as they are, byte arrays decoded (invalid UTF-8 replaced), the
/// first of several values; `null` and anything else is no text.
fn text_of(value: &Value) -> Option<String> {
    match value {
        Value::String(text) => Some(text.clone()),
        Value::Array(items) => {
            if items.iter().all(Value::is_number) {
                let bytes: Option<Vec<u8>> = items
                    .iter()
                    .map(|b| b.as_u64().and_then(|b| u8::try_from(b).ok()))
                    .collect();
                bytes.map(|bytes| String::from_utf8_lossy(&bytes).into_owned())
            } else {
                items.first().and_then(text_of)
            }
        }
        _ => None,
    }
}

/// Cuts `text` to at most `max` bytes at a character boundary.
fn truncate(mut text: String, max: usize) -> (String, bool) {
    if text.len() <= max {
        return (text, false);
    }
    let mut end = max;
    while !text.is_char_boundary(end) {
        end -= 1;
    }
    text.truncate(end);
    (text, true)
}

#[cfg(test)]
mod tests {
    use std::path::PathBuf;

    use proptest::prelude::*;

    use super::*;

    const DISTROS: [&str; 4] = ["debian-13", "ubuntu-24.04", "rockylinux-9", "rockylinux-8"];

    fn fixture(distro: &str, name: &str) -> Vec<u8> {
        let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
            .join("../../fixtures/logs")
            .join(distro)
            .join(name);
        std::fs::read(&path).unwrap_or_else(|e| panic!("{}: {e}", path.display()))
    }

    fn entries(distro: &str, name: &str) -> Vec<Entry> {
        fixture(distro, name)
            .split(|b| *b == b'\n')
            .filter(|line| !line.is_empty())
            .map(|line| parse_line(line).unwrap_or_else(|e| panic!("{distro}/{name}: {e}")))
            .collect()
    }

    #[test]
    fn every_fixture_line_parses_on_every_distro() {
        for distro in DISTROS {
            let security = entries(distro, "journal-security.jsonl");
            assert!(security.len() > 30, "{distro}: {} entries", security.len());
            for entry in &security {
                assert!(valid_cursor(&entry.cursor), "{distro}: {}", entry.cursor);
                assert!(entry.realtime_us > 1_700_000_000_000_000, "{distro}");
                assert!(entry.seqnum.is_some(), "{distro}: no sequence number");
                assert!(entry.boot_id.is_some(), "{distro}: no boot id");
            }
            assert!(
                security
                    .iter()
                    .any(|e| e.message.contains("Failed password for alice")),
                "{distro}: no failed ssh login"
            );
            assert_eq!(entries(distro, "journal-format.jsonl").len(), 3, "{distro}");
        }
    }

    #[test]
    fn reads_the_typed_fields() {
        let entry = entries("debian-13", "journal-security.jsonl")
            .into_iter()
            .find(|e| e.message.contains("Failed password for alice"))
            .unwrap();
        assert_eq!(entry.identifier.as_deref(), Some("sshd-session"));
        assert_eq!(entry.unit.as_deref(), Some("ssh.service"));
        assert_eq!(entry.transport.as_deref(), Some("syslog"));
        assert_eq!(entry.facility, Some(4));
        assert!(entry.priority.is_some() && entry.pid.is_some() && entry.uid == Some(0));
        let seq = entry.seqnum.unwrap();
        assert_eq!(
            Some(seq.clone()),
            Seqnum::from_cursor(&entry.cursor),
            "cursor and __SEQNUM agree"
        );
    }

    // Binary MESSAGE as a byte array, and on systemd 239 as raw bytes – invalid UTF-8 in the JSON.
    #[test]
    fn binary_messages_and_invalid_utf8() {
        for distro in DISTROS {
            let format = entries(distro, "journal-format.jsonl");
            let binary = format
                .iter()
                .find(|e| e.message.contains("binary"))
                .unwrap();
            assert!(
                binary.message.contains('\u{fffd}'),
                "{distro}: {:?}",
                binary.message
            );
            let long = format.iter().find(|e| e.message.len() >= 5000).unwrap();
            assert!(!long.message_truncated, "{distro}");
        }
        let raw =
            b"{\"__CURSOR\":\"s=a;i=1\",\"__REALTIME_TIMESTAMP\":\"1\",\"MESSAGE\":\"caf\xe9\"}";
        assert_eq!(parse_line(raw).unwrap().message, "caf\u{fffd}");
    }

    #[test]
    fn command_lines_never_leave_the_parser() {
        for distro in DISTROS {
            for entry in entries(distro, "journal-security.jsonl") {
                assert!(
                    !format!("{entry:?}").contains("fixture-password"),
                    "{distro}: {entry:?}"
                );
            }
        }
    }

    #[test]
    fn values_of_every_shape() {
        let line =
            br#"{"__CURSOR":"s=a;i=ff","__REALTIME_TIMESTAMP":"5","MESSAGE":["first","second"],
            "PRIORITY":"9","_PID":null,"_UID":[48,49],"_COMM":{"x":1},"_TRANSPORT":[[106],[107]]}"#;
        let entry = parse_line(line).unwrap();
        assert_eq!(entry.message, "first", "a field set twice: the first value");
        assert_eq!(entry.priority, None, "priorities are 0..7");
        assert_eq!(entry.pid, None);
        assert_eq!(entry.uid, Some(1), "byte array \"01\"");
        assert_eq!(entry.comm, None);
        assert_eq!(entry.transport.as_deref(), Some("j"));
        assert_eq!(
            entry.seqnum,
            Some(Seqnum {
                id: "a".into(),
                number: 255
            })
        );
    }

    #[test]
    fn long_values_are_cut_at_a_character_boundary() {
        let message = "ä".repeat(MAX_MESSAGE_BYTES); // 2 bytes each
        let line = format!(
            r#"{{"__CURSOR":"s=a;i=1","__REALTIME_TIMESTAMP":"1","MESSAGE":"{message}","_SYSTEMD_UNIT":"{}"}}"#,
            "u".repeat(1000)
        );
        let entry = parse_line(line.as_bytes()).unwrap();
        assert!(entry.message_truncated && entry.message.len() == MAX_MESSAGE_BYTES);
        assert_eq!(entry.unit.unwrap().len(), MAX_FIELD_BYTES);
    }

    #[test]
    fn rejects_lines_without_position_or_time() {
        assert!(matches!(
            parse_line(b"-- No entries --"),
            Err(LineError::Json(_))
        ));
        assert!(matches!(parse_line(b"[1,2]"), Err(LineError::Json(_))));
        assert_eq!(
            parse_line(br#"{"__REALTIME_TIMESTAMP":"1"}"#),
            Err(LineError::Cursor)
        );
        assert_eq!(
            parse_line(br#"{"__CURSOR":"s=a; i=1","__REALTIME_TIMESTAMP":"1"}"#),
            Err(LineError::Cursor),
            "a cursor with a space"
        );
        assert_eq!(
            parse_line(br#"{"__CURSOR":"s=a;i=1"}"#),
            Err(LineError::Timestamp)
        );
    }

    // Who sent an entry decides whether a security event is believed (M2-06): journald fills
    // _CAP_EFFECTIVE from /proc; a forged `logger -t sshd` has none.
    #[test]
    fn reads_the_sender_capabilities_and_the_monotonic_time() {
        let entries = entries("debian-13", "journal-security-variants.jsonl");
        let real = entries
            .iter()
            .find(|e| e.message.starts_with("Accepted publickey"))
            .unwrap();
        assert_eq!(real.cap_effective, Some(0x1ff_ffff_ffff));
        assert!(real.monotonic_us.is_some_and(|m| m > 0));
        let forged = entries
            .iter()
            .find(|e| e.message.contains("203.0.113.9") && e.identifier.as_deref() == Some("sshd"))
            .unwrap();
        // 0, or missing when logger was gone before journald read /proc.
        assert_eq!(forged.uid, Some(1000));
        assert!(forged.cap_effective.is_none_or(|c| c == 0), "{forged:?}");
        assert!(OUTPUT_FIELDS.contains(&"_CAP_EFFECTIVE"));
    }

    #[test]
    fn boot_of_a_cursor() {
        assert_eq!(
            Boot::from_cursor("s=a;i=5;b=cc8f;m=840bc89c4;t=660b;x=1"),
            Some(Boot {
                id: "cc8f".into(),
                monotonic_us: Some(0x8_40bc_89c4)
            })
        );
        assert_eq!(
            Boot::from_cursor("s=a;i=5;b=cc8f"),
            Some(Boot {
                id: "cc8f".into(),
                monotonic_us: None
            })
        );
        assert_eq!(Boot::from_cursor("s=a;i=5"), None);
    }

    #[test]
    fn sequence_gaps() {
        let at = |id: &str, number| Seqnum {
            id: id.into(),
            number,
        };
        assert_eq!(at("a", 5).lost_before(&at("a", 6)), 0);
        assert_eq!(at("a", 5).lost_before(&at("a", 9)), 3);
        assert_eq!(
            at("a", 5).lost_before(&at("b", 9)),
            0,
            "a new sequence is no gap"
        );
        assert_eq!(at("a", 5).lost_before(&at("a", 2)), 0);
        assert_eq!(Seqnum::from_cursor("s=abc;i=1f;b=x"), Some(at("abc", 31)));
        assert_eq!(Seqnum::from_cursor("opaque"), None);
    }

    proptest! {
        #[test]
        fn never_panics(line in proptest::collection::vec(any::<u8>(), 0..512)) {
            let _ = parse_line(&line);
        }

        #[test]
        fn never_panics_on_journal_like_objects(
            message in "\\PC{0,64}",
            bytes in proptest::collection::vec(any::<u16>(), 0..16),
            cursor in "[ -~]{0,40}",
        ) {
            let line = serde_json::json!({"__CURSOR": cursor, "__REALTIME_TIMESTAMP": "1", "MESSAGE": message, "_PID": bytes});
            let _ = parse_line(line.to_string().as_bytes());
        }
    }
}
