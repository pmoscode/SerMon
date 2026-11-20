//! The journal source in the agent (M2-05, ADR 0013): `journalctl` follows the journal, its
//! entries go to the security parsers (M2-06) – log shipping comes with E3.1 – and its counters to
//! the agent's own metrics.
//!
//! | Metric | Unit |
//! |---|---|
//! | `sermon_agent_journal_lost_entries` | entries missing since the agent started (sequence gaps) |
//! | `sermon_agent_journal_invalid_lines` | lines of `journalctl` that were no entry |

use std::sync::Arc;
use std::sync::atomic::Ordering;
use std::time::{Duration, SystemTime};

use sermon_collectors::{CollectError, Collector, Context};
use sermon_core::Sample;
use sermon_logs::journal::{
    Checkpoint, CursorFile, Entry, JournalConfig, JournalSource, JournalStats,
};
use sermon_security::{Parser, ParserConfig, SecurityEvent};
use sermon_transport::outbox::{MAX_EVENTS_PER_BATCH, Outbox};
use tokio::sync::mpsc;
use tokio::task::JoinHandle;
use tokio::time::Instant;
use tokio_util::sync::CancellationToken;

use crate::security;
use crate::state::StateLayout;

/// Entries in flight between source and consumer; a slow consumer holds `journalctl` back.
const QUEUE: usize = 256;
/// Events wait at most this long for more before they go to the WAL as one batch.
const FLUSH_AFTER: Duration = Duration::from_secs(1);
/// Events kept while the WAL refuses them; beyond it the oldest are dropped (with a warning).
const MAX_PENDING: usize = 10 * MAX_EVENTS_PER_BATCH;

/// Starts the source and its consumer; the task ends after `shutdown` with the cursor written.
#[must_use]
pub fn spawn(
    layout: &StateLayout,
    outbox: Outbox,
    shutdown: CancellationToken,
) -> (JournalMetrics, JoinHandle<()>) {
    spawn_with(
        JournalConfig::new(layout.journal_cursor_file()),
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
    )
}

/// [`spawn`] with the source's settings and the delivery of the events given.
fn spawn_with(
    config: JournalConfig,
    deliver: impl FnMut(&[SecurityEvent]) -> bool + Send + 'static,
    shutdown: CancellationToken,
) -> (JournalMetrics, JoinHandle<()>) {
    let cursor_file = CursorFile::new(&config.cursor_file);
    let source = JournalSource::new(config);
    let metrics = JournalMetrics(source.stats());
    let checkpoint = source.checkpoint();
    let parser = parser(&checkpoint);
    let (sender, entries) = mpsc::channel(QUEUE);
    let task = tokio::spawn(async move {
        let consumer = tokio::spawn(consume(entries, checkpoint.clone(), parser, deliver));
        source.run(sender, shutdown).await;
        let _ = consumer.await;
        // The consumer commits its last batch after the source wrote the cursor for the last
        // time; without this the next start would read those entries again.
        match tokio::task::spawn_blocking(move || checkpoint.persist(&cursor_file)).await {
            Ok(Ok(())) => {}
            Ok(Err(error)) => tracing::warn!("cannot store the journal cursor: {error}"),
            Err(error) => tracing::warn!("cannot store the journal cursor: {error}"),
        }
    });
    (metrics, task)
}

/// The parsers, knowing the boot of the last committed entry: a reboot while the agent was
/// stopped is reported with the first entry of the new boot.
fn parser(checkpoint: &Checkpoint) -> Parser {
    Parser::new(
        ParserConfig::default(),
        security::last_boot(checkpoint.committed().as_deref()),
    )
}

/// Takes the entries until the source ends. The security events of the entries are collected
/// (up to a full batch or `FLUSH_AFTER`) and handed to `deliver`, which writes them to the WAL;
/// only then is the cursor of the last entry committed, so a crash before the write brings the
/// entries back. Entries without events are committed at once while nothing waits. If `deliver`
/// fails, nothing is committed and the events are tried again.
async fn consume(
    mut entries: mpsc::Receiver<Entry>,
    checkpoint: Checkpoint,
    mut parser: Parser,
    mut deliver: impl FnMut(&[SecurityEvent]) -> bool,
) {
    let mut pending: Vec<SecurityEvent> = Vec::new();
    let mut last: Option<String> = None; // the cursor to commit once pending is stored
    let mut deadline = Instant::now();
    let mut flush = |pending: &mut Vec<SecurityEvent>, last: &mut Option<String>| {
        if pending.is_empty() || deliver(pending) {
            pending.clear();
            if let Some(cursor) = last.take() {
                checkpoint.commit(&cursor);
            }
            true
        } else {
            false
        }
    };
    loop {
        let next = if pending.is_empty() {
            entries.recv().await
        } else {
            let Ok(next) = tokio::time::timeout_at(deadline, entries.recv()).await else {
                if !flush(&mut pending, &mut last) {
                    deadline = Instant::now() + FLUSH_AFTER;
                }
                continue;
            };
            next
        };
        let Some(entry) = next else { break };
        let before = pending.len();
        parser.parse(&security::line(&entry), &mut pending);
        if before == 0 && !pending.is_empty() {
            deadline = Instant::now() + FLUSH_AFTER;
        }
        if pending.is_empty() {
            checkpoint.commit(&entry.cursor);
            continue;
        }
        last = Some(entry.cursor);
        if pending.len() > MAX_PENDING {
            let dropped = pending.len() - MAX_PENDING;
            pending.drain(..dropped);
            tracing::warn!(dropped, "security events dropped: the buffer refuses them");
        }
        if pending.len() >= MAX_EVENTS_PER_BATCH && !flush(&mut pending, &mut last) {
            deadline = Instant::now() + FLUSH_AFTER;
        }
    }
    if !flush(&mut pending, &mut last) {
        tracing::warn!(
            events = pending.len(),
            "security events not buffered at shutdown; they come again from the journal"
        );
    }
}

/// The source's counters as the agent's own metrics.
#[derive(Debug)]
pub struct JournalMetrics(Arc<JournalStats>);

impl Collector for JournalMetrics {
    fn name(&self) -> &'static str {
        "journal"
    }

    fn interval_ticks(&self) -> u32 {
        10
    }

    fn collect(&mut self, _: &Context<'_>, out: &mut Vec<Sample>) -> Result<(), CollectError> {
        let count = |value: u64| i64::try_from(value).unwrap_or(i64::MAX);
        out.push(Sample::int(
            "sermon_agent_journal_lost_entries",
            count(self.0.lost_entries.load(Ordering::Relaxed)),
        ));
        out.push(Sample::int(
            "sermon_agent_journal_invalid_lines",
            count(self.0.invalid_lines.load(Ordering::Relaxed)),
        ));
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use sermon_core::{Monotonic, ProcRoot, Value};

    use super::*;

    #[test]
    fn reports_the_counters() {
        let stats = Arc::new(JournalStats::default());
        stats.lost_entries.store(4, Ordering::Relaxed);
        stats.invalid_lines.store(2, Ordering::Relaxed);
        let root = ProcRoot::new("/nonexistent");
        let context = Context {
            root: &root,
            now: Monotonic::from_origin(Duration::ZERO),
        };
        let mut out = Vec::new();
        JournalMetrics(stats).collect(&context, &mut out).unwrap();
        let value = |name: &str| out.iter().find(|s| s.metric == name).map(|s| s.value);
        assert_eq!(
            value("sermon_agent_journal_lost_entries"),
            Some(Value::Int(4))
        );
        assert_eq!(
            value("sermon_agent_journal_invalid_lines"),
            Some(Value::Int(2))
        );
    }

    fn entry(n: u32, identifier: &str, message: &str) -> Entry {
        let line = format!(
            r#"{{"__CURSOR":"s=a;i={n:x};b=new","__REALTIME_TIMESTAMP":"1","_BOOT_ID":"new","_UID":"0","SYSLOG_IDENTIFIER":"{identifier}","MESSAGE":"{message}"}}"#
        );
        sermon_logs::journal::parse_line(line.as_bytes()).unwrap()
    }

    fn failed(n: u32) -> Entry {
        entry(
            n,
            "sshd",
            "Failed password for root from 192.0.2.7 port 22 ssh2",
        )
    }

    fn quiet(n: u32) -> Entry {
        entry(n, "cron", "pam_unix(cron:session): session opened")
    }

    /// The batches delivered, as event names; `fail` makes the next deliveries fail.
    #[derive(Clone, Default)]
    struct Sink {
        batches: Arc<std::sync::Mutex<Vec<Vec<&'static str>>>>,
        fail: Arc<std::sync::atomic::AtomicBool>,
    }

    impl Sink {
        fn deliver(&self) -> impl FnMut(&[SecurityEvent]) -> bool + use<> {
            let sink = self.clone();
            move |events| {
                if sink.fail.load(Ordering::Relaxed) {
                    return false;
                }
                sink.batches
                    .lock()
                    .unwrap()
                    .push(events.iter().map(|e| e.kind.name()).collect());
                true
            }
        }

        fn batches(&self) -> Vec<Vec<&'static str>> {
            self.batches.lock().unwrap().clone()
        }
    }

    struct Consumer {
        sender: mpsc::Sender<Entry>,
        checkpoint: Checkpoint,
        sink: Sink,
        task: JoinHandle<()>,
    }

    fn consumer(committed: Option<&str>) -> Consumer {
        let checkpoint = Checkpoint::new(committed.map(str::to_owned));
        let (sender, entries) = mpsc::channel(QUEUE);
        let sink = Sink::default();
        let task = tokio::spawn(consume(
            entries,
            checkpoint.clone(),
            parser(&checkpoint),
            sink.deliver(),
        ));
        Consumer {
            sender,
            checkpoint,
            sink,
            task,
        }
    }

    impl Consumer {
        async fn send(&self, entry: Entry) {
            self.sender.send(entry).await.unwrap();
            tokio::task::yield_now().await;
        }

        fn committed(&self) -> Option<String> {
            self.checkpoint.committed()
        }

        /// Ends the source and waits for the consumer.
        async fn close(self) -> Self {
            let Self {
                sender,
                checkpoint,
                sink,
                task,
            } = self;
            drop(sender);
            task.await.unwrap();
            let (sender, _) = mpsc::channel(1);
            Self {
                sender,
                checkpoint,
                sink,
                task: tokio::spawn(async {}),
            }
        }
    }

    // Events wait up to a second for more, go to the WAL as one batch, and only then the cursor
    // moves – after a crash before the write the entries come again.
    #[tokio::test(start_paused = true)]
    async fn events_are_batched_and_committed_after_the_write() {
        let c = consumer(Some("s=a;i=0;b=new"));
        c.send(quiet(1)).await;
        assert_eq!(
            c.committed().as_deref(),
            Some("s=a;i=1;b=new"),
            "nothing to store: at once"
        );
        c.send(failed(2)).await;
        c.send(quiet(3)).await;
        c.send(failed(4)).await;
        assert_eq!(
            c.sink.batches(),
            Vec::<Vec<&str>>::new(),
            "nothing written yet"
        );
        assert_eq!(
            c.committed().as_deref(),
            Some("s=a;i=1;b=new"),
            "events wait for the write"
        );
        tokio::time::sleep(FLUSH_AFTER + Duration::from_millis(10)).await;
        assert_eq!(
            c.sink.batches(),
            [vec!["ssh.login.failed", "ssh.login.failed"]]
        );
        assert_eq!(c.committed().as_deref(), Some("s=a;i=4;b=new"));
        c.close().await;
    }

    #[tokio::test(start_paused = true)]
    async fn a_full_batch_goes_at_once_and_the_rest_at_the_end() {
        let c = consumer(None);
        let n = u32::try_from(MAX_EVENTS_PER_BATCH).unwrap();
        for i in 1..=n + 1 {
            c.send(failed(i)).await;
        }
        assert_eq!(c.sink.batches().len(), 1, "a full batch does not wait");
        assert_eq!(c.committed(), Some(format!("s=a;i={n:x};b=new")));
        let c = c.close().await;
        let batches = c.sink.batches();
        assert_eq!(batches.len(), 2, "the rest is written when the source ends");
        assert_eq!(batches[1].len(), 1);
        assert_eq!(c.committed(), Some(format!("s=a;i={:x};b=new", n + 1)));
    }

    // The WAL refuses (disk full): nothing is committed, the events are tried again.
    #[tokio::test(start_paused = true)]
    async fn a_failed_write_commits_nothing_and_retries() {
        let c = consumer(Some("s=a;i=0;b=new"));
        c.sink.fail.store(true, Ordering::Relaxed);
        c.send(failed(1)).await;
        c.send(quiet(2)).await;
        tokio::time::sleep(FLUSH_AFTER * 3).await;
        assert_eq!(c.committed().as_deref(), Some("s=a;i=0;b=new"));
        c.sink.fail.store(false, Ordering::Relaxed);
        tokio::time::sleep(FLUSH_AFTER * 2).await;
        assert_eq!(c.sink.batches(), [vec!["ssh.login.failed"]]);
        assert_eq!(c.committed().as_deref(), Some("s=a;i=2;b=new"));
        c.close().await;
    }

    fn synthetic(n: u32) -> String {
        format!(
            r#"{{"__CURSOR":"s=a;i={n:x};b=new","__REALTIME_TIMESTAMP":"{}","_BOOT_ID":"new","_UID":"0","SYSLOG_IDENTIFIER":"sshd","MESSAGE":"Failed password for root from 192.0.2.7 port 22 ssh2"}}"#,
            1_795_000_000_000_000_u64 + u64::from(n)
        )
    }

    // Stopping the agent while events wait for their batch: they are written at the end, and the
    // cursor after them must reach the file – else the next start reads and sends them again
    // (found in the E2E test: every account event twice after a `systemctl stop`).
    #[tokio::test(flavor = "multi_thread")]
    async fn a_stop_with_waiting_events_writes_them_and_their_cursor() {
        let dir = tempfile::tempdir().unwrap();
        let fake = std::path::PathBuf::from(env!("CARGO_MANIFEST_DIR"))
            .join("../sermon-logs/tests/fake-journalctl");
        std::os::unix::fs::symlink(fake, dir.path().join("journalctl")).unwrap();
        std::fs::write(
            dir.path().join("fake.env"),
            format!(
                "journal='{}'\nargs='{}'\n",
                dir.path().join("journal").display(),
                dir.path().join("args").display()
            ),
        )
        .unwrap();
        let lines: String = (1..=3).map(|n| synthetic(n) + "\n").collect();
        std::fs::write(dir.path().join("journal"), lines).unwrap();
        let cursor_file = CursorFile::new(dir.path().join("journal.cursor"));
        cursor_file.store("s=a;i=0;b=new").unwrap();
        let config = || {
            let mut config = JournalConfig::new(dir.path().join("journal.cursor"));
            config.program = dir.path().join("journalctl");
            config.backoff = Duration::from_millis(20);
            config
        };
        let sink = Sink::default();

        let shutdown = CancellationToken::new();
        let (_, task) = spawn_with(config(), sink.deliver(), shutdown.clone());
        // Read, but the batch waits for FLUSH_AFTER.
        tokio::time::sleep(Duration::from_millis(300)).await;
        assert_eq!(sink.batches(), Vec::<Vec<&str>>::new());
        shutdown.cancel();
        task.await.unwrap();
        assert_eq!(sink.batches(), [vec!["ssh.login.failed"; 3]]);
        assert_eq!(
            cursor_file.load().unwrap().as_deref(),
            Some("s=a;i=3;b=new")
        );

        let again = CancellationToken::new();
        let (_, task) = spawn_with(config(), sink.deliver(), again.clone());
        tokio::time::sleep(FLUSH_AFTER + Duration::from_millis(300)).await;
        again.cancel();
        task.await.unwrap();
        assert_eq!(
            sink.batches().len(),
            1,
            "the events came again after the restart"
        );
    }

    // A reboot while the agent was stopped: the boot of the stored cursor was another.
    #[tokio::test(start_paused = true)]
    async fn the_boot_of_the_stored_cursor_counts() {
        let c = consumer(Some("s=a;i=0;b=old;m=5"));
        c.send(quiet(1)).await;
        let c = c.close().await;
        assert_eq!(c.sink.batches(), [vec!["system.reboot"]]);
    }
}
