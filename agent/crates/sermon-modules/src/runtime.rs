//! Runs the enabled modules (ADR 0039): a task each, a time budget per run, at most
//! [`MAX_SAMPLES`] samples a run. A module that panics is reported crashed and starts again after
//! a pause; one whose blocking work doesn't return starts no new run until it does. Neither
//! touches the agent or other modules. The state of every enabled module goes to the [`Sink`]
//! whenever it changes.

use std::collections::BTreeMap;
use std::sync::{Arc, Mutex};
use std::time::{Duration, SystemTime};

use sermon_core::{Clock, Sample};
use tokio::time::MissedTickBehavior;
use tokio_util::sync::CancellationToken;

use crate::{BuildError, Host, Module, Run, RunError, Schema, Secrets, Setup, Veto};

/// Most samples of one run.
pub const MAX_SAMPLES: usize = 256;
/// Bounds of the interval of a module.
pub const MIN_INTERVAL: Duration = Duration::from_secs(10);
/// Longest interval of a module.
pub const MAX_INTERVAL: Duration = Duration::from_hours(1);
/// Longest time budget of a run.
pub const MAX_BUDGET: Duration = Duration::from_secs(30);
/// Longest reason in a state.
const MAX_REASON: usize = 256;
/// Most labels of a sample and longest label value (the protocol's bounds).
const MAX_LABELS: usize = 8;
const MAX_LABEL_VALUE: usize = 256;

/// How a module does (the protocol's `ModuleState`).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum State {
    /// The last run succeeded.
    Ok,
    /// The last run failed.
    Error,
    /// The last run took longer than its budget.
    Timeout,
    /// Blocking work of a run doesn't return.
    Hung,
    /// The module panicked.
    Crashed,
    /// The configuration doesn't follow the schema, or the module is unknown.
    InvalidConfig,
    /// The host's veto forbids it.
    Vetoed,
}

/// The state of one enabled module.
#[derive(Debug, Clone, PartialEq)]
pub struct Health {
    /// The module.
    pub module: String,
    /// How it does.
    pub state: State,
    /// Why, if not ok: plain text, at most 256 bytes.
    pub reason: String,
    /// When a run last succeeded.
    pub last_success: Option<SystemTime>,
}

/// Where the runtime's output goes (the agent's outbox).
pub trait Sink: Send + Sync + 'static {
    /// The samples of a run.
    fn samples(&self, samples: Vec<Sample>, at: SystemTime);
    /// The state of all enabled modules, after a change.
    fn health(&self, report: Vec<Health>, at: SystemTime);
    /// The log lines a run of `module` read (M5-21); dropped by sinks without a log pipeline.
    fn logs(&self, module: &str, lines: Vec<crate::LogLine>) {
        let _ = (module, lines);
    }
}

/// A module as the configuration names it.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Spec {
    /// The module's ID.
    pub id: String,
    /// Whether it runs.
    pub enabled: bool,
    /// How often; `None`: the module's default.
    pub interval: Option<Duration>,
    /// Its configuration (JSON).
    pub config_json: String,
}

/// Pauses after a crash: the first, doubled up to the last.
#[derive(Debug, Clone, Copy)]
pub struct Pauses {
    /// After the first crash.
    pub first: Duration,
    /// At most.
    pub max: Duration,
}

impl Default for Pauses {
    fn default() -> Self {
        Self {
            first: Duration::from_secs(30),
            max: Duration::from_mins(30),
        }
    }
}

struct Shared {
    sink: Arc<dyn Sink>,
    clock: Arc<dyn Clock>,
    health: Mutex<BTreeMap<String, Health>>,
}

impl Shared {
    fn set(&self, module: &str, state: State, reason: &str) {
        let changed = {
            let mut health = lock(&self.health);
            let last_success = health.get(module).and_then(|h| h.last_success);
            let last_success = if state == State::Ok {
                Some(self.clock.now())
            } else {
                last_success
            };
            let next = Health {
                module: module.to_owned(),
                state,
                reason: bounded(reason),
                last_success,
            };
            let changed = health
                .get(module)
                .is_none_or(|h| h.state != next.state || h.reason != next.reason);
            health.insert(module.to_owned(), next);
            changed
        };
        if changed {
            self.report();
        }
    }

    fn remove(&self, module: &str) -> bool {
        lock(&self.health).remove(module).is_some()
    }

    fn report(&self) {
        let report = lock(&self.health).values().cloned().collect();
        self.sink.health(report, self.clock.now());
    }
}

fn lock<T>(m: &Mutex<T>) -> std::sync::MutexGuard<'_, T> {
    m.lock().unwrap_or_else(std::sync::PoisonError::into_inner)
}

/// A reason without control characters, cut to [`MAX_REASON`] bytes at a character boundary.
fn bounded(reason: &str) -> String {
    let clean: String = reason
        .chars()
        .map(|c| if c.is_control() { ' ' } else { c })
        .collect();
    let mut end = clean.len().min(MAX_REASON);
    while !clean.is_char_boundary(end) {
        end -= 1;
    }
    clean[..end].to_owned()
}

struct Task {
    spec: Spec,
    secrets: Secrets,
    cancel: CancellationToken,
}

/// The modules of the agent and the tasks of those enabled.
pub struct Runtime {
    modules: BTreeMap<&'static str, Arc<dyn Module>>,
    veto: Arc<Veto>,
    host: Arc<Host>,
    shared: Arc<Shared>,
    pauses: Pauses,
    tasks: BTreeMap<String, Task>,
}

impl std::fmt::Debug for Runtime {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Runtime")
            .field("modules", &self.modules.keys().collect::<Vec<_>>())
            .field("running", &self.tasks.keys().collect::<Vec<_>>())
            .finish_non_exhaustive()
    }
}

impl Runtime {
    /// The runtime of `modules`, bounded by `veto`.
    #[must_use]
    pub fn new(
        modules: Vec<Arc<dyn Module>>,
        veto: Veto,
        sink: Arc<dyn Sink>,
        clock: Arc<dyn Clock>,
    ) -> Self {
        Self {
            modules: modules.into_iter().map(|m| (m.id(), m)).collect(),
            veto: Arc::new(veto),
            host: Arc::new(Host::default()),
            shared: Arc::new(Shared {
                sink,
                clock,
                health: Mutex::new(BTreeMap::new()),
            }),
            pauses: Pauses::default(),
            tasks: BTreeMap::new(),
        }
    }

    /// The host's files and root helper the modules use (default: `/` without a helper).
    #[must_use]
    pub fn with_host(mut self, host: Host) -> Self {
        self.host = Arc::new(host);
        self
    }

    /// Other pauses after a crash (tests).
    #[must_use]
    pub fn with_pauses(mut self, pauses: Pauses) -> Self {
        self.pauses = pauses;
        self
    }

    /// The IDs of the modules built in (for `Hello`).
    #[must_use]
    pub fn ids(&self) -> Vec<String> {
        self.modules.keys().map(|id| (*id).to_owned()).collect()
    }

    /// Runs the enabled modules of `specs` with `secrets` (by module): starts what is new or
    /// changed, stops what is gone or off. Must be called within a tokio runtime.
    pub fn apply(&mut self, specs: &[Spec], secrets: &BTreeMap<String, Secrets>) {
        let enabled: BTreeMap<&str, &Spec> = specs
            .iter()
            .filter(|s| s.enabled)
            .map(|s| (s.id.as_str(), s))
            .collect();
        let mut changed = false;
        let gone: Vec<String> = self
            .tasks
            .keys()
            .filter(|id| !enabled.contains_key(id.as_str()))
            .cloned()
            .collect();
        for id in gone {
            if let Some(task) = self.tasks.remove(&id) {
                task.cancel.cancel();
            }
        }
        let known: Vec<String> = lock(&self.shared.health).keys().cloned().collect();
        for id in known {
            if !enabled.contains_key(id.as_str()) {
                changed |= self.shared.remove(&id);
            }
        }
        for (id, spec) in enabled {
            let module_secrets = secrets.get(id).cloned().unwrap_or_default();
            if let Some(task) = self.tasks.get(id)
                && task.spec == *spec
                && task.secrets == module_secrets
            {
                continue;
            }
            if let Some(old) = self.tasks.remove(id) {
                old.cancel.cancel();
            }
            let Some(module) = self.modules.get(id).cloned() else {
                self.shared
                    .set(id, State::InvalidConfig, "this agent has no such module");
                continue;
            };
            if !self.veto.allow {
                self.shared
                    .set(id, State::Vetoed, "allow_modules = false in agent.toml");
                continue;
            }
            let cancel = CancellationToken::new();
            tokio::spawn(supervise(Supervised {
                module,
                spec: spec.clone(),
                secrets: module_secrets.clone(),
                veto: self.veto.clone(),
                host: self.host.clone(),
                shared: self.shared.clone(),
                pauses: self.pauses,
                cancel: cancel.clone(),
            }));
            self.tasks.insert(
                id.to_owned(),
                Task {
                    spec: spec.clone(),
                    secrets: module_secrets,
                    cancel,
                },
            );
        }
        if changed {
            self.shared.report();
        }
    }

    /// Sends the state of all enabled modules again (every 10 min, ADR 0039); nothing without
    /// any.
    pub fn report(&self) {
        if !lock(&self.shared.health).is_empty() {
            self.shared.report();
        }
    }

    /// Stops every module.
    pub fn stop(&mut self) {
        for (_, task) in std::mem::take(&mut self.tasks) {
            task.cancel.cancel();
        }
    }
}

impl Drop for Runtime {
    fn drop(&mut self) {
        self.stop();
    }
}

struct Supervised {
    module: Arc<dyn Module>,
    spec: Spec,
    secrets: Secrets,
    veto: Arc<Veto>,
    host: Arc<Host>,
    shared: Arc<Shared>,
    pauses: Pauses,
    cancel: CancellationToken,
}

/// Checks the configuration against the schema and builds the module.
fn build(s: &Supervised) -> Result<Box<dyn crate::Instance>, (State, String)> {
    Schema::parse(s.module.schema())
        .map_err(|e| (State::InvalidConfig, format!("the module's schema: {e}")))
        .and_then(|schema| {
            schema
                .validate(&s.spec.config_json, &s.secrets.fields())
                .map_err(|e| (State::InvalidConfig, e))
        })
        .and_then(|config| {
            s.module
                .build(
                    &config,
                    &Setup {
                        veto: &s.veto,
                        secrets: &s.secrets,
                        host: &s.host,
                    },
                )
                .map_err(|e| match e {
                    BuildError::Invalid(why) => (State::InvalidConfig, why),
                    BuildError::Vetoed(r) => (State::Vetoed, r.0),
                })
        })
}

/// Builds and runs one module until it is cancelled; a crash builds it anew after a pause.
async fn supervise(s: Supervised) {
    let id = s.module.id();
    let mut pause = s.pauses.first;
    loop {
        let built = build(&s);
        let instance = match built {
            Ok(instance) => Arc::new(tokio::sync::Mutex::new(instance)),
            Err((state, why)) => {
                s.shared.set(id, state, &why);
                s.cancel.cancelled().await;
                return;
            }
        };
        let interval = s
            .spec
            .interval
            .unwrap_or_else(|| s.module.default_interval())
            .clamp(MIN_INTERVAL, MAX_INTERVAL);
        let mut tick = tokio::time::interval(interval);
        tick.set_missed_tick_behavior(MissedTickBehavior::Delay);
        let run = Run::new(s.shared.clock.clone());
        loop {
            tokio::select! {
                () = s.cancel.cancelled() => return,
                _ = tick.tick() => {}
            }
            if run.busy() {
                s.shared.set(
                    id,
                    State::Hung,
                    "a run doesn't return; no new one starts until it does",
                );
                continue;
            }
            let budget = instance.lock().await.timeout().min(MAX_BUDGET);
            let (job, job_run) = (instance.clone(), run.clone());
            let handle = tokio::spawn(async move { job.lock().await.collect(&job_run).await });
            let abort = handle.abort_handle();
            let outcome = tokio::select! {
                () = s.cancel.cancelled() => {
                    abort.abort();
                    return;
                }
                outcome = tokio::time::timeout(budget, handle) => outcome,
            };
            let lines = run.take_logs();
            if !lines.is_empty() {
                s.shared.sink.logs(id, lines);
            }
            match outcome {
                Err(_) => {
                    abort.abort();
                    let why = format!("the run took longer than {} s", budget.as_secs());
                    s.shared.set(id, State::Timeout, &why);
                }
                Ok(Err(join)) if join.is_panic() => {
                    s.shared.set(
                        id,
                        State::Crashed,
                        &format!(
                            "the module crashed; it starts again in {} s",
                            pause.as_secs()
                        ),
                    );
                    tokio::select! {
                        () = s.cancel.cancelled() => return,
                        () = tokio::time::sleep(pause) => {}
                    }
                    pause = (pause * 2).min(s.pauses.max);
                    break;
                }
                Ok(Err(_)) => return,
                Ok(Ok(Err(RunError::Vetoed(r)))) => s.shared.set(id, State::Vetoed, &r.0),
                Ok(Ok(Err(RunError::Failed(why)))) => s.shared.set(id, State::Error, &why),
                Ok(Ok(Ok(samples))) => {
                    if let Err(why) = check(id, &samples) {
                        s.shared.set(id, State::Error, &why);
                    } else {
                        let at = s.shared.clock.now();
                        s.shared.sink.samples(samples, at);
                        s.shared.set(id, State::Ok, "");
                        pause = s.pauses.first;
                    }
                }
            }
        }
    }
}

/// Checks what a run returned against the protocol's bounds: a batch the server refuses would
/// hold up all data of the node.
fn check(id: &str, samples: &[Sample]) -> Result<(), String> {
    if samples.len() > MAX_SAMPLES {
        return Err(format!(
            "{} samples in a run, at most {MAX_SAMPLES}",
            samples.len()
        ));
    }
    let prefix = format!("sermon_{id}_");
    for s in samples {
        let name_ok = s.metric.starts_with(&prefix)
            && s.metric.len() <= 127
            && s.metric[prefix.len()..]
                .bytes()
                .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'_');
        if !name_ok {
            return Err(format!("{}: not a metric of the module", s.metric));
        }
        if s.labels.len() > MAX_LABELS {
            return Err(format!("{}: more than {MAX_LABELS} labels", s.metric));
        }
        for (name, value) in &s.labels {
            let mut bytes = name.bytes();
            let name_ok = name.len() <= 32
                && bytes.next().is_some_and(|b| b.is_ascii_lowercase())
                && bytes.all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'_');
            if !name_ok || value.len() > MAX_LABEL_VALUE || value.chars().any(char::is_control) {
                return Err(format!("{}: label {name} out of bounds", s.metric));
            }
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests;
