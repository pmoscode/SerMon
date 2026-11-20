//! The journal source against a fake `journalctl` (tests/fake-journalctl): start position, resume
//! after the committed cursor, crashes, rotation, vacuum, broken lines (ADR 0013, M2-05).

// Test code: panicking on setup failures is the desired behaviour.
#![allow(clippy::expect_used, clippy::unwrap_used, clippy::panic)]

use std::fmt::Write as _;
use std::fs;
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::sync::atomic::Ordering;
use std::time::Duration;

use sermon_logs::journal::{
    Checkpoint, CursorFile, Entry, JournalConfig, JournalSource, JournalStats, OUTPUT_FIELDS,
    parse_line,
};
use tokio::sync::mpsc;
use tokio::task::JoinHandle;
use tokio_util::sync::CancellationToken;

const WAIT: Duration = Duration::from_secs(10);

/// A synthetic entry with sequence number `n` of the sequence `seq`.
fn line(n: u64) -> String {
    format!(
        r#"{{"__CURSOR":"s=seq;i={n:x};b=boot;x={n}","__REALTIME_TIMESTAMP":"{}","__SEQNUM":"{n}","__SEQNUM_ID":"seq","MESSAGE":"entry {n}","_BOOT_ID":"boot"}}"#,
        1_795_000_000_000_000 + n
    )
}

fn lines(range: impl IntoIterator<Item = u64>) -> String {
    range.into_iter().fold(String::new(), |mut out, n| {
        writeln!(out, "{}", line(n)).unwrap();
        out
    })
}

fn cursor(n: u64) -> String {
    format!("s=seq;i={n:x};b=boot;x={n}")
}

fn fixture(distro: &str) -> String {
    let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../fixtures/logs")
        .join(distro)
        .join("journal-security.jsonl");
    fs::read_to_string(path).unwrap()
}

struct Fake {
    dir: tempfile::TempDir,
}

impl Fake {
    /// A fake journal in its own directory; `exit_after` makes every start crash after n lines.
    fn new(journal: &str, exit_after: Option<usize>) -> Self {
        let dir = tempfile::tempdir().unwrap();
        let script = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fake-journalctl");
        std::os::unix::fs::symlink(script, dir.path().join("journalctl")).unwrap();
        let mut env = format!(
            "journal='{}'\nargs='{}'\n",
            dir.path().join("journal").display(),
            dir.path().join("args").display()
        );
        if let Some(n) = exit_after {
            writeln!(env, "exit_after={n}").unwrap();
        }
        fs::write(dir.path().join("fake.env"), env).unwrap();
        fs::write(dir.path().join("journal"), journal).unwrap();
        Self { dir }
    }

    fn path(&self, name: &str) -> PathBuf {
        self.dir.path().join(name)
    }

    fn config(&self) -> JournalConfig {
        let mut config = JournalConfig::new(self.path("journal.cursor"));
        config.program = self.path("journalctl");
        config.backoff = Duration::from_millis(20);
        config.max_backoff = Duration::from_millis(100);
        config.flush_interval = Duration::from_millis(20);
        config
    }

    fn append(&self, text: &str) {
        let mut journal = fs::OpenOptions::new()
            .append(true)
            .open(self.path("journal"))
            .unwrap();
        std::io::Write::write_all(&mut journal, text.as_bytes()).unwrap();
    }

    /// The argument lines of every start so far.
    fn args(&self) -> Vec<String> {
        fs::read_to_string(self.path("args"))
            .unwrap_or_default()
            .lines()
            .map(str::to_owned)
            .collect()
    }

    async fn wait_for_start(&self, starts: usize) {
        tokio::time::timeout(WAIT, async {
            while self.args().len() < starts {
                tokio::time::sleep(Duration::from_millis(10)).await;
            }
        })
        .await
        .expect("journalctl was not started");
    }
}

struct Running {
    entries: mpsc::Receiver<Entry>,
    checkpoint: Checkpoint,
    stats: Arc<JournalStats>,
    shutdown: CancellationToken,
    task: JoinHandle<()>,
}

fn start(config: JournalConfig) -> Running {
    let source = JournalSource::new(config);
    let (sender, entries) = mpsc::channel(16);
    let shutdown = CancellationToken::new();
    Running {
        entries,
        checkpoint: source.checkpoint(),
        stats: source.stats(),
        shutdown: shutdown.clone(),
        task: tokio::spawn(source.run(sender, shutdown)),
    }
}

impl Running {
    async fn receive(&mut self, n: usize) -> Vec<Entry> {
        let mut out = Vec::new();
        while out.len() < n {
            match tokio::time::timeout(WAIT, self.entries.recv()).await {
                Ok(Some(entry)) => out.push(entry),
                Ok(None) => panic!("source ended after {} entries", out.len()),
                Err(elapsed) => panic!("{elapsed} after {} of {n} entries", out.len()),
            }
        }
        out
    }

    /// Nothing more arrives within a moment.
    async fn quiet(&mut self) {
        if let Ok(Some(entry)) =
            tokio::time::timeout(Duration::from_millis(300), self.entries.recv()).await
        {
            panic!("unexpected entry {}", entry.message);
        }
    }

    async fn stop(self) {
        self.shutdown.cancel();
        tokio::time::timeout(WAIT, self.task)
            .await
            .expect("source did not stop")
            .unwrap();
    }
}

fn messages(entries: &[Entry]) -> Vec<String> {
    entries.iter().map(|e| e.message.clone()).collect()
}

fn expected(range: impl IntoIterator<Item = u64>) -> Vec<String> {
    range.into_iter().map(|n| format!("entry {n}")).collect()
}

#[tokio::test(flavor = "multi_thread")]
async fn first_start_follows_from_the_end_with_the_fixed_arguments() {
    let fake = Fake::new(&lines(1..=5), None);
    let mut running = start(fake.config());
    fake.wait_for_start(1).await;
    let args = &fake.args()[0];
    for want in [
        "-o json",
        "--all",
        "--follow",
        "--lines=0",
        "--quiet",
        "--no-pager",
    ] {
        assert!(args.contains(want), "{want} missing in {args}");
    }
    assert!(
        args.contains(&format!("--output-fields={}", OUTPUT_FIELDS.join(","))),
        "{args}"
    );
    assert!(
        !args.contains("_CMDLINE") && !args.contains("--cursor-file"),
        "{args}"
    );

    running.quiet().await;
    fake.append(&lines(6..=8));
    assert_eq!(messages(&running.receive(3).await), expected(6..=8));
    running.stop().await;
}

#[tokio::test(flavor = "multi_thread")]
async fn resumes_after_the_committed_cursor() {
    let fake = Fake::new(&lines(1..=20), None);
    CursorFile::new(fake.path("journal.cursor"))
        .store(&cursor(10))
        .unwrap();
    let mut running = start(fake.config());
    assert_eq!(messages(&running.receive(10).await), expected(11..=20));
    running.quiet().await;
    assert!(fake.args()[0].contains(&format!("--after-cursor={}", cursor(10))));
    assert_eq!(running.stats.lost_entries.load(Ordering::Relaxed), 0);
    running.stop().await;
}

// journalctl ends (crash, journald restart): the source starts it again after the last entry it
// read – no entry twice, none lost. The fixture streams of Debian and Ubuntu.
#[tokio::test(flavor = "multi_thread")]
async fn restarts_after_crashes_without_losing_or_repeating_entries() {
    for distro in ["debian-13", "ubuntu-24.04"] {
        let journal = fixture(distro);
        let all: Vec<Entry> = journal
            .lines()
            .map(|l| parse_line(l.as_bytes()).unwrap())
            .collect();
        let fake = Fake::new(&journal, Some(7));
        CursorFile::new(fake.path("journal.cursor"))
            .store(&all[0].cursor)
            .unwrap();
        let mut running = start(fake.config());
        let got = running.receive(all.len() - 1).await;
        assert_eq!(got, all[1..], "{distro}");
        running.quiet().await;
        assert!(
            running.stats.restarts.load(Ordering::Relaxed) >= 5,
            "{distro}"
        );
        running.stop().await;
    }
}

// The file holds what the consumer committed, not what the source read: after an agent restart
// the uncommitted entries come again, the committed ones don't.
#[tokio::test(flavor = "multi_thread")]
async fn uncommitted_entries_come_again_after_an_agent_restart() {
    let fake = Fake::new(&lines(1..=20), None);
    CursorFile::new(fake.path("journal.cursor"))
        .store(&cursor(0))
        .unwrap();
    let mut running = start(fake.config());
    let got = running.receive(20).await;
    running.checkpoint.commit(&got[9].cursor);
    running.stop().await;
    assert_eq!(
        CursorFile::new(fake.path("journal.cursor")).load().unwrap(),
        Some(cursor(10))
    );

    let mut again = start(fake.config());
    assert_eq!(messages(&again.receive(10).await), expected(11..=20));
    again.quiet().await;
    again.stop().await;
}

#[tokio::test(flavor = "multi_thread")]
async fn writes_the_committed_cursor_while_running() {
    let fake = Fake::new(&lines(1..=3), None);
    CursorFile::new(fake.path("journal.cursor"))
        .store(&cursor(0))
        .unwrap();
    let mut running = start(fake.config());
    let got = running.receive(3).await;
    running.checkpoint.commit(&got[2].cursor);
    let file = CursorFile::new(fake.path("journal.cursor"));
    tokio::time::timeout(WAIT, async {
        while file.load().unwrap() != Some(cursor(3)) {
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
    })
    .await
    .expect("cursor not written while running");
    running.stop().await;
}

// Rotation keeps the cursor: the next start continues after it. A vacuum removes it: journalctl
// continues at the oldest entry left, and the jump in the sequence counts as lost entries.
#[tokio::test(flavor = "multi_thread")]
async fn rotation_continues_and_a_vacuum_counts_the_lost_entries() {
    let fake = Fake::new("", Some(100));
    fs::write(fake.path("journal.1"), lines(1..=20)).unwrap();
    fs::write(fake.path("journal.2"), lines(15..=30)).unwrap(); // rotated: archive and new file
    fs::write(fake.path("journal.3"), lines(40..=45)).unwrap(); // vacuumed up to 39
    CursorFile::new(fake.path("journal.cursor"))
        .store(&cursor(0))
        .unwrap();
    let mut running = start(fake.config());
    assert_eq!(messages(&running.receive(20).await), expected(1..=20));
    assert_eq!(messages(&running.receive(10).await), expected(21..=30));
    assert_eq!(running.stats.lost_entries.load(Ordering::Relaxed), 0);
    assert_eq!(messages(&running.receive(6).await), expected(40..=45));
    assert_eq!(running.stats.lost_entries.load(Ordering::Relaxed), 9);
    running.stop().await;
}

// The sequence number of the stored cursor counts too: entries lost while the agent was stopped.
#[tokio::test(flavor = "multi_thread")]
async fn counts_entries_lost_while_the_agent_was_stopped() {
    let fake = Fake::new(&(lines(1..=10) + &lines(15..=16)), None);
    CursorFile::new(fake.path("journal.cursor"))
        .store(&cursor(10))
        .unwrap();
    let mut running = start(fake.config());
    assert_eq!(messages(&running.receive(2).await), expected(15..=16));
    assert_eq!(running.stats.lost_entries.load(Ordering::Relaxed), 4);
    running.stop().await;
}

// A broken line costs only itself: no JSON, too long, without a cursor.
#[tokio::test(flavor = "multi_thread")]
async fn broken_lines_are_counted_and_skipped() {
    // Raw bytes in the JSON, as systemd 239 writes binary messages.
    let raw = fs::read(
        PathBuf::from(env!("CARGO_MANIFEST_DIR"))
            .join("../../fixtures/logs/rockylinux-8/journal-format.jsonl"),
    )
    .unwrap();
    let invalid_utf8 = raw
        .split(|b| *b == b'\n')
        .find(|l| std::str::from_utf8(l).is_err())
        .unwrap()
        .to_vec();
    let mut journal = Vec::new();
    journal.extend(lines(1..=1).as_bytes());
    journal
        .extend(b"-- No entries --\n{\"MESSAGE\":\"no cursor\",\"__REALTIME_TIMESTAMP\":\"1\"}\n");
    journal.extend(format!("{{\"MESSAGE\":\"{}\"}}\n", "x".repeat(8192)).as_bytes());
    journal.extend(&invalid_utf8);
    journal.push(b'\n');
    journal.extend(lines(2..=2).as_bytes());
    let fake = Fake::new("", None);
    fs::write(fake.path("journal"), journal).unwrap();
    CursorFile::new(fake.path("journal.cursor"))
        .store(&cursor(0))
        .unwrap();
    let mut config = fake.config();
    config.max_line_bytes = 4096;
    let mut running = start(config);
    let got = running.receive(3).await;
    assert_eq!(got[0].message, "entry 1");
    assert!(got[1].message.contains('\u{fffd}'), "{:?}", got[1].message);
    assert_eq!(got[2].message, "entry 2");
    assert_eq!(running.stats.invalid_lines.load(Ordering::Relaxed), 3);
    assert_eq!(running.stats.entries.load(Ordering::Relaxed), 3);
    running.stop().await;
}

// No journal on the host (Alpine, containers): the source keeps trying, quietly, and stops on
// shutdown.
#[tokio::test(flavor = "multi_thread")]
async fn missing_journalctl_is_no_failure() {
    let dir = tempfile::tempdir().unwrap();
    let mut config = JournalConfig::new(dir.path().join("journal.cursor"));
    config.program = Path::new("/nonexistent/journalctl").to_path_buf();
    config.backoff = Duration::from_millis(10);
    config.max_backoff = Duration::from_millis(20);
    let running = start(config);
    tokio::time::sleep(Duration::from_millis(200)).await;
    assert!(running.stats.restarts.load(Ordering::Relaxed) >= 2);
    running.stop().await;
}

// Shutdown ends journalctl too (no orphaned follower).
#[tokio::test(flavor = "multi_thread")]
async fn shutdown_while_following() {
    let fake = Fake::new(&lines(1..=2), None);
    let running = start(fake.config());
    fake.wait_for_start(1).await;
    running.stop().await;
    fake.append(&lines(3..=3));
    tokio::time::sleep(Duration::from_millis(100)).await;
    assert_eq!(fake.args().len(), 1, "restarted after shutdown");
}
