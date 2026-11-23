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
                    let _ = streams
                        .send(Ok::<_, std::io::Error>(PeerStream(stream)))
                        .await;
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

/// The client certificate of a TLS connection, available in request extensions.
#[derive(Debug, Clone)]
pub struct PeerCertificate(pub Option<CertificateDer<'static>>);

/// A server-side TLS stream that exposes the client certificate to tonic handlers.
#[derive(Debug)]
struct PeerStream(tokio_rustls::server::TlsStream<tokio::net::TcpStream>);

impl tonic::transport::server::Connected for PeerStream {
    type ConnectInfo = PeerCertificate;

    fn connect_info(&self) -> Self::ConnectInfo {
        let (_, session) = self.0.get_ref();
        PeerCertificate(
            session
                .peer_certificates()
                .and_then(|certs| certs.first().cloned()),
        )
    }
}

impl tokio::io::AsyncRead for PeerStream {
    fn poll_read(
        mut self: std::pin::Pin<&mut Self>,
        cx: &mut std::task::Context<'_>,
        buf: &mut tokio::io::ReadBuf<'_>,
    ) -> std::task::Poll<std::io::Result<()>> {
        std::pin::Pin::new(&mut self.0).poll_read(cx, buf)
    }
}

impl tokio::io::AsyncWrite for PeerStream {
    fn poll_write(
        mut self: std::pin::Pin<&mut Self>,
        cx: &mut std::task::Context<'_>,
        buf: &[u8],
    ) -> std::task::Poll<std::io::Result<usize>> {
        std::pin::Pin::new(&mut self.0).poll_write(cx, buf)
    }

    fn poll_flush(
        mut self: std::pin::Pin<&mut Self>,
        cx: &mut std::task::Context<'_>,
    ) -> std::task::Poll<std::io::Result<()>> {
        std::pin::Pin::new(&mut self.0).poll_flush(cx)
    }

    fn poll_shutdown(
        mut self: std::pin::Pin<&mut Self>,
        cx: &mut std::task::Context<'_>,
    ) -> std::task::Poll<std::io::Result<()>> {
        std::pin::Pin::new(&mut self.0).poll_shutdown(cx)
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

/// TLS 1.3 server configuration that requires a client certificate chaining to `client_roots`
/// (the agent API).
#[must_use]
pub fn mtls_server_config(
    chain: Vec<CertificateDer<'static>>,
    key: PrivateKeyDer<'static>,
    client_roots: &[CertificateDer<'static>],
) -> Arc<ServerConfig> {
    let provider = Arc::new(rustls::crypto::ring::default_provider());
    let mut roots = rustls::RootCertStore::empty();
    for root in client_roots {
        roots.add(root.clone()).unwrap();
    }
    let verifier = rustls::server::WebPkiClientVerifier::builder_with_provider(
        Arc::new(roots),
        provider.clone(),
    )
    .build()
    .unwrap();
    let mut config = ServerConfig::builder_with_provider(provider)
        .with_protocol_versions(&[&rustls::version::TLS13])
        .unwrap()
        .with_client_cert_verifier(verifier)
        .with_single_cert(chain, key)
        .unwrap();
    config.alpn_protocols = vec![b"h2".to_vec()];
    Arc::new(config)
}

/// When the fake agent API acknowledges batches.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum AckMode {
    /// Every batch right after receiving it.
    Immediate,
    /// Only when the test calls [`FakeAgentApi::send`] with an `Ack`.
    Manual,
}

/// What happened in one session.
#[derive(Debug, Clone, Default)]
pub struct SessionLog {
    /// The client certificate the agent presented.
    pub client_cert: Option<CertificateDer<'static>>,
    /// The agent's `Hello`.
    pub hello: Option<sermon_transport::proto::agent::v1::Hello>,
    /// Heartbeats received.
    pub heartbeats: usize,
    /// Sequence numbers of the batches received, in order.
    pub batches: Vec<u64>,
    /// When each batch arrived.
    pub batch_times: Vec<std::time::Instant>,
    /// `RotateCertificate` requests received.
    pub rotations: usize,
    /// `ConfigApplied` answers received (ADR 0030).
    pub config_applied: Vec<sermon_transport::proto::agent::v1::ConfigApplied>,
    /// Whether the session has ended.
    pub ended: bool,
}

#[derive(Debug)]
struct AgentApiState {
    ack: AckMode,
    trust_roots: Vec<Vec<u8>>,
    sessions: Vec<SessionLog>,
    /// Highest sequence number "persisted" (deduplication like the real server).
    highest: u64,
    /// Unique sequence numbers in the order they were first received.
    unique: Vec<u64>,
    /// For upcoming sessions: end the session after this many batches.
    cut_after: std::collections::VecDeque<Option<usize>>,
    current: Option<tokio::sync::mpsc::Sender<Result<SessionResponse, Status>>>,
    end_after_rotation: bool,
}

/// `AgentService` for transport tests: records everything, acknowledges and rotates like the real
/// server, and lets the test send commands or cut sessions.
#[derive(Debug, Clone)]
pub struct FakeAgentApi {
    pki: Arc<TestPki>,
    state: Arc<Mutex<AgentApiState>>,
}

use sermon_transport::proto::agent::v1::{
    Ack, CertificateIssued, SessionRequest, SessionResponse, TrustBundle, session_request,
    session_response,
};

impl FakeAgentApi {
    /// A fake whose trust bundle is the root of `pki` and that signs rotations with it.
    #[must_use]
    pub fn new(pki: Arc<TestPki>) -> Self {
        let root = pki.root().to_vec();
        Self {
            pki,
            state: Arc::new(Mutex::new(AgentApiState {
                ack: AckMode::Immediate,
                trust_roots: vec![root],
                sessions: Vec::new(),
                highest: 0,
                unique: Vec::new(),
                cut_after: std::collections::VecDeque::new(),
                current: None,
                end_after_rotation: false,
            })),
        }
    }

    /// The service for [`serve_tls`].
    #[must_use]
    pub fn routes(&self) -> Routes {
        Routes::new(
            proto::agent_service_server::AgentServiceServer::new(self.clone())
                .accept_compressed(tonic::codec::CompressionEncoding::Gzip)
                .send_compressed(tonic::codec::CompressionEncoding::Gzip),
        )
    }

    /// Sets the acknowledgement mode.
    pub fn set_ack(&self, mode: AckMode) {
        self.state.lock().unwrap().ack = mode;
    }

    /// Roots sent as trust bundle at the start of every session.
    pub fn set_trust_roots(&self, roots: Vec<Vec<u8>>) {
        self.state.lock().unwrap().trust_roots = roots;
    }

    /// End the next sessions after the given numbers of batches (`None`: don't cut).
    pub fn cut_sessions(&self, after: impl IntoIterator<Item = Option<usize>>) {
        self.state.lock().unwrap().cut_after.extend(after);
    }

    /// End the session after the next rotation (like an on-demand rotation on the server).
    pub fn end_after_rotation(&self, end: bool) {
        self.state.lock().unwrap().end_after_rotation = end;
    }

    /// Sends a message on the current session; `false` without one.
    pub async fn send(&self, payload: session_response::Payload) -> bool {
        let current = self.state.lock().unwrap().current.clone();
        match current {
            Some(sender) => sender
                .send(Ok(SessionResponse {
                    payload: Some(payload),
                }))
                .await
                .is_ok(),
            None => false,
        }
    }

    /// The sessions so far.
    #[must_use]
    pub fn sessions(&self) -> Vec<SessionLog> {
        self.state.lock().unwrap().sessions.clone()
    }

    /// Unique sequence numbers received, in order of first arrival.
    #[must_use]
    pub fn received(&self) -> Vec<u64> {
        self.state.lock().unwrap().unique.clone()
    }

    /// Waits until `condition` holds for the sessions (polling), at most `timeout`.
    pub async fn wait_for(
        &self,
        timeout: Duration,
        condition: impl Fn(&[SessionLog]) -> bool,
    ) -> bool {
        let deadline = tokio::time::Instant::now() + timeout;
        loop {
            if condition(&self.sessions()) {
                return true;
            }
            if tokio::time::Instant::now() >= deadline {
                return false;
            }
            tokio::time::sleep(Duration::from_millis(5)).await;
        }
    }

    fn update(&self, index: usize, change: impl FnOnce(&mut SessionLog)) {
        let mut state = self.state.lock().unwrap();
        if let Some(log) = state.sessions.get_mut(index) {
            change(log);
        }
    }

    async fn run_session(
        &self,
        index: usize,
        mut inbound: tonic::Streaming<SessionRequest>,
        outgoing: tokio::sync::mpsc::Sender<Result<SessionResponse, Status>>,
        cut_after: Option<usize>,
    ) {
        let respond = |payload| SessionResponse {
            payload: Some(payload),
        };
        let first = inbound.message().await;
        let Ok(Some(SessionRequest {
            payload: Some(session_request::Payload::Hello(hello)),
        })) = first
        else {
            let _ = outgoing
                .send(Err(Status::invalid_argument("Hello first")))
                .await;
            return;
        };
        self.update(index, |log| log.hello = Some(hello));
        let roots = self.state.lock().unwrap().trust_roots.clone();
        if outgoing
            .send(Ok(respond(session_response::Payload::TrustBundle(
                TrustBundle { roots },
            ))))
            .await
            .is_err()
        {
            return;
        }
        let mut batches_here = 0;
        while let Ok(Some(message)) = inbound.message().await {
            match message.payload {
                Some(session_request::Payload::Heartbeat(_)) => {
                    self.update(index, |log| log.heartbeats += 1);
                }
                Some(session_request::Payload::Batch(batch)) => {
                    batches_here += 1;
                    let ack = {
                        let mut state = self.state.lock().unwrap();
                        if let Some(log) = state.sessions.get_mut(index) {
                            log.batches.push(batch.seq);
                            log.batch_times.push(std::time::Instant::now());
                        }
                        if batch.seq > state.highest {
                            state.highest = batch.seq;
                            state.unique.push(batch.seq);
                        }
                        state.ack == AckMode::Immediate
                    };
                    if ack {
                        let _ = outgoing
                            .send(Ok(respond(session_response::Payload::Ack(Ack {
                                seq: batch.seq,
                            }))))
                            .await;
                    }
                    if cut_after == Some(batches_here) {
                        let _ = outgoing
                            .send(Err(Status::unavailable("cut by the test")))
                            .await;
                        break;
                    }
                }
                Some(session_request::Payload::RotateCertificate(rotate)) => {
                    self.update(index, |log| log.rotations += 1);
                    let certificate = self.pki.sign_agent_csr(&rotate.csr, ORG_ID, NODE_ID);
                    let issued = CertificateIssued {
                        certificate: certificate.to_vec(),
                        intermediates: vec![self.pki.intermediate().to_vec()],
                        not_after: Some((SystemTime::now() + AGENT_CERT_LIFETIME).into()),
                        previous_valid_until: Some(
                            (SystemTime::now() + Duration::from_hours(1)).into(),
                        ),
                    };
                    let _ = outgoing
                        .send(Ok(respond(session_response::Payload::CertificateIssued(
                            issued,
                        ))))
                        .await;
                    if self.state.lock().unwrap().end_after_rotation {
                        let _ = outgoing.send(Err(Status::aborted("rotated"))).await;
                        break;
                    }
                }
                Some(session_request::Payload::ConfigApplied(applied)) => {
                    self.update(index, |log| log.config_applied.push(applied));
                }
                Some(session_request::Payload::Hello(_)) | None => {
                    let _ = outgoing
                        .send(Err(Status::invalid_argument("unexpected")))
                        .await;
                    break;
                }
            }
        }
    }
}

#[tonic::async_trait]
impl proto::agent_service_server::AgentService for FakeAgentApi {
    type SessionStream = tokio_stream::wrappers::ReceiverStream<Result<SessionResponse, Status>>;

    async fn session(
        &self,
        request: Request<tonic::Streaming<SessionRequest>>,
    ) -> Result<Response<Self::SessionStream>, Status> {
        let client_cert = request
            .extensions()
            .get::<PeerCertificate>()
            .and_then(|peer| peer.0.clone());
        let (outgoing, responses) = tokio::sync::mpsc::channel(64);
        let (index, cut_after) = {
            let mut state = self.state.lock().unwrap();
            state.sessions.push(SessionLog {
                client_cert,
                ..SessionLog::default()
            });
            state.current = Some(outgoing.clone());
            (
                state.sessions.len() - 1,
                state.cut_after.pop_front().flatten(),
            )
        };
        let this = self.clone();
        let inbound = request.into_inner();
        tokio::spawn(async move {
            this.run_session(index, inbound, outgoing, cut_after).await;
            this.update(index, |log| log.ended = true);
        });
        Ok(Response::new(tokio_stream::wrappers::ReceiverStream::new(
            responses,
        )))
    }
}
