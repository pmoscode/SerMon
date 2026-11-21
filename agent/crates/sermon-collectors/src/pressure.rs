//! Pressure stall information (PSI): `/proc/pressure/{cpu,memory,io}` (KONZEPT §8.1).
//!
//! | Metric | Labels | Unit |
//! |---|---|---|
//! | `sermon_pressure_stall_ratio` | `resource` (cpu, memory, io), `kind` (some, full) | ppm of wall time |
//!
//! The share is computed from the `total` counters (µs stalled), not from the kernel's 10 s
//! average, so that each 1 s value is exact. PSI needs Linux 4.20+ with PSI enabled; without it
//! there are no samples. `cpu full` exists from Linux 5.13 on.

use std::time::Duration;

use sermon_core::{Monotonic, Sample};

use crate::rate::{delta, time_ppm};
use crate::{CollectError, Collector, Context};

const RESOURCES: [&str; 3] = ["cpu", "memory", "io"];

/// Stall totals of one resource in microseconds.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct Pressure {
    /// Some tasks stalled.
    pub some: Option<u64>,
    /// All non-idle tasks stalled.
    pub full: Option<u64>,
}

/// Parses one `/proc/pressure/*` file.
#[must_use]
pub fn parse_pressure(text: &str) -> Pressure {
    let mut pressure = Pressure::default();
    for line in text.lines() {
        let mut fields = line.split_whitespace();
        let kind = fields.next();
        let total = fields
            .find_map(|field| field.strip_prefix("total="))
            .and_then(|value| value.parse().ok());
        match kind {
            Some("some") => pressure.some = total,
            Some("full") => pressure.full = total,
            _ => {}
        }
    }
    pressure
}

/// PSI collector.
#[derive(Debug, Default)]
pub struct PressureCollector {
    previous: Option<(Monotonic, [Option<Pressure>; 3])>,
}

impl Collector for PressureCollector {
    fn name(&self) -> &'static str {
        "pressure"
    }

    fn collect(
        &mut self,
        context: &Context<'_>,
        out: &mut Vec<Sample>,
    ) -> Result<(), CollectError> {
        // Missing or unreadable (PSI disabled: EOPNOTSUPP) means: not available on this host.
        let current = RESOURCES.map(|resource| {
            context
                .root
                .read_proc(&format!("pressure/{resource}"))
                .ok()
                .map(|text| parse_pressure(&text))
        });
        if let Some((at, previous)) = &self.previous {
            let elapsed = context.now.saturating_duration_since(*at);
            for ((resource, now), before) in RESOURCES.iter().zip(&current).zip(previous) {
                let (Some(now), Some(before)) = (now, before) else {
                    continue;
                };
                for (kind, now, before) in [
                    ("some", now.some, before.some),
                    ("full", now.full, before.full),
                ] {
                    let stalled = now.zip(before).and_then(|(now, before)| delta(before, now));
                    if let Some(value) =
                        stalled.and_then(|micros| time_ppm(Duration::from_micros(micros), elapsed))
                    {
                        out.push(
                            Sample::int("sermon_pressure_stall_ratio", value)
                                .label("resource", *resource)
                                .label("kind", kind),
                        );
                    }
                }
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
    use crate::testing::{at, fixture, int, root_with};

    #[test]
    fn parses_the_fixtures() {
        for distro in [
            "debian-13",
            "ubuntu-24.04",
            "rockylinux-9",
            "almalinux-10",
            "fedora-44",
        ] {
            for resource in RESOURCES {
                let text = fixture(distro)
                    .read_proc(&format!("pressure/{resource}"))
                    .unwrap();
                let pressure = parse_pressure(&text);
                assert!(
                    pressure.some.is_some() && pressure.full.is_some(),
                    "{distro} {resource}"
                );
            }
        }
    }

    #[test]
    fn stall_share_from_the_totals() {
        let line = |some: u64, full: u64| {
            format!(
                "some avg10=0.00 avg60=0.00 avg300=0.00 total={some}\nfull avg10=0.00 avg60=0.00 avg300=0.00 total={full}\n"
            )
        };
        let (_dir, root) = root_with(&[
            ("proc/pressure/memory", &line(1_000, 0)),
            ("proc/pressure/io", &line(0, 0)),
        ]);
        let mut collector = PressureCollector::default();
        collector
            .collect(
                &Context {
                    root: &root,
                    now: at(0),
                },
                &mut Vec::new(),
            )
            .unwrap();
        std::fs::write(root.proc("pressure/memory").unwrap(), line(201_000, 50_000)).unwrap();

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

        let stall = |resource, kind| {
            int(
                &samples,
                "sermon_pressure_stall_ratio",
                &[("resource", resource), ("kind", kind)],
            )
        };
        assert_eq!(stall("memory", "some"), Some(100_000), "200 ms of 2 s");
        assert_eq!(stall("memory", "full"), Some(25_000));
        assert_eq!(stall("io", "some"), Some(0));
        assert_eq!(stall("cpu", "some"), None, "no PSI file for cpu");
    }

    proptest! {
        #[test]
        fn parser_never_panics(text in "\\PC*") {
            let _ = parse_pressure(&text);
        }
    }
}
