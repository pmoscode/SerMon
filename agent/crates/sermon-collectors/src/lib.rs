//! System metric collectors reading `/proc` and `/sys` through an injectable `ProcRoot`.
//!
//! Every collector has a pure parser (`parse_*`, tested on fixtures and with arbitrary input) and
//! keeps the previous snapshot to turn counters into rates. The scheduler (M1-21) calls them.

pub mod agent;
pub mod cpu;
pub mod disk;
pub mod filesystem;
pub mod host;
pub mod memory;
pub mod network;
pub mod pressure;
pub mod raid;
pub mod rate;
pub mod system;
pub mod systemd;
pub mod timesync;

use sermon_core::{Monotonic, ProcError, ProcRoot, Sample};

/// What a collector gets for one run.
#[derive(Debug, Clone, Copy)]
pub struct Context<'a> {
    /// The file system root with `proc/` and `sys/`.
    pub root: &'a ProcRoot,
    /// Monotonic time of this run (for rates).
    pub now: Monotonic,
}

/// Errors of a collector run. Missing optional files are not errors (no samples instead).
#[derive(Debug, thiserror::Error)]
pub enum CollectError {
    /// A required file could not be read.
    #[error(transparent)]
    Read(#[from] ProcError),
    /// A file has an unexpected format.
    #[error("{file}: {reason}")]
    Parse {
        /// The file, e.g. `/proc/stat`.
        file: &'static str,
        /// What is wrong.
        reason: String,
    },
}

impl CollectError {
    pub(crate) fn parse(file: &'static str, reason: impl Into<String>) -> Self {
        Self::Parse {
            file,
            reason: reason.into(),
        }
    }
}

/// A source of samples.
pub trait Collector: Send {
    /// Short name for logs, e.g. `cpu`.
    fn name(&self) -> &'static str;

    /// Run every this many base ticks (1 s); expensive collectors run less often.
    fn interval_ticks(&self) -> u32 {
        1
    }

    /// Appends the current samples to `out`. Rates need two runs: the first one only remembers.
    ///
    /// # Errors
    ///
    /// [`CollectError`] if a required source is unreadable; the scheduler logs it and continues.
    fn collect(&mut self, context: &Context<'_>, out: &mut Vec<Sample>)
    -> Result<(), CollectError>;
}

/// Reads an optional file: `None` if it doesn't exist (older kernel, module not loaded).
pub(crate) fn optional(result: Result<String, ProcError>) -> Result<Option<String>, CollectError> {
    match result {
        Ok(text) => Ok(Some(text)),
        Err(error) if error.is_not_found() => Ok(None),
        Err(error) => Err(error.into()),
    }
}

/// The collectors of the base metrics (KONZEPT §8.1) in their default configuration.
#[must_use]
pub fn default_collectors() -> Vec<Box<dyn Collector>> {
    vec![
        Box::new(cpu::CpuCollector::default()),
        Box::new(memory::MemoryCollector::default()),
        Box::new(pressure::PressureCollector::default()),
        Box::new(disk::DiskCollector::default()),
        Box::new(filesystem::FilesystemCollector::new(Box::new(
            filesystem::StatvfsUsage,
        ))),
        Box::new(network::NetworkCollector::default()),
        Box::new(system::SystemCollector),
        Box::new(raid::RaidCollector),
        Box::new(timesync::TimeSyncCollector::default()),
        Box::new(agent::SelfCollector::default()),
    ]
}

#[cfg(test)]
pub(crate) mod testing {
    //! Helpers for collector tests.

    use std::path::PathBuf;
    use std::time::Duration;

    use sermon_core::{Monotonic, ProcRoot, Sample, Value};

    pub(crate) fn fixture(distro: &str) -> ProcRoot {
        ProcRoot::new(
            PathBuf::from(env!("CARGO_MANIFEST_DIR"))
                .join("../../fixtures")
                .join(distro),
        )
    }

    pub(crate) fn at(seconds: u64) -> Monotonic {
        Monotonic::from_origin(Duration::from_secs(seconds))
    }

    /// The value of the sample with `metric` and exactly these labels.
    pub(crate) fn value(
        samples: &[Sample],
        metric: &str,
        labels: &[(&str, &str)],
    ) -> Option<Value> {
        samples
            .iter()
            .find(|sample| {
                sample.metric == metric
                    && sample.labels.len() == labels.len()
                    && labels.iter().all(|(name, value)| {
                        sample.labels.iter().any(|(n, v)| n == name && v == value)
                    })
            })
            .map(|sample| sample.value)
    }

    pub(crate) fn int(samples: &[Sample], metric: &str, labels: &[(&str, &str)]) -> Option<i64> {
        match value(samples, metric, labels) {
            Some(Value::Int(value)) => Some(value),
            _ => None,
        }
    }

    pub(crate) fn float(samples: &[Sample], metric: &str, labels: &[(&str, &str)]) -> Option<f64> {
        match value(samples, metric, labels) {
            Some(Value::Float(value)) => Some(value),
            _ => None,
        }
    }

    /// A temporary root with the given files (path relative to the root → content).
    pub(crate) fn root_with(files: &[(&str, &str)]) -> (tempfile::TempDir, ProcRoot) {
        let dir = tempfile::tempdir().unwrap();
        for (path, content) in files {
            let path = dir.path().join(path);
            std::fs::create_dir_all(path.parent().unwrap()).unwrap();
            std::fs::write(path, content).unwrap();
        }
        let root = ProcRoot::new(dir.path());
        (dir, root)
    }
}
