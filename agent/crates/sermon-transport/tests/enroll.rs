//! Enrollment against an in-process fake server: TLS trust (pinning, system roots), status
//! mapping and the checks of the issued certificate.

// Test code: panicking on setup failures is the desired behaviour.
#![allow(clippy::unwrap_used, clippy::panic)]

use std::sync::Arc;
use std::time::{Duration, SystemTime};

use sermon_core::{Architecture, HostFacts, MachineId, SystemClock};
use sermon_testserver::{
    FakeEnrollment, NODE_ID, ORG_ID, TestPki, issue, serve_plain, serve_tls, tls_server_config,
};
use sermon_transport::channel::{self, ConnectError};
use sermon_transport::enrollment::{EnrollError, enroll};
use sermon_transport::fingerprint::Fingerprint;
use sermon_transport::identity::{AgentKey, IssuedCertificate};
use sermon_transport::proto::agent::v1 as pb;
use sermon_transport::tls::{ServerTrust, enrollment_config};
use tonic::Status;

const TOKEN: &str = "sme_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_abcde";

fn facts() -> HostFacts {
    HostFacts {
        hostname: "web-01".into(),
        machine_id: MachineId::parse("0123456789abcdef0123456789abcdef").unwrap(),
        os_id: "debian".into(),
        os_version: "13".into(),
        kernel_release: "6.12.0-1-amd64".into(),
        architecture: Architecture::Amd64,
        cpu_count: 4,
        memory_bytes: 8 << 30,
    }
}

#[derive(Debug)]
enum Failure {
    Connect(ConnectError),
    Enroll(EnrollError),
}

async fn enroll_at(
    url: &str,
    trust: Option<ServerTrust>,
    key: &AgentKey,
) -> Result<IssuedCertificate, Failure> {
    let tls = trust.map(|trust| enrollment_config(&trust).unwrap());
    let channel = channel::connect(url.parse().unwrap(), tls, Some(Duration::from_secs(10)))
        .await
        .map_err(Failure::Connect)?;
    enroll(channel, key, TOKEN, &facts(), "1.2.3", &SystemClock::new())
        .await
        .map_err(Failure::Enroll)
}

/// Web endpoint with a certificate from `web_pki` (like Caddy with an internal CA) and an agent CA.
struct Setup {
    web_pki: TestPki,
    agent_pki: Arc<TestPki>,
    leaf: Fingerprint,
}

impl Setup {
    fn new() -> Self {
        Self {
            web_pki: TestPki::new("Web"),
            agent_pki: Arc::new(TestPki::new("Agent")),
            leaf: Fingerprint::of(b""),
        }
    }

    async fn start(
        &mut self,
        service: &FakeEnrollment,
        host: &str,
    ) -> sermon_testserver::RunningServer {
        let (chain, key) = self.web_pki.server_identity(&[host]);
        self.leaf = Fingerprint::of(&chain[0]);
        serve_tls(service.routes(), tls_server_config(chain, key)).await
    }
}

#[tokio::test]
async fn enrolls_with_a_pinned_leaf_certificate() {
    let mut setup = Setup::new();
    let service = FakeEnrollment::issuing(setup.agent_pki.clone());
    let server = setup.start(&service, "localhost").await;
    let key = AgentKey::generate().unwrap();

    let issued = enroll_at(
        &server.https_url(),
        Some(ServerTrust::Pinned(setup.leaf)),
        &key,
    )
    .await
    .unwrap();

    assert_eq!(issued.identity.org_id, ORG_ID);
    assert_eq!(issued.identity.node_id, NODE_ID);
    assert_eq!(issued.trust_anchors, vec![setup.agent_pki.root()]);
    assert_eq!(issued.intermediates, vec![setup.agent_pki.intermediate()]);
    assert!(issued.not_after > SystemTime::now() + Duration::from_hours(29 * 24));

    let requests = service.requests();
    assert_eq!(requests.len(), 1);
    assert_eq!(requests[0].token, TOKEN);
    assert_eq!(requests[0].agent_version, "1.2.3");
    assert_eq!(requests[0].host_facts, Some(pb::HostFacts::from(&facts())));
    let csr =
        rcgen::CertificateSigningRequestParams::from_der(&requests[0].csr.clone().into()).unwrap();
    assert_eq!(
        rcgen::PublicKeyData::subject_public_key_info(&csr.public_key),
        key.public_key_info()
    );
}

#[tokio::test]
async fn enrolls_with_a_pinned_ca_certificate_and_checks_the_host_name() {
    let mut setup = Setup::new();
    let service = FakeEnrollment::issuing(setup.agent_pki.clone());
    let pin = ServerTrust::Pinned(Fingerprint::of(&setup.web_pki.intermediate()));

    let good = setup.start(&service, "localhost").await;
    let issued = enroll_at(
        &good.https_url(),
        Some(pin.clone()),
        &AgentKey::generate().unwrap(),
    )
    .await;
    assert!(issued.is_ok(), "{issued:?}");

    // Same CA, but the certificate is for another host: a CA pin doesn't skip the name check.
    let other = setup.start(&service, "other.example.com").await;
    let error = enroll_at(
        &other.https_url(),
        Some(pin),
        &AgentKey::generate().unwrap(),
    )
    .await
    .unwrap_err();
    let text = format!("{error:?}");
    assert!(matches!(error, Failure::Connect(_)), "{text}");
    assert!(text.contains("NotValidForName"), "{text}");
    assert_eq!(service.requests().len(), 1);
}

#[tokio::test]
async fn wrong_pin_aborts_before_anything_is_sent() {
    let mut setup = Setup::new();
    let service = FakeEnrollment::issuing(setup.agent_pki.clone());
    let server = setup.start(&service, "localhost").await;
    let wrong = Fingerprint::of(b"another certificate");

    let error = enroll_at(
        &server.https_url(),
        Some(ServerTrust::Pinned(wrong)),
        &AgentKey::generate().unwrap(),
    )
    .await
    .unwrap_err();

    let Failure::Connect(error) = error else {
        panic!("expected a connect error, got {error:?}");
    };
    let text = error.to_string();
    assert!(text.contains("pinned fingerprint"), "{text}");
    assert!(text.contains(&wrong.to_string()), "{text}");
    assert!(
        text.contains(&setup.leaf.to_string()),
        "presented fingerprints missing: {text}"
    );
    assert_eq!(service.requests().len(), 0);
}

#[tokio::test]
async fn system_roots_reject_a_private_ca() {
    let mut setup = Setup::new();
    let service = FakeEnrollment::issuing(setup.agent_pki.clone());
    let server = setup.start(&service, "localhost").await;

    let error = enroll_at(
        &server.https_url(),
        Some(ServerTrust::SystemRoots),
        &AgentKey::generate().unwrap(),
    )
    .await
    .unwrap_err();

    let text = format!("{error:?}");
    assert!(matches!(error, Failure::Connect(_)), "{text}");
    assert!(text.contains("UnknownIssuer"), "{text}");
    assert_eq!(service.requests().len(), 0);
}

#[tokio::test]
async fn enrolls_over_plain_http2_for_loopback_development() {
    let agent_pki = Arc::new(TestPki::new("Agent"));
    let service = FakeEnrollment::issuing(agent_pki);
    let server = serve_plain(service.routes()).await;

    let issued = enroll_at(&server.http_url(), None, &AgentKey::generate().unwrap()).await;

    assert!(issued.is_ok(), "{issued:?}");
}

#[tokio::test]
async fn maps_server_refusals() {
    let cases = [
        (Status::unauthenticated("invalid token"), "InvalidToken"),
        (Status::already_exists("machine-id"), "AlreadyEnrolled"),
        (Status::resource_exhausted("slow down"), "RateLimited"),
        (
            Status::invalid_argument("csr: bad"),
            "Rejected(\"csr: bad\")",
        ),
    ];
    for (status, expected) in cases {
        let service = FakeEnrollment::failing(status);
        let server = serve_plain(service.routes()).await;

        let error = enroll_at(&server.http_url(), None, &AgentKey::generate().unwrap())
            .await
            .unwrap_err();

        assert!(format!("{error:?}").contains(expected), "{error:?}");
    }
}

/// Runs an enrollment against a server answering with `modify(correct answer)`.
async fn enroll_with_answer(
    modify: impl Fn(&mut pb::EnrollResponse, &TestPki) + Send + Sync + 'static,
) -> String {
    let agent_pki = Arc::new(TestPki::new("Agent"));
    let service = FakeEnrollment::responding(agent_pki, move |request, pki| {
        let mut response = issue(request, pki);
        modify(&mut response, pki);
        Ok(response)
    });
    let server = serve_plain(service.routes()).await;
    match enroll_at(&server.http_url(), None, &AgentKey::generate().unwrap()).await {
        Ok(issued) => panic!("accepted {issued:?}"),
        Err(Failure::Enroll(error)) => error.to_string(),
        Err(error) => panic!("unexpected {error:?}"),
    }
}

#[tokio::test]
async fn rejects_a_certificate_for_another_key() {
    let error = enroll_with_answer(|response, pki| {
        let other = AgentKey::generate().unwrap();
        response.certificate = pki
            .sign_agent_csr(&other.csr_der().unwrap(), ORG_ID, NODE_ID)
            .to_vec();
    })
    .await;

    assert!(error.contains("issued for a different key"), "{error}");
}

#[tokio::test]
async fn rejects_a_certificate_for_another_node() {
    let error = enroll_with_answer(|response, _| {
        response.node_id = "0192f6c8-3333-7000-8000-000000000003".into();
    })
    .await;

    assert!(error.contains("issued for a different node"), "{error}");
}

#[tokio::test]
async fn rejects_a_certificate_not_signed_by_the_issuing_ca() {
    let error = enroll_with_answer(|response, _| {
        let stranger = TestPki::new("Stranger");
        response.intermediates = vec![stranger.intermediate().to_vec()];
    })
    .await;

    assert!(error.contains("chain does not verify"), "{error}");
}

#[tokio::test]
async fn rejects_missing_or_past_expiry_and_missing_anchors() {
    let past = enroll_with_answer(|response, _| {
        response.not_after = Some((SystemTime::now() - Duration::from_secs(1)).into());
    })
    .await;
    assert!(past.contains("already expired"), "{past}");

    let missing = enroll_with_answer(|response, _| response.not_after = None).await;
    assert!(missing.contains("missing expiry"), "{missing}");

    let no_anchor = enroll_with_answer(|response, _| {
        response.trust_anchors.clear();
        response.trust_anchor.clear();
    })
    .await;
    assert!(no_anchor.contains("no trust anchor"), "{no_anchor}");
}
