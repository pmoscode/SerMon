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
use sermon_security::{Parser, ParserConfig, SecurityEvent};
use tokio::sync::mpsc;
use tokio::task::JoinHandle;
use tokio_util::sync::CancellationToken;

use crate::security;
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
    let parser = parser(&checkpoint);
    let (sender, entries) = mpsc::channel(QUEUE);
    let task = tokio::spawn(async move {
        let consumer = tokio::spawn(consume(entries, checkpoint, parser, |events| {
            for event in events {
                tracing::debug!(
                    event = event.kind.name(),
                    "security event (sending follows)"
                );
            }
        }));
        source.run(sender, shutdown).await;
        let _ = consumer.await;
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

/// Takes the entries until the source ends: the security events of each go to `deliver`, then
/// the entry is committed. `deliver` only logs them until M2-07 writes them to the WAL; then the
/// commit follows the write.
async fn consume(
    mut entries: mpsc::Receiver<Entry>,
    checkpoint: Checkpoint,
    mut parser: Parser,
    mut deliver: impl FnMut(Vec<SecurityEvent>),
) {
    let mut events = Vec::new();
    while let Some(entry) = entries.recv().await {
        parser.parse(&security::line(&entry), &mut events);
        if !events.is_empty() {
            deliver(std::mem::take(&mut events));
        }
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
    async fn the_consumer_parses_and_commits_every_entry() {
        let checkpoint = Checkpoint::default();
        let (sender, entries) = mpsc::channel(4);
        let delivered = Arc::new(std::sync::Mutex::new(Vec::new()));
        let sink = delivered.clone();
        let parser = parser(&Checkpoint::new(Some("s=a;i=0;b=old;m=5".into())));
        let consumer = tokio::spawn(consume(
            entries,
            checkpoint.clone(),
            parser,
            move |events| {
                sink.lock()
                    .unwrap()
                    .extend(events.into_iter().map(|e| e.kind.name()));
            },
        ));
        for (n, identifier, message) in [
            (1, "sshd", "Invalid user mallory from 192.0.2.7 port 22"),
            (2, "cron", "pam_unix(cron:session): session opened"),
            (
                3,
                "sshd",
                "Failed password for root from 192.0.2.7 port 22 ssh2",
            ),
        ] {
            let line = format!(
                r#"{{"__CURSOR":"s=a;i={n};b=new","__REALTIME_TIMESTAMP":"1","_BOOT_ID":"new","_UID":"0","SYSLOG_IDENTIFIER":"{identifier}","MESSAGE":"{message}"}}"#
            );
            sender
                .send(sermon_logs::journal::parse_line(line.as_bytes()).unwrap())
                .await
                .unwrap();
        }
        drop(sender);
        consumer.await.unwrap();
        assert_eq!(checkpoint.committed().as_deref(), Some("s=a;i=3;b=new"));
        assert_eq!(
            *delivered.lock().unwrap(),
            [
                "system.reboot",
                "ssh.login.invalid_user",
                "ssh.login.failed"
            ],
            "the boot of the stored cursor was another"
        );
    }
}
