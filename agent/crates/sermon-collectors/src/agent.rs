//! The agent's own resource use (budget KONZEPT §4.1: < 1 % of a core, < 30 MB RSS).
//!
//! | Metric | Unit |
//! |---|---|
//! | `sermon_agent_cpu_ratio` | ppm of one core |
//! | `sermon_agent_memory_rss_bytes` | bytes |

use sermon_core::{Monotonic, Sample};

use crate::rate::{delta, time_ppm};
use crate::{CollectError, Collector, Context};

/// Clock ticks per second of `/proc/<pid>/stat` times (`USER_HZ`, 100 on every Linux target).
const USER_HZ: u64 = 100;

/// User plus system CPU time in clock ticks from `/proc/self/stat`.
#[must_use]
pub fn parse_cpu_ticks(stat: &str) -> Option<u64> {
    // Fields after the command name: state is field 3, utime 14, stime 15.
    let (_, rest) = stat.rsplit_once(')')?;
    let fields: Vec<&str> = rest.split_whitespace().collect();
    let user: u64 = fields.get(11)?.parse().ok()?;
    let system: u64 = fields.get(12)?.parse().ok()?;
    user.checked_add(system)
}

/// Resident set size in bytes from `/proc/self/status`.
#[must_use]
pub fn parse_rss(status: &str) -> Option<u64> {
    let line = status
        .lines()
        .find_map(|line| line.strip_prefix("VmRSS:"))?;
    let kib: u64 = line.split_whitespace().next()?.parse().ok()?;
    kib.checked_mul(1024)
}

/// Collector of the agent's own CPU and memory use.
#[derive(Debug, Default)]
pub struct SelfCollector {
    previous: Option<(Monotonic, u64)>,
}

impl Collector for SelfCollector {
    fn name(&self) -> &'static str {
        "agent"
    }

    fn interval_ticks(&self) -> u32 {
        10
    }

    fn collect(
        &mut self,
        context: &Context<'_>,
        out: &mut Vec<Sample>,
    ) -> Result<(), CollectError> {
        if let Some(rss) = parse_rss(&context.root.read_proc("self/status")?) {
            out.push(Sample::int(
                "sermon_agent_memory_rss_bytes",
                i64::try_from(rss).unwrap_or(i64::MAX),
            ));
        }
        let Some(ticks) = parse_cpu_ticks(&context.root.read_proc("self/stat")?) else {
            return Err(CollectError::parse("/proc/self/stat", "malformed"));
        };
        if let Some((at, before)) = self.previous {
            let used = delta(before, ticks)
                .map(|ticks| std::time::Duration::from_millis(ticks * 1000 / USER_HZ));
            if let Some(value) =
                used.and_then(|used| time_ppm(used, context.now.saturating_duration_since(at)))
            {
                out.push(Sample::int("sermon_agent_cpu_ratio", value));
            }
        }
        self.previous = Some((context.now, ticks));
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use proptest::prelude::*;

    use super::*;
    use crate::testing::{at, int, root_with};

    fn stat(ticks: u64) -> String {
        format!(
            "4242 (sermon-agent) S 1 4242 4242 0 -1 4194560 100 0 0 0 {ticks} 0 0 0 20 0 3 0 100 0 0\n"
        )
    }

    #[test]
    fn cpu_share_and_rss() {
        let (_dir, root) = root_with(&[
            ("proc/self/stat", &stat(10)),
            (
                "proc/self/status",
                "Name:\tsermon-agent\nVmRSS:\t   12345 kB\n",
            ),
        ]);
        let mut collector = SelfCollector::default();
        collector
            .collect(
                &Context {
                    root: &root,
                    now: at(0),
                },
                &mut Vec::new(),
            )
            .unwrap();
        std::fs::write(root.proc("self/stat").unwrap(), stat(15)).unwrap();

        let mut samples = Vec::new();
        collector
            .collect(
                &Context {
                    root: &root,
                    now: at(10),
                },
                &mut samples,
            )
            .unwrap();

        // 5 ticks = 50 ms in 10 s.
        assert_eq!(int(&samples, "sermon_agent_cpu_ratio", &[]), Some(5_000));
        assert_eq!(
            int(&samples, "sermon_agent_memory_rss_bytes", &[]),
            Some(12345 * 1024)
        );
    }

    #[test]
    fn reads_the_real_process_on_linux() {
        if cfg!(target_os = "linux") {
            let root = sermon_core::ProcRoot::host();
            assert!(parse_cpu_ticks(&root.read_proc("self/stat").unwrap()).is_some());
            assert!(parse_rss(&root.read_proc("self/status").unwrap()).is_some());
        }
    }

    proptest! {
        #[test]
        fn parsers_never_panic(text in "\\PC*") {
            let _ = parse_cpu_ticks(&text);
            let _ = parse_rss(&text);
        }
    }
}
