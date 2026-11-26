//! Normalized log entries (KONZEPT §10.2, M3-01): journal entries and lines of log files become
//! `{ts, source, unit/file, severity, message, fields}`.
//!
//! Everything here comes from the host and is only shown, never interpreted. A record keeps the
//! bounds of the protocol (`LogEntry` in `agent.proto`), so a batch never fails the server's
//! validation: texts are cut, values of the wrong shape are dropped.

use std::borrow::Cow;
use std::path::Path;

use crate::file::{Record, Sender};
use crate::journal::{Entry, MAX_FIELD_BYTES, MAX_MESSAGE_BYTES};

/// Longest path of a log file (Linux `PATH_MAX`).
pub const MAX_PATH_BYTES: usize = 4096;
/// A batch holds at most this many records (KONZEPT §5).
pub const MAX_BATCH_RECORDS: usize = 1000;
/// A batch holds at most this many bytes, counted with [`LogRecord::size`] (KONZEPT §5).
pub const MAX_BATCH_BYTES: usize = 256 * 1024;

/// The highest syslog facility (`local7`).
const MAX_FACILITY: u8 = 23;
/// What the fields besides the texts count in [`LogRecord::size`]: time, enums, numbers and the
/// encoding overhead of the protocol.
const FIELDS_SHARE: usize = 48;

/// The syslog priority of an entry.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
pub enum Severity {
    /// 0: the system is unusable.
    Emergency,
    /// 1: act immediately.
    Alert,
    /// 2
    Critical,
    /// 3
    Error,
    /// 4
    Warning,
    /// 5
    Notice,
    /// 6
    Info,
    /// 7
    Debug,
}

impl Severity {
    /// The severity of syslog priority 0..=7.
    #[must_use]
    pub fn from_priority(priority: u8) -> Option<Self> {
        Some(match priority {
            0 => Self::Emergency,
            1 => Self::Alert,
            2 => Self::Critical,
            3 => Self::Error,
            4 => Self::Warning,
            5 => Self::Notice,
            6 => Self::Info,
            7 => Self::Debug,
            _ => return None,
        })
    }
}

/// Where a record was read.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Origin {
    /// The systemd journal.
    Journal,
    /// A log file, by its absolute path.
    File(String),
    /// The output of a container (module `docker_socket`, M5-21).
    Container,
}

impl Origin {
    /// A file as origin: absolute and at most [`MAX_PATH_BYTES`] (invalid UTF-8 replaced).
    #[must_use]
    pub fn file(path: &Path) -> Option<Self> {
        let text = path.to_string_lossy();
        (text.starts_with('/') && text.len() <= MAX_PATH_BYTES)
            .then(|| Self::File(text.into_owned()))
    }
}

/// A normalized log entry.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct LogRecord {
    /// Microseconds since the epoch.
    pub at_us: u64,
    /// Where it was read.
    pub origin: Origin,
    /// The systemd unit, or the syslog identifier without one.
    pub unit: Option<String>,
    /// The syslog identifier, e.g. `sshd`.
    pub identifier: Option<String>,
    /// The priority, where the source tells.
    pub severity: Option<Severity>,
    /// The text, at most [`MAX_MESSAGE_BYTES`].
    pub message: String,
    /// The text was longer and has been cut.
    pub truncated: bool,
    /// The sending process.
    pub pid: Option<u32>,
    /// The name of the sending process (`_COMM`).
    pub process: Option<String>,
    /// The sender's user ID, as journald or the SerMon rsyslog template saw it.
    pub uid: Option<u32>,
    /// The boot (32 lowercase hex characters).
    pub boot_id: Option<String>,
    /// How journald received it (`journal`, `syslog`, `stdout`, `kernel`, …).
    pub transport: Option<String>,
    /// The syslog facility, 0..=23.
    pub facility: Option<u8>,
}

impl LogRecord {
    /// A journal entry as a record.
    #[must_use]
    pub fn from_journal(entry: &Entry) -> Self {
        Self {
            at_us: entry.realtime_us,
            origin: Origin::Journal,
            unit: entry.unit.clone().or_else(|| entry.identifier.clone()),
            identifier: entry.identifier.clone(),
            severity: entry.priority.and_then(Severity::from_priority),
            message: entry.message.clone(),
            truncated: entry.message_truncated,
            pid: entry.pid,
            process: entry.comm.clone(),
            uid: entry.uid,
            boot_id: entry.boot_id.clone().filter(|id| valid_boot_id(id)),
            transport: entry.transport.clone().filter(|t| valid_transport(t)),
            facility: entry.facility.filter(|f| *f <= MAX_FACILITY),
        }
    }

    /// A line of the file `origin` as a record; `truncated` if the line was cut while reading.
    #[must_use]
    pub fn from_file(origin: Origin, record: &Record<'_>, truncated: bool) -> Self {
        let (uid, transport) = match record.sender {
            Sender::Unknown => (None, None),
            Sender::Uid(uid) => (Some(uid), None),
            Sender::Kernel => (Some(0), Some("kernel".to_owned())),
        };
        let identifier = record
            .identifier
            .map(|i| cut(i, MAX_FIELD_BYTES).to_owned());
        Self {
            at_us: record.at_us,
            origin,
            unit: identifier.clone(),
            identifier,
            severity: record.priority.and_then(Severity::from_priority),
            message: crate::cut::message(record.message, MAX_MESSAGE_BYTES).to_owned(),
            truncated: truncated || record.message.len() > MAX_MESSAGE_BYTES,
            pid: record.pid,
            process: None,
            uid,
            boot_id: None,
            transport,
            facility: record.facility.filter(|f| *f <= MAX_FACILITY),
        }
    }

    /// An entry of a log file named by a glob (M3-05): the time it was read, the file name as
    /// unit; such files name no severity.
    #[must_use]
    pub fn from_plain_file(origin: Origin, at_us: u64, message: &str, truncated: bool) -> Self {
        let unit = match &origin {
            Origin::File(path) => Path::new(path)
                .file_name()
                .map(|n| cut(&n.to_string_lossy(), MAX_FIELD_BYTES).to_owned()),
            Origin::Journal | Origin::Container => None,
        };
        Self {
            at_us,
            origin,
            unit,
            identifier: None,
            severity: None,
            message: crate::cut::message(message, MAX_MESSAGE_BYTES).to_owned(),
            truncated: truncated || message.len() > MAX_MESSAGE_BYTES,
            pid: None,
            process: None,
            uid: None,
            boot_id: None,
            transport: None,
            facility: None,
        }
    }

    /// A line a container wrote (M5-21): its name as unit and identifier, `stdout` or `stderr`
    /// as transport; containers name no severity.
    #[must_use]
    pub fn from_container(at_us: u64, name: &str, stderr: bool, message: &str) -> Self {
        let name = cut(name, MAX_FIELD_BYTES).to_owned();
        Self {
            at_us,
            origin: Origin::Container,
            unit: Some(name.clone()),
            identifier: Some(name),
            severity: None,
            message: crate::cut::message(message, MAX_MESSAGE_BYTES).to_owned(),
            truncated: message.len() > MAX_MESSAGE_BYTES,
            pid: None,
            process: None,
            uid: None,
            boot_id: None,
            transport: Some(if stderr { "stderr" } else { "stdout" }.to_owned()),
            facility: None,
        }
    }

    /// Replaces the message with `redact(message)`, cut to [`MAX_MESSAGE_BYTES`] again: a mask can
    /// be longer than what it replaces.
    pub fn redact_with<F>(&mut self, redact: F)
    where
        F: for<'a> FnOnce(&'a str) -> Cow<'a, str>,
    {
        let redacted = match redact(&self.message) {
            Cow::Borrowed(same) if std::ptr::eq(same, self.message.as_str()) => return,
            other => other.into_owned(),
        };
        let kept = cut(&redacted, MAX_MESSAGE_BYTES).len();
        self.truncated |= kept < redacted.len();
        self.message = redacted;
        self.message.truncate(kept);
    }

    /// What the record counts against [`MAX_BATCH_BYTES`]: its texts plus a fixed share for the
    /// other fields.
    #[must_use]
    pub fn size(&self) -> usize {
        let text = |value: &Option<String>| value.as_ref().map_or(0, String::len);
        let file = match &self.origin {
            Origin::Journal | Origin::Container => 0,
            Origin::File(path) => path.len(),
        };
        FIELDS_SHARE
            + self.message.len()
            + file
            + text(&self.unit)
            + text(&self.identifier)
            + text(&self.process)
            + text(&self.boot_id)
            + text(&self.transport)
    }
}

/// Collects records into batches within [`MAX_BATCH_RECORDS`] and [`MAX_BATCH_BYTES`].
#[derive(Debug, Default)]
pub struct Batcher {
    records: Vec<LogRecord>,
    bytes: usize,
}

impl Batcher {
    /// Adds a record. If it does not fit any more, the batch so far is returned first and the
    /// record starts the next one.
    pub fn push(&mut self, record: LogRecord) -> Option<Vec<LogRecord>> {
        let size = record.size();
        let full = !self.records.is_empty()
            && (self.records.len() >= MAX_BATCH_RECORDS || self.bytes + size > MAX_BATCH_BYTES);
        let done = full.then(|| self.take());
        self.bytes += size;
        self.records.push(record);
        done
    }

    /// The records collected so far; the batcher is empty afterwards.
    pub fn take(&mut self) -> Vec<LogRecord> {
        self.bytes = 0;
        std::mem::take(&mut self.records)
    }

    /// Whether no record waits.
    #[must_use]
    pub fn is_empty(&self) -> bool {
        self.records.is_empty()
    }
}

/// `_BOOT_ID`: 128 bits as 32 lowercase hex characters.
fn valid_boot_id(id: &str) -> bool {
    id.len() == 32 && id.bytes().all(|b| matches!(b, b'0'..=b'9' | b'a'..=b'f'))
}

/// `_TRANSPORT`: a short lowercase word.
fn valid_transport(transport: &str) -> bool {
    transport.len() <= 32
        && transport
            .bytes()
            .all(|b| b.is_ascii_lowercase() || b == b'_' || b == b'-')
}

/// `text` cut to at most `max` bytes at a character boundary.
fn cut(text: &str, max: usize) -> &str {
    if text.len() <= max {
        return text;
    }
    let mut end = max;
    while !text.is_char_boundary(end) {
        end -= 1;
    }
    &text[..end]
}

#[cfg(test)]
mod tests {
    use std::path::PathBuf;

    use std::borrow::Cow;

    use proptest::prelude::*;

    use super::*;
    use crate::file::{Format, LocalTime, parse};
    use crate::journal::parse_line;

    /// Distros with captured journal fixtures (M2-06, M2-19).
    const DISTROS: [&str; 6] = [
        "debian-13",
        "ubuntu-24.04",
        "rockylinux-9",
        "rockylinux-8",
        "almalinux-10",
        "fedora-44",
    ];

    fn fixture(distro: &str, name: &str) -> Vec<u8> {
        let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
            .join("../../fixtures/logs")
            .join(distro)
            .join(name);
        std::fs::read(&path).unwrap_or_else(|e| panic!("{}: {e}", path.display()))
    }

    fn lines(data: &[u8]) -> impl Iterator<Item = &[u8]> {
        data.split(|b| *b == b'\n').filter(|l| !l.is_empty())
    }

    fn journal(distro: &str, name: &str) -> Vec<(Entry, LogRecord)> {
        lines(&fixture(distro, name))
            .map(|line| {
                let entry = parse_line(line).unwrap();
                let record = LogRecord::from_journal(&entry);
                (entry, record)
            })
            .collect()
    }

    fn journal_files() -> Vec<(&'static str, &'static str)> {
        DISTROS
            .iter()
            .flat_map(|d| {
                ["journal-security-variants.jsonl", "journal-oom.jsonl"]
                    .map(|f| (*d, f))
                    .into_iter()
            })
            .collect()
    }

    #[test]
    fn severities_follow_the_syslog_priorities() {
        let all = [
            Severity::Emergency,
            Severity::Alert,
            Severity::Critical,
            Severity::Error,
            Severity::Warning,
            Severity::Notice,
            Severity::Info,
            Severity::Debug,
        ];
        for (priority, severity) in (0u8..).zip(all) {
            assert_eq!(Severity::from_priority(priority), Some(severity));
        }
        assert_eq!(Severity::from_priority(8), None);
        assert!(Severity::Emergency < Severity::Debug, "most severe first");
    }

    #[test]
    fn journal_entries_keep_time_message_and_severity_on_every_distro() {
        for (distro, file) in journal_files() {
            let records = journal(distro, file);
            assert!(!records.is_empty(), "{distro}/{file}");
            for (entry, record) in &records {
                assert_eq!(record.origin, Origin::Journal);
                assert_eq!(record.at_us, entry.realtime_us);
                assert_eq!(record.message, entry.message);
                assert_eq!(
                    record.severity,
                    entry.priority.and_then(Severity::from_priority),
                    "{distro}/{file}: {}",
                    entry.message
                );
                assert_eq!(
                    (record.pid, record.uid, &record.process),
                    (entry.pid, entry.uid, &entry.comm)
                );
                assert_eq!(record.boot_id, entry.boot_id, "{distro}/{file}");
            }
            assert!(
                records
                    .iter()
                    .any(|(_, r)| r.severity == Some(Severity::Info)),
                "{distro}/{file}"
            );
        }
    }

    #[test]
    fn the_unit_is_the_systemd_unit_or_else_the_identifier() {
        let records = journal("debian-13", "journal-security.jsonl");
        let ssh = records
            .iter()
            .find(|(e, _)| e.message.contains("Failed password for alice"))
            .map(|(_, r)| r)
            .unwrap();
        assert_eq!(ssh.unit.as_deref(), Some("ssh.service"));
        assert_eq!(ssh.identifier.as_deref(), Some("sshd-session"));
        assert_eq!(ssh.transport.as_deref(), Some("syslog"));
        assert_eq!(ssh.facility, Some(4));
        // Kernel lines have no unit.
        let kernel = journal("debian-13", "journal-oom.jsonl")
            .into_iter()
            .find(|(e, _)| e.transport.as_deref() == Some("kernel"))
            .map(|(_, r)| r)
            .unwrap();
        assert_eq!(kernel.unit.as_deref(), Some("kernel"));
        assert_eq!(kernel.transport.as_deref(), Some("kernel"));
        // Neither unit nor identifier.
        let bare = LogRecord::from_journal(
            &parse_line(br#"{"__CURSOR":"s=a;i=1","__REALTIME_TIMESTAMP":"5","MESSAGE":"x"}"#)
                .unwrap(),
        );
        assert_eq!(
            (bare.unit, bare.identifier, bare.severity),
            (None, None, None)
        );
    }

    #[test]
    fn long_journal_messages_stay_cut_with_the_flag() {
        let line = format!(
            r#"{{"__CURSOR":"s=a;i=1","__REALTIME_TIMESTAMP":"5","MESSAGE":"{}"}}"#,
            "ä".repeat(MAX_MESSAGE_BYTES)
        );
        let record = LogRecord::from_journal(&parse_line(line.as_bytes()).unwrap());
        assert!(record.truncated);
        assert!(record.message.len() <= MAX_MESSAGE_BYTES);
    }

    // The protocol accepts only these shapes; anything else from the host is dropped instead of
    // failing the whole batch on the server.
    #[test]
    fn values_of_the_wrong_shape_are_dropped() {
        let line = br#"{"__CURSOR":"s=a;i=1","__REALTIME_TIMESTAMP":"5","MESSAGE":"x",
            "_BOOT_ID":"CC8F81A4-86F8","_TRANSPORT":"std\"out","SYSLOG_FACILITY":"24"}"#;
        let record = LogRecord::from_journal(&parse_line(line).unwrap());
        assert_eq!(
            (record.boot_id, record.transport, record.facility),
            (None, None, None)
        );
        let line = br#"{"__CURSOR":"s=a;i=1","__REALTIME_TIMESTAMP":"5","MESSAGE":"x",
            "_BOOT_ID":"cc8f81a486f84386b00bc31030b4e1bb","_TRANSPORT":"stdout","SYSLOG_FACILITY":"23"}"#;
        let record = LogRecord::from_journal(&parse_line(line).unwrap());
        assert_eq!(
            (
                record.boot_id.as_deref(),
                record.transport.as_deref(),
                record.facility
            ),
            (
                Some("cc8f81a486f84386b00bc31030b4e1bb"),
                Some("stdout"),
                Some(23)
            )
        );
    }

    #[test]
    fn an_entry_of_a_plain_file_names_the_file() {
        let origin = Origin::file(Path::new("/var/log/nginx/error.log")).unwrap();
        let long = "x".repeat(MAX_MESSAGE_BYTES + 5);
        let record = LogRecord::from_plain_file(origin.clone(), 7, &long, false);
        assert_eq!(record.unit.as_deref(), Some("error.log"));
        assert_eq!(record.identifier, None);
        assert_eq!(record.severity, None);
        assert_eq!(record.at_us, 7);
        assert!(
            record.truncated && record.message.len() == MAX_MESSAGE_BYTES - crate::cut::TOKEN_BACK
        );
        assert_eq!(record.origin, origin);
        assert!(!LogRecord::from_plain_file(origin, 7, "short", false).truncated);
    }

    // M5-33: a token the cut goes through doesn't stay half: the redaction wouldn't know it.
    #[test]
    fn a_cut_leaves_no_half_token() {
        let secret = "AKIAIOSFODNN7EXAMPLE";
        let filler = "a ".repeat(MAX_MESSAGE_BYTES / 2 - 4);
        let text = format!("{filler}{secret} tail");
        assert!(text.find(secret).unwrap() < MAX_MESSAGE_BYTES);
        let origin = Origin::file(Path::new("/var/log/app.log")).unwrap();
        for record in [
            LogRecord::from_container(1, "web", false, &text),
            LogRecord::from_plain_file(origin, 1, &text, false),
        ] {
            assert!(record.truncated);
            assert!(
                !record.message.contains("AKIA"),
                "{:?}",
                &record.message[record.message.len() - 20..]
            );
        }
    }

    #[test]
    fn files_are_absolute_paths() {
        assert_eq!(
            Origin::file(Path::new("/var/log/messages")),
            Some(Origin::File("/var/log/messages".into()))
        );
        assert_eq!(Origin::file(Path::new("var/log/messages")), None);
        assert_eq!(Origin::file(Path::new("")), None);
        let long = format!("/{}", "a".repeat(MAX_PATH_BYTES));
        assert_eq!(Origin::file(Path::new(&long)), None);
        let longest = format!("/{}", "a".repeat(MAX_PATH_BYTES - 1));
        assert!(Origin::file(Path::new(&longest)).is_some());
    }

    fn file_records(name: &str, format: Format) -> Vec<LogRecord> {
        let time = LocalTime::new(None, 1_795_291_200_000_000);
        let origin = Origin::file(Path::new("/var/log").join(name).as_path()).unwrap();
        let data = fixture("alpine-3.22", name);
        lines(&data)
            .map(|line| {
                let text = String::from_utf8_lossy(line);
                let record = parse(format, &text, &time).unwrap();
                LogRecord::from_file(origin.clone(), &record, false)
            })
            .collect()
    }

    #[test]
    fn busybox_lines_keep_facility_and_severity() {
        let records = file_records("messages", Format::Syslog);
        let login = records
            .iter()
            .find(|r| r.message.starts_with("Accepted password for alice"))
            .unwrap();
        assert_eq!(login.origin, Origin::File("/var/log/messages".into()));
        assert_eq!(login.severity, Some(Severity::Info));
        assert_eq!(login.facility, Some(4));
        assert_eq!(login.identifier.as_deref(), Some("sshd-session"));
        assert_eq!(login.unit, login.identifier, "files know no units");
        assert!(login.pid.is_some());
        assert_eq!((login.uid, login.boot_id.as_ref()), (None, None));
    }

    #[test]
    fn trusted_lines_keep_the_sender() {
        let records = file_records("security.log", Format::Trusted);
        let root = records
            .iter()
            .find(|r| r.message.starts_with("Failed password for alice"))
            .unwrap();
        assert_eq!(root.uid, Some(0));
        assert_eq!(root.severity, None, "the template has no priority");
        assert_eq!(root.transport, None);
        let time = LocalTime::new(None, 0);
        let origin = Origin::file(Path::new("/var/log/sermon/security.log")).unwrap();
        let kernel = parse(
            Format::Trusted,
            "2026-11-21T19:34:29+00:00 imklog uid= kernel: Out of memory: Killed process 42 (stress)",
            &time,
        )
        .unwrap();
        let kernel = LogRecord::from_file(origin, &kernel, true);
        assert_eq!(kernel.transport.as_deref(), Some("kernel"));
        assert_eq!(kernel.uid, Some(0));
        assert!(kernel.truncated);
    }

    fn record(message: &str) -> LogRecord {
        LogRecord {
            at_us: 1,
            origin: Origin::Journal,
            unit: Some("nginx.service".into()),
            identifier: Some("nginx".into()),
            severity: None,
            message: message.into(),
            truncated: false,
            pid: None,
            process: None,
            uid: None,
            boot_id: None,
            transport: None,
            facility: None,
        }
    }

    #[test]
    fn the_size_counts_the_texts() {
        let small = record("ab");
        let large = record(&"x".repeat(1000));
        assert_eq!(large.size() - small.size(), 998);
        let mut in_file = small.clone();
        in_file.origin = Origin::File("/var/log/nginx/error.log".into());
        assert_eq!(in_file.size() - small.size(), 24);
        assert!(small.size() > 2 + 13 + 5, "a share for the other fields");
    }

    #[test]
    fn redaction_keeps_the_message_within_its_bound() {
        let mut grown = record(&"a".repeat(MAX_MESSAGE_BYTES));
        grown.redact_with(|text: &str| Cow::Owned(text.replace('a', "***")));
        assert!(grown.truncated, "cut again after the redaction");
        assert_eq!(grown.message.len(), MAX_MESSAGE_BYTES);
        assert!(grown.message.bytes().all(|b| b == b'*'));

        let mut short = record("password=x");
        short.redact_with(|_: &str| Cow::Borrowed("password=***"));
        assert_eq!(short.message, "password=***");
        assert!(!short.truncated);

        let mut untouched = record("nothing");
        untouched.redact_with(|text: &str| Cow::Borrowed(text));
        assert_eq!(
            (untouched.message.as_str(), untouched.truncated),
            ("nothing", false)
        );

        // Only the message: units and identifiers are names, not texts.
        let mut named = record("x");
        named.redact_with(|_: &str| Cow::Borrowed("y"));
        assert_eq!(named.unit.as_deref(), Some("nginx.service"));
    }

    #[test]
    fn batches_stay_within_count_and_bytes() {
        let mut batcher = Batcher::default();
        assert!(batcher.is_empty());
        let mut full = Vec::new();
        for _ in 0..2500 {
            if let Some(done) = batcher.push(record("x")) {
                full.push(done);
            }
        }
        full.push(batcher.take());
        assert!(batcher.is_empty() && batcher.take().is_empty());
        assert_eq!(
            full.iter().map(Vec::len).collect::<Vec<_>>(),
            [1000, 1000, 500]
        );

        let big = record(&"y".repeat(MAX_MESSAGE_BYTES));
        let per_batch = MAX_BATCH_BYTES / big.size();
        let mut sizes = Vec::new();
        for _ in 0..40 {
            if let Some(batch) = batcher.push(big.clone()) {
                assert!(batch.iter().map(LogRecord::size).sum::<usize>() <= MAX_BATCH_BYTES);
                sizes.push(batch.len());
            }
        }
        assert_eq!(sizes, [per_batch, per_batch]);
        assert_eq!(batcher.take().len(), 40 - 2 * per_batch);
    }

    proptest! {
        #[test]
        fn batches_never_exceed_the_limits(lengths in proptest::collection::vec(0usize..=MAX_MESSAGE_BYTES, 1..200)) {
            let mut batcher = Batcher::default();
            let mut total = 0;
            let mut check = |batch: Vec<LogRecord>| {
                prop_assert!(!batch.is_empty() && batch.len() <= MAX_BATCH_RECORDS);
                prop_assert!(batch.iter().map(LogRecord::size).sum::<usize>() <= MAX_BATCH_BYTES);
                total += batch.len();
                Ok(())
            };
            for length in &lengths {
                if let Some(batch) = batcher.push(record(&"z".repeat(*length))) {
                    check(batch)?;
                }
            }
            check(batcher.take())?;
            prop_assert_eq!(total, lengths.len());
        }

        #[test]
        fn journal_records_always_fit_the_protocol(
            message in "\\PC{0,64}",
            unit in "\\PC{0,300}",
            boot in "[ -~]{0,40}",
            transport in "[ -~]{0,40}",
            facility in any::<u16>(),
        ) {
            let line = serde_json::json!({
                "__CURSOR": "s=a;i=1", "__REALTIME_TIMESTAMP": "1", "MESSAGE": message,
                "_SYSTEMD_UNIT": unit, "_BOOT_ID": boot, "_TRANSPORT": transport,
                "SYSLOG_FACILITY": facility.to_string(),
            });
            let record = LogRecord::from_journal(&parse_line(line.to_string().as_bytes()).unwrap());
            prop_assert!(record.unit.as_ref().is_none_or(|u| u.len() <= MAX_FIELD_BYTES));
            prop_assert!(record.boot_id.as_ref().is_none_or(|b| b.len() == 32
                && b.bytes().all(|c| c.is_ascii_digit() || (b'a'..=b'f').contains(&c))));
            prop_assert!(record.transport.as_ref().is_none_or(|t| t.len() <= 32
                && t.bytes().all(|c| c.is_ascii_lowercase() || c == b'_' || c == b'-')));
            prop_assert!(record.facility.is_none_or(|f| f <= 23));
        }
    }
}
