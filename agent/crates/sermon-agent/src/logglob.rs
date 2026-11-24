//! Log files named by the globs of the server's configuration (M3-05, KONZEPT §10.1), e.g.
//! `/var/log/nginx/*.log`.
//!
//! - **What is read** decides the host ([`sermon_logs::file::glob`]): below `log_file_prefixes`
//!   of `agent.toml` (default `/var/log`), never the state directory, `/etc/sermon`, `/proc`,
//!   `/sys` or `/dev`; symlinks are resolved and checked again; at most 64 files.
//! - **Tailing:** every second; the globs are expanded again every [`RESCAN`], so new files are
//!   found. Files of the first expansion of a pattern start at their end, later ones at their
//!   start. Rotation and truncation as for the classic log file (M2-20).
//! - **Entries:** lines are grouped by the pattern's multiline rule (a start regex, else
//!   indentation), at most 256 lines or 64 KiB; the time is the time read, the unit the file
//!   name, no severity.
//! - **Positions** (`logfiles.positions`): the end of the last entry in the WAL per file; a
//!   restart continues there. While logs are off, nothing is read and the positions are dropped:
//!   lines of that time are never sent.
//!
//! | Metric | Unit |
//! |---|---|
//! | `sermon_agent_log_files_followed` | files followed now |
//! | `sermon_agent_log_files_rejected` | files a glob matched that may not be read |

use std::collections::{BTreeMap, HashMap};
use std::fmt::Write as _;
use std::fs;
use std::io;
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use sermon_collectors::{CollectError, Collector, Context};
use sermon_core::Sample;
use sermon_logs::file::glob::Access;
use sermon_logs::file::{Assembler, Entry, Line, Position, Tail, indented};
use sermon_logs::record::{LogRecord, Origin};
use tokio::sync::watch;
use tokio::task::JoinHandle;
use tokio_util::sync::CancellationToken;

use crate::agent_config::{Active, FileSpec};
use crate::logship::Shipper;

/// How often the files are read.
pub const POLL: Duration = Duration::from_secs(1);
/// How often the globs are expanded again.
pub const RESCAN: Duration = Duration::from_secs(10);
/// How often the positions are written while lines come.
const PERSIST_EVERY: Duration = Duration::from_secs(5);

/// The settings of the source.
#[derive(Debug, Clone)]
pub struct GlobConfig {
    /// The root of the host's file system.
    pub host_root: PathBuf,
    /// Where globs may read (host paths).
    pub prefixes: Vec<PathBuf>,
    /// Where they never read, besides the fixed ones (the state directory).
    pub denied: Vec<PathBuf>,
    /// Where the positions are stored.
    pub position_file: PathBuf,
    /// How often the files are read.
    pub poll: Duration,
    /// How often the globs are expanded.
    pub rescan: Duration,
}

/// Counters for the agent's own metrics.
#[derive(Debug, Default)]
pub struct GlobStats {
    followed: AtomicU64,
    rejected: AtomicU64,
}

/// The counters as the agent's own metrics.
#[derive(Debug)]
pub struct GlobMetrics(pub Arc<GlobStats>);

impl Collector for GlobMetrics {
    fn name(&self) -> &'static str {
        "log_files"
    }

    fn interval_ticks(&self) -> u32 {
        10
    }

    fn collect(&mut self, _: &Context<'_>, out: &mut Vec<Sample>) -> Result<(), CollectError> {
        let count = |c: &AtomicU64| i64::try_from(c.load(Ordering::Relaxed)).unwrap_or(i64::MAX);
        out.push(Sample::int(
            "sermon_agent_log_files_followed",
            count(&self.0.followed),
        ));
        out.push(Sample::int(
            "sermon_agent_log_files_rejected",
            count(&self.0.rejected),
        ));
        Ok(())
    }
}

/// A followed file.
#[derive(Debug)]
struct Followed {
    tail: Tail,
    /// The index of its pattern in the configuration.
    pattern: usize,
    assembler: Assembler,
    origin: Option<Origin>,
    /// The end of the last entry handed to the shipper.
    offered: Option<Position>,
    /// The end of the last entry in the WAL.
    committed: Option<Position>,
    /// Reading failed; warned once.
    failing: bool,
}

/// The followed files of the configured patterns.
#[derive(Debug)]
pub struct Files {
    access: Access,
    specs: Vec<FileSpec>,
    followed: BTreeMap<PathBuf, Followed>,
    /// Positions from the last run, for files not followed yet.
    stored: HashMap<PathBuf, Position>,
    /// Patterns expanded at least once: their new files start at the start.
    expanded: Vec<bool>,
    rejected: usize,
    warned: Vec<String>,
}

fn same(a: &[FileSpec], b: &[FileSpec]) -> bool {
    a.len() == b.len()
        && a.iter().zip(b).all(|(x, y)| {
            x.pattern == y.pattern
                && x.multiline_start.as_ref().map(regex::Regex::as_str)
                    == y.multiline_start.as_ref().map(regex::Regex::as_str)
        })
}

impl Files {
    /// No patterns yet; `stored` are the positions of the last run.
    #[must_use]
    pub fn new(access: Access, stored: HashMap<PathBuf, Position>) -> Self {
        Self {
            access,
            specs: Vec::new(),
            followed: BTreeMap::new(),
            stored,
            expanded: Vec::new(),
            rejected: 0,
            warned: Vec::new(),
        }
    }

    /// Takes the patterns of a configuration; files of removed or changed patterns are dropped.
    /// Returns whether anything changed.
    pub fn configure(&mut self, specs: &[FileSpec]) -> bool {
        if same(&self.specs, specs) {
            return false;
        }
        let kept: Vec<Option<usize>> = self
            .specs
            .iter()
            .map(|old| {
                specs
                    .iter()
                    .position(|new| same(std::slice::from_ref(old), std::slice::from_ref(new)))
            })
            .collect();
        let followed = std::mem::take(&mut self.followed);
        for (path, mut f) in followed {
            if let Some(Some(index)) = kept.get(f.pattern) {
                f.pattern = *index;
                self.followed.insert(path, f);
            }
        }
        let mut expanded = vec![false; specs.len()];
        for (old, new) in kept.iter().enumerate() {
            if let Some(new) = new {
                expanded[*new] = self.expanded.get(old).copied().unwrap_or(false);
            }
        }
        self.expanded = expanded;
        self.specs = specs.to_vec();
        for spec in specs {
            if let Err(why) = self.access.check_pattern(&spec.pattern)
                && !self.warned.contains(&spec.pattern)
            {
                tracing::warn!(pattern = %spec.pattern, "log file pattern not followed: {why}");
                self.warned.push(spec.pattern.clone());
            }
        }
        true
    }

    /// Expands the patterns: new files are followed, files no longer matched are dropped.
    pub fn scan(&mut self) {
        let patterns: Vec<&str> = self.specs.iter().map(|s| s.pattern.as_str()).collect();
        let expansion = self.access.expand(&patterns);
        self.rejected = expansion.rejected;
        let found: BTreeMap<PathBuf, usize> = expansion.files.into_iter().collect();
        self.followed.retain(|path, _| found.contains_key(path));
        for (path, pattern) in found {
            if self.followed.contains_key(&path) {
                continue;
            }
            let on_host = self.access.on_host(&path);
            // Every open of the file is checked again, rotations included (M3-28).
            let guard = self.access.guard();
            let tail = match self.stored.remove(&path) {
                Some(position) => Tail::guarded(on_host, Some(position), false, guard),
                None => Tail::guarded(on_host, None, self.expanded[pattern], guard),
            };
            self.followed.insert(
                path.clone(),
                Followed {
                    tail,
                    pattern,
                    assembler: Assembler::default(),
                    origin: Origin::file(&path),
                    offered: None,
                    committed: None,
                    failing: false,
                },
            );
        }
        self.expanded.iter_mut().for_each(|e| *e = true);
    }

    /// Reads every file and hands complete entries to `shipper`, `at_us` as their time.
    pub fn read(&mut self, shipper: &mut Shipper, at_us: u64) {
        let now = tokio::time::Instant::now();
        for (path, f) in &mut self.followed {
            let mut lines: Vec<Line> = Vec::new();
            if let Err(error) = f.tail.read(&mut lines) {
                if !f.failing {
                    tracing::warn!(file = %path.display(), "cannot read the log file, retrying: {error}");
                    f.failing = true;
                }
                continue;
            }
            f.failing = false;
            let start = self
                .specs
                .get(f.pattern)
                .and_then(|s| s.multiline_start.as_ref());
            let mut entries: Vec<Entry> = Vec::new();
            for line in &lines {
                f.assembler.push(
                    line,
                    |text| start.map_or_else(|| !indented(text), |re| re.is_match(text)),
                    &mut entries,
                );
            }
            // Nothing new for a round: the open entry is complete.
            if lines.is_empty() {
                f.assembler.flush(&mut entries);
            }
            if let Some(origin) = &f.origin {
                for entry in entries {
                    shipper.offer(
                        LogRecord::from_plain_file(
                            origin.clone(),
                            at_us,
                            &entry.text,
                            entry.truncated,
                        ),
                        now,
                    );
                }
            }
            f.offered = f.assembler.settled().or(f.offered);
        }
    }

    /// The offered entries are in the WAL: their positions count.
    pub fn commit(&mut self) {
        for f in self.followed.values_mut() {
            f.committed = f.offered.or(f.committed);
        }
    }

    /// The committed positions of the followed files.
    #[must_use]
    pub fn positions(&self) -> Vec<(PathBuf, Position)> {
        self.followed
            .iter()
            .filter_map(|(path, f)| f.committed.map(|p| (path.clone(), p)))
            .collect()
    }

    /// Stops following everything and forgets all positions.
    pub fn clear(&mut self) {
        self.followed.clear();
        self.stored.clear();
        self.specs.clear();
        self.expanded.clear();
        self.rejected = 0;
    }

    /// Files followed now.
    #[must_use]
    pub fn followed(&self) -> usize {
        self.followed.len()
    }

    /// Matches refused at the last expansion.
    #[must_use]
    pub fn rejected(&self) -> usize {
        self.rejected
    }
}

/// The stored positions: `dev:inode:offset path` per line.
#[must_use]
pub fn load(file: &Path) -> HashMap<PathBuf, Position> {
    let Ok(text) = fs::read_to_string(file) else {
        return HashMap::new();
    };
    text.lines()
        .filter_map(|line| {
            let (position, path) = line.split_once(' ')?;
            Some((PathBuf::from(path), Position::decode(position)?))
        })
        .collect()
}

/// Writes the positions atomically with mode 0600; none: the file is removed.
///
/// # Errors
///
/// Writing failed.
pub fn store(file: &Path, positions: &[(PathBuf, Position)]) -> io::Result<()> {
    if positions.is_empty() {
        return match fs::remove_file(file) {
            Err(error) if error.kind() != io::ErrorKind::NotFound => Err(error),
            _ => Ok(()),
        };
    }
    let directory = file.parent().unwrap_or(Path::new("."));
    let temporary = directory.join(".logfiles.positions.new");
    let mut content = String::new();
    for (path, position) in positions {
        let Some(path) = path.to_str().filter(|p| !p.contains('\n')) else {
            continue;
        };
        let _ = writeln!(content, "{} {path}", position.encode());
    }
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

fn now_us() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |d| u64::try_from(d.as_micros()).unwrap_or(u64::MAX))
}

fn persist(file: &Path, files: &Files, written: &mut Vec<(PathBuf, Position)>) {
    let positions = files.positions();
    if &positions == written {
        return;
    }
    match store(file, &positions) {
        Ok(()) => *written = positions,
        Err(error) => tracing::warn!("cannot store the log file positions: {error}"),
    }
}

/// Starts the source; the task ends after `shutdown` with the positions written.
#[must_use]
pub fn spawn(
    config: GlobConfig,
    shipper: Shipper,
    active: watch::Receiver<Arc<Active>>,
    stats: Arc<GlobStats>,
    shutdown: CancellationToken,
) -> JoinHandle<()> {
    tokio::spawn(run(config, shipper, active, stats, shutdown))
}

/// Follows the files of the configuration until `shutdown`. An entry's position counts once the
/// shipper has written it; while entries wait, nothing more is read.
pub async fn run(
    config: GlobConfig,
    mut shipper: Shipper,
    active: watch::Receiver<Arc<Active>>,
    stats: Arc<GlobStats>,
    shutdown: CancellationToken,
) {
    let access = Access::new(&config.host_root, &config.prefixes, &config.denied);
    let mut files = Files::new(access, load(&config.position_file));
    let mut written = files.positions();
    let mut tick = tokio::time::interval(config.poll);
    tick.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Delay);
    let mut last_scan: Option<tokio::time::Instant> = None;
    let mut last_write = tokio::time::Instant::now();
    let mut was_on = true;
    loop {
        tokio::select! {
            () = shutdown.cancelled() => break,
            _ = tick.tick() => {}
        }
        let current = active.borrow().clone();
        if !current.logs.enabled || current.logs.files.is_empty() {
            if was_on {
                // Off: nothing of this time is ever sent.
                files.clear();
                let _ = shipper.flush();
                // Also the positions of an earlier run.
                match store(&config.position_file, &[]) {
                    Ok(()) => written.clear(),
                    Err(error) => tracing::warn!("cannot remove the log file positions: {error}"),
                }
                was_on = false;
            }
            stats.followed.store(0, Ordering::Relaxed);
            stats.rejected.store(0, Ordering::Relaxed);
            continue;
        }
        was_on = true;
        let changed = files.configure(&current.logs.files);
        let now = tokio::time::Instant::now();
        if changed || last_scan.is_none_or(|t| now.duration_since(t) >= config.rescan) {
            files.scan();
            last_scan = Some(now);
            let count = |n: usize| u64::try_from(n).unwrap_or(u64::MAX);
            stats
                .followed
                .store(count(files.followed()), Ordering::Relaxed);
            stats
                .rejected
                .store(count(files.rejected()), Ordering::Relaxed);
        }
        if shipper.is_empty() {
            files.read(&mut shipper, now_us());
        }
        if shipper.flush() {
            files.commit();
        }
        if last_write.elapsed() >= PERSIST_EVERY.min(config.poll * 5) {
            persist(&config.position_file, &files, &mut written);
            last_write = tokio::time::Instant::now();
        }
    }
    if shipper.flush() {
        files.commit();
    }
    persist(&config.position_file, &files, &mut written);
}

#[cfg(test)]
mod tests {
    use std::io::Write;

    use sermon_transport::proto::agent::v1::{AgentConfig, ConfigUpdate, LogConfig, LogFileGlob};

    use super::*;
    use crate::agent_config::compile;
    use crate::logship::Delivery;
    use crate::logship::tests::{Written, shipper};

    fn active(globs: &[(&str, &str)]) -> Arc<Active> {
        let update = ConfigUpdate {
            version: 3,
            config: Some(AgentConfig {
                logs: Some(LogConfig {
                    enabled: true,
                    files: globs
                        .iter()
                        .map(|(pattern, start)| LogFileGlob {
                            pattern: (*pattern).into(),
                            multiline_start: (*start).into(),
                        })
                        .collect(),
                    ..LogConfig::default()
                }),
                redaction_rules: Vec::new(),
            }),
        };
        Arc::new(compile(&update, true).unwrap())
    }

    struct Host {
        root: tempfile::TempDir,
        state: tempfile::TempDir,
    }

    impl Host {
        fn new() -> Self {
            Self {
                root: tempfile::tempdir().unwrap(),
                state: tempfile::tempdir().unwrap(),
            }
        }

        fn path(&self, path: &str) -> PathBuf {
            self.root.path().join(path.trim_start_matches('/'))
        }

        fn append(&self, path: &str, text: &str) {
            let p = self.path(path);
            fs::create_dir_all(p.parent().unwrap()).unwrap();
            let mut file = fs::OpenOptions::new()
                .create(true)
                .append(true)
                .open(p)
                .unwrap();
            file.write_all(text.as_bytes()).unwrap();
        }

        fn config(&self) -> GlobConfig {
            GlobConfig {
                host_root: self.root.path().to_owned(),
                prefixes: vec![PathBuf::from("/var/log")],
                denied: vec![PathBuf::from("/var/lib/sermon-agent")],
                position_file: self.state.path().join("logfiles.positions"),
                poll: Duration::from_millis(10),
                rescan: Duration::from_millis(30),
            }
        }

        fn files(&self) -> Files {
            let c = self.config();
            Files::new(
                Access::new(&c.host_root, &c.prefixes, &c.denied),
                load(&c.position_file),
            )
        }
    }

    /// One round: read, write, commit.
    fn round(files: &mut Files, s: &mut Shipper) {
        files.read(s, 1);
        if s.flush() {
            files.commit();
        }
    }

    #[tokio::test]
    async fn follows_new_and_rotated_files() {
        let host = Host::new();
        host.append("/var/log/app/a.log", "old line\n");
        let (mut s, written, _, _config) = shipper(true);
        let mut files = host.files();
        files.configure(&active(&[("/var/log/app/*.log", "")]).logs.files);
        files.scan();
        round(&mut files, &mut s);
        host.append("/var/log/app/a.log", "one\n");
        round(&mut files, &mut s);
        round(&mut files, &mut s); // idle: the open entry is complete
        assert_eq!(written.messages(), ["one"], "old lines are not read");

        // A file that appears later: from its start.
        host.append("/var/log/app/b.log", "first of b\n");
        files.scan();
        assert_eq!(files.followed(), 2);
        round(&mut files, &mut s);
        round(&mut files, &mut s);

        // Rotation: the rest of the old file, then the new one; the rotated name is not followed.
        host.append("/var/log/app/a.log", "before rotation\n");
        fs::rename(
            host.path("/var/log/app/a.log"),
            host.path("/var/log/app/a.log.1"),
        )
        .unwrap();
        host.append("/var/log/app/a.log", "after rotation\n");
        files.scan();
        assert_eq!(files.followed(), 2);
        for _ in 0..3 {
            round(&mut files, &mut s);
        }
        assert_eq!(
            written.messages(),
            ["one", "first of b", "before rotation", "after rotation"]
        );
        let units: Vec<_> = written
            .batches
            .lock()
            .unwrap()
            .iter()
            .flatten()
            .map(|r| r.unit.clone().unwrap())
            .collect();
        assert_eq!(units, ["a.log", "b.log", "a.log", "a.log"]);
    }

    #[tokio::test]
    async fn a_restart_continues_after_the_last_written_entry() {
        let host = Host::new();
        host.append("/var/log/app.log", "");
        let config = host.config();
        let (mut s, written, _, _config) = shipper(true);
        let mut files = host.files();
        files.configure(&active(&[("/var/log/*.log", "")]).logs.files);
        files.scan();
        host.append("/var/log/app.log", "one\ntwo\n");
        round(&mut files, &mut s);
        round(&mut files, &mut s);
        store(&config.position_file, &files.positions()).unwrap();
        // Written while the agent was stopped.
        host.append("/var/log/app.log", "three\n");

        let mut again = host.files();
        again.configure(&active(&[("/var/log/*.log", "")]).logs.files);
        again.scan();
        round(&mut again, &mut s);
        round(&mut again, &mut s);
        assert_eq!(written.messages(), ["one", "two", "three"]);
    }

    #[tokio::test]
    async fn groups_a_java_stack_trace() {
        let host = Host::new();
        host.append("/var/log/app/server.log", "");
        let (mut s, written, _, _config) = shipper(true);
        let mut files = host.files();
        files.configure(
            &active(&[("/var/log/app/server.log", r"^\d{4}-\d{2}-\d{2} ")])
                .logs
                .files,
        );
        files.scan();
        let fixture = include_str!("../../../fixtures/logs/multiline/java-stacktrace.log");
        host.append("/var/log/app/server.log", fixture);
        round(&mut files, &mut s);
        round(&mut files, &mut s);
        let messages = written.messages();
        assert_eq!(messages.len(), 3, "{messages:#?}");
        assert!(messages[1].starts_with("2026-11-23 12:00:01.512 ERROR"));
        assert!(messages[1].contains("Caused by: java.net.SocketTimeoutException"));
        assert!(messages[1].ends_with("... 42 more"));
    }

    #[tokio::test]
    async fn refuses_paths_outside_and_patterns_outside() {
        let host = Host::new();
        host.append("/var/lib/sermon-agent/identity/agent.key", "secret\n");
        host.append("/etc/shadow", "root:x\n");
        fs::create_dir_all(host.path("/var/log")).unwrap();
        std::os::unix::fs::symlink(
            host.path("/var/lib/sermon-agent/identity/agent.key"),
            host.path("/var/log/key.log"),
        )
        .unwrap();
        let mut files = host.files();
        files.configure(&active(&[("/var/log/*.log", ""), ("/etc/*", "")]).logs.files);
        files.scan();
        assert_eq!(files.followed(), 0);
        assert_eq!(files.rejected(), 1);
    }

    #[tokio::test]
    async fn a_failed_write_keeps_the_position() {
        let host = Host::new();
        host.append("/var/log/app.log", "");
        let (mut s, _w, _, _config) = shipper(true);
        let written = Written::new();
        let mut s2 = s.sibling(written.deliver());
        let mut files = host.files();
        files.configure(&active(&[("/var/log/app.log", "")]).logs.files);
        files.scan();
        host.append("/var/log/app.log", "one\n");
        *written.next.lock().unwrap() = Delivery::Failed;
        round(&mut files, &mut s2);
        round(&mut files, &mut s2);
        assert!(
            files.positions().is_empty(),
            "nothing written, nothing committed"
        );
        *written.next.lock().unwrap() = Delivery::Stored;
        assert!(s2.flush());
        files.commit();
        assert_eq!(files.positions().len(), 1);
        assert_eq!(written.messages(), ["one"]);
        let _ = s.flush();
    }

    #[tokio::test]
    async fn a_removed_file_is_no_longer_followed() {
        let host = Host::new();
        host.append("/var/log/a.log", "");
        host.append("/var/log/b.log", "");
        let mut files = host.files();
        files.configure(&active(&[("/var/log/*.log", "")]).logs.files);
        files.scan();
        assert_eq!(files.followed(), 2);
        fs::remove_file(host.path("/var/log/a.log")).unwrap();
        files.scan();
        assert_eq!(files.followed(), 1);
    }

    #[tokio::test]
    async fn changed_patterns_drop_their_files() {
        let host = Host::new();
        host.append("/var/log/a.log", "");
        host.append("/var/log/b.txt", "");
        let mut files = host.files();
        files.configure(
            &active(&[("/var/log/*.log", ""), ("/var/log/*.txt", "")])
                .logs
                .files,
        );
        files.scan();
        assert_eq!(files.followed(), 2);
        assert!(
            !files.configure(
                &active(&[("/var/log/*.log", ""), ("/var/log/*.txt", "")])
                    .logs
                    .files
            )
        );
        assert!(files.configure(&active(&[("/var/log/*.txt", "")]).logs.files));
        assert_eq!(files.followed(), 1);
    }

    // The task commits a position only once the buffer took the entry.
    #[tokio::test(flavor = "current_thread")]
    async fn the_task_keeps_the_position_while_the_buffer_refuses() {
        let host = Host::new();
        host.append("/var/log/app.log", "");
        let config = host.config();
        let (base, _, _, _shipper_config) = shipper(true);
        let written = Written::new();
        *written.next.lock().unwrap() = Delivery::Failed;
        let s = base.sibling(written.deliver());
        let (_switch, receiver) = watch::channel(active(&[("/var/log/app.log", "")]));
        let shutdown = CancellationToken::new();
        let task = spawn(
            config.clone(),
            s,
            receiver,
            Arc::default(),
            shutdown.clone(),
        );
        tokio::time::sleep(Duration::from_millis(50)).await;
        host.append("/var/log/app.log", "one\n");
        tokio::time::sleep(Duration::from_millis(200)).await;
        assert!(
            !config.position_file.exists(),
            "nothing in the buffer, nothing committed"
        );
        *written.next.lock().unwrap() = Delivery::Stored;
        tokio::time::sleep(Duration::from_millis(200)).await;
        shutdown.cancel();
        task.await.unwrap();
        assert_eq!(written.messages(), ["one"]);
        let positions = load(&config.position_file);
        assert_eq!(positions.values().next().map(|p| p.offset), Some(4));
    }

    #[tokio::test(flavor = "current_thread")]
    async fn the_task_reads_while_logs_are_on_and_forgets_while_off() {
        let host = Host::new();
        host.append("/var/log/app.log", "");
        let config = host.config();
        let (s, written, _, _shipper_config) = shipper(true);
        let (switch, receiver) = watch::channel(active(&[("/var/log/app.log", "")]));
        let stats = Arc::new(GlobStats::default());
        let shutdown = CancellationToken::new();
        let task = spawn(config.clone(), s, receiver, stats.clone(), shutdown.clone());
        tokio::time::sleep(Duration::from_millis(50)).await;
        host.append("/var/log/app.log", "while on\n");
        for _ in 0..100 {
            if written.messages().len() == 1 {
                break;
            }
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
        assert_eq!(written.messages(), ["while on"]);
        assert_eq!(stats.followed.load(Ordering::Relaxed), 1);
        tokio::time::sleep(Duration::from_millis(100)).await;
        assert!(config.position_file.exists());

        let mut off = (*active(&[("/var/log/app.log", "")])).clone();
        off.logs.enabled = false;
        switch.send_replace(Arc::new(off));
        tokio::time::sleep(Duration::from_millis(50)).await;
        host.append("/var/log/app.log", "while off\n");
        tokio::time::sleep(Duration::from_millis(50)).await;
        assert!(!config.position_file.exists(), "positions dropped");
        switch.send_replace(active(&[("/var/log/app.log", "")]));
        tokio::time::sleep(Duration::from_millis(100)).await;
        host.append("/var/log/app.log", "on again\n");
        tokio::time::sleep(Duration::from_millis(150)).await;
        shutdown.cancel();
        task.await.unwrap();
        assert_eq!(written.messages(), ["while on", "on again"]);
        assert!(config.position_file.exists());
    }
}
