//! Built-in integration modules (KONZEPT §15, ADR 0039): off by default, configured per node
//! group by the server, bounded by the host's veto in `agent.toml`.
//!
//! A module ([`Module`]) checks its configuration against its [`schema`] and builds an
//! [`Instance`], which the [`runtime`] runs in a task of its own: with a time budget per run,
//! bounded output, panics and hangs kept to the module. What a module reaches is bounded by the
//! [`veto`].

pub mod discovery;
pub mod fields;
#[cfg(feature = "http")]
pub mod http;
#[cfg(feature = "http_check")]
pub mod http_check;
pub mod runtime;
pub mod schema;
#[cfg(feature = "simulate")]
pub mod simulate;
#[cfg(feature = "smart")]
pub mod smart;
#[cfg(feature = "tcp_check")]
pub mod tcp_check;
pub mod veto;
pub mod x509;

use std::collections::BTreeMap;
use std::fmt;
use std::future::Future;
use std::pin::Pin;
use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::Duration;

use sermon_core::{Clock, ProcRoot, Sample, SystemClock};
use sermon_helper_proto::v1::{HelperRequest, HelperResponse};

pub use discovery::{Discovered, HostFacts, Reason};
pub use schema::{Config, Schema};
pub use veto::{Refused, Veto};

/// The modules built into this agent.
#[must_use]
pub fn builtin() -> Vec<Arc<dyn Module>> {
    // Every module is a feature (ADR 0039).
    let modules: Vec<Arc<dyn Module>> = vec![
        #[cfg(feature = "http_check")]
        Arc::new(http_check::HttpCheck),
        #[cfg(feature = "smart")]
        Arc::new(smart::Smart),
        #[cfg(feature = "tcp_check")]
        Arc::new(tcp_check::TcpCheck),
    ];
    with_simulate(modules)
}

#[cfg(feature = "simulate")]
fn with_simulate(mut modules: Vec<Arc<dyn Module>>) -> Vec<Arc<dyn Module>> {
    modules.push(Arc::new(simulate::Simulate));
    modules
}

#[cfg(not(feature = "simulate"))]
fn with_simulate(modules: Vec<Arc<dyn Module>>) -> Vec<Arc<dyn Module>> {
    modules
}

/// A future of a module, boxed: modules are trait objects.
pub type BoxFuture<'a, T> = Pin<Box<dyn Future<Output = T> + Send + 'a>>;

/// A module of the agent, e.g. nginx.
pub trait Module: Send + Sync + 'static {
    /// The ID, e.g. `nginx`: also the prefix of its metrics (`sermon_nginx_…`).
    fn id(&self) -> &'static str;

    /// The JSON schema of its configuration ([`schema`]).
    fn schema(&self) -> &'static str;

    /// How often it collects when the configuration doesn't say (at least 10 s).
    fn default_interval(&self) -> Duration {
        Duration::from_secs(30)
    }

    /// Builds the module for a configuration that follows its schema.
    ///
    /// # Errors
    ///
    /// [`BuildError`] if the configuration can't be used or the veto refuses it.
    fn build(&self, config: &Config, setup: &Setup<'_>) -> Result<Box<dyn Instance>, BuildError>;

    /// Whether the host runs what the module watches ([`discovery`]): a suggestion for the UI,
    /// nothing is activated. Runs every 10 min on facts the agent read; must be quick and must
    /// not reach anything.
    fn discover(&self, facts: &HostFacts) -> Option<Discovered> {
        let _ = facts;
        None
    }
}

/// What a module gets to build itself.
#[derive(Debug, Clone, Copy)]
pub struct Setup<'a> {
    /// What the host allows.
    pub veto: &'a Veto,
    /// The module's credentials.
    pub secrets: &'a Secrets,
    /// The host's files and its root helper.
    pub host: &'a Host,
}

/// What modules use of the host besides the network: its files below `/proc`, `/sys` and
/// `/etc` (only through [`ProcRoot`]) and the root helper's fixed operations.
#[derive(Clone)]
pub struct Host {
    /// The host's root (`/`, or a fixture in tests).
    pub root: ProcRoot,
    /// The root helper; `None`: the agent has none (tests, virtual nodes).
    pub helper: Option<Arc<dyn Helper>>,
}

impl Default for Host {
    /// This host without a helper.
    fn default() -> Self {
        Self {
            root: ProcRoot::host(),
            helper: None,
        }
    }
}

impl fmt::Debug for Host {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Host")
            .field("root", &self.root)
            .field("helper", &self.helper.is_some())
            .finish()
    }
}

/// The root helper (ADR 0035): one of its fixed operations, blocking – call it in
/// [`Run::blocking`].
pub trait Helper: Send + Sync + 'static {
    /// Sends `request` and returns the answer.
    ///
    /// # Errors
    ///
    /// Why there is no answer (no helper, no right to its socket, a broken answer).
    fn call(&self, request: &HelperRequest) -> Result<HelperResponse, String>;
}

/// Why a module can't be built.
#[derive(Debug, thiserror::Error)]
pub enum BuildError {
    /// The configuration follows the schema but doesn't work (e.g. a missing credential).
    #[error("{0}")]
    Invalid(String),
    /// The host's veto refuses it.
    #[error(transparent)]
    Vetoed(#[from] Refused),
}

/// A built module, run by the runtime.
pub trait Instance: Send + 'static {
    /// The time budget of a run (at most 30 s; the runtime cuts longer ones).
    fn timeout(&self) -> Duration {
        Duration::from_secs(5)
    }

    /// One run: the samples, named `sermon_<id>_…`, at most 256.
    fn collect<'a>(&'a mut self, run: &'a Run) -> BoxFuture<'a, Result<Vec<Sample>, RunError>>;
}

/// Why a run failed.
#[derive(Debug, thiserror::Error)]
pub enum RunError {
    /// The service refused, answered nonsense or too much (plain text, no credentials).
    #[error("{0}")]
    Failed(String),
    /// The host's veto refuses what the module wanted to reach.
    #[error(transparent)]
    Vetoed(#[from] Refused),
}

/// What a run may use: blocking work goes through [`Run::blocking`], so the runtime knows when
/// a run hangs; the time through [`Run::now`].
#[derive(Debug, Clone)]
pub struct Run {
    blocking: Arc<AtomicUsize>,
    clock: Arc<dyn Clock>,
}

impl Default for Run {
    fn default() -> Self {
        Self::new(Arc::new(SystemClock::new()))
    }
}

impl Run {
    /// The runs of a module on `clock`.
    #[must_use]
    pub fn new(clock: Arc<dyn Clock>) -> Self {
        Self {
            blocking: Arc::new(AtomicUsize::new(0)),
            clock,
        }
    }

    /// The agent's wall-clock time (e.g. for the days a certificate has left).
    #[must_use]
    pub fn now(&self) -> std::time::SystemTime {
        self.clock.now()
    }

    /// Runs `f` on a blocking thread. A run that times out can't stop it; until it returns, the
    /// module counts as hung and starts no new run.
    ///
    /// # Errors
    ///
    /// [`RunError::Failed`] if `f` panicked.
    pub async fn blocking<F, R>(&self, f: F) -> Result<R, RunError>
    where
        F: FnOnce() -> R + Send + 'static,
        R: Send + 'static,
    {
        let guard = Busy::new(&self.blocking);
        tokio::task::spawn_blocking(move || {
            let _guard = guard;
            f()
        })
        .await
        .map_err(|e| RunError::Failed(format!("a blocking part failed: {e}")))
    }

    /// Whether blocking work of an earlier run still runs.
    #[must_use]
    pub fn busy(&self) -> bool {
        self.blocking.load(Ordering::SeqCst) > 0
    }
}

/// Counts a blocking job while it runs, also when it panics.
struct Busy(Arc<AtomicUsize>);

impl Busy {
    fn new(count: &Arc<AtomicUsize>) -> Self {
        count.fetch_add(1, Ordering::SeqCst);
        Self(count.clone())
    }
}

impl Drop for Busy {
    fn drop(&mut self) {
        self.0.fetch_sub(1, Ordering::SeqCst);
    }
}

/// The credentials of a module by field; never printed.
#[derive(Clone, Default, PartialEq, Eq)]
pub struct Secrets(BTreeMap<String, String>);

impl Secrets {
    /// The credentials `fields` (field, value).
    #[must_use]
    pub fn new(fields: impl IntoIterator<Item = (String, String)>) -> Self {
        Self(fields.into_iter().collect())
    }

    /// The value of `field`.
    #[must_use]
    pub fn get(&self, field: &str) -> Option<&str> {
        self.0.get(field).map(String::as_str)
    }

    /// The fields that have a value.
    #[must_use]
    pub fn fields(&self) -> Vec<&str> {
        self.0.keys().map(String::as_str).collect()
    }
}

impl fmt::Debug for Secrets {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_set().entries(self.0.keys()).finish()
    }
}

/// Checks that `samples` of the module `id` are in its metric catalog
/// (`metrics/<id>.json`, which the server takes over with `just gen`): the server drops what is
/// not there. For the tests of the modules.
///
/// # Panics
///
/// If a sample is not in the catalog or has other labels.
#[cfg(test)]
// Used by the tests of the modules; without the feature `simulate` there may be none.
#[allow(dead_code)]
pub(crate) fn assert_in_catalog(id: &str, samples: &[Sample]) {
    let catalog = tests::catalog(id);
    for s in samples {
        let def = catalog
            .iter()
            .find(|d| d["name"] == s.metric)
            .unwrap_or_else(|| panic!("{}: not in metrics/{id}.json", s.metric));
        let mut want: Vec<&str> = def
            .get("labels")
            .and_then(serde_json::Value::as_array)
            .map_or_else(Vec::new, |l| {
                l.iter().filter_map(serde_json::Value::as_str).collect()
            });
        let mut have: Vec<&str> = s.labels.iter().map(|(k, _)| *k).collect();
        want.sort_unstable();
        have.sort_unstable();
        assert_eq!(have, want, "{}: labels", s.metric);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// The metric definitions of a module's catalog file.
    pub(crate) fn catalog(id: &str) -> Vec<serde_json::Value> {
        let path = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("metrics")
            .join(format!("{id}.json"));
        let text =
            std::fs::read_to_string(&path).unwrap_or_else(|e| panic!("{}: {e}", path.display()));
        let file: serde_json::Value = serde_json::from_str(&text).unwrap();
        file["metrics"].as_array().cloned().unwrap_or_default()
    }

    // ADR 0039: every module built in has a metric catalog, its metrics named after it; the
    // server's test checks units and aggregations.
    #[test]
    fn every_module_has_a_metric_catalog() {
        for module in builtin() {
            let catalog = catalog(module.id());
            assert!(!catalog.is_empty(), "{}: no metrics", module.id());
            let prefix = format!("sermon_{}_", module.id());
            for def in catalog {
                let name = def["name"].as_str().unwrap();
                assert!(name.starts_with(&prefix), "{name}");
            }
        }
    }

    // ADR 0039: every schema in `schemas/` (the server's copy, `just gen`) is in the subset and
    // named after its module; every module built in has one there.
    #[test]
    fn every_schema_is_in_the_subset() {
        let dir = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("schemas");
        let mut ids = Vec::new();
        for entry in std::fs::read_dir(&dir).unwrap() {
            let path = entry.unwrap().path();
            let text = std::fs::read_to_string(&path).unwrap();
            Schema::parse(&text).unwrap_or_else(|e| panic!("{}: {e}", path.display()));
            ids.push(path.file_stem().unwrap().to_string_lossy().into_owned());
        }
        for module in builtin() {
            assert!(
                ids.iter().any(|id| id == module.id()),
                "{}: no schema file",
                module.id()
            );
            Schema::parse(module.schema()).unwrap();
        }
    }

    #[test]
    fn secrets_print_their_fields_only() {
        let s = Secrets::new([("password".to_owned(), "hunter2".to_owned())]);
        assert_eq!(format!("{s:?}"), r#"{"password"}"#);
        assert_eq!(s.get("password"), Some("hunter2"));
    }
}
