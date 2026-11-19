//! Memory and swap: `/proc/meminfo` and `/proc/vmstat` (KONZEPT §8.1).
//!
//! | Metric | Unit |
//! |---|---|
//! | `sermon_memory_{total,free,available,used,buffers,cached,shared,slab}_bytes` | bytes |
//! | `sermon_memory_utilization_ratio` | ppm (used / total) |
//! | `sermon_swap_{total,used}_bytes`, `sermon_swap_utilization_ratio` | bytes, ppm |
//! | `sermon_swap_{in,out}_pages_per_second` | pages/s |
//! | `sermon_memory_major_faults_per_second`, `sermon_memory_oom_kills_per_second` | 1/s |
//!
//! "Used" is total minus available (what applications can't get without swapping), like `free`.

use std::collections::HashMap;

use sermon_core::{Monotonic, Sample};

use crate::rate::{per_second_float, ppm};
use crate::{CollectError, Collector, Context, optional};

/// Memory figures in bytes.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct MemInfo {
    /// Physical memory.
    pub total: u64,
    /// Unused memory.
    pub free: u64,
    /// Available for new applications without swapping.
    pub available: u64,
    /// Block device buffers.
    pub buffers: u64,
    /// Page cache.
    pub cached: u64,
    /// Shared memory (tmpfs, shm).
    pub shared: u64,
    /// Kernel slab.
    pub slab: u64,
    /// Swap space.
    pub swap_total: u64,
    /// Unused swap space.
    pub swap_free: u64,
}

/// Parses `/proc/meminfo` (values in kB).
///
/// # Errors
///
/// A description if `MemTotal` is missing.
pub fn parse_meminfo(text: &str) -> Result<MemInfo, String> {
    let values: HashMap<&str, u64> = text
        .lines()
        .filter_map(|line| {
            let (key, rest) = line.split_once(':')?;
            let kib: u64 = rest.split_whitespace().next()?.parse().ok()?;
            Some((key, kib.saturating_mul(1024)))
        })
        .collect();
    let get = |key| values.get(key).copied().unwrap_or_default();
    let total = values.get("MemTotal").copied().ok_or("no MemTotal")?;
    let free = get("MemFree");
    let buffers = get("Buffers");
    let cached = get("Cached");
    Ok(MemInfo {
        total,
        free,
        // MemAvailable exists since Linux 3.14; before that, estimate like `free` did.
        available: values
            .get("MemAvailable")
            .copied()
            .unwrap_or_else(|| free.saturating_add(buffers).saturating_add(cached))
            .min(total),
        buffers,
        cached,
        shared: get("Shmem"),
        slab: get("Slab"),
        swap_total: get("SwapTotal"),
        swap_free: get("SwapFree"),
    })
}

/// Counters from `/proc/vmstat`.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct VmStat {
    /// Pages swapped in.
    pub swap_in: u64,
    /// Pages swapped out.
    pub swap_out: u64,
    /// Major page faults (needed disk I/O).
    pub major_faults: u64,
    /// Processes killed by the OOM killer (Linux 4.13+).
    pub oom_kills: Option<u64>,
}

/// Parses `/proc/vmstat`.
#[must_use]
pub fn parse_vmstat(text: &str) -> VmStat {
    let mut stat = VmStat::default();
    for line in text.lines() {
        let mut fields = line.split_whitespace();
        let (Some(key), Some(Ok(value))) = (fields.next(), fields.next().map(str::parse::<u64>))
        else {
            continue;
        };
        match key {
            "pswpin" => stat.swap_in = value,
            "pswpout" => stat.swap_out = value,
            "pgmajfault" => stat.major_faults = value,
            "oom_kill" => stat.oom_kills = Some(value),
            _ => {}
        }
    }
    stat
}

fn bytes(metric: &'static str, value: u64) -> Sample {
    Sample::int(metric, i64::try_from(value).unwrap_or(i64::MAX))
}

/// Samples of the current memory state.
#[must_use]
pub fn memory_samples(info: &MemInfo) -> Vec<Sample> {
    let used = info.total.saturating_sub(info.available);
    let swap_used = info.swap_total.saturating_sub(info.swap_free);
    let mut samples = vec![
        bytes("sermon_memory_total_bytes", info.total),
        bytes("sermon_memory_free_bytes", info.free),
        bytes("sermon_memory_available_bytes", info.available),
        bytes("sermon_memory_used_bytes", used),
        bytes("sermon_memory_buffers_bytes", info.buffers),
        bytes("sermon_memory_cached_bytes", info.cached),
        bytes("sermon_memory_shared_bytes", info.shared),
        bytes("sermon_memory_slab_bytes", info.slab),
        bytes("sermon_swap_total_bytes", info.swap_total),
        bytes("sermon_swap_used_bytes", swap_used),
    ];
    if let Some(value) = ppm(used, info.total) {
        samples.push(Sample::int("sermon_memory_utilization_ratio", value));
    }
    if let Some(value) = ppm(swap_used, info.swap_total) {
        samples.push(Sample::int("sermon_swap_utilization_ratio", value));
    }
    samples
}

/// Memory collector.
#[derive(Debug, Default)]
pub struct MemoryCollector {
    previous: Option<(Monotonic, VmStat)>,
}

impl Collector for MemoryCollector {
    fn name(&self) -> &'static str {
        "memory"
    }

    fn collect(
        &mut self,
        context: &Context<'_>,
        out: &mut Vec<Sample>,
    ) -> Result<(), CollectError> {
        let info = parse_meminfo(&context.root.read_proc("meminfo")?)
            .map_err(|reason| CollectError::parse("/proc/meminfo", reason))?;
        out.extend(memory_samples(&info));

        let Some(text) = optional(context.root.read_proc("vmstat"))? else {
            return Ok(());
        };
        let current = parse_vmstat(&text);
        if let Some((at, previous)) = &self.previous {
            let elapsed = context.now.saturating_duration_since(*at);
            let rates = [
                (
                    "sermon_swap_in_pages_per_second",
                    previous.swap_in,
                    current.swap_in,
                ),
                (
                    "sermon_swap_out_pages_per_second",
                    previous.swap_out,
                    current.swap_out,
                ),
                (
                    "sermon_memory_major_faults_per_second",
                    previous.major_faults,
                    current.major_faults,
                ),
            ];
            for (metric, before, now) in rates {
                if let Some(rate) = per_second_float(before, now, elapsed) {
                    out.push(Sample::float(metric, rate));
                }
            }
            if let (Some(before), Some(now)) = (previous.oom_kills, current.oom_kills)
                && let Some(rate) = per_second_float(before, now, elapsed)
            {
                out.push(Sample::float("sermon_memory_oom_kills_per_second", rate));
            }
        }
        self.previous = Some((context.now, current));
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use proptest::prelude::*;

    use super::*;
    use crate::testing::{at, fixture, float, int, root_with};

    #[test]
    fn parses_meminfo_and_vmstat_of_debian_and_ubuntu() {
        for distro in ["debian-13", "ubuntu-24.04"] {
            let info = parse_meminfo(&fixture(distro).read_proc("meminfo").unwrap()).unwrap();
            assert_eq!(info.total, 8_123_740 * 1024, "{distro}");
            assert!(
                info.available > 0 && info.available <= info.total,
                "{distro}"
            );
            let vmstat = parse_vmstat(&fixture(distro).read_proc("vmstat").unwrap());
            assert!(vmstat.oom_kills.is_some(), "{distro}");
        }
    }

    #[test]
    fn used_is_total_minus_available() {
        let info = parse_meminfo(
            "MemTotal: 1000 kB\nMemFree: 100 kB\nMemAvailable: 600 kB\nBuffers: 50 kB\nCached: 300 kB\nSwapTotal: 200 kB\nSwapFree: 150 kB\n",
        )
        .unwrap();

        let samples = memory_samples(&info);

        assert_eq!(
            int(&samples, "sermon_memory_used_bytes", &[]),
            Some(400 * 1024)
        );
        assert_eq!(
            int(&samples, "sermon_memory_utilization_ratio", &[]),
            Some(400_000)
        );
        assert_eq!(
            int(&samples, "sermon_swap_used_bytes", &[]),
            Some(50 * 1024)
        );
        assert_eq!(
            int(&samples, "sermon_swap_utilization_ratio", &[]),
            Some(250_000)
        );
    }

    #[test]
    fn estimates_available_on_old_kernels_and_skips_ratios_without_swap() {
        let info =
            parse_meminfo("MemTotal: 1000 kB\nMemFree: 100 kB\nBuffers: 50 kB\nCached: 300 kB\n")
                .unwrap();

        assert_eq!(info.available, 450 * 1024);
        assert_eq!(
            int(&memory_samples(&info), "sermon_swap_utilization_ratio", &[]),
            None
        );
        assert!(parse_meminfo("MemFree: 1 kB\n").is_err());
    }

    #[test]
    fn rates_from_vmstat() {
        let (_dir, root) = root_with(&[
            ("proc/meminfo", "MemTotal: 1000 kB\nMemAvailable: 500 kB\n"),
            (
                "proc/vmstat",
                "pswpin 10\npswpout 20\npgmajfault 100\noom_kill 0\n",
            ),
        ]);
        let mut collector = MemoryCollector::default();
        collector
            .collect(
                &Context {
                    root: &root,
                    now: at(0),
                },
                &mut Vec::new(),
            )
            .unwrap();
        std::fs::write(
            root.proc("vmstat").unwrap(),
            "pswpin 30\npswpout 20\npgmajfault 150\noom_kill 1\n",
        )
        .unwrap();

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

        assert_eq!(
            float(&samples, "sermon_swap_in_pages_per_second", &[]),
            Some(10.0)
        );
        assert_eq!(
            float(&samples, "sermon_swap_out_pages_per_second", &[]),
            Some(0.0)
        );
        assert_eq!(
            float(&samples, "sermon_memory_major_faults_per_second", &[]),
            Some(25.0)
        );
        assert_eq!(
            float(&samples, "sermon_memory_oom_kills_per_second", &[]),
            Some(0.5)
        );
    }

    proptest! {
        #[test]
        fn parsers_never_panic(text in "\\PC*") {
            if let Ok(info) = parse_meminfo(&text) {
                prop_assert!(info.available <= info.total);
                let _ = memory_samples(&info);
            }
            let _ = parse_vmstat(&text);
        }
    }
}
