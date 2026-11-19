//! Test support only: an in-process gRPC server that plays `sermon-server` for agent tests.
//!
//! - [`TestPki`]: a root and an intermediate CA, server certificates and signed agent CSRs.
//! - [`serve_tls`] / [`serve_plain`]: run tonic services on a random loopback port.
//! - [`FakeEnrollment`]: `EnrollmentService` that signs CSRs like the real server, or fails.
//!
//! Never a dependency of the agent binary (dev-dependency only).

#![allow(
    clippy::unwrap_used,
    clippy::expect_used,
    clippy::panic,
    clippy::missing_panics_doc,
    reason = "test support: failing loudly is the desired behaviour"
)]

use std::net::SocketAddr;
use std::sync::{Arc, Mutex};
use std::time::{Duration, SystemTime};

use rcgen::{
    BasicConstraints, CertificateParams, CertificateSigningRequestParams, CertifiedIssuer,
    DistinguishedName, DnType, ExtendedKeyUsagePurpose, IsCa, KeyPair, KeyUsagePurpose, SanType,
};
use rustls::ServerConfig;
use rustls::pki_types::{CertificateDer, PrivateKeyDer, PrivatePkcs8KeyDer};
use sermon_transport::proto::agent::v1::{EnrollRequest, EnrollResponse};
use tokio::net::TcpListener;
use tokio::sync::oneshot;
use tokio_rustls::TlsAcceptor;
use tonic::service::Routes;
use tonic::{Request, Response, Status};

/// Generated tonic server stubs (`just gen`); messages come from `sermon-transport`.
#[allow(
    clippy::all,
    clippy::pedantic,
    clippy::as_conversions,
    missing_debug_implementations,
    unused_qualifications,
    reason = "generated code"
)]
pub mod proto {
    include!("gen/sermon/agent/v1/sermon.agent.v1.tonic.rs");
}

use proto::enrollment_service_server::{EnrollmentService, EnrollmentServiceServer};

/// Organization of the fake server's agents.
pub const ORG_ID: &str = "0192f6c8-1111-7000-8000-000000000001";
/// Node ID the fake server assigns.
pub const NODE_ID: &str = "0192f6c8-2222-7000-8000-000000000002";
/// Lifetime of issued agent certificates (as on the real server).
pub const AGENT_CERT_LIFETIME: Duration = Duration::from_hours(30 * 24);

/// A root CA with an intermediate, like the server's agent CA (or Caddy's internal CA).
#[derive(Debug)]
pub struct TestPki {
    root: CertifiedIssuer<'static, KeyPair>,
    intermediate: CertifiedIssuer<'static, KeyPair>,
}

impl TestPki {
    /// A new PKI whose CA names start with `name`.
    #[must_use]
    pub fn new(name: &str) -> Self {
        let root = CertifiedIssuer::self_signed(
            ca_params(&format!("{name} Root")),
            KeyPair::generate().unwrap(),
        )
        .unwrap();
        let intermediate = CertifiedIssuer::signed_by(
            ca_params(&format!("{name} Intermediate")),
            KeyPair::generate().unwrap(),
            &root,
        )
        .unwrap();
        Self { root, intermediate }
    }

    /// The root certificate (trust anchor).
    #[must_use]
    pub fn root(&self) -> CertificateDer<'static> {
        self.root.der().clone()
    }

    /// The intermediate certificate.
    #[must_use]
    pub fn intermediate(&self) -> CertificateDer<'static> {
        self.intermediate.der().clone()
    }

    /// A server certificate for `hosts` (DNS names or IPs), with the chain `[leaf, intermediate]`.
    #[must_use]
    pub fn server_identity(
        &self,
        hosts: &[&str],
    ) -> (Vec<CertificateDer<'static>>, PrivateKeyDer<'static>) {
        let key = KeyPair::generate().unwrap();
        let mut params = CertificateParams::new(
            hosts
                .iter()
                .map(|host| (*host).to_owned())
                .collect::<Vec<_>>(),
        )
        .unwrap();
        params.extended_key_usages = vec![ExtendedKeyUsagePurpose::ServerAuth];
        let leaf = params.signed_by(&key, &self.intermediate).unwrap();
        (
            vec![leaf.der().clone(), self.intermediate()],
            PrivatePkcs8KeyDer::from(key.serialize_der()).into(),
        )
    }

    /// A self-signed server certificate for `hosts` without any CA.
    #[must_use]
    pub fn self_signed_server(
        hosts: &[&str],
    ) -> (Vec<CertificateDer<'static>>, PrivateKeyDer<'static>) {
        let key = KeyPair::generate().unwrap();
        let params = CertificateParams::new(
            hosts
                .iter()
                .map(|host| (*host).to_owned())
                .collect::<Vec<_>>(),
        )
        .unwrap();
        let cert = params.self_signed(&key).unwrap();
        (
            vec![cert.der().clone()],
            PrivatePkcs8KeyDer::from(key.serialize_der()).into(),
        )
    }

    /// Signs an agent CSR like the server: only the public key is used; the certificate is for
    /// client authentication with the SPIFFE ID of `org_id`/`node_id`.
    #[must_use]
    pub fn sign_agent_csr(
        &self,
        csr_der: &[u8],
        org_id: &str,
        node_id: &str,
    ) -> CertificateDer<'static> {
        let csr = CertificateSigningRequestParams::from_der(&csr_der.to_vec().into()).unwrap();
        let mut params = CertificateParams::default();
        params.distinguished_name = DistinguishedName::new();
        params.subject_alt_names = vec![SanType::URI(
            format!("spiffe://sermon/org/{org_id}/node/{node_id}")
                .try_into()
                .unwrap(),
        )];
        params.extended_key_usages = vec![ExtendedKeyUsagePurpose::ClientAuth];
        params.key_usages = vec![KeyUsagePurpose::DigitalSignature];
        let now = SystemTime::now();
        params.not_before = (now - Duration::from_mins(5)).into();
        params.not_after = (now + AGENT_CERT_LIFETIME).into();
        params
            .signed_by(&csr.public_key, &self.intermediate)
            .unwrap()
            .der()
            .clone()
    }
}

fn ca_params(name: &str) -> CertificateParams {
    let mut params = CertificateParams::default();
    params.distinguished_name.push(DnType::CommonName, name);
    params.is_ca = IsCa::Ca(BasicConstraints::Unconstrained);
    params.key_usages = vec![
        KeyUsagePurpose::KeyCertSign,
        KeyUsagePurpose::CrlSign,
        KeyUsagePurpose::DigitalSignature,
    ];
    params
}

/// TLS 1.3 server configuration with ALPN h2.
#[must_use]
pub fn tls_server_config(
    chain: Vec<CertificateDer<'static>>,
    key: PrivateKeyDer<'static>,
) -> Arc<ServerConfig> {
    let mut config =
        ServerConfig::builder_with_provider(Arc::new(rustls::crypto::ring::default_provider()))
            .with_protocol_versions(&[&rustls::version::TLS13])
            .unwrap()
            .with_no_client_auth()
            .with_single_cert(chain, key)
            .unwrap();
    config.alpn_protocols = vec![b"h2".to_vec()];
    Arc::new(config)
}

/// A running test server; stops when dropped.
#[derive(Debug)]
pub struct RunningServer {
    /// Loopback address with the random port.
    pub addr: SocketAddr,
    shutdown: Option<oneshot::Sender<()>>,
}

impl RunningServer {
    /// `https://localhost:<port>` (the server certificate should be issued for `localhost`).
    #[must_use]
    pub fn https_url(&self) -> String {
        format!("https://localhost:{}", self.addr.port())
    }

    /// `http://127.0.0.1:<port>` for plain HTTP/2 servers.
    #[must_use]
    pub fn http_url(&self) -> String {
        format!("http://127.0.0.1:{}", self.addr.port())
    }
}

impl Drop for RunningServer {
    fn drop(&mut self) {
        if let Some(shutdown) = self.shutdown.take() {
            let _ = shutdown.send(());
        }
    }
}

/// Serves `routes` with TLS on a random loopback port. Failed handshakes are ignored (they are
/// what many tests provoke).
pub async fn serve_tls(routes: Routes, tls: Arc<ServerConfig>) -> RunningServer {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let acceptor = TlsAcceptor::from(tls);
    let (streams, incoming) = tokio::sync::mpsc::channel(16);
    tokio::spawn(async move {
        while let Ok((tcp, _)) = listener.accept().await {
            let (acceptor, streams) = (acceptor.clone(), streams.clone());
            tokio::spawn(async move {
                if let Ok(stream) = acceptor.accept(tcp).await {
                    let _ = streams.send(Ok::<_, std::io::Error>(stream)).await;
                }
            });
        }
    });
    let (shutdown, stop) = oneshot::channel::<()>();
    tokio::spawn(
        tonic::transport::Server::builder()
            .add_routes(routes)
            .serve_with_incoming_shutdown(
                tokio_stream::wrappers::ReceiverStream::new(incoming),
                async {
                    let _ = stop.await;
                },
            ),
    );
    RunningServer {
        addr,
        shutdown: Some(shutdown),
    }
}

/// Serves `routes` over plain HTTP/2 (h2c) on a random loopback port.
pub async fn serve_plain(routes: Routes) -> RunningServer {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let (shutdown, stop) = oneshot::channel::<()>();
    tokio::spawn(
        tonic::transport::Server::builder()
            .add_routes(routes)
            .serve_with_incoming_shutdown(
                tokio_stream::wrappers::TcpListenerStream::new(listener),
                async {
                    let _ = stop.await;
                },
            ),
    );
    RunningServer {
        addr,
        shutdown: Some(shutdown),
    }
}

type Responder = dyn Fn(&EnrollRequest, &TestPki) -> Result<EnrollResponse, Status> + Send + Sync;

/// `EnrollmentService` that answers like the real server (or as told) and records all requests.
#[derive(Clone)]
pub struct FakeEnrollment {
    pki: Arc<TestPki>,
    responder: Arc<Responder>,
    requests: Arc<Mutex<Vec<EnrollRequest>>>,
}

impl std::fmt::Debug for FakeEnrollment {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("FakeEnrollment").finish_non_exhaustive()
    }
}

impl FakeEnrollment {
    /// Signs every CSR for [`ORG_ID`]/[`NODE_ID`] with the agent CA `pki`.
    #[must_use]
    pub fn issuing(pki: Arc<TestPki>) -> Self {
        Self::responding(pki, |request, pki| Ok(issue(request, pki)))
    }

    /// Fails every request with `status`.
    #[must_use]
    pub fn failing(status: Status) -> Self {
        let pki = Arc::new(TestPki::new("Unused"));
        Self::responding(pki, move |_, _| Err(status.clone()))
    }

    /// Answers with `responder`; [`issue`] builds a correct answer to modify.
    #[must_use]
    pub fn responding(
        pki: Arc<TestPki>,
        responder: impl Fn(&EnrollRequest, &TestPki) -> Result<EnrollResponse, Status>
        + Send
        + Sync
        + 'static,
    ) -> Self {
        Self {
            pki,
            responder: Arc::new(responder),
            requests: Arc::default(),
        }
    }

    /// The requests received so far.
    #[must_use]
    pub fn requests(&self) -> Vec<EnrollRequest> {
        self.requests.lock().unwrap().clone()
    }

    /// The service for [`serve_tls`] / [`serve_plain`].
    #[must_use]
    pub fn routes(&self) -> Routes {
        Routes::new(EnrollmentServiceServer::new(self.clone()))
    }
}

/// The answer of the real server: certificate for the CSR, intermediate, roots, expiry.
#[must_use]
pub fn issue(request: &EnrollRequest, pki: &TestPki) -> EnrollResponse {
    let certificate = pki.sign_agent_csr(&request.csr, ORG_ID, NODE_ID);
    EnrollResponse {
        node_id: NODE_ID.to_owned(),
        certificate: certificate.to_vec(),
        intermediates: vec![pki.intermediate().to_vec()],
        trust_anchor: pki.root().to_vec(),
        not_after: Some((SystemTime::now() + AGENT_CERT_LIFETIME).into()),
        trust_anchors: vec![pki.root().to_vec()],
    }
}

#[tonic::async_trait]
impl EnrollmentService for FakeEnrollment {
    async fn enroll(
        &self,
        request: Request<EnrollRequest>,
    ) -> Result<Response<EnrollResponse>, Status> {
        let request = request.into_inner();
        self.requests.lock().unwrap().push(request.clone());
        (self.responder)(&request, &self.pki).map(Response::new)
    }
}
