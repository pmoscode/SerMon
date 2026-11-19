//! Disk I/O per block device: `/proc/diskstats` (KONZEPT §8.1).
//!
//! | Metric | Labels | Unit |
//! |---|---|---|
//! | `sermon_disk_{read,write}_bytes_per_second` | `device` | bytes/s |
//! | `sermon_disk_{reads,writes}_per_second` | `device` | operations/s (IOPS) |
//! | `sermon_disk_busy_ratio` | `device` | ppm of time with I/O in flight ("utilization") |
//! | `sermon_disk_await_seconds` | `device` | average time per operation incl. queueing |
//! | `sermon_disk_queue_length` | `device` | average requests in flight |
//!
//! Only whole devices (`/sys/block`) that have ever done I/O are reported; partitions would
//! count everything twice. `loop*`, `ram*`, `zram*`, `nbd*` and `fd*` are left out.

use std::collections::{BTreeMap, BTreeSet};
use std::time::Duration;

use sermon_core::{Monotonic, Sample};

use crate::rate::{delta, per_second, per_second_float, time_ppm, to_f64};
use crate::{CollectError, Collector, Context};

const SECTOR_BYTES: u64 = 512;
const IGNORED_PREFIXES: [&str; 5] = ["loop", "ram", "zram", "nbd", "fd"];

/// Counters of one device.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct DiskCounters {
    /// Completed reads.
    pub reads: u64,
    /// Sectors read (512 bytes each).
    pub sectors_read: u64,
    /// Milliseconds spent reading.
    pub read_ms: u64,
    /// Completed writes.
    pub writes: u64,
    /// Sectors written.
    pub sectors_written: u64,
    /// Milliseconds spent writing.
    pub write_ms: u64,
    /// Milliseconds with I/O in flight.
    pub io_ms: u64,
    /// Weighted milliseconds (time × requests in flight).
    pub weighted_io_ms: u64,
}

/// Parses `/proc/diskstats` into counters per device name.
#[must_use]
pub fn parse_diskstats(text: &str) -> BTreeMap<String, DiskCounters> {
    let mut devices = BTreeMap::new();
    for line in text.lines() {
        let fields: Vec<&str> = line.split_whitespace().collect();
        let [_, _, name, rest @ ..] = fields.as_slice() else {
            continue;
        };
        let Ok(values) = rest
            .iter()
            .take(11)
            .map(|value| value.parse::<u64>())
            .collect::<Result<Vec<_>, _>>()
        else {
            continue;
        };
        if values.len() < 11 {
            continue;
        }
        devices.insert(
            (*name).to_owned(),
            DiskCounters {
                reads: values[0],
                sectors_read: values[2],
                read_ms: values[3],
                writes: values[4],
                sectors_written: values[6],
                write_ms: values[7],
                io_ms: values[9],
                weighted_io_ms: values[10],
            },
        );
    }
    devices
}

/// Samples of one device from two snapshots.
#[must_use]
pub fn device_samples(
    device: &str,
    previous: &DiskCounters,
    current: &DiskCounters,
    elapsed: Duration,
) -> Vec<Sample> {
    let mut samples = Vec::new();
    let labelled = |sample: Sample| sample.label("device", device);
    let byte_rates = [
        (
            "sermon_disk_read_bytes_per_second",
            previous.sectors_read,
            current.sectors_read,
        ),
        (
            "sermon_disk_write_bytes_per_second",
            previous.sectors_written,
            current.sectors_written,
        ),
    ];
    for (metric, before, now) in byte_rates {
        if let Some(rate) = per_second(
            before.saturating_mul(SECTOR_BYTES),
            now.saturating_mul(SECTOR_BYTES),
            elapsed,
        ) {
            samples.push(labelled(Sample::int(metric, rate)));
        }
    }
    for (metric, before, now) in [
        (
            "sermon_disk_reads_per_second",
            previous.reads,
            current.reads,
        ),
        (
            "sermon_disk_writes_per_second",
            previous.writes,
            current.writes,
        ),
    ] {
        if let Some(rate) = per_second_float(before, now, elapsed) {
            samples.push(labelled(Sample::float(metric, rate)));
        }
    }
    if let Some(value) = delta(previous.io_ms, current.io_ms)
        .and_then(|ms| time_ppm(Duration::from_millis(ms), elapsed))
    {
        samples.push(labelled(Sample::int("sermon_disk_busy_ratio", value)));
    }
    let operations =
        delta(previous.reads, current.reads).zip(delta(previous.writes, current.writes));
    let time =
        delta(previous.read_ms, current.read_ms).zip(delta(previous.write_ms, current.write_ms));
    if let (Some((reads, writes)), Some((read_ms, write_ms))) = (operations, time) {
        let operations = reads.saturating_add(writes);
        #[allow(
            clippy::cast_precision_loss,
            reason = "averages don't need 64-bit precision"
        )]
        let await_seconds = if operations == 0 {
            0.0
        } else {
            to_f64(read_ms.saturating_add(write_ms)) / to_f64(operations) / 1000.0
        };
        samples.push(labelled(Sample::float(
            "sermon_disk_await_seconds",
            await_seconds,
        )));
    }
    if let Some(weighted) = delta(previous.weighted_io_ms, current.weighted_io_ms) {
        let elapsed_ms = elapsed.as_secs_f64() * 1000.0;
        if elapsed_ms > 0.0 {
            #[allow(
                clippy::cast_precision_loss,
                reason = "averages don't need 64-bit precision"
            )]
            samples.push(labelled(Sample::float(
                "sermon_disk_queue_length",
                to_f64(weighted) / elapsed_ms,
            )));
        }
    }
    samples
}

fn reported(name: &str, counters: &DiskCounters, whole_devices: Option<&BTreeSet<String>>) -> bool {
    !IGNORED_PREFIXES
        .iter()
        .any(|prefix| name.starts_with(prefix))
        && whole_devices.is_none_or(|devices| devices.contains(name))
        && counters.reads.saturating_add(counters.writes) > 0
}

/// Disk I/O collector.
#[derive(Debug, Default)]
pub struct DiskCollector {
    previous: Option<(Monotonic, BTreeMap<String, DiskCounters>)>,
}

impl Collector for DiskCollector {
    fn name(&self) -> &'static str {
        "disk"
    }

    fn collect(
        &mut self,
        context: &Context<'_>,
        out: &mut Vec<Sample>,
    ) -> Result<(), CollectError> {
        let mut devices = parse_diskstats(&context.root.read_proc("diskstats")?);
        // Without /sys (some containers) partitions can't be told apart; report everything.
        let whole: Option<BTreeSet<String>> = context
            .root
            .list_sys("block")
            .ok()
            .map(|names| names.into_iter().collect());
        devices.retain(|name, counters| reported(name, counters, whole.as_ref()));
        if let Some((at, previous)) = &self.previous {
            let elapsed = context.now.saturating_duration_since(*at);
            for (name, current) in &devices {
                if let Some(before) = previous.get(name) {
                    out.extend(device_samples(name, before, current, elapsed));
                }
            }
        }
        self.previous = Some((context.now, devices));
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use proptest::prelude::*;

    use super::*;
    use crate::testing::{at, fixture, float, int, root_with};

    #[test]
    fn parses_the_fixtures_and_keeps_only_used_whole_devices() {
        let root = fixture("debian-13");
        let devices = parse_diskstats(&root.read_proc("diskstats").unwrap());
        assert!(devices.contains_key("vda1") && devices.contains_key("loop0"));

        let mut collector = DiskCollector::default();
        collector
            .collect(
                &Context {
                    root: &root,
                    now: at(0),
                },
                &mut Vec::new(),
            )
            .unwrap();
        let mut samples = Vec::new();
        collector
            .collect(
                &Context {
                    root: &root,
                    now: at(1),
                },
                &mut samples,
            )
            .unwrap();

        let devices: BTreeSet<&str> = samples
            .iter()
            .filter_map(|sample| sample.labels.iter().find(|(name, _)| *name == "device"))
            .map(|(_, device)| device.as_str())
            .collect();
        assert_eq!(devices, BTreeSet::from(["vda", "vdb"]));
    }

    const BEFORE: &str = "   8       0 sda 100 0 2000 500 50 0 4000 1000 0 600 1500 0 0 0 0\n   8       1 sda1 100 0 2000 500 50 0 4000 1000 0 600 1500 0 0 0 0\n";
    const AFTER: &str = "   8       0 sda 200 0 4048 900 150 0 8096 1400 2 1100 2500 0 0 0 0\n   8       1 sda1 200 0 4048 900 150 0 8096 1400 2 1100 2500 0 0 0 0\n";

    #[test]
    fn computes_throughput_iops_utilization_latency_and_queue() {
        let (_dir, root) = root_with(&[("proc/diskstats", BEFORE), ("sys/block/sda/stat", "")]);
        let mut collector = DiskCollector::default();
        collector
            .collect(
                &Context {
                    root: &root,
                    now: at(0),
                },
                &mut Vec::new(),
            )
            .unwrap();
        std::fs::write(root.proc("diskstats").unwrap(), AFTER).unwrap();

        let mut samples = Vec::new();
        collector
            .collect(
                &Context {
                    root: &root,
                    now: at(2),
                },
                &mut samples,
            )
            .unwrap();

        let device = [("device", "sda")];
        assert_eq!(
            int(&samples, "sermon_disk_read_bytes_per_second", &device),
            Some(2048 * 512 / 2)
        );
        assert_eq!(
            int(&samples, "sermon_disk_write_bytes_per_second", &device),
            Some(4096 * 512 / 2)
        );
        assert_eq!(
            float(&samples, "sermon_disk_reads_per_second", &device),
            Some(50.0)
        );
        assert_eq!(
            float(&samples, "sermon_disk_writes_per_second", &device),
            Some(50.0)
        );
        assert_eq!(
            int(&samples, "sermon_disk_busy_ratio", &device),
            Some(250_000),
            "500 ms of 2 s"
        );
        assert_eq!(
            float(&samples, "sermon_disk_await_seconds", &device),
            Some(0.004),
            "800 ms / 200 ops"
        );
        assert_eq!(
            float(&samples, "sermon_disk_queue_length", &device),
            Some(0.5)
        );
        assert!(
            samples.iter().all(|sample| sample.labels[0].1 == "sda"),
            "partition reported"
        );
    }

    #[test]
    fn idle_device_has_zero_latency_not_nan() {
        let counters = DiskCounters {
            reads: 5,
            ..DiskCounters::default()
        };

        let samples = device_samples("sda", &counters, &counters, Duration::from_secs(1));

        assert_eq!(
            float(&samples, "sermon_disk_await_seconds", &[("device", "sda")]),
            Some(0.0)
        );
    }

    #[test]
    fn a_reset_device_gives_no_rates() {
        let before = parse_diskstats(AFTER)["sda"];
        let after = parse_diskstats(BEFORE)["sda"];

        let samples = device_samples("sda", &before, &after, Duration::from_secs(1));

        assert_eq!(samples, Vec::new());
    }

    proptest! {
        #[test]
        fn parser_never_panics(text in "\\PC*") {
            let _ = parse_diskstats(&text);
        }
    }
}
