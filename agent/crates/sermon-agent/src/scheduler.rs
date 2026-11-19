//! Collection schedule (M1-21): a 1 s tick runs the due collectors, limits the number of series per
//! node and hands the samples to the outbox.
//!
//! - The tick follows a fixed schedule (no drift accumulates); a tick that is late because the
//!   machine was busy is skipped instead of being caught up with a burst.
//! - Collectors read files, so they run on a blocking thread, never on the async runtime.
//! - Series limit (KONZEPT §8: 2 000 per node): samples of new series beyond the limit are dropped
//!   and counted; series not seen for an hour are forgotten.

use std::collections::{HashMap, HashSet};
use std::sync::Arc;
use std::time::Duration;

use sermon_collectors::{Collector, Context};
use sermon_core::{Clock, Monotonic, ProcRoot, Sample};
use sermon_transport::outbox::Outbox;
use tokio::time::MissedTickBehavior;
use tokio_util::sync::CancellationToken;

/// Interval of the base tick.
pub const TICK: Duration = Duration::from_secs(1);
/// Series per node at most (KONZEPT §8).
pub const SERIES_LIMIT: usize = 2000;
/// Series not seen for this many ticks are forgotten (interfaces or disks that went away).
const FORGET_AFTER_TICKS: u64 = 3600;

/// Keeps the number of distinct series bounded.
#[derive(Debug)]
pub struct SeriesLimit {
    limit: usize,
    last_seen: HashMap<String, u64>,
    dropped: u64,
    warned: bool,
}

impl SeriesLimit {
    /// A limit of `limit` series.
    #[must_use]
    pub fn new(limit: usize) -> Self {
        Self {
            limit,
            last_seen: HashMap::new(),
            dropped: 0,
            warned: false,
        }
    }

    /// The samples whose series fit into the limit; known series always pass.
    pub fn admit(&mut self, samples: Vec<Sample>, tick: u64) -> Vec<Sample> {
        if tick.is_multiple_of(FORGET_AFTER_TICKS) {
            self.last_seen
                .retain(|_, seen| tick.saturating_sub(*seen) < FORGET_AFTER_TICKS);
        }
        let mut admitted = Vec::with_capacity(samples.len());
        for sample in samples {
            let key = sample.series_key();
            if let Some(seen) = self.last_seen.get_mut(&key) {
                *seen = tick;
            } else if self.last_seen.len() < self.limit {
                self.last_seen.insert(key, tick);
            } else {
                self.dropped += 1;
                if !self.warned {
                    tracing::warn!(
                        limit = self.limit,
                        metric = sample.metric,
                        "series limit reached, dropping new series"
                    );
                    self.warned = true;
                }
                continue;
            }
            admitted.push(sample);
        }
        admitted
    }

    /// Samples dropped so far.
    #[must_use]
    pub fn dropped(&self) -> u64 {
        self.dropped
    }
}

/// Runs the collectors tick by tick.
pub struct Scheduler {
    collectors: Vec<Box<dyn Collector>>,
    root: ProcRoot,
    limit: SeriesLimit,
    tick: u64,
    failing: HashSet<&'static str>,
}

impl std::fmt::Debug for Scheduler {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Scheduler")
            .field(
                "collectors",
                &self.collectors.iter().map(|c| c.name()).collect::<Vec<_>>(),
            )
            .field("tick", &self.tick)
            .finish_non_exhaustive()
    }
}

impl Scheduler {
    /// A scheduler for `collectors` reading below `root`.
    #[must_use]
    pub fn new(collectors: Vec<Box<dyn Collector>>, root: ProcRoot, series_limit: usize) -> Self {
        Self {
            collectors,
            root,
            limit: SeriesLimit::new(series_limit),
            tick: 0,
            failing: HashSet::new(),
        }
    }

    /// One tick: runs every collector that is due, then applies the series limit.
    pub fn tick(&mut self, now: Monotonic) -> Vec<Sample> {
        let context = Context {
            root: &self.root,
            now,
        };
        let mut samples = Vec::new();
        for collector in &mut self.collectors {
            let interval = u64::from(collector.interval_ticks().max(1));
            if !self.tick.is_multiple_of(interval) {
                continue;
            }
            let name = collector.name();
            match collector.collect(&context, &mut samples) {
                Ok(()) => {
                    if self.failing.remove(name) {
                        tracing::info!(collector = name, "collector works again");
                    }
                }
                // Logged once per failure streak, not every second.
                Err(error) => {
                    if self.failing.insert(name) {
                        tracing::warn!(collector = name, "collector failed: {error}");
                    }
                }
            }
        }
        let samples = self.limit.admit(samples, self.tick);
        self.tick += 1;
        samples
    }

    /// Samples dropped by the series limit so far.
    #[must_use]
    pub fn dropped_series_samples(&self) -> u64 {
        self.limit.dropped()
    }
}

/// Buffer and limit figures of the agent itself, added to every tick.
fn agent_samples(outbox: &Outbox, dropped_series: u64) -> Vec<Sample> {
    let stats = outbox.stats();
    let count = |value: u64| i64::try_from(value).unwrap_or(i64::MAX);
    vec![
        Sample::int(
            "sermon_agent_buffer_pending_batches",
            count(stats.pending_records),
        ),
        Sample::int("sermon_agent_buffer_bytes", count(stats.bytes)),
        Sample::int(
            "sermon_agent_buffer_dropped_batches",
            count(stats.dropped_records),
        ),
        Sample::int("sermon_agent_series_dropped_samples", count(dropped_series)),
    ]
}

/// Ticks until `shutdown`: collect on a blocking thread, then buffer the samples.
pub async fn run(
    mut scheduler: Scheduler,
    outbox: Outbox,
    clock: Arc<dyn Clock>,
    shutdown: CancellationToken,
) {
    let mut interval = tokio::time::interval(TICK);
    interval.set_missed_tick_behavior(MissedTickBehavior::Skip);
    loop {
        tokio::select! {
            () = shutdown.cancelled() => return,
            _ = interval.tick() => {}
        }
        let collected_at = clock.now();
        let now = clock.monotonic();
        let result = tokio::task::spawn_blocking(move || {
            let samples = scheduler.tick(now);
            (scheduler, samples)
        })
        .await;
        let mut samples;
        (scheduler, samples) = match result {
            Ok(done) => done,
            Err(error) => {
                // A panicking collector must not end the agent; the scheduler is lost with it.
                tracing::error!("collection failed: {error}");
                return;
            }
        };
        samples.extend(agent_samples(&outbox, scheduler.dropped_series_samples()));
        if let Err(error) = outbox.push_samples(&samples, collected_at) {
            tracing::warn!("cannot buffer the samples: {error}");
        }
    }
}

#[cfg(test)]
mod tests {
    use std::sync::Mutex;

    use sermon_buffer::{Seq, Wal, WalConfig};
    use sermon_collectors::CollectError;
    use sermon_core::SystemClock;

    use super::*;

    /// Emits `series` samples per run and records the ticks it ran at.
    #[derive(Debug)]
    struct Fake {
        name: &'static str,
        interval: u32,
        series: usize,
        runs: Arc<Mutex<Vec<Monotonic>>>,
        fail: bool,
    }

    impl Collector for Fake {
        fn name(&self) -> &'static str {
            self.name
        }

        fn interval_ticks(&self) -> u32 {
            self.interval
        }

        fn collect(
            &mut self,
            context: &Context<'_>,
            out: &mut Vec<Sample>,
        ) -> Result<(), CollectError> {
            self.runs.lock().unwrap().push(context.now);
            if self.fail {
                return Err(CollectError::Parse {
                    file: "/proc/x",
                    reason: "broken".into(),
                });
            }
            out.extend(
                (0..self.series).map(|i| Sample::int("sermon_x", 1).label("n", i.to_string())),
            );
            Ok(())
        }
    }

    fn fake(
        name: &'static str,
        interval: u32,
        series: usize,
    ) -> (Box<dyn Collector>, Arc<Mutex<Vec<Monotonic>>>) {
        let runs = Arc::new(Mutex::new(Vec::new()));
        (
            Box::new(Fake {
                name,
                interval,
                series,
                runs: runs.clone(),
                fail: false,
            }),
            runs,
        )
    }

    fn at(seconds: u64) -> Monotonic {
        Monotonic::from_origin(Duration::from_secs(seconds))
    }

    #[test]
    fn runs_each_collector_at_its_interval() {
        let (fast, fast_runs) = fake("fast", 1, 1);
        let (slow, slow_runs) = fake("slow", 10, 1);
        let mut scheduler = Scheduler::new(vec![fast, slow], ProcRoot::host(), SERIES_LIMIT);

        for second in 0..25 {
            scheduler.tick(at(second));
        }

        assert_eq!(fast_runs.lock().unwrap().len(), 25);
        assert_eq!(*slow_runs.lock().unwrap(), vec![at(0), at(10), at(20)]);
    }

    #[test]
    fn a_failing_collector_does_not_stop_the_others() {
        let runs = Arc::new(Mutex::new(Vec::new()));
        let broken = Box::new(Fake {
            name: "broken",
            interval: 1,
            series: 0,
            runs: runs.clone(),
            fail: true,
        });
        let (good, _) = fake("good", 1, 3);
        let mut scheduler = Scheduler::new(vec![broken, good], ProcRoot::host(), SERIES_LIMIT);

        let samples = scheduler.tick(at(0));
        scheduler.tick(at(1));

        assert_eq!(samples.len(), 3);
        assert_eq!(runs.lock().unwrap().len(), 2, "retried every tick");
    }

    #[test]
    fn the_series_limit_drops_only_new_series() {
        let mut limit = SeriesLimit::new(3);
        let series = |names: &[&str]| -> Vec<Sample> {
            names
                .iter()
                .map(|name| Sample::int("sermon_x", 1).label("n", *name))
                .collect()
        };

        assert_eq!(limit.admit(series(&["a", "b"]), 0).len(), 2);
        let admitted = limit.admit(series(&["a", "c", "d", "e"]), 1);

        let names: Vec<&str> = admitted
            .iter()
            .map(|sample| sample.labels[0].1.as_str())
            .collect();
        assert_eq!(names, vec!["a", "c"]);
        assert_eq!(limit.dropped(), 2);
        // Known series keep passing.
        assert_eq!(limit.admit(series(&["b", "c", "a"]), 2).len(), 3);
    }

    #[test]
    fn series_not_seen_for_an_hour_free_their_slot() {
        let mut limit = SeriesLimit::new(1);
        let one = |name: &str| vec![Sample::int("sermon_x", 1).label("n", name)];
        limit.admit(one("gone"), 0);
        assert_eq!(limit.admit(one("new"), 10).len(), 0);

        assert_eq!(limit.admit(one("new"), FORGET_AFTER_TICKS).len(), 1);
    }

    #[test]
    fn real_collectors_on_the_fixture_stay_within_the_limit() {
        let root = ProcRoot::new(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/../../fixtures/debian-13"
        ));
        let mut scheduler =
            Scheduler::new(sermon_collectors::default_collectors(), root, SERIES_LIMIT);

        scheduler.tick(at(0));
        let samples = scheduler.tick(at(1));

        assert!(
            samples.len() > 40 && samples.len() < SERIES_LIMIT,
            "{}",
            samples.len()
        );
        assert_eq!(scheduler.dropped_series_samples(), 0);
    }

    /// The rules of `proto/sermon/agent/v1/agent.proto` (Sample): the server rejects the whole
    /// session for a single invalid sample.
    fn assert_valid_for_the_protocol(sample: &Sample) {
        let name = sample
            .metric
            .strip_prefix("sermon_")
            .unwrap_or_else(|| panic!("{}", sample.metric));
        assert!(
            (1..=120).contains(&name.len())
                && name
                    .bytes()
                    .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'_'),
            "metric {}",
            sample.metric
        );
        assert!(sample.labels.len() <= 8, "{sample:?}");
        for (key, value) in &sample.labels {
            let first_ok = key.bytes().next().is_some_and(|b| b.is_ascii_lowercase());
            let rest_ok = key
                .bytes()
                .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'_');
            assert!(first_ok && rest_ok && key.len() <= 32, "label {key}");
            assert!(value.chars().count() <= 256, "label value {value}");
        }
        if let sermon_core::Value::Float(value) = sample.value {
            assert!(value.is_finite(), "{sample:?}");
        }
    }

    #[test]
    fn every_sample_satisfies_the_protocol_rules() {
        let mut roots = vec![
            ProcRoot::new(concat!(
                env!("CARGO_MANIFEST_DIR"),
                "/../../fixtures/debian-13"
            )),
            ProcRoot::new(concat!(
                env!("CARGO_MANIFEST_DIR"),
                "/../../fixtures/ubuntu-24.04"
            )),
        ];
        if cfg!(target_os = "linux") {
            roots.push(ProcRoot::host());
        }
        for root in roots {
            let mut scheduler =
                Scheduler::new(sermon_collectors::default_collectors(), root, SERIES_LIMIT);
            scheduler.tick(at(0));
            let samples = scheduler.tick(at(1));
            assert_ne!(samples.len(), 0);
            samples.iter().for_each(assert_valid_for_the_protocol);
        }
    }

    #[tokio::test(start_paused = true)]
    async fn ticks_every_second_and_buffers_one_batch_per_tick() {
        let dir = tempfile::tempdir().unwrap();
        let clock = Arc::new(SystemClock::new());
        let (wal, _) = Wal::open(
            dir.path(),
            WalConfig::default(),
            std::time::SystemTime::now(),
        )
        .unwrap();
        let outbox = Outbox::new(wal);
        let (collector, runs) = fake("fast", 1, 2);
        let scheduler = Scheduler::new(vec![collector], ProcRoot::host(), SERIES_LIMIT);
        let shutdown = CancellationToken::new();
        let task = tokio::spawn(run(scheduler, outbox.clone(), clock, shutdown.clone()));

        // Paused time: sleeping lets the interval fire deterministically.
        tokio::time::sleep(Duration::from_millis(4500)).await;
        shutdown.cancel();
        task.await.unwrap();

        assert_eq!(runs.lock().unwrap().len(), 5, "ticks at 0, 1, 2, 3 and 4 s");
        let stats = outbox.stats();
        assert_eq!(stats.pending_records, 5);
        assert!(stats.acked < Seq::new(stats.next_seq.get()));
    }
}
