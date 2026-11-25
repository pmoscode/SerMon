//! The outbox: producers (collectors) append batches to the write-ahead log, the session sends
//! them and removes them after the server's acknowledgement.

use std::collections::BTreeMap;
use std::sync::{Arc, Mutex, MutexGuard, PoisonError};
use std::time::SystemTime;

use prost::Message;
use sermon_buffer::{Record, Seq, Wal, WalError, WalStats};
use tokio::sync::Notify;

use crate::proto::agent::v1::{
    Batch, LogBatch, LogEntry, MetricBatch, SecurityEvent, SecurityEventBatch, UpdateReport, batch,
};

/// Shared handle to the write-ahead log; cheap to clone.
#[derive(Debug, Clone)]
pub struct Outbox {
    inner: Arc<Inner>,
}

#[derive(Debug)]
struct Inner {
    wal: Mutex<Wal>,
    ready: Notify,
    /// Unacknowledged log batches with their size (M3-04); always locked after `wal`.
    logs: Mutex<BTreeMap<Seq, u64>>,
    /// The bytes logs may take: half of the log, so they never crowd out metrics and events.
    log_share: u64,
}

/// Why log entries were not buffered.
#[derive(Debug, thiserror::Error)]
pub enum LogPushError {
    /// No entries.
    #[error("no log entries")]
    Empty,
    /// Unacknowledged logs take their share of the buffer already; the entries are dropped, not
    /// retried (metrics and security events keep their room).
    #[error("the log share of the buffer is full")]
    ShareFull,
    /// The log could not be written; worth retrying.
    #[error(transparent)]
    Wal(#[from] WalError),
}

impl Outbox {
    /// Wraps an opened log.
    #[must_use]
    pub fn new(wal: Wal) -> Self {
        let log_share = wal.config().max_bytes / 2;
        Self {
            inner: Arc::new(Inner {
                wal: Mutex::new(wal),
                ready: Notify::new(),
                logs: Mutex::new(BTreeMap::new()),
                log_share,
            }),
        }
    }

    /// Buffers log entries as one batch (the caller keeps it within the protocol's limits) and
    /// wakes the session.
    ///
    /// Unacknowledged log batches take at most half of the buffer. Batches buffered before a
    /// restart are not counted until they are sent.
    ///
    /// # Errors
    ///
    /// [`LogPushError::ShareFull`] beyond the share, [`LogPushError::Wal`] if writing failed.
    pub fn push_logs(&self, entries: Vec<LogEntry>, now: SystemTime) -> Result<Seq, LogPushError> {
        if entries.is_empty() {
            return Err(LogPushError::Empty);
        }
        let payload = Batch {
            seq: 0,
            content: Some(batch::Content::Logs(LogBatch { entries })),
        }
        .encode_to_vec();
        let size = u64::try_from(payload.len()).unwrap_or(u64::MAX);
        let mut wal = self.wal();
        let mut logs = self.logs();
        // Acknowledged batches and what the log dropped at its limits (age, bytes) no longer take
        // room: everything before the oldest pending record.
        let stats = wal.stats();
        let oldest = Seq::new(stats.next_seq.get().saturating_sub(stats.pending_records));
        logs.retain(|seq, _| *seq >= oldest);
        if logs.values().sum::<u64>().saturating_add(size) > self.inner.log_share {
            return Err(LogPushError::ShareFull);
        }
        let seq = wal.append(&payload, now)?;
        logs.insert(seq, size);
        drop(logs);
        drop(wal);
        self.inner.ready.notify_one();
        Ok(seq)
    }

    /// Buffers the samples of one collection tick and wakes the session.
    ///
    /// # Errors
    ///
    /// [`WalError`] if the batch can't be written.
    pub fn push_metrics(&self, metrics: MetricBatch, now: SystemTime) -> Result<Seq, WalError> {
        // The sequence number is assigned by the log and set when sending.
        let payload = Batch {
            seq: 0,
            content: Some(batch::Content::Metrics(metrics)),
        }
        .encode_to_vec();
        let seq = self.wal().append(&payload, now)?;
        self.inner.ready.notify_one();
        Ok(seq)
    }

    /// Buffers the samples of one tick as metric batches (at most [`MAX_SAMPLES_PER_BATCH`] each);
    /// returns the sequence numbers. No samples, no batch.
    ///
    /// # Errors
    ///
    /// [`WalError`] if a batch can't be written.
    pub fn push_samples(
        &self,
        samples: &[sermon_core::Sample],
        collected_at: SystemTime,
    ) -> Result<Vec<Seq>, WalError> {
        samples
            .chunks(MAX_SAMPLES_PER_BATCH)
            .map(|chunk| {
                self.push_metrics(
                    MetricBatch {
                        collected_at: Some(collected_at.into()),
                        samples: chunk.iter().map(Into::into).collect(),
                    },
                    collected_at,
                )
            })
            .collect()
    }

    /// Buffers security events (at most [`MAX_EVENTS_PER_BATCH`] per batch) and wakes the session;
    /// returns the sequence numbers. No events, no batch.
    ///
    /// # Errors
    ///
    /// [`WalError`] if a batch can't be written.
    pub fn push_security_events(
        &self,
        events: Vec<SecurityEvent>,
        now: SystemTime,
    ) -> Result<Vec<Seq>, WalError> {
        let mut seqs = Vec::new();
        let mut events = events.into_iter().peekable();
        while events.peek().is_some() {
            let chunk: Vec<SecurityEvent> = events.by_ref().take(MAX_EVENTS_PER_BATCH).collect();
            let payload = Batch {
                seq: 0,
                content: Some(batch::Content::SecurityEvents(SecurityEventBatch {
                    events: chunk,
                })),
            }
            .encode_to_vec();
            seqs.push(self.wal().append(&payload, now)?);
        }
        if !seqs.is_empty() {
            self.inner.ready.notify_one();
        }
        Ok(seqs)
    }

    /// Buffers an update report (M4-07) and wakes the session.
    ///
    /// # Errors
    ///
    /// [`WalError`] if the batch can't be written.
    pub fn push_update_report(
        &self,
        report: UpdateReport,
        now: SystemTime,
    ) -> Result<Seq, WalError> {
        let payload = Batch {
            seq: 0,
            content: Some(batch::Content::UpdateReport(report)),
        }
        .encode_to_vec();
        let seq = self.wal().append(&payload, now)?;
        self.inner.ready.notify_one();
        Ok(seq)
    }

    /// Housekeeping, called periodically: fsync and the age limit (also while disconnected).
    ///
    /// # Errors
    ///
    /// [`WalError`] from the log.
    pub fn maintain(&self, now: SystemTime) -> Result<(), WalError> {
        let mut wal = self.wal();
        wal.expire(now)?;
        wal.sync(now)
    }

    /// Counters of the log.
    #[must_use]
    pub fn stats(&self) -> WalStats {
        self.wal().stats()
    }

    pub(crate) fn read_after(
        &self,
        after: Seq,
        max_records: usize,
    ) -> Result<Vec<Record>, WalError> {
        self.wal().read_after(after, max_records, READ_BYTES)
    }

    pub(crate) fn ack(&self, seq: Seq) -> Result<(), WalError> {
        self.wal().ack(seq)
    }

    /// Completes after the next push (or immediately if one happened since the last wait).
    pub(crate) async fn ready(&self) {
        self.inner.ready.notified().await;
    }

    fn logs(&self) -> MutexGuard<'_, BTreeMap<Seq, u64>> {
        self.inner
            .logs
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
    }

    fn wal(&self) -> MutexGuard<'_, Wal> {
        // The log stays consistent even if a holder panicked: every operation is self-contained.
        self.inner
            .wal
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
    }
}

/// Largest number of samples in one batch (protocol limit).
pub const MAX_SAMPLES_PER_BATCH: usize = 2000;

/// Largest number of security events in one batch (protocol limit).
pub const MAX_EVENTS_PER_BATCH: usize = 500;

/// Bytes read from the log at once (several batches, bounded memory).
const READ_BYTES: u64 = 1024 * 1024;

/// Decodes a buffered payload and sets its sequence number.
pub(crate) fn batch_for(record: &Record) -> Result<Batch, prost::DecodeError> {
    let mut batch = Batch::decode(record.payload.as_slice())?;
    batch.seq = record.seq.get();
    Ok(batch)
}

#[cfg(test)]
mod tests {
    use std::time::{Duration, UNIX_EPOCH};

    use sermon_buffer::WalConfig;

    use super::*;
    use crate::proto::agent::v1::{LogEntry, Sample, sample};

    fn metrics() -> MetricBatch {
        MetricBatch {
            collected_at: Some(UNIX_EPOCH.into()),
            samples: vec![Sample {
                metric: "sermon_load_1m".into(),
                labels: std::collections::HashMap::default(),
                value: Some(sample::Value::IntValue(3)),
            }],
        }
    }

    fn login(n: usize) -> SecurityEvent {
        SecurityEvent {
            occurred_at: Some(UNIX_EPOCH.into()),
            event: Some(
                crate::proto::agent::v1::security_event::Event::SshInvalidUser(
                    crate::proto::agent::v1::SshInvalidUser {
                        user: format!("u{n}"),
                        remote: None,
                    },
                ),
            ),
            unverified: false,
        }
    }

    // Security events share the log with the metrics; at most MAX_EVENTS_PER_BATCH per batch.
    #[test]
    fn security_events_are_buffered_in_batches_within_the_protocol_limit() {
        let dir = tempfile::tempdir().unwrap();
        let now = UNIX_EPOCH + Duration::from_hours(498_624);
        let (wal, _) = Wal::open(dir.path(), WalConfig::default(), now).unwrap();
        let outbox = Outbox::new(wal);
        let events: Vec<SecurityEvent> = (0..=MAX_EVENTS_PER_BATCH).map(login).collect();

        let seqs = outbox.push_security_events(events.clone(), now).unwrap();
        assert_eq!(seqs.len(), 2);
        assert_eq!(
            outbox.push_security_events(Vec::new(), now).unwrap(),
            Vec::new()
        );
        let records = outbox.read_after(Seq::default(), 10).unwrap();
        let back: Vec<SecurityEvent> = records
            .iter()
            .flat_map(|record| match batch_for(record).unwrap().content {
                Some(batch::Content::SecurityEvents(b)) => b.events,
                other => panic!("{other:?}"),
            })
            .collect();
        assert_eq!(back, events);
    }

    // M4-07: an update report is a batch of its own and wakes the session.
    #[test]
    fn update_reports_are_buffered_as_one_batch() {
        let dir = tempfile::tempdir().unwrap();
        let now = UNIX_EPOCH + Duration::from_hours(498_624);
        let (wal, _) = Wal::open(dir.path(), WalConfig::default(), now).unwrap();
        let outbox = Outbox::new(wal);
        let report = UpdateReport {
            collected_at: Some(now.into()),
            total_count: 3,
            ..UpdateReport::default()
        };
        let seq = outbox.push_update_report(report.clone(), now).unwrap();
        let records = outbox.read_after(Seq::default(), 10).unwrap();
        assert_eq!(records.len(), 1);
        assert_eq!(records[0].seq, seq);
        match batch_for(&records[0]).unwrap().content {
            Some(batch::Content::UpdateReport(back)) => assert_eq!(back, report),
            other => panic!("{other:?}"),
        }
    }

    fn log_entry(message: &str) -> LogEntry {
        LogEntry {
            occurred_at: Some(UNIX_EPOCH.into()),
            source: crate::proto::agent::v1::LogSource::Journal.into(),
            message: message.into(),
            ..LogEntry::default()
        }
    }

    fn small_wal(dir: &std::path::Path, now: SystemTime) -> Wal {
        let config = WalConfig {
            max_bytes: 4 * 1024 * 1024,
            segment_bytes: 256 * 1024,
            max_record_bytes: 512 * 1024,
            ..WalConfig::default()
        };
        Wal::open(dir, config, now).unwrap().0
    }

    // Logs take at most half of the buffer (M3-04): metrics and security events are never
    // crowded out by them; an acknowledgement frees the share again.
    #[test]
    fn logs_get_at_most_half_of_the_buffer() {
        let dir = tempfile::tempdir().unwrap();
        let now = UNIX_EPOCH + Duration::from_hours(498_624);
        let outbox = Outbox::new(small_wal(dir.path(), now));
        let big = log_entry(&"x".repeat(16 * 1024));
        let batch: Vec<LogEntry> = vec![big; 15]; // about 240 KiB
        let mut seqs = Vec::new();
        let full = loop {
            match outbox.push_logs(batch.clone(), now) {
                Ok(seq) => seqs.push(seq),
                Err(error) => break error,
            }
            assert!(seqs.len() < 20, "the share has no end");
        };
        assert!(matches!(full, LogPushError::ShareFull), "{full}");
        // 2 MiB of 4 MiB: 8 batches of about 240 KiB fit.
        assert_eq!(seqs.len(), 8, "{seqs:?}");
        // Metrics and events still go in.
        outbox.push_metrics(metrics(), now).unwrap();
        outbox.push_security_events(vec![login(1)], now).unwrap();

        outbox.ack(seqs[2]).unwrap();
        assert_eq!(
            outbox.push_logs(batch.clone(), now).ok().map(|_| ()),
            Some(())
        );
        let records = outbox.read_after(seqs[2], 20).unwrap();
        assert!(records.iter().any(|r| matches!(
            batch_for(r).unwrap().content,
            Some(batch::Content::Logs(ref logs)) if logs.entries.len() == 15
        )));
    }

    #[test]
    fn an_empty_log_batch_is_not_buffered() {
        let dir = tempfile::tempdir().unwrap();
        let now = UNIX_EPOCH + Duration::from_hours(498_624);
        let outbox = Outbox::new(small_wal(dir.path(), now));
        assert!(matches!(
            outbox.push_logs(Vec::new(), now),
            Err(LogPushError::Empty)
        ));
        assert_eq!(outbox.stats().pending_records, 0);
    }

    // Records the buffer drops at its limits (age, bytes) no longer count against the share.
    #[test]
    fn dropped_records_free_the_share() {
        let dir = tempfile::tempdir().unwrap();
        let now = UNIX_EPOCH + Duration::from_hours(498_624);
        let config = WalConfig {
            max_bytes: 4 * 1024 * 1024,
            segment_bytes: 256 * 1024,
            max_record_bytes: 512 * 1024,
            max_age: Duration::from_hours(2),
            segment_span: Duration::from_mins(10),
            ..WalConfig::default()
        };
        let outbox = Outbox::new(Wal::open(dir.path(), config, now).unwrap().0);
        let batch: Vec<LogEntry> = vec![log_entry(&"y".repeat(16 * 1024)); 15];
        while outbox.push_logs(batch.clone(), now).is_ok() {}
        let later = now + Duration::from_hours(3);
        outbox.maintain(later).unwrap();
        assert!(
            outbox.push_logs(batch, later).is_ok(),
            "the expired logs freed their share"
        );
    }

    #[test]
    fn samples_are_split_into_batches_within_the_protocol_limit() {
        let dir = tempfile::tempdir().unwrap();
        let now = UNIX_EPOCH + Duration::from_hours(498_624);
        let (wal, _) = Wal::open(dir.path(), WalConfig::default(), now).unwrap();
        let outbox = Outbox::new(wal);
        let samples = vec![sermon_core::Sample::int("sermon_x", 1); MAX_SAMPLES_PER_BATCH + 1];

        let seqs = outbox.push_samples(&samples, now).unwrap();
        assert_eq!(outbox.push_samples(&[], now).unwrap(), Vec::new());

        assert_eq!(seqs.len(), 2);
        let records = outbox.read_after(Seq::default(), 10).unwrap();
        let sizes: Vec<usize> = records
            .iter()
            .map(|record| match batch_for(record).unwrap().content {
                Some(batch::Content::Metrics(metrics)) => metrics.samples.len(),
                _ => 0,
            })
            .collect();
        assert_eq!(sizes, vec![MAX_SAMPLES_PER_BATCH, 1]);
    }

    #[tokio::test]
    async fn pushed_batches_come_back_with_their_sequence_number_and_wake_the_session() {
        let dir = tempfile::tempdir().unwrap();
        let now = UNIX_EPOCH + Duration::from_hours(498_624);
        let (wal, _) = Wal::open(dir.path(), WalConfig::default(), now).unwrap();
        let outbox = Outbox::new(wal);

        let seq = outbox.push_metrics(metrics(), now).unwrap();
        outbox.ready().await; // the permit from the push: completes at once

        let records = outbox.read_after(Seq::default(), 10).unwrap();
        let batch = batch_for(&records[0]).unwrap();
        assert_eq!(batch.seq, seq.get());
        assert_eq!(batch.content, Some(batch::Content::Metrics(metrics())));
    }
}
