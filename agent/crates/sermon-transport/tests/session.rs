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
    Ack, MetricBatch, RotateNow, Sample, Throttle, sample, session_response::Payload,
};
use sermon_transport::session::{BoxError, IdentitySource, Transport, TransportConfig};
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
}

impl Setup {
    fn new() -> Self {
        Self {
            pki: Arc::new(TestPki::new("Agent")),
            identity: None,
            server_pki: None,
            tweak: |_| {},
            api: None,
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
        let transport = Transport::new(
            config,
            identity.clone(),
            Arc::new(|| Ok(facts())),
            outbox.clone(),
            Arc::new(SystemClock::new()),
        );
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
