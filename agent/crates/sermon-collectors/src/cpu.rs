//! CPU and load: `/proc/stat` and `/proc/loadavg` (KONZEPT §8.1).
//!
//! | Metric | Labels | Unit |
//! |---|---|---|
//! | `sermon_cpu_utilization_ratio` | `mode` (user, nice, system, idle, iowait, irq, softirq, steal) | ppm of all CPUs |
//! | `sermon_cpu_core_busy_ratio` | `cpu` | ppm of one core (everything but idle and iowait) |
//! | `sermon_cpu_context_switches_per_second`, `sermon_cpu_interrupts_per_second`, `sermon_cpu_forks_per_second` | – | 1/s |
//! | `sermon_cpu_logical_count` | – | count |
//! | `sermon_processes_running`, `sermon_processes_blocked` | – | count |
//! | `sermon_load_average`, `sermon_load_average_per_cpu` | `period` (1m, 5m, 15m) | load |
//!
//! Per-core values are only the busy share, not every mode: 8 modes × 128 cores would use half
//! the series budget of a node (§8: 2 000) for one chart.

use sermon_core::{Monotonic, Sample};

use crate::rate::{per_second, ppm};
use crate::{CollectError, Collector, Context};

/// CPU time per mode in clock ticks, as in a `cpu` line of `/proc/stat`.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct CpuTimes {
    /// Normal processes in user mode (includes guest time).
    pub user: u64,
    /// Niced processes in user mode.
    pub nice: u64,
    /// Kernel mode.
    pub system: u64,
    /// Idle.
    pub idle: u64,
    /// Idle while waiting for I/O.
    pub iowait: u64,
    /// Hardware interrupts.
    pub irq: u64,
    /// Software interrupts.
    pub softirq: u64,
    /// Taken by the hypervisor for other guests.
    pub steal: u64,
}

impl CpuTimes {
    fn modes(&self) -> [(&'static str, u64); 8] {
        [
            ("user", self.user),
            ("nice", self.nice),
            ("system", self.system),
            ("idle", self.idle),
            ("iowait", self.iowait),
            ("irq", self.irq),
            ("softirq", self.softirq),
            ("steal", self.steal),
        ]
    }

    fn total(&self) -> u64 {
        self.modes()
            .iter()
            .fold(0, |sum, (_, ticks)| sum.saturating_add(*ticks))
    }

    fn busy(&self) -> u64 {
        self.total()
            .saturating_sub(self.idle)
            .saturating_sub(self.iowait)
    }
}

/// The parts of `/proc/stat` this collector uses.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Stat {
    /// All CPUs together.
    pub total: CpuTimes,
    /// Per online CPU: number and times.
    pub cores: Vec<(u32, CpuTimes)>,
    /// Context switches since boot.
    pub context_switches: u64,
    /// Interrupts since boot.
    pub interrupts: u64,
    /// Processes created since boot.
    pub forks: u64,
    /// Runnable processes now.
    pub running: u64,
    /// Processes blocked on I/O now.
    pub blocked: u64,
    /// Boot time, seconds since the epoch.
    pub boot_time: Option<u64>,
}

/// Parses `/proc/stat`.
///
/// # Errors
///
/// A description if the aggregate `cpu` line is missing or malformed.
pub fn parse_stat(text: &str) -> Result<Stat, String> {
    let mut stat = Stat::default();
    let mut has_total = false;
    for line in text.lines() {
        let mut fields = line.split_whitespace();
        let Some(key) = fields.next() else { continue };
        let number = |value: Option<&str>| value.and_then(|value| value.parse::<u64>().ok());
        match key {
            "cpu" => {
                stat.total = parse_times(fields).ok_or("malformed cpu line")?;
                has_total = true;
            }
            _ if key.starts_with("cpu") => {
                let Ok(core) = key[3..].parse::<u32>() else {
                    continue;
                };
                if let Some(times) = parse_times(fields) {
                    stat.cores.push((core, times));
                }
            }
            "ctxt" => stat.context_switches = number(fields.next()).unwrap_or_default(),
            "intr" => stat.interrupts = number(fields.next()).unwrap_or_default(),
            "processes" => stat.forks = number(fields.next()).unwrap_or_default(),
            "procs_running" => stat.running = number(fields.next()).unwrap_or_default(),
            "procs_blocked" => stat.blocked = number(fields.next()).unwrap_or_default(),
            "btime" => stat.boot_time = number(fields.next()),
            _ => {}
        }
    }
    if has_total {
        Ok(stat)
    } else {
        Err("no cpu line".to_owned())
    }
}

fn parse_times<'a>(fields: impl Iterator<Item = &'a str>) -> Option<CpuTimes> {
    let values: Vec<u64> = fields
        .take(8)
        .map(str::parse)
        .collect::<Result<_, _>>()
        .ok()?;
    // Kernels before 2.6.11 have fewer columns; missing ones are zero.
    let at = |index: usize| values.get(index).copied().unwrap_or_default();
    (values.len() >= 4).then(|| CpuTimes {
        user: at(0),
        nice: at(1),
        system: at(2),
        idle: at(3),
        iowait: at(4),
        irq: at(5),
        softirq: at(6),
        steal: at(7),
    })
}

/// Load averages from `/proc/loadavg`.
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct LoadAverage {
    /// Over 1 minute.
    pub one: f64,
    /// Over 5 minutes.
    pub five: f64,
    /// Over 15 minutes.
    pub fifteen: f64,
}

/// Parses `/proc/loadavg`.
///
/// # Errors
///
/// A description if the three averages are missing.
pub fn parse_loadavg(text: &str) -> Result<LoadAverage, String> {
    let values: Vec<f64> = text
        .split_whitespace()
        .take(3)
        .map(str::parse)
        .collect::<Result<_, _>>()
        .map_err(|_| "malformed load average")?;
    match values.as_slice() {
        [one, five, fifteen]
            if values
                .iter()
                .all(|value| value.is_finite() && *value >= 0.0) =>
        {
            Ok(LoadAverage {
                one: *one,
                five: *five,
                fifteen: *fifteen,
            })
        }
        _ => Err("expected three load averages".to_owned()),
    }
}

/// CPU utilization from two snapshots.
#[must_use]
pub fn utilization(previous: &Stat, current: &Stat, elapsed: std::time::Duration) -> Vec<Sample> {
    let mut samples = Vec::new();
    if let Some(total) = current.total.total().checked_sub(previous.total.total()) {
        for ((mode, now), (_, before)) in current.total.modes().iter().zip(previous.total.modes()) {
            if let Some(value) = now.checked_sub(before).and_then(|ticks| ppm(ticks, total)) {
                samples
                    .push(Sample::int("sermon_cpu_utilization_ratio", value).label("mode", *mode));
            }
        }
    }
    for (core, now) in &current.cores {
        let Some((_, before)) = previous.cores.iter().find(|(number, _)| number == core) else {
            continue; // came online: no previous value
        };
        let busy = now.busy().checked_sub(before.busy());
        let total = now.total().checked_sub(before.total());
        if let Some(value) = busy.zip(total).and_then(|(busy, total)| ppm(busy, total)) {
            samples.push(
                Sample::int("sermon_cpu_core_busy_ratio", value).label("cpu", core.to_string()),
            );
        }
    }
    let rates = [
        (
            "sermon_cpu_context_switches_per_second",
            previous.context_switches,
            current.context_switches,
        ),
        (
            "sermon_cpu_interrupts_per_second",
            previous.interrupts,
            current.interrupts,
        ),
        ("sermon_cpu_forks_per_second", previous.forks, current.forks),
    ];
    for (metric, before, now) in rates {
        if let Some(rate) = per_second(before, now, elapsed) {
            samples.push(Sample::int(metric, rate));
        }
    }
    samples
}

/// CPU and load collector.
#[derive(Debug, Default)]
pub struct CpuCollector {
    previous: Option<(Monotonic, Stat)>,
}

impl Collector for CpuCollector {
    fn name(&self) -> &'static str {
        "cpu"
    }

    fn collect(
        &mut self,
        context: &Context<'_>,
        out: &mut Vec<Sample>,
    ) -> Result<(), CollectError> {
        let stat = parse_stat(&context.root.read_proc("stat")?)
            .map_err(|reason| CollectError::parse("/proc/stat", reason))?;
        if let Some((at, previous)) = &self.previous {
            out.extend(utilization(
                previous,
                &stat,
                context.now.saturating_duration_since(*at),
            ));
        }
        let cpus = u32::try_from(stat.cores.len()).unwrap_or(u32::MAX).max(1);
        out.push(Sample::int("sermon_cpu_logical_count", i64::from(cpus)));
        out.push(Sample::int(
            "sermon_processes_running",
            i64::try_from(stat.running).unwrap_or(i64::MAX),
        ));
        out.push(Sample::int(
            "sermon_processes_blocked",
            i64::try_from(stat.blocked).unwrap_or(i64::MAX),
        ));

        let load = parse_loadavg(&context.root.read_proc("loadavg")?)
            .map_err(|reason| CollectError::parse("/proc/loadavg", reason))?;
        for (period, value) in [("1m", load.one), ("5m", load.five), ("15m", load.fifteen)] {
            out.push(Sample::float("sermon_load_average", value).label("period", period));
            out.push(
                Sample::float("sermon_load_average_per_cpu", value / f64::from(cpus))
                    .label("period", period),
            );
        }
        self.previous = Some((context.now, stat));
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use std::time::Duration;

    use proptest::prelude::*;
    use sermon_core::PPM;

    use super::*;
    use crate::testing::{at, fixture, float, int, root_with};

    #[test]
    fn parses_stat_of_debian_and_ubuntu() {
        for distro in [
            "debian-13",
            "ubuntu-24.04",
            "rockylinux-9",
            "almalinux-10",
            "fedora-44",
        ] {
            let stat = parse_stat(&fixture(distro).read_proc("stat").unwrap()).unwrap();
            assert_eq!(stat.cores.len(), 16, "{distro}");
            assert!(stat.total.user > 0 && stat.total.idle > 0, "{distro}");
            assert!(
                stat.context_switches > 0 && stat.interrupts > 0 && stat.forks > 0,
                "{distro}"
            );
            assert!(stat.boot_time.is_some(), "{distro}");
            assert!(
                parse_loadavg(&fixture(distro).read_proc("loadavg").unwrap()).is_ok(),
                "{distro}"
            );
        }
    }

    #[test]
    fn old_kernels_without_steal_columns_parse() {
        let stat = parse_stat("cpu  1 2 3 4\ncpu0 1 2 3 4\n").unwrap();

        assert_eq!(stat.total.idle, 4);
        assert_eq!(stat.total.steal, 0);
    }

    #[test]
    fn rejects_stat_without_cpu_line() {
        assert!(parse_stat("ctxt 1\n").is_err());
        assert!(parse_stat("cpu  x y z w\n").is_err());
        assert!(parse_loadavg("0.1 0.2\n").is_err());
        assert!(parse_loadavg("nan 0.2 0.3\n").is_err());
    }

    const BEFORE: &str = "cpu  100 0 100 700 100 0 0 0 0 0\ncpu0 50 0 50 350 50 0 0 0\ncpu1 50 0 50 350 50 0 0 0\nctxt 1000\nintr 500 1 2\nprocesses 10\nprocs_running 2\nprocs_blocked 1\nbtime 1795000000\n";
    const AFTER: &str = "cpu  200 0 150 1300 150 0 0 0 0 0\ncpu0 150 0 100 600 50 0 0 0\ncpu1 50 0 50 700 100 0 0 0\nctxt 3000\nintr 1500 1 2\nprocesses 14\nprocs_running 3\nprocs_blocked 0\nbtime 1795000000\n";

    #[test]
    fn computes_mode_shares_core_busy_and_rates_from_two_runs() {
        let (_dir, root) = root_with(&[
            ("proc/stat", BEFORE),
            ("proc/loadavg", "1.00 0.50 0.25 2/100 42\n"),
        ]);
        let mut collector = CpuCollector::default();
        let mut first = Vec::new();
        collector
            .collect(
                &Context {
                    root: &root,
                    now: at(10),
                },
                &mut first,
            )
            .unwrap();
        assert_eq!(
            int(&first, "sermon_cpu_utilization_ratio", &[("mode", "user")]),
            None,
            "no rate yet"
        );
        assert_eq!(int(&first, "sermon_cpu_logical_count", &[]), Some(2));
        assert_eq!(
            float(&first, "sermon_load_average_per_cpu", &[("period", "1m")]),
            Some(0.5)
        );

        std::fs::write(root.proc("stat").unwrap(), AFTER).unwrap();
        let mut second = Vec::new();
        collector
            .collect(
                &Context {
                    root: &root,
                    now: at(12),
                },
                &mut second,
            )
            .unwrap();

        // Deltas: user 100, system 50, idle 600, iowait 50 of 800 ticks.
        let mode = |name| int(&second, "sermon_cpu_utilization_ratio", &[("mode", name)]);
        assert_eq!(mode("user"), Some(125_000));
        assert_eq!(mode("system"), Some(62_500));
        assert_eq!(mode("idle"), Some(750_000));
        assert_eq!(mode("iowait"), Some(62_500));
        assert_eq!(mode("steal"), Some(0));
        // cpu0: busy 150 of 400; cpu1: busy 0 of 400.
        assert_eq!(
            int(&second, "sermon_cpu_core_busy_ratio", &[("cpu", "0")]),
            Some(375_000)
        );
        assert_eq!(
            int(&second, "sermon_cpu_core_busy_ratio", &[("cpu", "1")]),
            Some(0)
        );
        assert_eq!(
            int(&second, "sermon_cpu_context_switches_per_second", &[]),
            Some(1000)
        );
        assert_eq!(
            int(&second, "sermon_cpu_interrupts_per_second", &[]),
            Some(500)
        );
        assert_eq!(int(&second, "sermon_cpu_forks_per_second", &[]), Some(2));
        assert_eq!(int(&second, "sermon_processes_running", &[]), Some(3));
    }

    #[test]
    fn a_counter_reset_gives_no_rates_instead_of_garbage() {
        let previous = parse_stat(AFTER).unwrap();
        let current = parse_stat(BEFORE).unwrap();

        let samples = utilization(&previous, &current, Duration::from_secs(1));

        assert_eq!(samples, Vec::new());
    }

    #[test]
    fn a_core_coming_online_is_skipped_once() {
        let previous = parse_stat("cpu  1 0 1 8\ncpu0 1 0 1 8\n").unwrap();
        let current = parse_stat("cpu  2 0 2 16\ncpu0 2 0 2 16\ncpu1 0 0 0 0\n").unwrap();

        let samples = utilization(&previous, &current, Duration::from_secs(1));

        assert!(int(&samples, "sermon_cpu_core_busy_ratio", &[("cpu", "0")]).is_some());
        assert_eq!(
            int(&samples, "sermon_cpu_core_busy_ratio", &[("cpu", "1")]),
            None
        );
    }

    fn times() -> impl Strategy<Value = [u64; 8]> {
        proptest::array::uniform8(0..1_000_000_u64)
    }

    proptest! {
        /// The mode shares add up to 100 % (± rounding of each of the 8 values).
        #[test]
        fn mode_shares_sum_to_one(before in times(), increase in times()) {
            let line = |values: [u64; 8]| format!("cpu  {}\n", values.map(|v| v.to_string()).join(" "));
            let after: Vec<u64> = before.iter().zip(increase).map(|(b, i)| b + i).collect();
            let after: [u64; 8] = after.try_into().unwrap();
            prop_assume!(increase.iter().sum::<u64>() > 0);

            let samples = utilization(
                &parse_stat(&line(before)).unwrap(),
                &parse_stat(&line(after)).unwrap(),
                Duration::from_secs(1),
            );

            let sum: i64 = samples
                .iter()
                .filter(|sample| sample.metric == "sermon_cpu_utilization_ratio")
                .map(|sample| match sample.value { sermon_core::Value::Int(v) => v, sermon_core::Value::Float(_) => 0 })
                .sum();
            prop_assert!((sum - PPM).abs() <= 8, "sum {}", sum);
        }

        #[test]
        fn parsers_never_panic(text in "\\PC*") {
            let _ = parse_stat(&text);
            let _ = parse_loadavg(&text);
        }
    }
}
