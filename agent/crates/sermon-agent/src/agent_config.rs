//! The configuration the server sends (ADR 0030, M3-03): checked completely, stored atomically
//! in the state directory and published to the log pipeline (M3-04).
//!
//! - Nothing from the server is trusted: the agent checks the bounds of the protocol itself and
//!   compiles every regex (Rust `regex`, linear time, bounded size). A configuration that fails
//!   is not used; the previous one stays and the server is told why.
//! - The local veto wins: with `allow_logs = false` no logs are sent, whatever the server says.
//! - Until a configuration arrives, no logs are sent: a server that sends none gets none.

use std::collections::BTreeSet;
use std::fs;
use std::io;
use std::path::{Path, PathBuf};
use std::sync::Arc;

use prost::Message;
use regex::{Regex, RegexBuilder};
use sermon_logs::record::Severity;
use sermon_security::{Redactor, RuleSpec};
use sermon_transport::proto::agent::v1::{ConfigApplied, ConfigUpdate, LogFileGlob};
use sermon_transport::session::ConfigSink;
use tokio::sync::watch;

/// Bounds of the protocol (`LogConfig`, `LogFileGlob`), checked again here.
const MAX_UNITS: usize = 64;
const MAX_UNIT_BYTES: usize = 256;
const MAX_FILES: usize = 16;
const MAX_GLOB_BYTES: usize = 1024;
const MAX_REGEX_BYTES: usize = 1024;
/// Memory a compiled multiline regex may take.
const REGEX_SIZE_LIMIT: usize = 1024 * 1024;
/// Longest reason sent back to the server.
const MAX_ERROR_BYTES: usize = 1024;

/// A file glob of the configuration (M3-05 reads them).
#[derive(Debug, Clone)]
pub struct FileSpec {
    /// The absolute pattern.
    pub pattern: String,
    /// The first line of an entry; `None`: every line is one.
    pub multiline_start: Option<Regex>,
}

/// Which log entries to send.
#[derive(Debug, Clone, Default)]
pub struct LogSelection {
    /// Send logs at all: the server's choice and not vetoed locally.
    pub enabled: bool,
    /// Drop entries less severe than this; entries without a severity are kept.
    pub min_severity: Option<Severity>,
    /// Units and identifiers whose entries are not sent.
    pub exclude_units: BTreeSet<String>,
    /// Log files besides the journal.
    pub files: Vec<FileSpec>,
}

impl LogSelection {
    /// Whether an entry of `unit` with `severity` is sent.
    #[must_use]
    pub fn sends(&self, unit: Option<&str>, severity: Option<Severity>) -> bool {
        self.enabled
            && unit.is_none_or(|u| !self.exclude_units.contains(u))
            && match (self.min_severity, severity) {
                (Some(min), Some(s)) => s <= min,
                _ => true,
            }
    }
}

/// The configuration in use.
#[derive(Debug, Clone, Default)]
pub struct Active {
    /// The version from the server; 0 before the first one.
    pub version: u64,
    /// Which logs to send.
    pub logs: LogSelection,
    /// The built-in patterns and the rules of the organization.
    pub redactor: Redactor,
}

/// Checks `update` and builds what the agent uses; the error is the reason for the server.
///
/// # Errors
///
/// A text naming the first problem.
pub fn compile(update: &ConfigUpdate, allow_logs: bool) -> Result<Active, String> {
    let config = update.config.as_ref().ok_or("the configuration is empty")?;
    let logs = config.logs.as_ref().ok_or("the log settings are missing")?;
    let min_severity = match u8::try_from(logs.min_severity) {
        Ok(0) => None,
        Ok(code @ 1..=8) => Severity::from_priority(code - 1),
        _ => return Err(format!("unknown severity {}", logs.min_severity)),
    };
    if logs.exclude_units.len() > MAX_UNITS {
        return Err(format!("more than {MAX_UNITS} excluded units"));
    }
    for (i, unit) in logs.exclude_units.iter().enumerate() {
        if unit.is_empty()
            || unit.len() > MAX_UNIT_BYTES
            || !unit
                .bytes()
                .all(|b| b.is_ascii_alphanumeric() || b"@:._\\-".contains(&b))
        {
            return Err(format!("excluded unit {}: not a unit name", i + 1));
        }
    }
    if logs.files.len() > MAX_FILES {
        return Err(format!("more than {MAX_FILES} file patterns"));
    }
    let files = logs
        .files
        .iter()
        .enumerate()
        .map(|(i, glob)| {
            file_spec(glob).map_err(|reason| format!("file pattern {}: {reason}", i + 1))
        })
        .collect::<Result<Vec<_>, _>>()?;
    let rules: Vec<RuleSpec> = config
        .redaction_rules
        .iter()
        .map(|r| RuleSpec {
            pattern: r.pattern.clone(),
        })
        .collect();
    // RuleError counts from 0, the UI from 1.
    let redactor = Redactor::with_rules(&rules).map_err(|error| match error {
        sermon_security::redact::RuleError::Invalid { index, reason } => {
            format!("rule {}: {reason}", index + 1)
        }
        other => other.to_string(),
    })?;
    Ok(Active {
        version: update.version,
        logs: LogSelection {
            enabled: logs.enabled && allow_logs,
            min_severity,
            exclude_units: logs.exclude_units.iter().cloned().collect(),
            files,
        },
        redactor,
    })
}

fn file_spec(glob: &LogFileGlob) -> Result<FileSpec, String> {
    let p = &glob.pattern;
    if p.len() < 2 || p.len() > MAX_GLOB_BYTES || !p.starts_with('/') || p.contains('\0') {
        return Err("not an absolute path".into());
    }
    if p.contains("/../") || p.ends_with("/..") {
        return Err("contains .. segments".into());
    }
    if glob.multiline_start.len() > MAX_REGEX_BYTES {
        return Err("the multiline regex is too long".into());
    }
    let multiline_start = (!glob.multiline_start.is_empty())
        .then(|| {
            RegexBuilder::new(&glob.multiline_start)
                .size_limit(REGEX_SIZE_LIMIT)
                .dfa_size_limit(REGEX_SIZE_LIMIT)
                .build()
                .map_err(|error| format!("multiline regex: {error}"))
        })
        .transpose()?;
    Ok(FileSpec {
        pattern: p.clone(),
        multiline_start,
    })
}

/// Holds the configuration in use, stores what the server sends and tells the log pipeline.
#[derive(Debug)]
pub struct ConfigStore {
    file: PathBuf,
    allow_logs: bool,
    active: watch::Sender<Arc<Active>>,
}

impl ConfigStore {
    /// The store with the configuration from `file`. A missing, damaged or no longer valid file
    /// starts without one (version 0): the server sends its configuration again.
    #[must_use]
    pub fn open(file: PathBuf, allow_logs: bool) -> Self {
        let stored = match fs::read(&file) {
            Ok(bytes) => ConfigUpdate::decode(bytes.as_slice())
                .map_err(|error| error.to_string())
                .and_then(|update| compile(&update, allow_logs)),
            Err(error) if error.kind() == io::ErrorKind::NotFound => Ok(Active::default()),
            Err(error) => Err(error.to_string()),
        };
        let active = stored.unwrap_or_else(|reason| {
            tracing::warn!(
                file = %file.display(),
                "ignoring the stored configuration, the server sends it again: {reason}"
            );
            Active::default()
        });
        let (active, _) = watch::channel(Arc::new(active));
        Self {
            file,
            allow_logs,
            active,
        }
    }

    /// The configuration in use.
    #[must_use]
    pub fn current(&self) -> Arc<Active> {
        self.active.borrow().clone()
    }

    /// Learns about every new configuration.
    #[must_use]
    pub fn subscribe(&self) -> watch::Receiver<Arc<Active>> {
        self.active.subscribe()
    }

    fn take(&self, update: &ConfigUpdate) -> Result<(), String> {
        let active = compile(update, self.allow_logs)?;
        store(&self.file, &update.encode_to_vec())
            .map_err(|error| format!("cannot store the configuration: {error}"))?;
        self.active.send_replace(Arc::new(active));
        Ok(())
    }
}

impl ConfigSink for ConfigStore {
    fn version(&self) -> u64 {
        self.current().version
    }

    fn apply(&self, update: ConfigUpdate) -> ConfigApplied {
        let error = self.take(&update).err().unwrap_or_default();
        ConfigApplied {
            version: update.version,
            error: cut(error, MAX_ERROR_BYTES),
            logs_vetoed: !self.allow_logs,
        }
    }
}

/// Writes `bytes` to `file` atomically with mode 0600.
fn store(file: &Path, bytes: &[u8]) -> io::Result<()> {
    let directory = file.parent().unwrap_or_else(|| Path::new("."));
    let temporary = file.with_extension("tmp");
    let result = (|| {
        let mut options = fs::OpenOptions::new();
        options.write(true).create(true).truncate(true);
        std::os::unix::fs::OpenOptionsExt::mode(&mut options, 0o600);
        let mut out = options.open(&temporary)?;
        io::Write::write_all(&mut out, bytes)?;
        out.sync_all()?;
        fs::rename(&temporary, file)?;
        fs::File::open(directory)?.sync_all()
    })();
    if result.is_err() {
        let _ = fs::remove_file(&temporary);
    }
    result
}

fn cut(mut text: String, max: usize) -> String {
    if text.len() > max {
        let mut end = max;
        while !text.is_char_boundary(end) {
            end -= 1;
        }
        text.truncate(end);
    }
    text
}

#[cfg(test)]
mod tests {
    use std::os::unix::fs::PermissionsExt;

    use sermon_transport::proto::agent::v1::{AgentConfig, LogConfig, LogSeverity, RedactionRule};

    use super::*;

    fn update(version: u64, change: impl FnOnce(&mut AgentConfig)) -> ConfigUpdate {
        let mut config = AgentConfig {
            logs: Some(LogConfig {
                enabled: true,
                min_severity: LogSeverity::Warning.into(),
                exclude_units: vec!["cron.service".into(), "user@1000.service".into()],
                files: vec![LogFileGlob {
                    pattern: "/var/log/nginx/*.log".into(),
                    multiline_start: r"^\d{4}-".into(),
                }],
            }),
            redaction_rules: vec![RedactionRule {
                pattern: r"customer-(?P<secret>\d{6})".into(),
            }],
        };
        change(&mut config);
        ConfigUpdate {
            version,
            config: Some(config),
        }
    }

    #[test]
    fn compiles_every_part_of_a_valid_configuration() {
        let active = compile(&update(7, |_| {}), true).unwrap();
        assert_eq!(active.version, 7);
        assert!(active.logs.enabled);
        assert_eq!(active.logs.min_severity, Some(Severity::Warning));
        assert!(active.logs.exclude_units.contains("cron.service"));
        let file = &active.logs.files[0];
        assert_eq!(file.pattern, "/var/log/nginx/*.log");
        assert!(
            file.multiline_start
                .as_ref()
                .unwrap()
                .is_match("2026-11-23 x")
        );
        assert_eq!(
            active.redactor.redact("order customer-123456 password=x"),
            "order customer-*** password=***"
        );
    }

    #[test]
    fn the_local_veto_switches_logs_off() {
        let active = compile(&update(1, |_| {}), false).unwrap();
        assert!(!active.logs.enabled);
        assert!(
            !active
                .logs
                .sends(Some("nginx.service"), Some(Severity::Error))
        );
    }

    #[test]
    fn selects_entries_by_unit_and_severity() {
        let logs = compile(&update(1, |_| {}), true).unwrap().logs;
        assert!(logs.sends(Some("nginx.service"), Some(Severity::Error)));
        assert!(logs.sends(Some("nginx.service"), Some(Severity::Warning)));
        assert!(!logs.sends(Some("nginx.service"), Some(Severity::Notice)));
        assert!(
            logs.sends(None, None),
            "entries without unit or severity stay"
        );
        assert!(!logs.sends(Some("cron.service"), Some(Severity::Emergency)));
        assert!(
            !LogSelection::default().sends(None, None),
            "nothing before the first configuration"
        );
    }

    #[test]
    fn rejects_what_the_protocol_does_not_allow() {
        let cases: Vec<(&str, ConfigUpdate)> = vec![
            (
                "empty",
                ConfigUpdate {
                    version: 1,
                    config: None,
                },
            ),
            ("log settings", update(1, |c| c.logs = None)),
            (
                "severity",
                update(1, |c| c.logs.as_mut().unwrap().min_severity = 9),
            ),
            (
                "unit",
                update(1, |c| {
                    c.logs.as_mut().unwrap().exclude_units = vec!["a b".into()];
                }),
            ),
            (
                "unit",
                update(1, |c| {
                    c.logs.as_mut().unwrap().exclude_units = vec![String::new()];
                }),
            ),
            (
                "excluded units",
                update(1, |c| {
                    c.logs.as_mut().unwrap().exclude_units = vec!["u".into(); 65];
                }),
            ),
            (
                "file pattern 1",
                update(1, |c| {
                    c.logs.as_mut().unwrap().files[0].pattern = "var/log/x".into();
                }),
            ),
            (
                "file pattern 1",
                update(1, |c| {
                    c.logs.as_mut().unwrap().files[0].pattern = "/var/log/../../etc/shadow".into();
                }),
            ),
            (
                "file pattern 1",
                update(1, |c| {
                    c.logs.as_mut().unwrap().files[0].pattern = "/var/log/..".into();
                }),
            ),
            (
                "multiline",
                update(1, |c| {
                    c.logs.as_mut().unwrap().files[0].multiline_start = "(unclosed".into();
                }),
            ),
            (
                "multiline",
                update(1, |c| {
                    c.logs.as_mut().unwrap().files[0].multiline_start = r"\w{100}".into();
                }),
            ),
            (
                "file patterns",
                update(1, |c| {
                    let glob = c.logs.as_ref().unwrap().files[0].clone();
                    c.logs.as_mut().unwrap().files = vec![glob; 17];
                }),
            ),
            (
                "rule 2",
                update(1, |c| {
                    c.redaction_rules.push(RedactionRule {
                        pattern: "(?<!x)y".into(),
                    });
                }),
            ),
            (
                "rules",
                update(1, |c| {
                    c.redaction_rules = vec![
                        RedactionRule {
                            pattern: "a".into()
                        };
                        33
                    ];
                }),
            ),
        ];
        for (want, case) in cases {
            let error = compile(&case, true).unwrap_err();
            assert!(error.contains(want), "{want}: {error}");
        }
    }

    #[test]
    fn stores_applies_and_survives_a_restart() {
        let dir = tempfile::tempdir().unwrap();
        let file = dir.path().join("agent-config.pb");
        let store = ConfigStore::open(file.clone(), true);
        assert_eq!(store.version(), 0);
        let mut seen = store.subscribe();

        let answer = store.apply(update(3, |_| {}));
        assert_eq!(
            (answer.version, answer.error.as_str(), answer.logs_vetoed),
            (3, "", false)
        );
        assert_eq!(store.version(), 3);
        assert!(seen.has_changed().unwrap());
        assert_eq!(seen.borrow_and_update().version, 3);
        assert_eq!(
            fs::metadata(&file).unwrap().permissions().mode() & 0o777,
            0o600
        );

        let restarted = ConfigStore::open(file.clone(), true);
        assert_eq!(restarted.version(), 3);
        assert_eq!(
            restarted.current().logs.min_severity,
            Some(Severity::Warning)
        );
    }

    #[test]
    fn a_rejected_configuration_keeps_the_previous_one() {
        let dir = tempfile::tempdir().unwrap();
        let file = dir.path().join("agent-config.pb");
        let store = ConfigStore::open(file.clone(), false);
        store.apply(update(3, |_| {}));
        let stored = fs::read(&file).unwrap();
        let seen = store.subscribe();

        let answer = store.apply(update(4, |c| {
            c.redaction_rules = vec![RedactionRule {
                pattern: "(?=x)".into(),
            }];
        }));
        assert_eq!(answer.version, 4);
        assert!(answer.error.starts_with("rule 1:"), "{}", answer.error);
        assert!(answer.logs_vetoed, "the veto is reported with every answer");
        assert_eq!(store.version(), 3);
        assert_eq!(
            fs::read(&file).unwrap(),
            stored,
            "the stored file is unchanged"
        );
        assert!(!seen.has_changed().unwrap());
    }

    #[test]
    fn a_damaged_file_starts_without_a_configuration() {
        let dir = tempfile::tempdir().unwrap();
        let file = dir.path().join("agent-config.pb");
        fs::write(&file, b"\xff\xff\xff not protobuf").unwrap();
        assert_eq!(ConfigStore::open(file.clone(), true).version(), 0);
        // Stored by an older agent but invalid for this one: the same.
        fs::write(&file, update(5, |c| c.logs = None).encode_to_vec()).unwrap();
        assert_eq!(ConfigStore::open(file, true).version(), 0);
    }

    #[test]
    fn long_reasons_are_cut() {
        assert_eq!(cut("ä".repeat(600), MAX_ERROR_BYTES).len(), MAX_ERROR_BYTES);
    }
}
