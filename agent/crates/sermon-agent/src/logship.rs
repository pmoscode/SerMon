//! Shipping log entries (M3-04, KONZEPT §10): the entries the server's configuration selects
//! (ADR 0030), within the agent's rate, redacted before they reach the WAL, in batches of at most
//! 1 000 entries or 256 KiB.
//!
//! - **Rate:** a node sends at most [`LINES_PER_SECOND`] entries and [`BYTES_PER_SECOND`] bytes
//!   (with bursts); beyond it entries are dropped and counted.
//! - **Buffer:** logs take at most half of the WAL (`Outbox::push_logs`); beyond it they are
//!   dropped and counted, metrics and security events keep their room. Batches the WAL refuses
//!   for another reason (disk) wait and are tried again, at most [`MAX_READY`] of them.
//!
//! | Metric | Unit |
//! |---|---|
//! | `sermon_agent_logs_sent_entries` | entries buffered for sending since the start |
//! | `sermon_agent_logs_dropped_entries{reason}` | entries dropped since the start: `rate`, `buffer` |
//! | `sermon_agent_logs_redacted_entries` | entries in which redaction replaced something |

use std::collections::VecDeque;
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{Duration, SystemTime};

use sermon_collectors::{CollectError, Collector, Context};
use sermon_core::Sample;
use sermon_logs::record::{Batcher, LogRecord};
use sermon_security::Redactor;
use sermon_transport::outbox::{LogPushError, Outbox};
use tokio::sync::watch;
use tokio::time::Instant;

use crate::agent_config::Active;
use crate::logs;

/// Entries per second a node may send, and the burst.
pub const LINES_PER_SECOND: f64 = 500.0;
const LINES_BURST: f64 = 5_000.0;
/// Bytes per second a node may send (counted with `LogRecord::size`), and the burst.
pub const BYTES_PER_SECOND: f64 = 512.0 * 1024.0;
const BYTES_BURST: f64 = 5.0 * 1024.0 * 1024.0;
/// Full batches kept while the WAL refuses them; beyond it the oldest are dropped.
pub const MAX_READY: usize = 8;
/// Entries wait at most this long for more before they go to the WAL (KONZEPT §5).
pub const FLUSH_AFTER: Duration = Duration::from_secs(1);

/// What became of a batch.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Delivery {
    /// In the WAL.
    Stored,
    /// Dropped for good (the log share of the buffer is full).
    Dropped,
    /// Not written; try again.
    Failed,
}

/// Counters for the agent's own metrics.
#[derive(Debug, Default)]
pub struct ShipStats {
    sent: AtomicU64,
    dropped_rate: AtomicU64,
    dropped_buffer: AtomicU64,
    redacted: AtomicU64,
}

fn add(counter: &AtomicU64, n: usize) {
    counter.fetch_add(u64::try_from(n).unwrap_or(u64::MAX), Ordering::Relaxed);
}

/// A token bucket.
#[derive(Debug)]
struct Bucket {
    rate: f64,
    burst: f64,
    tokens: f64,
    last: Option<Instant>,
}

impl Bucket {
    fn new(rate: f64, burst: f64) -> Self {
        Self {
            rate,
            burst,
            tokens: burst,
            last: None,
        }
    }

    fn refill(&mut self, now: Instant) {
        if let Some(last) = self.last {
            let elapsed = now.saturating_duration_since(last).as_secs_f64();
            self.tokens = (self.tokens + elapsed * self.rate).min(self.burst);
        }
        self.last = Some(now);
    }
}

/// Hands a batch to the buffer.
pub type Deliver = Box<dyn FnMut(&[LogRecord]) -> Delivery + Send>;

/// Selects, limits, redacts and batches the log entries of one source.
pub struct Shipper {
    config: watch::Receiver<Arc<Active>>,
    redactor_version: Option<u64>,
    lines: Bucket,
    bytes: Bucket,
    batcher: Batcher,
    ready: VecDeque<Vec<LogRecord>>,
    deliver: Deliver,
    stats: Arc<ShipStats>,
}

impl std::fmt::Debug for Shipper {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Shipper")
            .field("ready", &self.ready.len())
            .finish_non_exhaustive()
    }
}

impl Shipper {
    /// A shipper following `config` and writing with `deliver`.
    #[must_use]
    pub fn new(
        config: watch::Receiver<Arc<Active>>,
        stats: Arc<ShipStats>,
        deliver: Deliver,
    ) -> Self {
        Self {
            config,
            redactor_version: None,
            lines: Bucket::new(LINES_PER_SECOND, LINES_BURST),
            bytes: Bucket::new(BYTES_PER_SECOND, BYTES_BURST),
            batcher: Batcher::default(),
            ready: VecDeque::new(),
            deliver,
            stats,
        }
    }

    /// Whether logs are sent at all (worth building records for).
    #[must_use]
    pub fn enabled(&self) -> bool {
        self.config.borrow().logs.enabled
    }

    /// The redactor of a configuration not seen yet (for the sudo commands of the security
    /// parsers).
    pub fn changed_redactor(&mut self) -> Option<Redactor> {
        let active = self.config.borrow().clone();
        (self.redactor_version != Some(active.version)).then(|| {
            self.redactor_version = Some(active.version);
            active.redactor.clone()
        })
    }

    /// Takes an entry: dropped if not selected or over the rate, else redacted and batched.
    pub fn offer(&mut self, mut record: LogRecord, now: Instant) {
        let active = self.config.borrow().clone();
        if !active.logs.sends(
            record.unit.as_deref(),
            record.identifier.as_deref(),
            record.severity,
        ) {
            return;
        }
        self.lines.refill(now);
        self.bytes.refill(now);
        let size = f64::from(u32::try_from(record.size()).unwrap_or(u32::MAX));
        if self.lines.tokens < 1.0 || self.bytes.tokens < size {
            add(&self.stats.dropped_rate, 1);
            return;
        }
        self.lines.tokens -= 1.0;
        self.bytes.tokens -= size;
        let before = record.message.len();
        let mut changed = false;
        record.redact_with(|text: &str| {
            let out = active.redactor.redact(text);
            changed = matches!(out, std::borrow::Cow::Owned(_));
            out
        });
        if changed || record.message.len() != before {
            add(&self.stats.redacted, 1);
        }
        if let Some(full) = self.batcher.push(record) {
            self.ready.push_back(full);
            if self.ready.len() > MAX_READY
                && let Some(oldest) = self.ready.pop_front()
            {
                add(&self.stats.dropped_buffer, oldest.len());
            }
        }
    }

    /// Whether a full batch waits.
    #[must_use]
    pub fn has_full(&self) -> bool {
        !self.ready.is_empty()
    }

    /// Whether nothing waits.
    #[must_use]
    pub fn is_empty(&self) -> bool {
        self.ready.is_empty() && self.batcher.is_empty()
    }

    /// Writes everything waiting; `false` if the buffer failed (the rest waits for the next try).
    pub fn flush(&mut self) -> bool {
        if !self.batcher.is_empty() {
            let partial = self.batcher.take();
            self.ready.push_back(partial);
        }
        while let Some(batch) = self.ready.front() {
            match (self.deliver)(batch) {
                Delivery::Stored => add(&self.stats.sent, batch.len()),
                Delivery::Dropped => add(&self.stats.dropped_buffer, batch.len()),
                Delivery::Failed => return false,
            }
            self.ready.pop_front();
        }
        true
    }
}

/// Delivery into the outbox.
pub fn into_outbox(outbox: Outbox) -> Deliver {
    Box::new(move |batch| {
        let entries = batch.iter().map(logs::to_proto).collect();
        match outbox.push_logs(entries, SystemTime::now()) {
            Ok(_) => Delivery::Stored,
            Err(LogPushError::ShareFull | LogPushError::Empty) => Delivery::Dropped,
            Err(LogPushError::Wal(error)) => {
                tracing::warn!("cannot buffer log entries, retrying: {error}");
                Delivery::Failed
            }
        }
    })
}

/// The counters as the agent's own metrics.
#[derive(Debug)]
pub struct LogMetrics(pub Arc<ShipStats>);

impl Collector for LogMetrics {
    fn name(&self) -> &'static str {
        "logs"
    }

    fn interval_ticks(&self) -> u32 {
        10
    }

    fn collect(&mut self, _: &Context<'_>, out: &mut Vec<Sample>) -> Result<(), CollectError> {
        let count = |c: &AtomicU64| i64::try_from(c.load(Ordering::Relaxed)).unwrap_or(i64::MAX);
        let stats = &self.0;
        out.push(Sample::int(
            "sermon_agent_logs_sent_entries",
            count(&stats.sent),
        ));
        out.push(
            Sample::int(
                "sermon_agent_logs_dropped_entries",
                count(&stats.dropped_rate),
            )
            .label("reason", "rate"),
        );
        out.push(
            Sample::int(
                "sermon_agent_logs_dropped_entries",
                count(&stats.dropped_buffer),
            )
            .label("reason", "buffer"),
        );
        out.push(Sample::int(
            "sermon_agent_logs_redacted_entries",
            count(&stats.redacted),
        ));
        Ok(())
    }
}

#[cfg(test)]
pub(crate) mod tests {
    use std::sync::Mutex;

    use sermon_core::{Monotonic, ProcRoot, Value};
    use sermon_logs::record::{MAX_BATCH_RECORDS, Origin, Severity};
    use sermon_transport::proto::agent::v1::{
        AgentConfig, ConfigUpdate, LogConfig, LogSeverity, RedactionRule,
    };

    use super::*;
    use crate::agent_config::compile;

    pub(crate) fn active(enabled: bool) -> Arc<Active> {
        let update = ConfigUpdate {
            version: 2,
            config: Some(AgentConfig {
                logs: Some(LogConfig {
                    enabled,
                    min_severity: LogSeverity::Info.into(),
                    exclude_units: vec!["cron.service".into()],
                    files: Vec::new(),
                }),
                redaction_rules: vec![RedactionRule {
                    pattern: r"customer-\d{6}".into(),
                }],
            }),
        };
        Arc::new(compile(&update, true).unwrap())
    }

    pub(crate) fn record(unit: &str, severity: Option<Severity>, message: &str) -> LogRecord {
        LogRecord {
            at_us: 1_795_000_000_000_000,
            origin: Origin::Journal,
            unit: Some(unit.into()),
            identifier: None,
            severity,
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

    /// The batches written, and what the next write returns.
    #[derive(Clone)]
    pub(crate) struct Written {
        pub(crate) batches: Arc<Mutex<Vec<Vec<LogRecord>>>>,
        pub(crate) next: Arc<Mutex<Delivery>>,
    }

    impl Written {
        pub(crate) fn new() -> Self {
            Self {
                batches: Arc::default(),
                next: Arc::new(Mutex::new(Delivery::Stored)),
            }
        }

        pub(crate) fn deliver(&self) -> Deliver {
            let written = self.clone();
            Box::new(move |batch| {
                let next = *written.next.lock().unwrap();
                if next == Delivery::Stored {
                    written.batches.lock().unwrap().push(batch.to_vec());
                }
                next
            })
        }

        pub(crate) fn messages(&self) -> Vec<String> {
            self.batches
                .lock()
                .unwrap()
                .iter()
                .flatten()
                .map(|r| r.message.clone())
                .collect()
        }
    }

    pub(crate) fn shipper(
        enabled: bool,
    ) -> (Shipper, Written, Arc<ShipStats>, watch::Sender<Arc<Active>>) {
        let (config, receiver) = watch::channel(active(enabled));
        let written = Written::new();
        let stats = Arc::new(ShipStats::default());
        let shipper = Shipper::new(receiver, stats.clone(), written.deliver());
        (shipper, written, stats, config)
    }

    #[tokio::test(start_paused = true)]
    async fn selects_redacts_and_batches_until_the_flush() {
        let (mut s, written, stats, _config) = shipper(true);
        let now = Instant::now();
        s.offer(
            record(
                "nginx.service",
                Some(Severity::Error),
                "order customer-123456 failed, password=hunter2",
            ),
            now,
        );
        s.offer(
            record("nginx.service", Some(Severity::Debug), "too chatty"),
            now,
        );
        s.offer(
            record("cron.service", Some(Severity::Error), "excluded"),
            now,
        );
        s.offer(record("app.service", None, "no severity"), now);
        assert!(!s.is_empty() && !s.has_full());
        assert!(written.messages().is_empty(), "nothing before the flush");
        assert!(s.flush());
        assert_eq!(
            written.messages(),
            ["order *** failed, password=***", "no severity"]
        );
        assert!(s.is_empty());
        assert_eq!(stats.sent.load(Ordering::Relaxed), 2);
        assert_eq!(stats.redacted.load(Ordering::Relaxed), 1);
    }

    #[tokio::test(start_paused = true)]
    async fn nothing_is_shipped_while_logs_are_off() {
        let (mut s, written, _, config) = shipper(false);
        assert!(!s.enabled());
        s.offer(record("nginx.service", None, "x"), Instant::now());
        assert!(s.is_empty() && s.flush() && written.messages().is_empty());
        // Switched on by the server: from then on.
        config.send_replace(active(true));
        assert!(s.enabled());
        s.offer(record("nginx.service", None, "y"), Instant::now());
        assert!(s.flush());
        assert_eq!(written.messages(), ["y"]);
    }

    #[tokio::test(start_paused = true)]
    async fn a_full_batch_is_ready_at_once() {
        let (mut s, _, _, _config) = shipper(true);
        let now = Instant::now();
        for i in 0..MAX_BATCH_RECORDS {
            s.offer(record("a.service", None, &format!("{i}")), now);
        }
        assert!(!s.has_full(), "the batch is full but not closed yet");
        s.offer(record("a.service", None, "next"), now);
        assert!(s.has_full());
    }

    // A flood: the burst passes, then the rate; the rest is counted.
    #[tokio::test(start_paused = true)]
    async fn a_flood_is_limited_to_the_rate() {
        let (mut s, written, stats, _config) = shipper(true);
        let start = Instant::now();
        for _ in 0..6_000 {
            s.offer(record("a.service", None, "x"), start);
        }
        assert_eq!(
            stats.dropped_rate.load(Ordering::Relaxed),
            1_000,
            "beyond the burst"
        );
        assert!(s.flush());
        tokio::time::advance(Duration::from_secs(2)).await;
        for _ in 0..2_000 {
            s.offer(record("a.service", None, "x"), Instant::now());
        }
        assert!(s.flush());
        let sent = written.messages().len();
        assert_eq!(sent, 5_000 + 1_000, "burst plus two seconds at the rate");
        assert_eq!(stats.dropped_rate.load(Ordering::Relaxed), 2_000);
    }

    #[tokio::test(start_paused = true)]
    async fn large_entries_are_limited_by_bytes() {
        let (mut s, _, stats, _config) = shipper(true);
        let big = "z".repeat(16 * 1024);
        for _ in 0..400 {
            s.offer(record("a.service", None, &big), Instant::now());
        }
        let dropped = stats.dropped_rate.load(Ordering::Relaxed);
        // 5 MiB burst / ~16 KiB per entry: about 320 pass.
        assert!((70..=90).contains(&dropped), "dropped {dropped}");
    }

    // The WAL refuses (disk): the batches wait and are tried again; the share of the buffer is
    // full: they are dropped and counted, the source moves on.
    #[tokio::test(start_paused = true)]
    async fn failed_writes_wait_and_a_full_share_drops() {
        let (mut s, written, stats, _config) = shipper(true);
        *written.next.lock().unwrap() = Delivery::Failed;
        s.offer(record("a.service", None, "kept"), Instant::now());
        assert!(!s.flush());
        assert!(!s.is_empty());
        *written.next.lock().unwrap() = Delivery::Stored;
        assert!(s.flush());
        assert_eq!(written.messages(), ["kept"]);

        *written.next.lock().unwrap() = Delivery::Dropped;
        s.offer(record("a.service", None, "lost"), Instant::now());
        assert!(s.flush() && s.is_empty());
        assert_eq!(stats.dropped_buffer.load(Ordering::Relaxed), 1);
    }

    #[tokio::test(start_paused = true)]
    async fn waiting_batches_are_bounded() {
        let (mut s, written, stats, _config) = shipper(true);
        *written.next.lock().unwrap() = Delivery::Failed;
        for i in 0..(MAX_READY + 2) * MAX_BATCH_RECORDS {
            if i % MAX_BATCH_RECORDS == 0 {
                tokio::time::advance(Duration::from_secs(3)).await; // within the rate
            }
            s.offer(record("a.service", None, &i.to_string()), Instant::now());
        }
        assert_eq!(
            stats.dropped_buffer.load(Ordering::Relaxed),
            u64::try_from(MAX_BATCH_RECORDS).unwrap(),
            "the oldest full batch"
        );
    }

    #[tokio::test(start_paused = true)]
    async fn hands_out_the_redactor_of_each_new_configuration_once() {
        let (mut s, _, _, config) = shipper(true);
        let first = s.changed_redactor().unwrap();
        assert_eq!(first.redact("customer-123456"), "***");
        assert!(s.changed_redactor().is_none());
        let mut next = (*active(true)).clone();
        next.version = 3;
        config.send_replace(Arc::new(next));
        assert!(s.changed_redactor().is_some());
    }

    // KONZEPT §10.3, T42: redaction happens before the WAL – the secret is in no file of the
    // buffer, the masked text is.
    #[tokio::test(start_paused = true)]
    async fn no_secret_reaches_the_files_of_the_buffer() {
        let dir = tempfile::tempdir().unwrap();
        let now = SystemTime::now();
        let (wal, _) =
            sermon_buffer::Wal::open(dir.path(), sermon_buffer::WalConfig::default(), now).unwrap();
        let outbox = Outbox::new(wal);
        let (_config, receiver) = watch::channel(active(true));
        let mut s = Shipper::new(
            receiver,
            Arc::new(ShipStats::default()),
            into_outbox(outbox.clone()),
        );
        for message in [
            "login password=hunter2 for alice",
            "export for customer-654321 done",
            "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.c2ln",
        ] {
            s.offer(record("app.service", None, message), Instant::now());
        }
        assert!(s.flush());
        outbox.maintain(now).unwrap();
        let mut bytes = Vec::new();
        let mut stack = vec![dir.path().to_owned()];
        while let Some(path) = stack.pop() {
            for entry in std::fs::read_dir(&path).unwrap() {
                let path = entry.unwrap().path();
                if path.is_dir() {
                    stack.push(path);
                } else {
                    bytes.extend(std::fs::read(path).unwrap());
                }
            }
        }
        let text = String::from_utf8_lossy(&bytes);
        for secret in ["hunter2", "654321", "eyJzdWIiOiJ4In0"] {
            assert!(!text.contains(secret), "{secret} reached the buffer");
        }
        assert!(
            text.contains("password=***") && text.contains("export for *** done"),
            "the masked text is buffered"
        );
        assert_eq!(outbox.stats().pending_records, 1, "one log batch");
    }

    #[test]
    fn reports_the_counters() {
        let stats = Arc::new(ShipStats::default());
        stats.sent.store(7, Ordering::Relaxed);
        stats.dropped_rate.store(2, Ordering::Relaxed);
        stats.dropped_buffer.store(3, Ordering::Relaxed);
        stats.redacted.store(1, Ordering::Relaxed);
        let root = ProcRoot::new("/nonexistent");
        let context = Context {
            root: &root,
            now: Monotonic::from_origin(Duration::ZERO),
        };
        let mut out = Vec::new();
        LogMetrics(stats).collect(&context, &mut out).unwrap();
        let value = |name: &str, reason: Option<&str>| {
            out.iter()
                .find(|s| {
                    s.metric == name
                        && reason
                            .is_none_or(|r| s.labels.iter().any(|(k, v)| *k == "reason" && v == r))
                })
                .map(|s| s.value)
        };
        assert_eq!(
            value("sermon_agent_logs_sent_entries", None),
            Some(Value::Int(7))
        );
        assert_eq!(
            value("sermon_agent_logs_dropped_entries", Some("rate")),
            Some(Value::Int(2))
        );
        assert_eq!(
            value("sermon_agent_logs_dropped_entries", Some("buffer")),
            Some(Value::Int(3))
        );
        assert_eq!(
            value("sermon_agent_logs_redacted_entries", None),
            Some(Value::Int(1))
        );
    }
}
