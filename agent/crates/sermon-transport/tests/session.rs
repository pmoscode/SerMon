//! The transport against an in-process fake agent API (mTLS): Hello, heartbeats, batches, acks,
//! in-flight limit, reconnects without data loss, throttling, trust bundle and rotation.

// Test code: panicking on setup failures is the desired behaviour.
#![allow(clippy::unwrap_used, clippy::expect_used, clippy::panic)]

use std::sync::{Arc, Mutex};
use std::time::{Duration, SystemTime};

use proptest::prelude::*;
use rustls::pki_types::CertificateDer;
use sermon_buffer::{Seq, Wal, WalConfig};
use sermon_core::{Architecture, HostFacts, MachineId, SystemClock};
use sermon_testserver::{
    AckMode, FakeAgentApi, NODE_ID, ORG_ID, RunningServer, TestPki, mtls_server_config, serve_tls,
};
use sermon_transport::identity::{AgentIdentity, AgentKey, Credentials, IssuedCertificate};
use sermon_transport::outbox::Outbox;
use sermon_transport::proto::agent::v1::{
    Ack, AgentConfig, ConfigApplied, ConfigUpdate, LogConfig, MetricBatch, ProcessSnapshot,
    RotateNow, Sample, StartLiveView, StopLiveView, SystemSummary, Throttle, sample,
    session_response::Payload,
};
use sermon_transport::session::{
    BoxError, ConfigSink, IdentitySource, LiveSource, Transport, TransportConfig,
};
use tokio_util::sync::CancellationToken;

const WAIT: Duration = Duration::from_secs(10);

fn facts() -> HostFacts {
    HostFacts {
        hostname: "web-01".into(),
        machine_id: MachineId::parse("0123456789abcdef0123456789abcdef").unwrap(),
        os_id: "debian".into(),
        os_version: "13".into(),
        kernel_release: "6.12.0".into(),
        architecture: Architecture::Arm64,
        cpu_count: 2,
        memory_bytes: 1 << 30,
        security_source: sermon_core::SecuritySource::Journal,
    }
}

fn metrics(value: i64) -> MetricBatch {
    MetricBatch {
        collected_at: Some(SystemTime::now().into()),
        samples: vec![Sample {
            metric: "sermon_load_1m".into(),
            labels: std::collections::HashMap::default(),
            value: Some(sample::Value::IntValue(value)),
        }],
    }
}

/// Credentials as files would hold them; replaced on rotation like the real store.
#[derive(Debug)]
struct Stored {
    key_pem: String,
    certificate: CertificateDer<'static>,
    intermediates: Vec<CertificateDer<'static>>,
    trust_anchors: Vec<CertificateDer<'static>>,
    not_after: SystemTime,
    issued_at: SystemTime,
}

#[derive(Debug)]
struct FakeIdentity(Mutex<Stored>);

impl FakeIdentity {
    fn issued_by(pki: &TestPki, issued_at: SystemTime, not_after: SystemTime) -> Self {
        let key = AgentKey::generate().unwrap();
        Self(Mutex::new(Stored {
            certificate: pki.sign_agent_csr(&key.csr_der().unwrap(), ORG_ID, NODE_ID),
            key_pem: key.to_pem(),
            intermediates: vec![pki.intermediate()],
            trust_anchors: vec![pki.root()],
            not_after,
            issued_at,
        }))
    }

    fn certificate(&self) -> CertificateDer<'static> {
        self.0.lock().unwrap().certificate.clone()
    }

    fn trust_anchors(&self) -> Vec<CertificateDer<'static>> {
        self.0.lock().unwrap().trust_anchors.clone()
    }
}

impl IdentitySource for FakeIdentity {
    fn load(&self) -> Result<Credentials, BoxError> {
        let stored = self.0.lock().unwrap();
        Ok(Credentials {
            key: AgentKey::from_pem(&stored.key_pem)?,
            identity: AgentIdentity {
                org_id: ORG_ID.into(),
                node_id: NODE_ID.into(),
            },
            certificate: stored.certificate.clone(),
            intermediates: stored.intermediates.clone(),
            trust_anchors: stored.trust_anchors.clone(),
            not_after: stored.not_after,
            issued_at: Some(stored.issued_at),
        })
    }

    fn store_rotated(
        &self,
        key: &AgentKey,
        issued: &IssuedCertificate,
        issued_at: SystemTime,
    ) -> Result<(), BoxError> {
        let mut stored = self.0.lock().unwrap();
        stored.key_pem = key.to_pem();
        stored.certificate = issued.certificate.clone();
        stored.intermediates.clone_from(&issued.intermediates);
        stored.not_after = issued.not_after;
        stored.issued_at = issued_at;
        Ok(())
    }

    fn store_trust_bundle(&self, roots: &[CertificateDer<'static>]) -> Result<(), BoxError> {
        self.0.lock().unwrap().trust_anchors = roots.to_vec();
        Ok(())
    }
}

struct Harness {
    api: FakeAgentApi,
    identity: Arc<FakeIdentity>,
    outbox: Outbox,
    shutdown: CancellationToken,
    task: tokio::task::JoinHandle<()>,
    _server: RunningServer,
    _dir: tempfile::TempDir,
}

struct Setup {
    pki: Arc<TestPki>,
    identity: Option<FakeIdentity>,
    server_pki: Option<TestPki>,
    tweak: fn(&mut TransportConfig),
    api: Option<FakeAgentApi>,
    config: Option<Arc<dyn ConfigSink>>,
    live: Option<Arc<FakeLive>>,
    refresh: Option<Arc<tokio::sync::Notify>>,
}

impl Setup {
    fn new() -> Self {
        Self {
            pki: Arc::new(TestPki::new("Agent")),
            identity: None,
            server_pki: None,
            tweak: |_| {},
            api: None,
            config: None,
            live: None,
            refresh: None,
        }
    }

    async fn start(self) -> Harness {
        let _ = tracing_subscriber::fmt().with_test_writer().try_init();
        let now = SystemTime::now();
        let identity = Arc::new(self.identity.unwrap_or_else(|| {
            FakeIdentity::issued_by(&self.pki, now, now + Duration::from_hours(720))
        }));
        let api = self
            .api
            .unwrap_or_else(|| FakeAgentApi::new(self.pki.clone()));
        let (chain, key) = self
            .server_pki
            .as_ref()
            .unwrap_or(&self.pki)
            .server_identity(&["localhost"]);
        let server = serve_tls(
            api.routes(),
            mtls_server_config(chain, key, &[self.pki.root()]),
        )
        .await;

        let dir = tempfile::tempdir().unwrap();
        let (wal, _) = Wal::open(dir.path(), WalConfig::default(), now).unwrap();
        let outbox = Outbox::new(wal);
        let mut config = TransportConfig::new(server.https_url().parse().unwrap(), "1.2.3");
        config.heartbeat_interval = Duration::from_millis(50);
        config.reconnect_initial = Duration::from_millis(5);
        config.reconnect_max = Duration::from_millis(50);
        config.expired_retry = Duration::from_millis(50);
        (self.tweak)(&mut config);
        let mut transport = Transport::new(
            config,
            identity.clone(),
            Arc::new(|| Ok(facts())),
            outbox.clone(),
            Arc::new(SystemClock::new()),
        );
        if let Some(sink) = self.config {
            transport = transport.with_config(sink);
        }
        if let Some(live) = self.live {
            transport = transport.with_live(live);
        }
        if let Some(refresh) = self.refresh {
            transport = transport.with_update_refresh(refresh);
        }
        let shutdown = CancellationToken::new();
        let token = shutdown.clone();
        let task = tokio::spawn(async move { transport.run(token).await });
        Harness {
            api,
            identity,
            outbox,
            shutdown,
            task,
            _server: server,
            _dir: dir,
        }
    }
}

impl Harness {
    fn push(&self, count: usize) -> Vec<u64> {
        (0..count)
            .map(|i| {
                self.outbox
                    .push_metrics(metrics(i64::try_from(i).unwrap()), SystemTime::now())
                    .unwrap()
                    .get()
            })
            .collect()
    }

    async fn wait_until(&self, condition: impl Fn(&Self) -> bool) -> bool {
        let deadline = tokio::time::Instant::now() + WAIT;
        while tokio::time::Instant::now() < deadline {
            if condition(self) {
                return true;
            }
            tokio::time::sleep(Duration::from_millis(5)).await;
        }
        false
    }

    async fn stop(self) {
        self.shutdown.cancel();
        tokio::time::timeout(Duration::from_secs(5), self.task)
            .await
            .expect("transport did not stop")
            .unwrap();
    }
}

#[tokio::test(flavor = "multi_thread")]
async fn connects_with_mtls_sends_hello_and_heartbeats() {
    let harness = Setup::new().start().await;

    assert!(
        harness
            .api
            .wait_for(WAIT, |s| s.first().is_some_and(|s| s.heartbeats >= 2))
            .await
    );

    let session = &harness.api.sessions()[0];
    let hello = session.hello.clone().unwrap();
    assert_eq!(hello.agent_version, "1.2.3");
    assert_eq!(hello.protocol_version, 1);
    assert_eq!(hello.host_facts.unwrap().hostname, "web-01");
    assert_eq!(session.client_cert, Some(harness.identity.certificate()));
    harness.stop().await;
}

#[tokio::test(flavor = "multi_thread")]
async fn sends_buffered_batches_in_order_and_forgets_them_after_the_ack() {
    let setup = Setup::new();
    let harness = setup.start().await;
    let mut pushed = harness.push(5);
    assert!(harness.api.wait_for(WAIT, |s| !s.is_empty()).await);
    pushed.extend(harness.push(5));

    assert!(harness.wait_until(|h| h.api.received().len() == 10).await);
    assert_eq!(harness.api.received(), pushed);
    assert!(
        harness
            .wait_until(|h| h.outbox.stats().pending_records == 0)
            .await
    );
    assert_eq!(harness.outbox.stats().acked, Seq::new(pushed[9]));
    harness.stop().await;
}

#[tokio::test(flavor = "multi_thread")]
async fn respects_the_in_flight_limit_until_acknowledged() {
    let mut setup = Setup::new();
    setup.tweak = |config| config.max_in_flight = 3;
    let api = FakeAgentApi::new(setup.pki.clone());
    api.set_ack(AckMode::Manual);
    setup.api = Some(api);
    let harness = setup.start().await;
    let pushed = harness.push(10);

    assert!(
        harness
            .api
            .wait_for(WAIT, |s| s.first().is_some_and(|s| s.batches.len() == 3))
            .await
    );
    tokio::time::sleep(Duration::from_millis(100)).await;
    assert_eq!(
        harness.api.sessions()[0].batches.len(),
        3,
        "more than 3 in flight"
    );

    assert!(harness.api.send(Payload::Ack(Ack { seq: pushed[2] })).await);

    assert!(
        harness
            .api
            .wait_for(WAIT, |s| s[0].batches.len() == 6)
            .await
    );
    assert_eq!(harness.api.sessions()[0].batches, pushed[..6]);
    harness.stop().await;
}

#[tokio::test(flavor = "multi_thread")]
async fn reconnects_and_resends_only_what_was_not_acknowledged() {
    let setup = Setup::new();
    let api = FakeAgentApi::new(setup.pki.clone());
    api.cut_sessions([Some(2), None]);
    let mut setup = setup;
    setup.api = Some(api);
    let harness = setup.start().await;
    let pushed = harness.push(4);

    assert!(
        harness
            .wait_until(|h| h.outbox.stats().pending_records == 0)
            .await
    );

    let sessions = harness.api.sessions();
    assert!(sessions.len() >= 2, "{sessions:?}");
    assert_eq!(sessions[0].batches[..2], pushed[..2]);
    assert!(sessions[0].ended);
    // The first two were acknowledged before the cut: the next session starts after them.
    assert_eq!(sessions[1].batches.first(), Some(&pushed[2]));
    assert_eq!(harness.api.received(), pushed);
    harness.stop().await;
}

#[tokio::test(flavor = "multi_thread")]
async fn stores_a_new_trust_bundle() {
    let setup = Setup::new();
    let next_root = TestPki::new("Next").root();
    let api = FakeAgentApi::new(setup.pki.clone());
    api.set_trust_roots(vec![setup.pki.root().to_vec(), next_root.to_vec()]);
    let mut setup = setup;
    setup.api = Some(api);
    let harness = setup.start().await;

    assert!(
        harness
            .wait_until(|h| h.identity.trust_anchors().len() == 2)
            .await
    );
    assert_eq!(harness.identity.trust_anchors()[1], next_root);
    harness.stop().await;
}

#[tokio::test(flavor = "multi_thread")]
async fn rotates_when_due_and_reconnects_with_the_new_certificate() {
    let mut setup = Setup::new();
    let now = SystemTime::now();
    // 25 of 30 days are over: past two thirds of the lifetime.
    setup.identity = Some(FakeIdentity::issued_by(
        &setup.pki,
        now - Duration::from_hours(600),
        now + Duration::from_hours(120),
    ));
    let harness = setup.start().await;
    let old = harness.identity.certificate();

    assert!(
        harness
            .api
            .wait_for(WAIT, |s| s.len() >= 2 && s[1].hello.is_some())
            .await
    );

    let sessions = harness.api.sessions();
    assert_eq!(sessions[0].rotations, 1);
    assert_eq!(sessions[0].client_cert, Some(old.clone()));
    let new = harness.identity.certificate();
    assert_ne!(new, old);
    assert_eq!(sessions[1].client_cert, Some(new));
    tokio::time::sleep(Duration::from_millis(150)).await;
    assert_eq!(harness.api.sessions()[1].rotations, 0, "rotated again");
    harness.stop().await;
}

#[tokio::test(flavor = "multi_thread")]
async fn rotates_on_demand() {
    let setup = Setup::new();
    let api = FakeAgentApi::new(setup.pki.clone());
    api.end_after_rotation(true);
    let mut setup = setup;
    setup.api = Some(api);
    let harness = setup.start().await;
    let old = harness.identity.certificate();
    assert!(
        harness
            .api
            .wait_for(WAIT, |s| s.first().is_some_and(|s| s.hello.is_some()))
            .await
    );

    assert!(harness.api.send(Payload::RotateNow(RotateNow {})).await);

    assert!(
        harness
            .api
            .wait_for(WAIT, |s| s.len() >= 2 && s[1].hello.is_some())
            .await
    );
    let sessions = harness.api.sessions();
    assert_eq!(sessions[0].rotations, 1);
    assert_ne!(sessions[1].client_cert, Some(old));
    harness.stop().await;
}

#[tokio::test(flavor = "multi_thread")]
async fn slows_down_when_throttled_and_speeds_up_when_lifted() {
    let harness = Setup::new().start().await;
    assert!(
        harness
            .api
            .wait_for(WAIT, |s| s.first().is_some_and(|s| s.hello.is_some()))
            .await
    );
    let throttle = |millis| {
        Payload::Throttle(Throttle {
            min_interval: Some(Duration::from_millis(millis).try_into().unwrap()),
        })
    };
    assert!(harness.api.send(throttle(150)).await);
    tokio::time::sleep(Duration::from_millis(50)).await;

    harness.push(4);

    assert!(harness.wait_until(|h| h.api.received().len() == 4).await);
    let times = harness.api.sessions()[0].batch_times.clone();
    for pair in times.windows(2) {
        assert!(
            pair[1] - pair[0] >= Duration::from_millis(140),
            "{:?}",
            pair[1] - pair[0]
        );
    }

    assert!(harness.api.send(throttle(0)).await);
    tokio::time::sleep(Duration::from_millis(200)).await;
    let start = std::time::Instant::now();
    harness.push(4);
    assert!(harness.wait_until(|h| h.api.received().len() == 8).await);
    assert!(start.elapsed() < Duration::from_millis(400));
    harness.stop().await;
}

#[tokio::test(flavor = "multi_thread")]
async fn does_not_connect_with_an_expired_certificate() {
    let mut setup = Setup::new();
    let now = SystemTime::now();
    setup.identity = Some(FakeIdentity::issued_by(
        &setup.pki,
        now - Duration::from_hours(800),
        now - Duration::from_secs(1),
    ));
    let harness = setup.start().await;

    tokio::time::sleep(Duration::from_millis(300)).await;

    assert_eq!(harness.api.sessions().len(), 0);
    harness.stop().await;
}

#[tokio::test(flavor = "multi_thread")]
async fn refuses_a_server_outside_the_trust_bundle() {
    let mut setup = Setup::new();
    setup.server_pki = Some(TestPki::new("Impostor"));
    let harness = setup.start().await;
    harness.push(1);

    tokio::time::sleep(Duration::from_millis(300)).await;

    assert_eq!(harness.api.sessions().len(), 0);
    assert_eq!(harness.outbox.stats().pending_records, 1);
    harness.stop().await;
}

proptest! {
    #![proptest_config(ProptestConfig::with_cases(12))]

    /// Whatever the pattern of broken connections: every buffered batch reaches the server,
    /// and in the end nothing is pending.
    #[test]
    fn loses_nothing_across_random_disconnects(
        cuts in proptest::collection::vec(proptest::option::of(1..6_usize), 0..6),
        rounds in proptest::collection::vec(1..6_usize, 1..5),
    ) {
        let runtime = tokio::runtime::Builder::new_multi_thread().enable_all().build().unwrap();
        runtime.block_on(async {
            let setup = Setup::new();
            let api = FakeAgentApi::new(setup.pki.clone());
            api.cut_sessions(cuts.clone());
            let mut setup = setup;
            setup.api = Some(api);
            let harness = setup.start().await;
            let mut pushed = Vec::new();
            for count in &rounds {
                pushed.extend(harness.push(*count));
                tokio::time::sleep(Duration::from_millis(10)).await;
            }

            assert!(harness.wait_until(|h| h.outbox.stats().pending_records == 0).await,
                "pending {:?}, received {:?}", harness.outbox.stats(), harness.api.received());
            assert_eq!(harness.api.received(), pushed);
            harness.stop().await;
        });
    }
}

/// A configuration sink that takes every version but 13, which it rejects.
#[derive(Debug, Default)]
struct FakeConfig {
    version: Mutex<u64>,
    seen: Mutex<Vec<u64>>,
}

impl ConfigSink for FakeConfig {
    fn version(&self) -> u64 {
        *self.version.lock().unwrap()
    }

    fn apply(&self, update: ConfigUpdate) -> ConfigApplied {
        self.seen.lock().unwrap().push(update.version);
        if update.version == 13 {
            return ConfigApplied {
                version: 13,
                error: "rule 1: look-around is not supported".into(),
                logs_vetoed: true,
            };
        }
        *self.version.lock().unwrap() = update.version;
        ConfigApplied {
            version: update.version,
            error: String::new(),
            logs_vetoed: true,
        }
    }
}

fn config_update(version: u64) -> Payload {
    Payload::ConfigUpdate(ConfigUpdate {
        version,
        config: Some(AgentConfig {
            logs: Some(LogConfig {
                enabled: true,
                ..LogConfig::default()
            }),
            redaction_rules: Vec::new(),
        }),
    })
}

// ADR 0030: the Hello names the version in use, every ConfigUpdate goes to the sink and its
// answer back to the server; after a reconnect the Hello names the new version.
#[tokio::test(flavor = "multi_thread")]
async fn hands_configurations_to_the_sink_and_answers_the_server() {
    let sink = Arc::new(FakeConfig::default());
    *sink.version.lock().unwrap() = 4;
    let mut setup = Setup::new();
    let api = FakeAgentApi::new(setup.pki.clone());
    api.end_after_rotation(true);
    setup.api = Some(api);
    setup.config = Some(sink.clone());
    let harness = setup.start().await;
    assert!(
        harness
            .api
            .wait_for(WAIT, |s| s.first().is_some_and(|s| s.hello.is_some()))
            .await
    );
    assert_eq!(
        harness.api.sessions()[0]
            .hello
            .clone()
            .unwrap()
            .config_version,
        4
    );

    assert!(harness.api.send(config_update(5)).await);
    assert!(harness.api.send(config_update(13)).await);
    assert!(
        harness
            .api
            .wait_for(WAIT, |s| s
                .first()
                .is_some_and(|s| s.config_applied.len() == 2))
            .await
    );
    let answers = harness.api.sessions()[0].config_applied.clone();
    assert_eq!((answers[0].version, answers[0].error.as_str()), (5, ""));
    assert_eq!(answers[1].version, 13);
    assert!(answers[1].error.contains("look-around") && answers[1].logs_vetoed);
    assert_eq!(*sink.seen.lock().unwrap(), [5, 13]);

    // The next session (here after a rotation) starts with the version taken, not the rejected.
    assert!(harness.api.send(Payload::RotateNow(RotateNow {})).await);
    assert!(
        harness
            .api
            .wait_for(WAIT, |s| s.len() >= 2 && s[1].hello.is_some())
            .await
    );
    assert_eq!(
        harness.api.sessions()[1]
            .hello
            .clone()
            .unwrap()
            .config_version,
        5
    );
    harness.stop().await;
}

// M4-07: RefreshUpdateInfo asks the update collection for a report with fresh sources.
#[tokio::test(flavor = "multi_thread")]
async fn passes_a_refresh_of_the_update_info_on() {
    use sermon_transport::proto::agent::v1::RefreshUpdateInfo;
    let refresh = Arc::new(tokio::sync::Notify::new());
    let mut setup = Setup::new();
    setup.refresh = Some(refresh.clone());
    let harness = setup.start().await;
    assert!(
        harness
            .api
            .wait_for(WAIT, |s| s.first().is_some_and(|s| s.hello.is_some()))
            .await
    );
    let asked = refresh.notified();
    assert!(
        harness
            .api
            .send(Payload::RefreshUpdateInfo(RefreshUpdateInfo {}))
            .await
    );
    tokio::time::timeout(WAIT, asked)
        .await
        .expect("no refresh requested");
    harness.stop().await;
}

// M4-05: the commands of the root helper are part of the protocol before this agent handles
// them (M4-12): ignored like a newer server's, the session goes on. Without an update
// collection a refresh is ignored too.
#[tokio::test(flavor = "multi_thread")]
async fn ignores_the_commands_it_does_not_handle_yet() {
    use sermon_transport::proto::agent::v1::{
        RebootNode, RefreshUpdateInfo, RunUpdate, SelfUpdate, UpdateScope,
    };
    let sink = Arc::new(FakeConfig::default());
    let mut setup = Setup::new();
    setup.config = Some(sink.clone());
    let harness = setup.start().await;
    assert!(
        harness
            .api
            .wait_for(WAIT, |s| s.first().is_some_and(|s| s.hello.is_some()))
            .await
    );
    let run_id = "0193f5a0-0000-7000-8000-000000000001".to_owned();
    for command in [
        Payload::RefreshUpdateInfo(RefreshUpdateInfo {}),
        Payload::RunUpdate(RunUpdate {
            run_id: run_id.clone(),
            scope: UpdateScope::Security.into(),
        }),
        Payload::RebootNode(RebootNode {
            run_id: run_id.clone(),
            reason: "updates".into(),
        }),
        Payload::SelfUpdate(SelfUpdate {
            run_id,
            version: "0.5.0".into(),
        }),
    ] {
        assert!(harness.api.send(command).await);
    }
    assert!(harness.api.send(config_update(5)).await);
    assert!(
        harness
            .api
            .wait_for(WAIT, |s| s
                .first()
                .is_some_and(|s| s.config_applied.len() == 1))
            .await
    );
    assert_eq!(harness.api.sessions().len(), 1, "the session ended");
    harness.stop().await;
}

/// A live source of fixed snapshots, or one that refuses.
#[derive(Debug, Default)]
struct FakeLive {
    refuse: Option<String>,
    taken: Mutex<Vec<bool>>,
    stopped: Mutex<usize>,
}

impl LiveSource for FakeLive {
    fn refusal(&self) -> Option<String> {
        self.refuse.clone()
    }

    fn snapshot(&self, hide_cmdline: bool) -> Option<ProcessSnapshot> {
        self.taken.lock().unwrap().push(hide_cmdline);
        Some(ProcessSnapshot {
            taken_at: Some(SystemTime::now().into()),
            summary: Some(SystemSummary::default()),
            processes: Vec::new(),
            omitted: 0,
        })
    }

    fn stopped(&self) {
        *self.stopped.lock().unwrap() += 1;
    }
}

fn start_live(interval_ms: u64, hide_cmdline: bool) -> Payload {
    Payload::StartLiveView(StartLiveView {
        interval: Some(Duration::from_millis(interval_ms).try_into().unwrap()),
        hide_cmdline,
    })
}

fn snapshots(harness: &Harness) -> usize {
    harness
        .api
        .sessions()
        .iter()
        .map(|s| s.snapshots.len())
        .sum()
}

// KONZEPT §9, M3-13: a StartLiveView makes snapshots every interval, sent at once (not buffered),
// until StopLiveView; renewing takes the new settings.
#[tokio::test(flavor = "multi_thread")]
async fn sends_snapshots_while_the_live_view_runs() {
    let live = Arc::new(FakeLive::default());
    let mut setup = Setup::new();
    setup.live = Some(live.clone());
    let harness = setup.start().await;
    assert!(
        harness
            .api
            .wait_for(WAIT, |s| s.first().is_some_and(|s| s.hello.is_some()))
            .await
    );
    tokio::time::sleep(Duration::from_millis(200)).await;
    assert_eq!(snapshots(&harness), 0, "nothing before a start");

    // Below the bound of 1 s the agent takes 1 s.
    assert!(harness.api.send(start_live(10, false)).await);
    assert!(harness.wait_until(|h| snapshots(h) >= 2).await);
    let times: Vec<_> = harness.api.sessions()[0]
        .snapshots
        .iter()
        .map(|(t, _)| *t)
        .collect();
    assert!(
        times[1].duration_since(times[0]) >= Duration::from_millis(900),
        "{:?}",
        times[1].duration_since(times[0])
    );
    let before = harness.outbox.stats().next_seq;
    assert!(harness.wait_until(|h| snapshots(h) >= 3).await);
    assert_eq!(
        harness.outbox.stats().next_seq,
        before,
        "nothing goes into the buffer"
    );

    assert!(harness.api.send(start_live(1000, true)).await);
    assert!(
        harness
            .wait_until(|_| live.taken.lock().unwrap().last() == Some(&true))
            .await
    );

    assert!(
        harness
            .api
            .send(Payload::StopLiveView(StopLiveView {}))
            .await
    );
    tokio::time::sleep(Duration::from_millis(300)).await;
    let after_stop = snapshots(&harness);
    tokio::time::sleep(Duration::from_millis(1500)).await;
    assert_eq!(
        snapshots(&harness),
        after_stop,
        "no snapshots after the stop"
    );
    assert!(*live.stopped.lock().unwrap() >= 1);
    harness.stop().await;
}

// Without a renewal the live view ends by itself after the lease.
#[tokio::test(flavor = "multi_thread")]
async fn the_live_view_ends_without_a_renewal() {
    let live = Arc::new(FakeLive::default());
    let mut setup = Setup::new();
    setup.live = Some(live.clone());
    setup.tweak = |c| c.live_lease = Duration::from_millis(2500);
    let harness = setup.start().await;
    assert!(
        harness
            .api
            .wait_for(WAIT, |s| s.first().is_some_and(|s| s.hello.is_some()))
            .await
    );
    assert!(harness.api.send(start_live(1000, false)).await);
    assert!(harness.wait_until(|h| snapshots(h) >= 1).await);
    tokio::time::sleep(Duration::from_millis(3500)).await;
    let ended = snapshots(&harness);
    assert!((2..=4).contains(&ended), "{ended} snapshots in the lease");
    tokio::time::sleep(Duration::from_millis(1500)).await;
    assert_eq!(snapshots(&harness), ended, "the lease ended");
    assert!(*live.stopped.lock().unwrap() >= 1);
    harness.stop().await;
}

// agent.toml forbids the live view (or there is none): refused with a reason, no snapshots.
#[tokio::test(flavor = "multi_thread")]
async fn refuses_the_live_view_with_a_reason() {
    for live in [
        Some(Arc::new(FakeLive {
            refuse: Some("agent.toml forbids the live view".into()),
            ..FakeLive::default()
        })),
        None,
    ] {
        let mut setup = Setup::new();
        setup.live = live.clone();
        let harness = setup.start().await;
        assert!(
            harness
                .api
                .wait_for(WAIT, |s| s.first().is_some_and(|s| s.hello.is_some()))
                .await
        );
        assert!(harness.api.send(start_live(1000, false)).await);
        assert!(
            harness
                .api
                .wait_for(WAIT, |s| s
                    .first()
                    .is_some_and(|s| !s.live_refusals.is_empty()))
                .await
        );
        let reason = harness.api.sessions()[0].live_refusals[0].clone();
        assert!(
            if live.is_some() {
                reason.contains("agent.toml")
            } else {
                !reason.is_empty()
            },
            "{reason}"
        );
        tokio::time::sleep(Duration::from_millis(1200)).await;
        assert_eq!(snapshots(&harness), 0);
        harness.stop().await;
    }
}
