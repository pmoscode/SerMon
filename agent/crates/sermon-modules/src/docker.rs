//! Containers of Docker and Podman from cgroup v2 (M5-16): CPU, memory, IO, network and
//! processes per container, without the runtimes' sockets and without extra rights. Names
//! come from the containers' first process; the socket (M5-17) can name them better.
//!
//! Metrics (labels `container` – the short ID of `docker ps` –, `name`, `runtime`):
//!
//! | Metric | Meaning |
//! |---|---|
//! | `sermon_docker_containers` | running containers found (no label) |
//! | `sermon_docker_truncated` | 1: more containers than reported (`max_containers`) (no label) |
//! | `sermon_docker_cpu_utilization_ratio` | CPU, ppm of all CPUs of the host |
//! | `sermon_docker_memory_used_bytes` | memory without the inactive page cache |
//! | `sermon_docker_memory_limit_bytes` | its limit, only with one |
//! | `sermon_docker_oom_kills` | processes the OOM killer ended in it |
//! | `sermon_docker_read_bytes_per_second`, `sermon_docker_write_bytes_per_second` | block IO |
//! | `sermon_docker_receive_bytes_per_second`, `sermon_docker_transmit_bytes_per_second` | network, not with the host's network |
//! | `sermon_docker_processes` | processes and threads |
//!
//! Rates need two runs: a new container has them from its second run on.

pub mod cgroups;

use std::collections::{BTreeMap, BTreeSet};

use serde_json::Value;
use sermon_collectors::rate::{per_second, ppm};
use sermon_core::{Monotonic, ProcRoot, Sample};

use crate::discovery::{Discovered, HostFacts};
use crate::runtime::MAX_SAMPLES;
use crate::{BoxFuture, BuildError, Config, Instance, Module, Run, RunError, Setup};
use cgroups::{Found, Reading};

const SCHEMA: &str = include_str!("../schemas/docker.json");
/// Samples a container gives at most.
const PER_CONTAINER: usize = 9;
/// Most containers reported: their samples and the two totals stay within the runtime's bound.
pub const MAX_CONTAINERS: usize = (MAX_SAMPLES - 2) / PER_CONTAINER;

/// The module `docker`.
#[derive(Debug, Clone, Copy, Default)]
pub struct Docker;

impl Module for Docker {
    fn id(&self) -> &'static str {
        "docker"
    }

    fn schema(&self) -> &'static str {
        SCHEMA
    }

    fn build(&self, config: &Config, setup: &Setup<'_>) -> Result<Box<dyn Instance>, BuildError> {
        let max = config
            .get("max_containers")
            .and_then(Value::as_u64)
            .and_then(|m| usize::try_from(m).ok())
            .unwrap_or(20);
        Ok(Box::new(Containers::new(setup.host.root.clone(), max)))
    }

    /// dockerd, or conmon (a process per Podman container).
    fn discover(&self, facts: &HostFacts) -> Option<Discovered> {
        Discovered::evidence(
            facts,
            &["dockerd", "conmon"],
            &["docker.service", "podman.service"],
            &[],
        )
    }
}

/// The counters of the last run, for the rates.
#[derive(Debug)]
struct Last {
    at: Monotonic,
    reading: Reading,
}

/// The running module: the host's root and the counters of its last run.
#[derive(Debug)]
pub struct Containers {
    root: ProcRoot,
    max: usize,
    last: BTreeMap<String, Last>,
}

impl Containers {
    /// Reads the containers below `root`, reporting at most `max`.
    #[must_use]
    pub fn new(root: ProcRoot, max: usize) -> Self {
        Self {
            root,
            max: max.clamp(1, MAX_CONTAINERS),
            last: BTreeMap::new(),
        }
    }

    /// The samples of `readings` taken `at`; keeps their counters for the next run (only of
    /// those still there).
    fn samples(
        &mut self,
        readings: Vec<(Found, Reading)>,
        cpus: u64,
        at: Monotonic,
    ) -> Vec<Sample> {
        let total = readings.len();
        let mut shown: Vec<&(Found, Reading)> = readings.iter().collect();
        // The largest by memory: on a host with many containers those matter most.
        shown.sort_by(|a, b| b.1.memory.cmp(&a.1.memory).then_with(|| a.0.cmp(&b.0)));
        shown.truncate(self.max);
        let mut out = vec![
            Sample::int(
                "sermon_docker_containers",
                i64::try_from(total).unwrap_or(i64::MAX),
            ),
            Sample::int("sermon_docker_truncated", i64::from(total > self.max)),
        ];
        for (found, now) in shown {
            let labels = |s: Sample| {
                s.label("container", found.id[..12].to_owned())
                    .label("name", now.name.clone())
                    .label("runtime", found.runtime.name())
            };
            let gauge = |m, v: Option<u64>| Some(labels(Sample::int(m, i64::try_from(v?).ok()?)));
            out.extend(
                [
                    gauge("sermon_docker_memory_used_bytes", now.memory),
                    gauge("sermon_docker_memory_limit_bytes", now.limit),
                    gauge("sermon_docker_oom_kills", now.oom_kills),
                    gauge("sermon_docker_processes", now.processes),
                ]
                .into_iter()
                .flatten(),
            );
            let Some(before) = self.last.get(&found.id) else {
                continue;
            };
            let elapsed = at.saturating_duration_since(before.at);
            let was = &before.reading;
            let rate = |m, b: Option<u64>, n: Option<u64>| {
                Some(labels(Sample::int(m, per_second(b?, n?, elapsed)?)))
            };
            let cpu = was.cpu_usec.zip(now.cpu_usec).and_then(|(b, n)| {
                let usec = u64::try_from(elapsed.as_micros()).ok()?.checked_mul(cpus)?;
                Some(labels(Sample::int(
                    "sermon_docker_cpu_utilization_ratio",
                    ppm(n.checked_sub(b)?, usec)?,
                )))
            });
            out.extend(
                [
                    cpu,
                    rate(
                        "sermon_docker_read_bytes_per_second",
                        was.io.map(|i| i.0),
                        now.io.map(|i| i.0),
                    ),
                    rate(
                        "sermon_docker_write_bytes_per_second",
                        was.io.map(|i| i.1),
                        now.io.map(|i| i.1),
                    ),
                    rate(
                        "sermon_docker_receive_bytes_per_second",
                        was.net.map(|n| n.0),
                        now.net.map(|n| n.0),
                    ),
                    rate(
                        "sermon_docker_transmit_bytes_per_second",
                        was.net.map(|n| n.1),
                        now.net.map(|n| n.1),
                    ),
                ]
                .into_iter()
                .flatten(),
            );
        }
        self.last = readings
            .into_iter()
            .map(|(f, reading)| (f.id, Last { at, reading }))
            .collect();
        out
    }
}

/// The containers of `root` with their readings and the number of CPUs; `None` without cgroup v2.
#[must_use]
pub fn read_all(root: &ProcRoot) -> Option<(Vec<(Found, Reading)>, u64)> {
    let found = cgroups::scan(root)?;
    let host: BTreeSet<String> = root
        .read_proc("net/dev")
        .map_or_else(|_| BTreeSet::new(), |t| cgroups::interfaces(&t));
    let cpus = root
        .read_proc("stat")
        .map_or(0, |t| {
            t.lines()
                .filter(|l| {
                    l.strip_prefix("cpu")
                        .is_some_and(|r| r.starts_with(|c: char| c.is_ascii_digit()))
                })
                .count()
        })
        .max(1);
    let readings = found
        .into_iter()
        .map(|f| {
            let reading = cgroups::read(root, &f.dir, &host);
            (f, reading)
        })
        .collect();
    Some((readings, u64::try_from(cpus).unwrap_or(1)))
}

impl Instance for Containers {
    fn collect<'a>(&'a mut self, run: &'a Run) -> BoxFuture<'a, Result<Vec<Sample>, RunError>> {
        Box::pin(async move {
            let root = self.root.clone();
            let at = run.monotonic();
            let (readings, cpus) =
                run.blocking(move || read_all(&root))
                    .await?
                    .ok_or_else(|| {
                        RunError::Failed(
                            "no cgroup v2 at /sys/fs/cgroup: the module reads cgroup v2 only"
                                .into(),
                        )
                    })?;
            Ok(self.samples(readings, cpus, at))
        })
    }
}

#[cfg(test)]
mod tests;
