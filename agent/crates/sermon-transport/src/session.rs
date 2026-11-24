//! The long-lived session with the server (KONZEPT §5, §6): mTLS stream, Hello, heartbeats,
//! batches from the outbox, acknowledgements, throttling, trust bundle and certificate rotation,
//! with reconnects (exponential backoff with jitter).

use std::sync::Arc;
use std::time::{Duration, SystemTime};

use http::Uri;
use rustls::pki_types::CertificateDer;
use sermon_buffer::Seq;
use sermon_core::{Clock, HostFacts};
use tokio::sync::mpsc;
use tokio::time::{Instant, MissedTickBehavior};
use tokio_stream::wrappers::ReceiverStream;
use tokio_util::sync::CancellationToken;
use tonic::Streaming;
use tonic::codec::CompressionEncoding;

use crate::backoff::{Backoff, random_u64};
use crate::channel::{self, error_chain, printable};
use crate::identity::{AgentKey, CertificateOffer, Credentials, IssuedCertificate};
use crate::outbox::{Outbox, batch_for};
use crate::proto::agent::v1::agent_service_client::AgentServiceClient;
use crate::proto::agent::v1::{
    ConfigApplied, ConfigUpdate, Heartbeat, Hello, LiveViewRefused, ProcessSnapshot,
    RotateCertificate, SessionRequest, SessionResponse, StartLiveView, session_request,
    session_response,
};
use crate::tls::agent_api_config;

/// Waits for the next snapshot of a running live view; forever without one.
async fn live_tick(live: Option<Live>) {
    match live {
        Some(live) => tokio::time::sleep_until(live.next.min(live.until)).await,
        None => std::future::pending().await,
    }
}

/// Version of the agent protocol sent in `Hello`.
pub const PROTOCOL_VERSION: u32 = 1;
/// Messages queued for sending; a full queue slows the session down (backpressure).
const OUTGOING_QUEUE: usize = 16;
/// Longest server message taken into the log.
const MAX_SERVER_MESSAGE_CHARS: usize = 300;
/// Longest throttle interval honoured (the protocol allows no more).
const MAX_THROTTLE: Duration = Duration::from_hours(1);

/// Errors of the ports, as text for the log.
pub type BoxError = Box<dyn std::error::Error + Send + Sync>;

/// Where the transport gets its credentials from and stores renewed ones.
pub trait IdentitySource: Send + Sync {
    /// The current credentials (read again before every connection).
    ///
    /// # Errors
    ///
    /// If they can't be loaded; the transport retries later.
    fn load(&self) -> Result<Credentials, BoxError>;

    /// Replaces key and certificate after a rotation, as a whole.
    ///
    /// # Errors
    ///
    /// If storing fails; the agent then keeps using the old certificate.
    fn store_rotated(
        &self,
        key: &AgentKey,
        issued: &IssuedCertificate,
        issued_at: SystemTime,
    ) -> Result<(), BoxError>;

    /// Replaces the trusted roots (trust bundle from the server).
    ///
    /// # Errors
    ///
    /// If storing fails.
    fn store_trust_bundle(&self, roots: &[CertificateDer<'static>]) -> Result<(), BoxError>;
}

/// Takes the configurations the server sends (ADR 0030): checks, stores and applies them.
pub trait ConfigSink: Send + Sync {
    /// The version in use, 0 without one; every `Hello` names it.
    fn version(&self) -> u64;
    /// Takes `update` or keeps the previous configuration; the answer goes to the server.
    fn apply(&self, update: ConfigUpdate) -> ConfigApplied;
}

/// Takes the snapshots of the live view (KONZEPT §9, M3-13).
pub trait LiveSource: Send + Sync {
    /// Why the live view is refused (e.g. `allow_live_view = false`), or `None`.
    fn refusal(&self) -> Option<String>;
    /// A snapshot; blocks while it reads `/proc`. `None` if it failed (the source logs why).
    fn snapshot(&self, hide_cmdline: bool) -> Option<ProcessSnapshot>;
    /// The live view ended: the history for CPU shares is forgotten.
    fn stopped(&self);
}

/// Reads the host facts for `Hello`.
pub type FactsSource = Arc<dyn Fn() -> Result<HostFacts, BoxError> + Send + Sync>;

/// Settings of the transport.
#[derive(Debug, Clone)]
pub struct TransportConfig {
    /// mTLS agent API of the server.
    pub agent_url: Uri,
    /// Version of the agent binary (for `Hello`).
    pub agent_version: String,
    /// Interval of heartbeats (KONZEPT §5: 15 s).
    pub heartbeat_interval: Duration,
    /// First reconnect delay bound.
    pub reconnect_initial: Duration,
    /// Largest reconnect delay.
    pub reconnect_max: Duration,
    /// Batches sent but not acknowledged yet, at most.
    pub max_in_flight: u64,
    /// Wait before trying again with an expired certificate.
    pub expired_retry: Duration,
    /// How long a `StartLiveView` holds without renewal (KONZEPT §9: 60 s).
    pub live_lease: Duration,
}

impl TransportConfig {
    /// Defaults for `agent_url`.
    #[must_use]
    pub fn new(agent_url: Uri, agent_version: impl Into<String>) -> Self {
        Self {
            agent_url,
            agent_version: agent_version.into(),
            heartbeat_interval: Duration::from_secs(15),
            reconnect_initial: Duration::from_secs(1),
            reconnect_max: Duration::from_secs(60),
            max_in_flight: 64,
            expired_retry: Duration::from_mins(5),
            live_lease: Duration::from_mins(1),
        }
    }
}

/// The transport: connects, runs sessions and reconnects until shut down.
pub struct Transport {
    config: TransportConfig,
    identity: Arc<dyn IdentitySource>,
    facts: FactsSource,
    outbox: Outbox,
    clock: Arc<dyn Clock>,
    config_sink: Option<Arc<dyn ConfigSink>>,
    live: Option<Arc<dyn LiveSource>>,
}

impl std::fmt::Debug for Transport {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Transport")
            .field("config", &self.config)
            .finish_non_exhaustive()
    }
}

/// How a session ended without an error.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum End {
    /// The agent shuts down.
    Shutdown,
    /// A new certificate was stored: reconnect with it at once.
    Rotated,
    /// The server closed the stream.
    ServerClosed,
}

/// Why a session failed; `established` says whether it got as far as the trust bundle.
#[derive(Debug)]
struct Failure {
    message: String,
    established: bool,
    retry_after: Option<Duration>,
}

impl Failure {
    fn new(message: impl Into<String>) -> Self {
        Self {
            message: message.into(),
            established: false,
            retry_after: None,
        }
    }
}

impl Transport {
    /// A transport sending from `outbox`.
    #[must_use]
    pub fn new(
        config: TransportConfig,
        identity: Arc<dyn IdentitySource>,
        facts: FactsSource,
        outbox: Outbox,
        clock: Arc<dyn Clock>,
    ) -> Self {
        Self {
            config,
            identity,
            facts,
            outbox,
            clock,
            config_sink: None,
            live: None,
        }
    }

    /// Takes the live view's snapshots from `source`; without one a start is refused.
    #[must_use]
    pub fn with_live(mut self, source: Arc<dyn LiveSource>) -> Self {
        self.live = Some(source);
        self
    }

    /// Hands the server's configurations to `sink`; without one they are ignored.
    #[must_use]
    pub fn with_config(mut self, sink: Arc<dyn ConfigSink>) -> Self {
        self.config_sink = Some(sink);
        self
    }

    /// Runs sessions until `shutdown` is cancelled. Errors are logged and lead to a reconnect.
    pub async fn run(&self, shutdown: CancellationToken) {
        let mut backoff = Backoff::new(self.config.reconnect_initial, self.config.reconnect_max);
        loop {
            let delay = match self.session(&shutdown).await {
                Ok(End::Shutdown) => return,
                Ok(End::Rotated) => {
                    backoff.reset();
                    Duration::ZERO
                }
                Ok(End::ServerClosed) => {
                    tracing::info!("server closed the session, reconnecting");
                    backoff.reset();
                    backoff.next_delay(random_u64())
                }
                Err(failure) => {
                    if failure.established {
                        backoff.reset();
                    }
                    let delay = failure
                        .retry_after
                        .unwrap_or_else(|| backoff.next_delay(random_u64()));
                    tracing::warn!(
                        retry_in_ms = u64::try_from(delay.as_millis()).unwrap_or(u64::MAX),
                        "session failed: {}",
                        failure.message
                    );
                    delay
                }
            };
            tokio::select! {
                () = shutdown.cancelled() => return,
                () = tokio::time::sleep(delay) => {}
            }
        }
    }

    async fn session(&self, shutdown: &CancellationToken) -> Result<End, Failure> {
        let credentials = self
            .identity
            .load()
            .map_err(|error| Failure::new(format!("cannot load the identity: {error}")))?;
        if credentials.is_expired(self.clock.now()) {
            return Err(Failure {
                retry_after: Some(self.config.expired_retry),
                ..Failure::new(
                    "the agent certificate has expired – enroll again with \
                     `sudo -u sermon sermon-agent enroll --force` and a new token",
                )
            });
        }
        let facts = (self.facts)()
            .map_err(|error| Failure::new(format!("cannot read the host facts: {error}")))?;
        let tls = agent_api_config(&credentials)
            .map_err(|error| Failure::new(format!("unusable credentials: {error}")))?;
        let channel = channel::connect(self.config.agent_url.clone(), Some(tls), None)
            .await
            .map_err(|error| Failure::new(with_hint(error.to_string())))?;

        let (outgoing, queue) = mpsc::channel(OUTGOING_QUEUE);
        let hello = request(session_request::Payload::Hello(Hello {
            protocol_version: PROTOCOL_VERSION,
            agent_version: self.config.agent_version.clone(),
            host_facts: Some((&facts).into()),
            config_version: self.config_sink.as_ref().map_or(0, |sink| sink.version()),
        }));
        outgoing
            .send(hello)
            .await
            .map_err(|_| Failure::new("session closed before Hello"))?;
        let mut client = AgentServiceClient::new(channel)
            .send_compressed(CompressionEncoding::Gzip)
            .accept_compressed(CompressionEncoding::Gzip);
        let inbound = client
            .session(ReceiverStream::new(queue))
            .await
            .map_err(|status| Failure::new(with_hint(status_text(&status))))?
            .into_inner();

        let acked = self.outbox.stats().acked;
        let mut session = Session {
            transport: self,
            credentials,
            outgoing,
            sent: acked,
            acked,
            min_interval: Duration::ZERO,
            last_batch: None,
            pending_rotation: None,
            established: false,
            live: None,
        };
        let result = session.run(inbound, shutdown).await;
        // The live view belongs to the session.
        session.stop_live();
        let established = session.established;
        result.map_err(|mut failure| {
            failure.established = established;
            failure
        })
    }
}

/// State of one connected session.
struct Session<'t> {
    transport: &'t Transport,
    credentials: Credentials,
    outgoing: mpsc::Sender<SessionRequest>,
    /// Highest sequence number sent in this session.
    sent: Seq,
    /// Highest sequence number acknowledged by the server.
    acked: Seq,
    min_interval: Duration,
    last_batch: Option<Instant>,
    pending_rotation: Option<AgentKey>,
    established: bool,
    live: Option<Live>,
}

/// A running live view.
#[derive(Debug, Clone, Copy)]
struct Live {
    interval: Duration,
    hide_cmdline: bool,
    /// The lease: without a renewal it ends here.
    until: Instant,
    next: Instant,
}

/// Bounds of the interval (protocol: 1 to 10 s), checked again here.
const LIVE_MIN_INTERVAL: Duration = Duration::from_secs(1);
const LIVE_MAX_INTERVAL: Duration = Duration::from_secs(10);

impl Session<'_> {
    async fn run(
        &mut self,
        mut inbound: Streaming<SessionResponse>,
        shutdown: &CancellationToken,
    ) -> Result<End, Failure> {
        let config = &self.transport.config;
        let mut heartbeat = tokio::time::interval_at(
            Instant::now() + config.heartbeat_interval,
            config.heartbeat_interval,
        );
        heartbeat.set_missed_tick_behavior(MissedTickBehavior::Delay);
        let rotation_due = tokio::time::sleep(self.until(self.credentials.rotate_at()));
        tokio::pin!(rotation_due);

        loop {
            let outbox = self.transport.outbox.clone();
            let ready = outbox.ready();
            tokio::pin!(ready);
            self.pump().await?;
            let throttle = self.throttle_wait();
            tokio::select! {
                biased;
                () = shutdown.cancelled() => return Ok(End::Shutdown),
                message = inbound.message() => match message {
                    Ok(Some(message)) => {
                        if let Some(end) = self.handle(message).await? {
                            return Ok(end);
                        }
                    }
                    Ok(None) => return Ok(End::ServerClosed),
                    Err(status) => return Err(Failure::new(with_hint(status_text(&status)))),
                },
                _ = heartbeat.tick() => {
                    let sent_at = Some(self.transport.clock.now().into());
                    self.send(session_request::Payload::Heartbeat(Heartbeat { sent_at })).await?;
                }
                () = &mut rotation_due, if self.pending_rotation.is_none() => {
                    tracing::info!("certificate due for rotation");
                    self.start_rotation().await?;
                }
                () = &mut ready => {}
                () = throttle => {}
                () = live_tick(self.live) => self.live_snapshot().await?,
            }
        }
    }

    /// Starts or renews the live view, or refuses it.
    async fn start_live(&mut self, start: StartLiveView) -> Result<(), Failure> {
        let refusal = match &self.transport.live {
            None => Some("this agent has no live view".to_owned()),
            Some(source) => source.refusal(),
        };
        if let Some(reason) = refusal {
            tracing::info!("live view refused: {reason}");
            return self
                .send(session_request::Payload::LiveViewRefused(LiveViewRefused {
                    reason,
                }))
                .await;
        }
        let interval = start
            .interval
            .and_then(|d| Duration::try_from(d).ok())
            .unwrap_or(LIVE_MIN_INTERVAL)
            .clamp(LIVE_MIN_INTERVAL, LIVE_MAX_INTERVAL);
        let now = Instant::now();
        let next = if let Some(live) = self.live {
            live.next.min(now + interval)
        } else {
            tracing::info!("live view started");
            now
        };
        self.live = Some(Live {
            interval,
            hide_cmdline: start.hide_cmdline,
            until: now + self.transport.config.live_lease,
            next,
        });
        Ok(())
    }

    fn stop_live(&mut self) {
        if self.live.take().is_some() {
            tracing::info!("live view stopped");
            if let Some(source) = &self.transport.live {
                source.stopped();
            }
        }
    }

    /// Takes and sends a snapshot, or ends the live view after its lease.
    async fn live_snapshot(&mut self) -> Result<(), Failure> {
        let (Some(live), Some(source)) = (self.live, self.transport.live.clone()) else {
            return Ok(());
        };
        let now = Instant::now();
        if now >= live.until {
            self.stop_live();
            return Ok(());
        }
        if let Some(l) = &mut self.live {
            l.next = now + live.interval;
        }
        let hide = live.hide_cmdline;
        let snapshot = tokio::task::spawn_blocking(move || source.snapshot(hide))
            .await
            .ok()
            .flatten();
        match snapshot {
            Some(snapshot) => {
                self.send(session_request::Payload::ProcessSnapshot(snapshot))
                    .await
            }
            None => Ok(()),
        }
    }

    /// Sends buffered batches as far as the in-flight limit and the throttle allow.
    async fn pump(&mut self) -> Result<(), Failure> {
        let max_in_flight = self.transport.config.max_in_flight;
        loop {
            let in_flight = self.sent.get().saturating_sub(self.acked.get());
            if in_flight >= max_in_flight || self.throttled() {
                return Ok(());
            }
            let room = usize::try_from(max_in_flight - in_flight).unwrap_or(usize::MAX);
            let records = self
                .transport
                .outbox
                .read_after(
                    self.sent,
                    if self.min_interval.is_zero() { room } else { 1 },
                )
                .map_err(|error| Failure::new(format!("cannot read the buffer: {error}")))?;
            if records.is_empty() {
                return Ok(());
            }
            for record in records {
                self.sent = record.seq;
                match batch_for(&record) {
                    Ok(batch) => {
                        self.send(session_request::Payload::Batch(batch)).await?;
                        self.last_batch = Some(Instant::now());
                    }
                    // Undecodable data can't be sent; the server's next ack removes it.
                    Err(error) => {
                        tracing::error!(seq = %record.seq, "skipping unreadable batch: {error}");
                    }
                }
            }
        }
    }

    async fn handle(&mut self, message: SessionResponse) -> Result<Option<End>, Failure> {
        let Some(payload) = message.payload else {
            // A newer server may send commands this agent doesn't know: ignore them.
            tracing::debug!("ignoring an unknown message from the server");
            return Ok(None);
        };
        match payload {
            session_response::Payload::Ack(ack) => {
                let seq = Seq::new(ack.seq);
                self.transport.outbox.ack(seq).map_err(|error| {
                    Failure::new(format!("invalid acknowledgement from the server: {error}"))
                })?;
                self.acked = self.acked.max(seq);
                self.sent = self.sent.max(self.acked);
            }
            session_response::Payload::TrustBundle(bundle) => {
                self.established = true;
                self.update_trust_bundle(bundle.roots);
            }
            session_response::Payload::Throttle(throttle) => {
                let interval = throttle
                    .min_interval
                    .and_then(|duration| Duration::try_from(duration).ok())
                    .unwrap_or_default()
                    .min(MAX_THROTTLE);
                if interval.is_zero() {
                    tracing::info!("server lifted the throttling");
                } else {
                    tracing::warn!(
                        min_interval_ms = u64::try_from(interval.as_millis()).unwrap_or(u64::MAX),
                        "server throttles this agent (quota exceeded)"
                    );
                }
                self.min_interval = interval;
            }
            session_response::Payload::RotateNow(_) => {
                tracing::info!("server requests an immediate certificate rotation");
                if self.pending_rotation.is_none() {
                    self.start_rotation().await?;
                }
            }
            session_response::Payload::CertificateIssued(issued) => {
                return self.finish_rotation(&issued).map(|()| Some(End::Rotated));
            }
            session_response::Payload::StartLiveView(start) => self.start_live(start).await?,
            session_response::Payload::StopLiveView(_) => self.stop_live(),
            session_response::Payload::ConfigUpdate(update) => {
                let Some(sink) = &self.transport.config_sink else {
                    tracing::debug!("ignoring a configuration, none is applied here");
                    return Ok(None);
                };
                let answer = sink.apply(update);
                if answer.error.is_empty() {
                    tracing::info!(
                        version = answer.version,
                        "configuration from the server applied"
                    );
                } else {
                    tracing::warn!(
                        version = answer.version,
                        "configuration from the server rejected, keeping the previous one: {}",
                        answer.error
                    );
                }
                self.send(session_request::Payload::ConfigApplied(answer))
                    .await?;
            }
            // The commands of the root helper (M4-05) follow with M4-12; until then like a newer
            // server's unknown ones.
            session_response::Payload::RefreshUpdateInfo(_)
            | session_response::Payload::RunUpdate(_)
            | session_response::Payload::RebootNode(_)
            | session_response::Payload::SelfUpdate(_) => {
                tracing::debug!("ignoring a command of the root helper, not handled yet");
            }
        }
        Ok(None)
    }

    fn update_trust_bundle(&mut self, roots: Vec<Vec<u8>>) {
        let roots: Vec<CertificateDer<'static>> =
            roots.into_iter().map(CertificateDer::from).collect();
        let usable = !roots.is_empty()
            && roots
                .iter()
                .all(|root| webpki::anchor_from_trusted_cert(root).is_ok());
        if !usable {
            tracing::warn!("ignoring an empty or malformed trust bundle");
            return;
        }
        if roots == self.credentials.trust_anchors {
            return;
        }
        match self.transport.identity.store_trust_bundle(&roots) {
            Ok(()) => {
                tracing::info!(roots = roots.len(), "trust bundle updated");
                self.credentials.trust_anchors = roots;
            }
            Err(error) => tracing::error!("cannot store the trust bundle: {error}"),
        }
    }

    async fn start_rotation(&mut self) -> Result<(), Failure> {
        let key = AgentKey::generate()
            .map_err(|error| Failure::new(format!("cannot create a new key: {error}")))?;
        let csr = key
            .csr_der()
            .map_err(|error| Failure::new(format!("cannot create the CSR: {error}")))?;
        self.send(session_request::Payload::RotateCertificate(
            RotateCertificate { csr },
        ))
        .await?;
        self.pending_rotation = Some(key);
        Ok(())
    }

    fn finish_rotation(
        &mut self,
        issued: &crate::proto::agent::v1::CertificateIssued,
    ) -> Result<(), Failure> {
        let Some(key) = self.pending_rotation.take() else {
            tracing::warn!("ignoring a certificate the agent did not ask for");
            return Ok(());
        };
        let anchors: Vec<Vec<u8>> = self
            .credentials
            .trust_anchors
            .iter()
            .map(|root| root.to_vec())
            .collect();
        let offer = CertificateOffer {
            certificate: &issued.certificate,
            intermediates: &issued.intermediates,
            trust_anchors: &anchors,
            not_after: issued
                .not_after
                .and_then(|timestamp| SystemTime::try_from(timestamp).ok()),
        };
        let now = self.transport.clock.now();
        let node = self.credentials.identity.node_id.clone();
        let certificate = IssuedCertificate::verify(&key, &offer, Some(&node), now)
            .map_err(|error| Failure::new(format!("rotation failed: {error}")))?;
        self.transport
            .identity
            .store_rotated(&key, &certificate, now)
            .map_err(|error| Failure::new(format!("cannot store the new certificate: {error}")))?;
        tracing::info!(node = %node, "certificate rotated, reconnecting with the new one");
        Ok(())
    }

    async fn send(&self, payload: session_request::Payload) -> Result<(), Failure> {
        self.outgoing
            .send(request(payload))
            .await
            .map_err(|_| Failure::new("the stream to the server is closed"))
    }

    fn throttled(&self) -> bool {
        !self.min_interval.is_zero()
            && self
                .last_batch
                .is_some_and(|last| last.elapsed() < self.min_interval)
    }

    /// Completes when the throttle allows the next batch; never if not throttled.
    fn throttle_wait(&self) -> impl Future<Output = ()> + use<> {
        let deadline = self
            .last_batch
            .filter(|_| self.throttled())
            .map(|last| last + self.min_interval);
        async move {
            match deadline {
                Some(deadline) => tokio::time::sleep_until(deadline).await,
                None => std::future::pending().await,
            }
        }
    }

    /// Time from now until `at` on the wall clock (zero if already past).
    fn until(&self, at: SystemTime) -> Duration {
        at.duration_since(self.transport.clock.now())
            .unwrap_or(Duration::ZERO)
    }
}

fn request(payload: session_request::Payload) -> SessionRequest {
    SessionRequest {
        payload: Some(payload),
    }
}

fn status_text(status: &tonic::Status) -> String {
    let message = printable(status.message(), MAX_SERVER_MESSAGE_CHARS);
    match std::error::Error::source(status) {
        Some(source) => format!("{:?}: {message}: {}", status.code(), error_chain(source)),
        None => format!("{:?}: {message}", status.code()),
    }
}

/// Adds a hint when the server refused the client certificate.
fn with_hint(message: String) -> String {
    const REJECTED: [&str; 4] = [
        "BadCertificate",
        "CertificateRevoked",
        "CertificateUnknown",
        "certificate revoked or unknown",
    ];
    if REJECTED.iter().any(|marker| message.contains(marker)) {
        format!(
            "{message} – the server rejected this agent's certificate (revoked, node locked or \
             removed); enroll again with `sudo -u sermon sermon-agent enroll --force` and a new token"
        )
    } else {
        message
    }
}
