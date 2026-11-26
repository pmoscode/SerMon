//! The modules of the agent (ADR 0039): the runtime of `sermon-modules` follows the server's
//! configuration; samples and states go to the outbox.

use std::collections::BTreeMap;
use std::sync::Arc;
use std::time::{Duration, SystemTime};

use sermon_core::Sample;
use sermon_modules::runtime::{Health, Runtime, Sink, State};
use sermon_transport::outbox::Outbox;
use sermon_transport::proto::agent::v1::{ModuleHealth, ModuleHealthReport, ModuleState};
use tokio::sync::watch;
use tokio_util::sync::CancellationToken;

use crate::agent_config::Active;

/// How often the state of the modules goes out without a change (ADR 0039).
pub const REPORT_EVERY: Duration = Duration::from_mins(10);

/// Redacts a reason before it leaves the host.
pub type Redact = Arc<dyn Fn(&str) -> String + Send + Sync>;

/// The runtime's output into the outbox; reasons redacted like logs.
#[derive(Clone)]
pub struct OutboxSink {
    outbox: Outbox,
    redact: Redact,
}

impl std::fmt::Debug for OutboxSink {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("OutboxSink").finish_non_exhaustive()
    }
}

impl OutboxSink {
    /// A sink into `outbox`.
    #[must_use]
    pub fn new(outbox: Outbox, redact: Redact) -> Self {
        Self { outbox, redact }
    }
}

impl Sink for OutboxSink {
    fn samples(&self, samples: Vec<Sample>, at: SystemTime) {
        if let Err(error) = self.outbox.push_samples(&samples, at) {
            tracing::warn!("cannot buffer the samples of a module: {error}");
        }
    }

    fn health(&self, report: Vec<Health>, at: SystemTime) {
        let report = to_report(&report, at, self.redact.as_ref());
        if let Err(error) = self.outbox.push_module_health(report, at) {
            tracing::warn!("cannot buffer the state of the modules: {error}");
        }
    }
}

/// The protocol's report of `health`.
fn to_report(
    health: &[Health],
    at: SystemTime,
    redact: &dyn Fn(&str) -> String,
) -> ModuleHealthReport {
    ModuleHealthReport {
        collected_at: Some(at.into()),
        modules: health
            .iter()
            .map(|h| ModuleHealth {
                module_id: h.module.clone(),
                state: state(h.state).into(),
                reason: cut(redact(&h.reason), 256),
                last_success: h.last_success.map(Into::into),
            })
            .collect(),
    }
}

fn state(state: State) -> ModuleState {
    match state {
        State::Ok => ModuleState::Ok,
        State::Error => ModuleState::Error,
        State::Timeout => ModuleState::Timeout,
        State::Hung => ModuleState::Hung,
        State::Crashed => ModuleState::Crashed,
        State::InvalidConfig => ModuleState::InvalidConfig,
        State::Vetoed => ModuleState::Vetoed,
    }
}

/// `text` cut to `max` bytes at a character boundary: redaction may make it longer.
fn cut(mut text: String, max: usize) -> String {
    let mut end = text.len().min(max);
    while !text.is_char_boundary(end) {
        end -= 1;
    }
    text.truncate(end);
    text
}

/// Runs the modules of the configuration until `shutdown`: a change applies at once, the state
/// goes out again every [`REPORT_EVERY`].
pub async fn run(
    mut runtime: Runtime,
    mut config: watch::Receiver<Arc<Active>>,
    shutdown: CancellationToken,
) {
    // The credentials come with M5-05.
    let secrets = BTreeMap::new();
    let first = config.borrow_and_update().clone();
    runtime.apply(&first.modules, &secrets);
    let mut report =
        tokio::time::interval_at(tokio::time::Instant::now() + REPORT_EVERY, REPORT_EVERY);
    loop {
        tokio::select! {
            () = shutdown.cancelled() => break,
            changed = config.changed() => {
                if changed.is_err() {
                    break;
                }
                let active = config.borrow_and_update().clone();
                runtime.apply(&active.modules, &secrets);
            }
            _ = report.tick() => runtime.report(),
        }
    }
    runtime.stop();
}

#[cfg(test)]
mod tests {
    use sermon_buffer::{Wal, WalConfig};
    use sermon_core::SystemClock;
    use sermon_modules::Veto;
    use sermon_transport::proto::agent::v1::{AgentConfig, ConfigUpdate, LogConfig, ModuleConfig};
    use sermon_transport::session::ConfigSink;

    use super::*;
    use crate::agent_config::ConfigStore;

    #[test]
    fn the_report_names_every_state_and_redacts_the_reason() {
        let at = SystemTime::UNIX_EPOCH + Duration::from_hours(1);
        let health = [
            Health {
                module: "nginx".into(),
                state: State::Error,
                reason: "login as alice with secret-x".into(),
                last_success: Some(at),
            },
            Health {
                module: "redis".into(),
                state: State::Vetoed,
                reason: "ä".repeat(200),
                last_success: None,
            },
        ];
        let report = to_report(&health, at, &|r: &str| r.replace("secret-x", "***"));
        assert_eq!(report.collected_at, Some(at.into()));
        assert_eq!(report.modules[0].module_id, "nginx");
        assert_eq!(report.modules[0].state(), ModuleState::Error);
        assert_eq!(report.modules[0].reason, "login as alice with ***");
        assert_eq!(report.modules[0].last_success, Some(at.into()));
        assert_eq!(report.modules[1].state(), ModuleState::Vetoed);
        assert!(report.modules[1].reason.len() <= 256);
        assert_eq!(report.modules[1].last_success, None);
        for (s, p) in [
            (State::Ok, ModuleState::Ok),
            (State::Timeout, ModuleState::Timeout),
            (State::Hung, ModuleState::Hung),
            (State::Crashed, ModuleState::Crashed),
            (State::InvalidConfig, ModuleState::InvalidConfig),
        ] {
            assert_eq!(state(s), p);
        }
    }

    #[derive(Default)]
    struct Reports(std::sync::Mutex<Vec<Vec<Health>>>);

    impl Sink for Reports {
        fn samples(&self, _: Vec<Sample>, _: SystemTime) {}
        fn health(&self, report: Vec<Health>, _: SystemTime) {
            self.0.lock().unwrap().push(report);
        }
    }

    impl Reports {
        fn all(&self) -> Vec<Vec<Health>> {
            self.0.lock().unwrap().clone()
        }
    }

    // The sink buffers both, a batch each.
    #[test]
    fn the_sink_buffers_samples_and_states() {
        let dir = tempfile::tempdir().unwrap();
        let now = SystemTime::now();
        let (wal, _) = Wal::open(dir.path(), WalConfig::default(), now).unwrap();
        let outbox = Outbox::new(wal);
        let sink = OutboxSink::new(outbox.clone(), Arc::new(|r: &str| r.to_owned()));
        sink.samples(vec![Sample::int("sermon_nginx_up", 1)], now);
        sink.health(Vec::new(), now);
        assert_eq!(outbox.stats().pending_records, 2);
    }

    // ADR 0039: the runtime follows the server's configuration; a module this agent doesn't have
    // is reported invalid, and the report goes out again every 10 min.
    #[tokio::test(start_paused = true)]
    async fn follows_the_configuration() {
        let dir = tempfile::tempdir().unwrap();
        let store = ConfigStore::open(dir.path().join("agent-config.pb"), true, true);
        let sink = Arc::new(Reports::default());
        let runtime = Runtime::new(
            Vec::new(),
            Veto::default(),
            sink.clone(),
            Arc::new(SystemClock::new()),
        );
        let shutdown = CancellationToken::new();
        let task = tokio::spawn(run(runtime, store.subscribe(), shutdown.clone()));
        tokio::time::sleep(Duration::from_secs(1)).await;
        assert!(sink.all().is_empty(), "no modules, no report");

        store.apply(ConfigUpdate {
            version: 2,
            config: Some(AgentConfig {
                modules: vec![ModuleConfig {
                    id: "nginx".into(),
                    enabled: true,
                    interval: None,
                    config_json: "{}".into(),
                }],
                logs: Some(LogConfig::default()),
                ..AgentConfig::default()
            }),
        });
        tokio::time::sleep(Duration::from_secs(1)).await;
        let reports = sink.all();
        assert_eq!(reports.len(), 1);
        assert_eq!(reports[0][0].module, "nginx");
        assert_eq!(reports[0][0].state, State::InvalidConfig);

        tokio::time::sleep(REPORT_EVERY).await;
        assert_eq!(sink.all().len(), 2, "again after 10 min");

        store.apply(ConfigUpdate {
            version: 3,
            config: Some(AgentConfig {
                logs: Some(LogConfig::default()),
                ..AgentConfig::default()
            }),
        });
        tokio::time::sleep(Duration::from_secs(1)).await;
        let reports = sink.all();
        assert_eq!(reports.len(), 3);
        assert!(reports[2].is_empty(), "the module is gone");
        tokio::time::sleep(REPORT_EVERY).await;
        assert_eq!(sink.all().len(), 3, "nothing to report");

        shutdown.cancel();
        task.await.unwrap();
    }
}
