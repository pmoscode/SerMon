//! The modules of the agent (ADR 0039): the runtime of `sermon-modules` follows the server's
//! configuration; samples and states go to the outbox.

use std::collections::BTreeMap;
use std::sync::Arc;
use std::time::{Duration, SystemTime};

use sermon_core::Sample;
use sermon_modules::Secrets;
use sermon_modules::runtime::{Health, Runtime, Sink, State};
use sermon_transport::outbox::Outbox;
use sermon_transport::proto::agent::v1::{
    ModuleHealth, ModuleHealthReport, ModuleSecrets, ModuleState,
};
use sermon_transport::session::ModuleSecretsSink;
use tokio::sync::watch;
use tokio_util::sync::CancellationToken;

use crate::agent_config::Active;

/// How often the state of the modules goes out without a change (ADR 0039).
pub const REPORT_EVERY: Duration = Duration::from_mins(10);

/// Redacts a reason before it leaves the host.
pub type Redact = Arc<dyn Fn(&str) -> String + Send + Sync>;

/// The runtime's output into the outbox; reasons redacted like logs, log lines of modules
/// (M5-21) through a shipper of the log pipeline.
#[derive(Clone)]
pub struct OutboxSink {
    outbox: Outbox,
    redact: Redact,
    logs: Option<Arc<std::sync::Mutex<crate::logship::Shipper>>>,
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
        Self {
            outbox,
            redact,
            logs: None,
        }
    }

    /// Log lines of modules go through `shipper` (selection, rate, redaction of the log
    /// pipeline); without one they are dropped.
    #[must_use]
    pub fn with_logs(mut self, shipper: crate::logship::Shipper) -> Self {
        self.logs = Some(Arc::new(std::sync::Mutex::new(shipper)));
        self
    }
}

/// A module's log line as a record: a container's output (the only kind so far).
fn record(line: &sermon_modules::LogLine) -> sermon_logs::record::LogRecord {
    let at_us = line
        .at
        .duration_since(SystemTime::UNIX_EPOCH)
        .map_or(0, |d| u64::try_from(d.as_micros()).unwrap_or(u64::MAX));
    sermon_logs::record::LogRecord::from_container(at_us, &line.name, line.stderr, &line.message)
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

    fn logs(&self, _: &str, lines: Vec<sermon_modules::LogLine>) {
        let Some(shipper) = &self.logs else {
            return;
        };
        let mut shipper = shipper
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        let now = tokio::time::Instant::now();
        for line in &lines {
            shipper.offer(record(line), now);
        }
        // What the buffer refuses now waits in the shipper for the next lines.
        let _ = shipper.flush();
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

/// systemd for the modules (M5-10): the collector's `systemctl`, fixed arguments.
#[derive(Debug)]
pub struct Systemd(pub sermon_collectors::systemd::Systemctl);

impl sermon_modules::Systemd for Systemd {
    fn show(&self, units: &[String]) -> Result<String, String> {
        self.0.show(units).map_err(|e| format!("systemctl: {e}"))
    }
}

/// The credentials of the modules by module (ADR 0039).
pub type SecretMap = BTreeMap<String, Secrets>;

/// Most credentials a set may hold, longest module ID, field and value (agent.proto).
const MAX_SECRETS: usize = 64;
const MAX_MODULE_ID: usize = 32;
const MAX_FIELD: usize = 64;
const MAX_VALUE: usize = 4096;

/// The credentials of the modules as the server sent them (M5-05): in memory only, never in the
/// state directory, the WAL or a log; after a restart they come with the next session.
#[derive(Debug, Clone)]
pub struct SecretStore {
    current: watch::Sender<Arc<SecretMap>>,
}

impl Default for SecretStore {
    fn default() -> Self {
        Self {
            current: watch::Sender::new(Arc::new(SecretMap::new())),
        }
    }
}

impl SecretStore {
    /// Learns about every new set.
    #[must_use]
    pub fn subscribe(&self) -> watch::Receiver<Arc<SecretMap>> {
        self.current.subscribe()
    }
}

/// A name of the protocol: lower case, digits and `_`, a letter first.
fn valid_name(name: &str, max: usize) -> bool {
    let mut bytes = name.bytes();
    name.len() <= max
        && bytes.next().is_some_and(|b| b.is_ascii_lowercase())
        && bytes.all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'_')
}

/// The credentials of `secrets` by module, within the bounds of the protocol; the first value of
/// a field counts.
fn to_map(secrets: ModuleSecrets) -> SecretMap {
    let mut fields: BTreeMap<String, BTreeMap<String, String>> = BTreeMap::new();
    for s in secrets.secrets.into_iter().take(MAX_SECRETS) {
        if !valid_name(&s.module_id, MAX_MODULE_ID)
            || !valid_name(&s.field, MAX_FIELD)
            || s.value.is_empty()
            || s.value.len() > MAX_VALUE
        {
            continue;
        }
        fields
            .entry(s.module_id)
            .or_default()
            .entry(s.field)
            .or_insert(s.value);
    }
    fields
        .into_iter()
        .map(|(module, f)| (module, Secrets::new(f)))
        .collect()
}

impl ModuleSecretsSink for SecretStore {
    fn replace(&self, secrets: ModuleSecrets) {
        let next = to_map(secrets);
        // Only a change restarts the modules concerned.
        self.current.send_if_modified(|current| {
            if **current == next {
                return false;
            }
            *current = Arc::new(next);
            true
        });
    }
}

/// Hands the credentials of the modules from `transport` to the modules of `runtime`, which then
/// follow `config` (M5-05); the future runs them until `shutdown`.
pub fn start(
    runtime: Runtime,
    transport: sermon_transport::session::Transport,
    config: &crate::agent_config::ConfigStore,
    shutdown: &CancellationToken,
) -> (
    sermon_transport::session::Transport,
    impl Future<Output = ()> + use<>,
) {
    let secrets = Arc::new(SecretStore::default());
    let modules = run(
        runtime,
        config.subscribe(),
        secrets.subscribe(),
        shutdown.clone(),
    );
    (transport.with_module_secrets(secrets), modules)
}

/// Runs the modules of the configuration with their credentials until `shutdown`: a change of
/// either applies at once, the state goes out again every [`REPORT_EVERY`].
pub async fn run(
    mut runtime: Runtime,
    mut config: watch::Receiver<Arc<Active>>,
    mut secrets: watch::Receiver<Arc<SecretMap>>,
    shutdown: CancellationToken,
) {
    let first = config.borrow_and_update().clone();
    let mut current = secrets.borrow_and_update().clone();
    let mut secrets_open = true;
    runtime.apply(&first.modules, &current);
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
                runtime.apply(&active.modules, &current);
            }
            changed = secrets.changed(), if secrets_open => {
                if changed.is_err() {
                    // No more credentials will come (the transport is gone): keep these.
                    secrets_open = false;
                    continue;
                }
                current = secrets.borrow_and_update().clone();
                let active = config.borrow().clone();
                runtime.apply(&active.modules, &current);
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

    // M5-21: log lines of modules go through the log pipeline – selected, redacted, buffered –
    // and nowhere without it or with logs off.
    #[tokio::test]
    async fn the_sink_ships_log_lines_of_modules() {
        let dir = tempfile::tempdir().unwrap();
        let now = SystemTime::now();
        let (wal, _) = Wal::open(dir.path(), WalConfig::default(), now).unwrap();
        let outbox = Outbox::new(wal);
        let store = ConfigStore::open(dir.path().join("agent-config.pb"), true, true);
        let line = sermon_modules::LogLine {
            at: now,
            name: "web".into(),
            stderr: true,
            message: "login password=hunter2".into(),
        };
        let shipper = |store: &ConfigStore| {
            crate::logship::Shipper::new(
                store.subscribe(),
                Arc::default(),
                crate::logship::into_outbox(outbox.clone()),
            )
        };
        let off = OutboxSink::new(outbox.clone(), Arc::new(|r: &str| r.to_owned()))
            .with_logs(shipper(&store));
        off.logs("docker_socket", vec![line.clone()]);
        assert_eq!(
            outbox.stats().pending_records,
            0,
            "logs are off before the server says"
        );

        store.apply(ConfigUpdate {
            version: 2,
            config: Some(AgentConfig {
                logs: Some(LogConfig {
                    enabled: true,
                    ..LogConfig::default()
                }),
                ..AgentConfig::default()
            }),
        });
        let on = OutboxSink::new(outbox.clone(), Arc::new(|r: &str| r.to_owned()))
            .with_logs(shipper(&store));
        on.logs("docker_socket", vec![line.clone()]);
        assert_eq!(outbox.stats().pending_records, 1);
        let plain = OutboxSink::new(outbox.clone(), Arc::new(|r: &str| r.to_owned()));
        plain.logs("docker_socket", vec![line]);
        assert_eq!(outbox.stats().pending_records, 1, "no shipper, no logs");
        let record = record(&sermon_modules::LogLine {
            at: SystemTime::UNIX_EPOCH + Duration::from_secs(2),
            name: "web".into(),
            stderr: true,
            message: "x".into(),
        });
        assert_eq!(record.at_us, 2_000_000);
        assert_eq!(record.unit.as_deref(), Some("web"));
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
        let secrets = SecretStore::default();
        let task = tokio::spawn(run(
            runtime,
            store.subscribe(),
            secrets.subscribe(),
            shutdown.clone(),
        ));
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

    /// A module that reports the length of its credential `token`.
    struct Echo;

    struct EchoRun(i64);

    impl sermon_modules::Module for Echo {
        fn id(&self) -> &'static str {
            "echo"
        }
        fn schema(&self) -> &'static str {
            r#"{"type":"object","additionalProperties":false,"required":["token"],"properties":{"token":{"type":"string","writeOnly":true,"maxLength":64}}}"#
        }
        fn default_interval(&self) -> Duration {
            Duration::from_secs(10)
        }
        fn build(
            &self,
            _: &sermon_modules::Config,
            setup: &sermon_modules::Setup<'_>,
        ) -> Result<Box<dyn sermon_modules::Instance>, sermon_modules::BuildError> {
            let len = setup.secrets.get("token").map_or(0, str::len);
            Ok(Box::new(EchoRun(i64::try_from(len).unwrap_or(0))))
        }
    }

    impl sermon_modules::Instance for EchoRun {
        fn collect<'a>(
            &'a mut self,
            _: &'a sermon_modules::Run,
        ) -> sermon_modules::BoxFuture<'a, Result<Vec<Sample>, sermon_modules::RunError>> {
            let value = self.0;
            Box::pin(async move { Ok(vec![Sample::int("sermon_echo_token_len", value)]) })
        }
    }

    fn secrets(values: &[(&str, &str, &str)]) -> ModuleSecrets {
        use sermon_transport::proto::agent::v1::ModuleSecret;
        ModuleSecrets {
            secrets: values
                .iter()
                .map(|(m, f, v)| ModuleSecret {
                    module_id: (*m).into(),
                    field: (*f).into(),
                    value: (*v).into(),
                })
                .collect(),
        }
    }

    // The protocol's bounds: names of the protocol only, no empty or huge value, the first value
    // of a field counts; Debug shows the fields only.
    #[test]
    fn a_set_of_credentials_keeps_what_the_protocol_allows() {
        let long = "x".repeat(MAX_VALUE + 1);
        let map = to_map(secrets(&[
            ("postgresql", "password", "first-value"),
            ("postgresql", "password", "second-value"),
            ("postgresql", "user_name", "alice"),
            ("Bad", "password", "x"),
            ("redis", "Pass", "x"),
            ("redis", "password", ""),
            ("redis", "token", &long),
        ]));
        assert_eq!(map.keys().collect::<Vec<_>>(), ["postgresql"]);
        assert_eq!(map["postgresql"].get("password"), Some("first-value"));
        assert_eq!(map["postgresql"].get("user_name"), Some("alice"));
        let store = SecretStore::default();
        store.replace(secrets(&[("postgresql", "password", "first-value")]));
        let text = format!("{store:?} {:?}", store.subscribe().borrow());
        assert!(
            !text.contains("first-value") && text.contains("password"),
            "{text}"
        );
        let many: Vec<(String, String, String)> = (0..100)
            .map(|i| ("m".to_owned(), format!("f{i}"), "v".to_owned()))
            .collect();
        let many: Vec<(&str, &str, &str)> = many
            .iter()
            .map(|(a, b, c)| (a.as_str(), b.as_str(), c.as_str()))
            .collect();
        assert_eq!(to_map(secrets(&many))["m"].fields().len(), MAX_SECRETS);
    }

    /// Keeps the samples.
    #[derive(Default)]
    struct Samples(std::sync::Mutex<Vec<Sample>>);

    impl Sink for Samples {
        fn samples(&self, samples: Vec<Sample>, _: SystemTime) {
            self.0.lock().unwrap().extend(samples);
        }
        fn health(&self, _: Vec<Health>, _: SystemTime) {}
    }

    fn last_len(sink: &Samples) -> Option<sermon_core::Value> {
        sink.0.lock().unwrap().last().map(|s| s.value)
    }

    // M5-05: the module gets its credentials from the server's set; a new value (rotation)
    // restarts it with that one, an empty set takes them away – and none of it lands in the
    // state directory or the WAL.
    #[tokio::test(start_paused = true)]
    async fn the_modules_run_with_the_credentials_in_memory_only() {
        let dir = tempfile::tempdir().unwrap();
        let store = ConfigStore::open(dir.path().join("agent-config.pb"), true, true);
        let (wal, _) = Wal::open(
            &dir.path().join("wal"),
            WalConfig::default(),
            SystemTime::now(),
        )
        .unwrap();
        let outbox = Outbox::new(wal);
        let samples = Arc::new(Samples::default());
        let runtime = Runtime::new(
            vec![Arc::new(Echo)],
            Veto::default(),
            samples.clone(),
            Arc::new(SystemClock::new()),
        );
        let credentials = SecretStore::default();
        let shutdown = CancellationToken::new();
        let task = tokio::spawn(run(
            runtime,
            store.subscribe(),
            credentials.subscribe(),
            shutdown.clone(),
        ));
        store.apply(ConfigUpdate {
            version: 2,
            config: Some(AgentConfig {
                modules: vec![ModuleConfig {
                    id: "echo".into(),
                    enabled: true,
                    interval: None,
                    config_json: "{}".into(),
                }],
                logs: Some(LogConfig::default()),
                ..AgentConfig::default()
            }),
        });
        let settle = || async {
            for _ in 0..3 {
                tokio::time::sleep(Duration::from_secs(11)).await;
            }
        };
        settle().await;
        assert_eq!(
            last_len(&samples),
            None,
            "without its credential it doesn't run"
        );

        credentials.replace(secrets(&[("echo", "token", "old-token-value")]));
        settle().await;
        assert_eq!(last_len(&samples), Some(sermon_core::Value::Int(15)));
        credentials.replace(secrets(&[("echo", "token", "rotated")]));
        settle().await;
        assert_eq!(
            last_len(&samples),
            Some(sermon_core::Value::Int(7)),
            "rotated"
        );

        // What a real agent buffers goes through the outbox: its files hold no credential.
        let sink = OutboxSink::new(outbox.clone(), Arc::new(|r: &str| r.to_owned()));
        sink.samples(samples.0.lock().unwrap().clone(), SystemTime::now());
        outbox.maintain(SystemTime::now()).unwrap();
        for entry in walk(dir.path()) {
            let bytes = std::fs::read(&entry).unwrap();
            for secret in ["old-token-value", "rotated"] {
                assert!(
                    !bytes.windows(secret.len()).any(|w| w == secret.as_bytes()),
                    "{secret} in {}",
                    entry.display()
                );
            }
        }

        credentials.replace(ModuleSecrets::default());
        samples.0.lock().unwrap().clear();
        settle().await;
        assert_eq!(last_len(&samples), None, "taken away");
        shutdown.cancel();
        task.await.unwrap();
    }

    fn walk(dir: &std::path::Path) -> Vec<std::path::PathBuf> {
        let mut out = Vec::new();
        for entry in std::fs::read_dir(dir).unwrap() {
            let path = entry.unwrap().path();
            if path.is_dir() {
                out.extend(walk(&path));
            } else {
                out.push(path);
            }
        }
        out
    }
}
