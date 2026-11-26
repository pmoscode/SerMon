//! Built-in integration modules (KONZEPT §15, ADR 0039): off by default, configured per node
//! group by the server, bounded by the host's veto in `agent.toml`.
//!
//! A module ([`Module`]) checks its configuration against its [`schema`] and builds an
//! [`Instance`], which the [`runtime`] runs in a task of its own: with a time budget per run,
//! bounded output, panics and hangs kept to the module. What a module reaches is bounded by the
//! [`veto`].

pub mod discovery;
pub mod runtime;
pub mod schema;
#[cfg(feature = "simulate")]
pub mod simulate;
pub mod veto;

use std::collections::BTreeMap;
use std::fmt;
use std::future::Future;
use std::pin::Pin;
use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::Duration;

use sermon_core::Sample;

pub use discovery::{Discovered, HostFacts, Reason};
pub use schema::{Config, Schema};
pub use veto::{Refused, Veto};

/// The modules built into this agent.
#[must_use]
pub fn builtin() -> Vec<Arc<dyn Module>> {
    let modules: Vec<Arc<dyn Module>> = Vec::new();
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
/// a run hangs.
#[derive(Debug, Clone, Default)]
pub struct Run {
    blocking: Arc<AtomicUsize>,
}

impl Run {
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

#[cfg(test)]
mod tests {
    use super::*;

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
