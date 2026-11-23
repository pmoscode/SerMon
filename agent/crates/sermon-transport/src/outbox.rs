//! The outbox: producers (collectors) append batches to the write-ahead log, the session sends
//! them and removes them after the server's acknowledgement.

use std::sync::{Arc, Mutex, MutexGuard, PoisonError};
use std::time::SystemTime;

use prost::Message;
use sermon_buffer::{Record, Seq, Wal, WalError, WalStats};
use tokio::sync::Notify;

use crate::proto::agent::v1::{Batch, MetricBatch, SecurityEvent, SecurityEventBatch, batch};

/// Shared handle to the write-ahead log; cheap to clone.
#[derive(Debug, Clone)]
pub struct Outbox {
    inner: Arc<Inner>,
}

#[derive(Debug)]
struct Inner {
    wal: Mutex<Wal>,
    ready: Notify,
}

impl Outbox {
    /// Wraps an opened log.
    #[must_use]
    pub fn new(wal: Wal) -> Self {
        Self {
            inner: Arc::new(Inner {
                wal: Mutex::new(wal),
                ready: Notify::new(),
            }),
        }
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
    use crate::proto::agent::v1::{Sample, sample};

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
                Some(batch::Content::SecurityEvents(_) | batch::Content::Logs(_)) | None => 0,
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
