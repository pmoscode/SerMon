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

use sermon_collectors::{CollectError, Collector, Context};
use sermon_core::Sample;
use sermon_logs::journal::{Checkpoint, Entry, JournalConfig, JournalSource, JournalStats};
use tokio::sync::mpsc;
use tokio::task::JoinHandle;
use tokio_util::sync::CancellationToken;

use crate::state::StateLayout;

/// Entries in flight between source and consumer; a slow consumer holds `journalctl` back.
const QUEUE: usize = 256;

/// Starts the source and its consumer; the task ends after `shutdown` with the cursor written.
#[must_use]
pub fn spawn(
    layout: &StateLayout,
    shutdown: CancellationToken,
) -> (JournalMetrics, JoinHandle<()>) {
    let source = JournalSource::new(JournalConfig::new(layout.journal_cursor_file()));
    let metrics = JournalMetrics(source.stats());
    let checkpoint = source.checkpoint();
    let (sender, entries) = mpsc::channel(QUEUE);
    let task = tokio::spawn(async move {
        let consumer = tokio::spawn(consume(entries, checkpoint));
        source.run(sender, shutdown).await;
        let _ = consumer.await;
    });
    (metrics, task)
}

/// Takes the entries until the source ends. Nothing is stored from them yet, so each is committed
/// at once; with the security parsers (M2-06) an entry is committed once its events are in the WAL.
async fn consume(mut entries: mpsc::Receiver<Entry>, checkpoint: Checkpoint) {
    while let Some(entry) = entries.recv().await {
        checkpoint.commit(&entry.cursor);
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
            now: Monotonic::from_origin(std::time::Duration::ZERO),
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

    #[tokio::test]
    async fn the_interim_consumer_commits_every_entry() {
        let checkpoint = Checkpoint::default();
        let (sender, entries) = mpsc::channel(4);
        let consumer = tokio::spawn(consume(entries, checkpoint.clone()));
        for n in 1..=3 {
            let line = format!(r#"{{"__CURSOR":"s=a;i={n}","__REALTIME_TIMESTAMP":"1"}}"#);
            sender
                .send(sermon_logs::journal::parse_line(line.as_bytes()).unwrap())
                .await
                .unwrap();
        }
        drop(sender);
        consumer.await.unwrap();
        assert_eq!(checkpoint.committed().as_deref(), Some("s=a;i=3"));
    }
}
