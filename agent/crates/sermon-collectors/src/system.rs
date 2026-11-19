//! System: uptime, boot time, processes, threads, zombies, file descriptors, entropy and
//! temperatures (KONZEPT §8.1). Runs every 10 s: counting zombies reads every `/proc/<pid>/stat`.
//!
//! | Metric | Labels | Unit |
//! |---|---|---|
//! | `sermon_system_uptime_seconds` | – | seconds |
//! | `sermon_system_boot_time_seconds` | – | seconds since the epoch (changes on reboot) |
//! | `sermon_system_processes`, `sermon_system_threads`, `sermon_system_zombies` | – | count |
//! | `sermon_system_file_descriptors_used`, `sermon_system_file_descriptors_limit` | – | count |
//! | `sermon_system_entropy_bits` | – | bits |
//! | `sermon_hardware_temperature_celsius` | `sensor` (chip/sensor, at most 32) | °C |
//!
//! Time synchronisation (`adjtimex`) follows with the state collectors (E2.8).

use sermon_core::Sample;

use crate::cpu::parse_stat;
use crate::{CollectError, Collector, Context, optional};

const MAX_SENSORS: usize = 32;

/// Seconds since boot from `/proc/uptime`.
#[must_use]
pub fn parse_uptime(text: &str) -> Option<i64> {
    let seconds: f64 = text.split_whitespace().next()?.parse().ok()?;
    (seconds.is_finite() && seconds >= 0.0).then_some(crate::rate::round_to_i64(seconds.floor()))
}

/// Total threads (scheduling entities) from the fourth field of `/proc/loadavg` (`running/total`).
#[must_use]
pub fn parse_threads(loadavg: &str) -> Option<i64> {
    loadavg
        .split_whitespace()
        .nth(3)?
        .split_once('/')?
        .1
        .parse()
        .ok()
}

/// Used and maximum file handles from `/proc/sys/fs/file-nr` (`allocated unused max`).
#[must_use]
pub fn parse_file_nr(text: &str) -> Option<(i64, i64)> {
    let values: Vec<i64> = text
        .split_whitespace()
        .filter_map(|value| value.parse().ok())
        .collect();
    match values.as_slice() {
        [allocated, unused, max] => Some((allocated.saturating_sub(*unused), *max)),
        _ => None,
    }
}

/// Whether a `/proc/<pid>/stat` line belongs to a zombie. The state follows the command name in
/// parentheses, which may itself contain spaces and parentheses.
#[must_use]
pub fn is_zombie(stat: &str) -> bool {
    stat.rsplit_once(')')
        .is_some_and(|(_, rest)| rest.trim_start().starts_with('Z'))
}

/// System collector.
#[derive(Debug, Default)]
pub struct SystemCollector;

impl SystemCollector {
    fn processes(context: &Context<'_>, out: &mut Vec<Sample>) -> Result<(), CollectError> {
        let pids: Vec<String> = context
            .root
            .list_proc("")?
            .into_iter()
            .filter(|name| name.bytes().all(|byte| byte.is_ascii_digit()))
            .collect();
        // A process may end between listing and reading: unreadable entries are skipped.
        let zombies = pids
            .iter()
            .filter(|pid| {
                context
                    .root
                    .read_proc(&format!("{pid}/stat"))
                    .is_ok_and(|stat| is_zombie(&stat))
            })
            .count();
        out.push(Sample::int(
            "sermon_system_processes",
            i64::try_from(pids.len()).unwrap_or(i64::MAX),
        ));
        out.push(Sample::int(
            "sermon_system_zombies",
            i64::try_from(zombies).unwrap_or(i64::MAX),
        ));
        Ok(())
    }

    fn temperatures(context: &Context<'_>, out: &mut Vec<Sample>) {
        let Ok(chips) = context.root.list_sys("class/hwmon") else {
            return;
        };
        let mut sensors = 0;
        for chip in chips {
            let read = |file: &str| {
                context
                    .root
                    .read_sys(&format!("class/hwmon/{chip}/{file}"))
                    .ok()
            };
            let name = read("name").map_or_else(|| chip.clone(), |name| name.trim().to_owned());
            let Ok(files) = context.root.list_sys(&format!("class/hwmon/{chip}")) else {
                continue;
            };
            for input in files
                .iter()
                .filter(|file| file.starts_with("temp") && file.ends_with("_input"))
            {
                if sensors >= MAX_SENSORS {
                    return;
                }
                let Some(millidegrees) =
                    read(input).and_then(|value| value.trim().parse::<i32>().ok())
                else {
                    continue;
                };
                let base = input.trim_end_matches("_input");
                let label = read(&format!("{base}_label"))
                    .map_or_else(|| base.to_owned(), |label| label.trim().to_owned());
                out.push(
                    Sample::float(
                        "sermon_hardware_temperature_celsius",
                        f64::from(millidegrees) / 1000.0,
                    )
                    .label("sensor", format!("{name}/{label}")),
                );
                sensors += 1;
            }
        }
    }
}

impl Collector for SystemCollector {
    fn name(&self) -> &'static str {
        "system"
    }

    fn interval_ticks(&self) -> u32 {
        10
    }

    fn collect(
        &mut self,
        context: &Context<'_>,
        out: &mut Vec<Sample>,
    ) -> Result<(), CollectError> {
        let uptime = parse_uptime(&context.root.read_proc("uptime")?)
            .ok_or_else(|| CollectError::parse("/proc/uptime", "malformed"))?;
        out.push(Sample::int("sermon_system_uptime_seconds", uptime));
        if let Ok(Some(boot)) =
            parse_stat(&context.root.read_proc("stat")?).map(|stat| stat.boot_time)
        {
            out.push(Sample::int(
                "sermon_system_boot_time_seconds",
                i64::try_from(boot).unwrap_or(i64::MAX),
            ));
        }
        if let Some(threads) = parse_threads(&context.root.read_proc("loadavg")?) {
            out.push(Sample::int("sermon_system_threads", threads));
        }
        Self::processes(context, out)?;
        if let Some((used, limit)) = optional(context.root.read_proc("sys/fs/file-nr"))?
            .as_deref()
            .and_then(parse_file_nr)
        {
            out.push(Sample::int("sermon_system_file_descriptors_used", used));
            out.push(Sample::int("sermon_system_file_descriptors_limit", limit));
        }
        if let Some(bits) = optional(context.root.read_proc("sys/kernel/random/entropy_avail"))?
            .and_then(|text| text.trim().parse::<i64>().ok())
        {
            out.push(Sample::int("sermon_system_entropy_bits", bits));
        }
        Self::temperatures(context, out);
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use proptest::prelude::*;

    use super::*;
    use crate::testing::{at, fixture, float, int, root_with};

    #[test]
    fn reads_the_fixtures() {
        let mut samples = Vec::new();
        SystemCollector
            .collect(
                &Context {
                    root: &fixture("debian-13"),
                    now: at(0),
                },
                &mut samples,
            )
            .unwrap();

        assert!(
            int(&samples, "sermon_system_uptime_seconds", &[]).is_some_and(|uptime| uptime > 0)
        );
        assert!(int(&samples, "sermon_system_boot_time_seconds", &[]).is_some());
        assert!(int(&samples, "sermon_system_threads", &[]).is_some());
        assert!(int(&samples, "sermon_system_file_descriptors_limit", &[]).is_some());
        assert_eq!(int(&samples, "sermon_system_entropy_bits", &[]), Some(256));
        assert_eq!(
            int(&samples, "sermon_system_processes", &[]),
            Some(0),
            "fixture has no /proc/<pid>"
        );
    }

    #[test]
    fn counts_processes_and_zombies_and_reads_temperatures() {
        let (_dir, root) = root_with(&[
            ("proc/uptime", "12345.67 9999.00\n"),
            ("proc/stat", "cpu  1 2 3 4\nbtime 1795000000\n"),
            ("proc/loadavg", "0.00 0.00 0.00 1/321 99\n"),
            ("proc/1/stat", "1 (systemd) S 0 1 1\n"),
            ("proc/42/stat", "42 (weird ) Z name) Z 1 42 42\n"),
            ("proc/43/stat", "43 (bash) R 1 43 43\n"),
            ("proc/self/stat", "43 (bash) R 1\n"),
            ("sys/class/hwmon/hwmon0/name", "coretemp\n"),
            ("sys/class/hwmon/hwmon0/temp1_input", "45500\n"),
            ("sys/class/hwmon/hwmon0/temp1_label", "Package id 0\n"),
            ("sys/class/hwmon/hwmon0/temp2_input", "-5000\n"),
        ]);

        let mut samples = Vec::new();
        SystemCollector
            .collect(
                &Context {
                    root: &root,
                    now: at(0),
                },
                &mut samples,
            )
            .unwrap();

        assert_eq!(
            int(&samples, "sermon_system_uptime_seconds", &[]),
            Some(12345)
        );
        assert_eq!(
            int(&samples, "sermon_system_boot_time_seconds", &[]),
            Some(1_795_000_000)
        );
        assert_eq!(int(&samples, "sermon_system_threads", &[]), Some(321));
        assert_eq!(int(&samples, "sermon_system_processes", &[]), Some(3));
        assert_eq!(int(&samples, "sermon_system_zombies", &[]), Some(1));
        assert_eq!(
            float(
                &samples,
                "sermon_hardware_temperature_celsius",
                &[("sensor", "coretemp/Package id 0")]
            ),
            Some(45.5)
        );
        assert_eq!(
            float(
                &samples,
                "sermon_hardware_temperature_celsius",
                &[("sensor", "coretemp/temp2")]
            ),
            Some(-5.0)
        );
        assert_eq!(
            int(&samples, "sermon_system_file_descriptors_used", &[]),
            None,
            "file-nr missing"
        );
    }

    #[test]
    fn small_parsers() {
        assert_eq!(
            parse_file_nr("1024 0 9223372036854775807\n"),
            Some((1024, i64::MAX))
        );
        assert_eq!(parse_file_nr("1 2\n"), None);
        assert_eq!(parse_uptime("-1 0\n"), None);
        assert!(!is_zombie("7 (Z) S 1"));
        assert!(is_zombie("7 (a) b) Z 1"));
    }

    proptest! {
        #[test]
        fn parsers_never_panic(text in "\\PC*") {
            let _ = parse_uptime(&text);
            let _ = parse_threads(&text);
            let _ = parse_file_nr(&text);
            let _ = is_zombie(&text);
        }
    }
}
