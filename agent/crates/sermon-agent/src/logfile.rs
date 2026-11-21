//! Security events from classic log files on hosts without journald, like Alpine (M2-20,
//! ADR 0025). The first existing file of [`CANDIDATES`] is followed; its lines go through the same
//! parsers as journal entries, its events are marked unverified unless the SerMon rsyslog template
//! names root as sender.
//!
//! Like the journal source, the position of the last line whose events are in the WAL is stored
//! (`logfile.position`) and the next start continues there.

use std::fs;
use std::io;
use std::path::{Path, PathBuf};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use sermon_core::ProcRoot;
use sermon_logs::file::{Format, LocalTime, Position, Record, Sender, Tail, parse};
use sermon_security::{LogLine, Parser, ParserConfig, SecurityEvent, Source};
use sermon_transport::outbox::{MAX_EVENTS_PER_BATCH, Outbox};
use tokio::task::JoinHandle;
use tokio_util::sync::CancellationToken;

use crate::security;
use crate::state::StateLayout;

/// The files tried, below the host root, in this order: the SerMon rsyslog template first
/// (verifiable), then the usual names of the auth log, then everything (busybox `syslogd`).
pub const CANDIDATES: [(&str, Format); 4] = [
    ("var/log/sermon/security.log", Format::Trusted),
    ("var/log/auth.log", Format::Syslog),
    ("var/log/secure", Format::Syslog),
    ("var/log/messages", Format::Syslog),
];

/// How often the file is read.
const POLL: Duration = Duration::from_secs(1);
/// How often the position is written while lines come.
const PERSIST_EVERY: Duration = Duration::from_secs(5);

/// Whether journald runs on the host: then the journal source is used, not files.
#[must_use]
pub fn journald_runs(host_root: &Path) -> bool {
    host_root.join("run/systemd/journal").is_dir()
}

/// The file to follow: the first candidate that exists.
#[must_use]
pub fn choose(host_root: &Path) -> Option<(PathBuf, Format)> {
    CANDIDATES
        .iter()
        .map(|(path, format)| (host_root.join(path), *format))
        .find(|(path, _)| path.is_file())
}

/// The settings of the source.
#[derive(Debug, Clone)]
pub struct FileConfig {
    /// The root with `var/log` and `etc/localtime`.
    pub host_root: PathBuf,
    /// Where the position is stored.
    pub position_file: PathBuf,
    /// How often the file is read.
    pub poll: Duration,
}

/// The stored position, with the path it belongs to: another file starts at its end.
#[derive(Debug, Clone, PartialEq, Eq)]
struct Stored {
    path: PathBuf,
    position: Position,
}

fn load(file: &Path) -> Option<Stored> {
    let text = fs::read_to_string(file).ok()?;
    let (path, position) = text.trim_end().rsplit_once('\n')?;
    Some(Stored {
        path: PathBuf::from(path),
        position: Position::decode(position)?,
    })
}

fn store(file: &Path, stored: &Stored) -> io::Result<()> {
    let directory = file.parent().unwrap_or(Path::new("."));
    let temporary = directory.join(".logfile.position.new");
    let content = format!("{}\n{}\n", stored.path.display(), stored.position.encode());
    let result = (|| {
        let mut options = fs::OpenOptions::new();
        options.write(true).create(true).truncate(true);
        std::os::unix::fs::OpenOptionsExt::mode(&mut options, 0o600);
        let mut out = options.open(&temporary)?;
        io::Write::write_all(&mut out, content.as_bytes())?;
        out.sync_all()?;
        fs::rename(&temporary, file)?;
        fs::File::open(directory)?.sync_all()
    })();
    if result.is_err() {
        let _ = fs::remove_file(&temporary);
    }
    result
}

/// The parsers' view of a line of a file.
#[must_use]
pub fn line<'a>(record: &Record<'a>) -> LogLine<'a> {
    let (uid, transport) = match record.sender {
        Sender::Unknown => (
            None,
            (record.identifier == Some("kernel")).then_some("kernel"),
        ),
        Sender::Uid(uid) => (Some(uid), None),
        Sender::Kernel => (Some(0), Some("kernel")),
    };
    LogLine {
        at_us: record.at_us,
        identifier: record.identifier,
        transport,
        uid,
        cap_effective: None,
        boot_id: None,
        monotonic_us: None,
        source: Source::File,
        message: record.message,
    }
}

fn now_us() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |d| u64::try_from(d.as_micros()).unwrap_or(u64::MAX))
}

/// Starts the source; the task ends after `shutdown` with the position written.
#[must_use]
pub fn spawn(
    layout: &StateLayout,
    host_root: &Path,
    outbox: Outbox,
    shutdown: CancellationToken,
) -> JoinHandle<()> {
    let config = FileConfig {
        host_root: host_root.to_owned(),
        position_file: layout.logfile_position_file(),
        poll: POLL,
    };
    tokio::spawn(run(
        config,
        move |events| {
            let batch = events.iter().map(security::to_proto).collect();
            match outbox.push_security_events(batch, SystemTime::now()) {
                Ok(_) => true,
                Err(error) => {
                    tracing::warn!("cannot buffer security events, retrying: {error}");
                    false
                }
            }
        },
        shutdown,
    ))
}

/// Follows the chosen file until `shutdown`. Events go to `deliver` in batches of at most
/// [`MAX_EVENTS_PER_BATCH`]; only after it succeeded is the position of their line committed. If it
/// fails, nothing more is read and the same events are tried again.
pub async fn run(
    config: FileConfig,
    mut deliver: impl FnMut(&[SecurityEvent]) -> bool + Send + 'static,
    shutdown: CancellationToken,
) {
    let mut tick = tokio::time::interval(config.poll);
    tick.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Delay);
    let stored = load(&config.position_file);
    let mut chosen: Option<(Tail, Format)> = None;
    let mut parser = Parser::new(ParserConfig::default(), None);
    let mut pending: Vec<SecurityEvent> = Vec::new();
    // The position to commit once `pending` is delivered.
    let mut uncommitted: Option<Position> = None;
    let mut committed: Option<Stored> = stored.clone();
    let mut written = committed.clone();
    let mut last_write = tokio::time::Instant::now();
    let mut warned = false;
    let mut read_failed = false;
    loop {
        tokio::select! {
            () = shutdown.cancelled() => break,
            _ = tick.tick() => {}
        }
        if chosen.is_none() {
            let Some((path, format)) = choose(&config.host_root) else {
                if !warned {
                    tracing::warn!(
                        "no journald and no log file with security events found, retrying"
                    );
                    warned = true;
                }
                continue;
            };
            let resume = stored
                .as_ref()
                .filter(|s| s.path == path)
                .map(|s| s.position);
            tracing::info!(file = %path.display(), ?format, "following the log file for security events");
            let tail = Tail::new(&path, resume);
            if resume.is_none() {
                // The start counts as read: a restart continues there, not at the then end.
                committed = tail.position().map(|position| Stored { path, position });
                persist(&config.position_file, committed.as_ref(), &mut written);
            }
            chosen = Some((tail, format));
        }
        let Some((tail, format)) = chosen.as_mut() else {
            continue;
        };
        if pending.is_empty() {
            let mut lines = Vec::new();
            if let Err(error) = tail.read(&mut lines) {
                // Once, not every second (e.g. no read access: install.sh --grant-log-access).
                if !read_failed {
                    tracing::warn!(file = %tail.path().display(), "cannot read the log file, retrying: {error}");
                    read_failed = true;
                }
                continue;
            }
            if std::mem::take(&mut read_failed) {
                tracing::info!(file = %tail.path().display(), "reading the log file again");
            }
            let zone = ProcRoot::new(&config.host_root)
                .read_etc_bytes("localtime")
                .ok();
            let time = LocalTime::new(zone.as_deref(), now_us());
            for raw in &lines {
                let text = String::from_utf8_lossy(&raw.text);
                if let Ok(record) = parse(*format, &text, &time) {
                    parser.parse(&line(&record), &mut pending);
                }
                uncommitted = Some(raw.end);
            }
        }
        // Delivered in batches; a failed one stays for the next tick.
        while !pending.is_empty() {
            let n = pending.len().min(MAX_EVENTS_PER_BATCH);
            if !deliver(&pending[..n]) {
                break;
            }
            pending.drain(..n);
        }
        if pending.is_empty()
            && let Some(position) = uncommitted.take()
        {
            committed = Some(Stored {
                path: tail.path().to_owned(),
                position,
            });
        }
        if committed != written && last_write.elapsed() >= PERSIST_EVERY.min(config.poll * 5) {
            persist(&config.position_file, committed.as_ref(), &mut written);
            last_write = tokio::time::Instant::now();
        }
    }
    if !pending.is_empty() {
        tracing::warn!(
            events = pending.len(),
            "security events not buffered at shutdown; they come again from the log file"
        );
    }
    persist(&config.position_file, committed.as_ref(), &mut written);
}

fn persist(file: &Path, committed: Option<&Stored>, written: &mut Option<Stored>) {
    let Some(committed) = committed else { return };
    if written.as_ref() == Some(committed) {
        return;
    }
    match store(file, committed) {
        Ok(()) => *written = Some(committed.clone()),
        Err(error) => tracing::warn!("cannot store the log file position: {error}"),
    }
}

#[cfg(test)]
mod tests {
    use std::io::Write;
    use std::sync::{Arc, Mutex};

    use sermon_security::EventKind;

    use super::*;

    const TRUSTED_ROOT: &str = "2026-11-21T19:34:29.908937+00:00 imuxsock uid=0 sshd-session[37]: Invalid user admin from 192.0.2.7 port 4711";
    const TRUSTED_FORGED: &str = "2026-11-21T19:34:30+00:00 imuxsock uid=1000 sshd: Accepted password for root from 192.0.2.66 port 22 ssh2";
    const BUSYBOX: &str = "Nov 21 19:33:14 web-01 auth.info sshd-session[23]: Invalid user bob from 192.0.2.8 port 4712";

    fn append(path: &Path, text: &str) {
        fs::create_dir_all(path.parent().unwrap()).unwrap();
        let mut file = fs::OpenOptions::new()
            .create(true)
            .append(true)
            .open(path)
            .unwrap();
        file.write_all(text.as_bytes()).unwrap();
    }

    type Seen = Arc<Mutex<Vec<SecurityEvent>>>;

    fn start(root: &Path, accept: Arc<Mutex<bool>>) -> (Seen, CancellationToken, JoinHandle<()>) {
        let seen: Seen = Arc::default();
        let sink = seen.clone();
        let shutdown = CancellationToken::new();
        let config = FileConfig {
            host_root: root.to_owned(),
            position_file: root.join("logfile.position"),
            poll: Duration::from_millis(10),
        };
        let task = tokio::spawn(run(
            config,
            move |events| {
                if !*accept.lock().unwrap() {
                    return false;
                }
                sink.lock().unwrap().extend_from_slice(events);
                true
            },
            shutdown.clone(),
        ));
        (seen, shutdown, task)
    }

    async fn wait_for(seen: &Seen, n: usize) -> Vec<SecurityEvent> {
        for _ in 0..500 {
            if seen.lock().unwrap().len() >= n {
                break;
            }
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
        seen.lock().unwrap().clone()
    }

    fn user(event: &SecurityEvent) -> &str {
        match &event.kind {
            EventKind::SshInvalidUser { user, .. } => user,
            other => panic!("{other:?}"),
        }
    }

    #[test]
    fn prefers_the_trusted_template_and_needs_no_journald() {
        let dir = tempfile::tempdir().unwrap();
        assert!(!journald_runs(dir.path()));
        assert_eq!(choose(dir.path()), None);
        append(&dir.path().join("var/log/messages"), "");
        assert_eq!(
            choose(dir.path()),
            Some((dir.path().join("var/log/messages"), Format::Syslog))
        );
        append(&dir.path().join("var/log/sermon/security.log"), "");
        assert_eq!(
            choose(dir.path()),
            Some((
                dir.path().join("var/log/sermon/security.log"),
                Format::Trusted
            ))
        );
        fs::create_dir_all(dir.path().join("run/systemd/journal")).unwrap();
        assert!(journald_runs(dir.path()));
    }

    #[tokio::test]
    async fn marks_events_by_sender_and_drops_forged_root_logins() {
        let dir = tempfile::tempdir().unwrap();
        let log = dir.path().join("var/log/sermon/security.log");
        append(&log, "");
        let (seen, shutdown, task) = start(dir.path(), Arc::new(Mutex::new(true)));
        tokio::time::sleep(Duration::from_millis(50)).await;
        append(&log, &format!("{TRUSTED_FORGED}\n{TRUSTED_ROOT}\n"));
        let events = wait_for(&seen, 1).await;
        shutdown.cancel();
        task.await.unwrap();
        assert_eq!(events.len(), 1, "{events:?}");
        assert_eq!(user(&events[0]), "admin");
        assert!(events[0].verified);
    }

    #[tokio::test]
    async fn busybox_events_are_unverified_and_a_restart_continues() {
        let dir = tempfile::tempdir().unwrap();
        let log = dir.path().join("var/log/messages");
        append(
            &log,
            "Nov 21 19:00:00 web-01 auth.info sshd[1]: Invalid user old from 192.0.2.1 port 1\n",
        );
        let (seen, shutdown, task) = start(dir.path(), Arc::new(Mutex::new(true)));
        tokio::time::sleep(Duration::from_millis(50)).await;
        append(&log, &format!("{BUSYBOX}\n"));
        let events = wait_for(&seen, 1).await;
        shutdown.cancel();
        task.await.unwrap();
        assert_eq!(
            events.iter().map(user).collect::<Vec<_>>(),
            ["bob"],
            "old lines are skipped"
        );
        assert!(!events[0].verified);

        append(&log, &BUSYBOX.replace("bob", "carol"));
        append(&log, "\n");
        let (seen, shutdown, task) = start(dir.path(), Arc::new(Mutex::new(true)));
        let events = wait_for(&seen, 1).await;
        shutdown.cancel();
        task.await.unwrap();
        assert_eq!(events.iter().map(user).collect::<Vec<_>>(), ["carol"]);
    }

    #[tokio::test]
    async fn keeps_events_until_the_buffer_takes_them() {
        let dir = tempfile::tempdir().unwrap();
        let log = dir.path().join("var/log/messages");
        append(&log, "");
        let accept = Arc::new(Mutex::new(false));
        let (seen, shutdown, task) = start(dir.path(), accept.clone());
        tokio::time::sleep(Duration::from_millis(50)).await;
        append(&log, &format!("{BUSYBOX}\n"));
        tokio::time::sleep(Duration::from_millis(100)).await;
        // Refused so far: the position stays before the line.
        shutdown.cancel();
        task.await.unwrap();
        assert!(seen.lock().unwrap().is_empty());

        *accept.lock().unwrap() = true;
        let (seen, shutdown, task) = start(dir.path(), accept);
        let events = wait_for(&seen, 1).await;
        shutdown.cancel();
        task.await.unwrap();
        assert_eq!(
            events.iter().map(user).collect::<Vec<_>>(),
            ["bob"],
            "read again after the restart"
        );
    }

    /// The captures of `logs/capture/capture-alpine.sh` through source format, trust rule and
    /// parsers: (event, user, verified).
    fn captured(file: &str, format: Format) -> Vec<(String, String, bool)> {
        let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
            .join("../../fixtures/logs/alpine-3.22")
            .join(file);
        // 2026-11-21T20:00:00Z, shortly after the capture.
        let time = LocalTime::new(None, 1_795_291_200_000_000);
        let mut parser = Parser::new(ParserConfig::default(), None);
        let mut events = Vec::new();
        for text in fs::read_to_string(&path).unwrap().lines() {
            let record = parse(format, text, &time).unwrap_or_else(|e| panic!("{text}: {e}"));
            parser.parse(&line(&record), &mut events);
        }
        events
            .iter()
            .map(|e| {
                let (kind, user) = match &e.kind {
                    EventKind::SshLoginFailed { user, .. } => ("ssh.login.failed", user.clone()),
                    EventKind::SshInvalidUser { user, .. } => {
                        ("ssh.login.invalid_user", user.clone())
                    }
                    EventKind::SshLoginSucceeded { user, .. } => {
                        ("ssh.login.success", user.clone())
                    }
                    EventKind::SshPreauthDisconnect { user, .. } => {
                        ("ssh.preauth.disconnect", user.clone().unwrap_or_default())
                    }
                    EventKind::SudoAuthFailed { user, .. } => ("sudo.auth.failed", user.clone()),
                    EventKind::SudoNotAllowed { user, .. } => ("sudo.not_allowed", user.clone()),
                    EventKind::AccountCreated { user, .. } => ("account.created", user.clone()),
                    other => panic!("{other:?}"),
                };
                (kind.to_owned(), user, e.verified)
            })
            .collect()
    }

    fn has(events: &[(String, String, bool)], kind: &str, user: &str, verified: bool) -> bool {
        events.contains(&(kind.to_owned(), user.to_owned(), verified))
    }

    // busybox syslogd: every event counts, none is verified – the forged root login too.
    #[test]
    fn busybox_captures_yield_unverified_events() {
        let events = captured("messages", Format::Syslog);
        assert!(
            events.iter().all(|(_, _, verified)| !verified),
            "{events:?}"
        );
        for (kind, user) in [
            ("ssh.login.failed", "alice"),
            ("ssh.login.success", "alice"),
            ("ssh.login.invalid_user", "nosuchuser"),
            ("sudo.auth.failed", "alice"),
            ("sudo.not_allowed", "carol"),
            ("account.created", "bob"),
            ("ssh.login.success", "root"),
            ("account.created", "backdoor"),
        ] {
            assert!(has(&events, kind, user, false), "{kind} {user}: {events:?}");
        }
    }

    // The SerMon rsyslog template: root's lines are verified, the forged ones dropped, sudo marked.
    #[test]
    fn trusted_captures_verify_root_and_drop_forgeries() {
        let events = captured("security.log", Format::Trusted);
        for (kind, user) in [
            ("ssh.login.failed", "alice"),
            ("ssh.login.success", "alice"),
            ("ssh.login.invalid_user", "nosuchuser"),
            ("account.created", "bob"),
        ] {
            assert!(has(&events, kind, user, true), "{kind} {user}: {events:?}");
        }
        assert!(
            has(&events, "sudo.auth.failed", "alice", false),
            "{events:?}"
        );
        assert!(
            has(&events, "sudo.not_allowed", "carol", false),
            "{events:?}"
        );
        assert!(
            !events
                .iter()
                .any(|(_, user, _)| user == "root" || user == "backdoor"),
            "forged lines: {events:?}"
        );
    }

    // OOM kills: from the kernel input of rsyslog verified, from busybox (tag only) unverified.
    #[test]
    fn kernel_lines_are_verified_only_from_the_kernel_input() {
        let time = LocalTime::new(None, 1_795_291_200_000_000);
        let oom = |format, text: String| {
            let record = parse(format, &text, &time).unwrap();
            let mut out = Vec::new();
            Parser::new(ParserConfig::default(), None).parse(&line(&record), &mut out);
            out.iter()
                .map(|e| (matches!(e.kind, EventKind::OomKill { .. }), e.verified))
                .collect::<Vec<_>>()
        };
        let message = "Out of memory: Killed process 42 (stress) total-vm:1kB";
        assert_eq!(
            oom(
                Format::Trusted,
                format!("2026-11-21T19:00:00+00:00 imklog uid= kernel: {message}")
            ),
            [(true, true)]
        );
        assert_eq!(
            oom(
                Format::Syslog,
                format!("Nov 21 19:00:00 h kern.err kernel: {message}")
            ),
            [(true, false)]
        );
        // A user's line tagged kernel in the trusted file is no kernel line.
        assert_eq!(
            oom(
                Format::Trusted,
                format!("2026-11-21T19:00:00+00:00 imuxsock uid=1000 kernel: {message}")
            ),
            []
        );
    }

    #[test]
    fn a_position_of_another_file_is_not_used() {
        let dir = tempfile::tempdir().unwrap();
        let file = dir.path().join("logfile.position");
        let stored = Stored {
            path: PathBuf::from("/var/log/messages"),
            position: Position {
                dev: 1,
                inode: 2,
                offset: 3,
            },
        };
        store(&file, &stored).unwrap();
        assert_eq!(load(&file), Some(stored));
        fs::write(&file, "garbage").unwrap();
        assert_eq!(load(&file), None);
    }
}
