//! The HTTP client of the modules (ADR 0039, M5-08): nginx, Apache, the Prometheus scraper and
//! the HTTP check reach services only through it, and it keeps them inside the host's veto.
//!
//! - **Addresses:** every host is resolved once through [`Veto::resolve`], which checks every
//!   address; the client connects to those addresses, never to the name again (no DNS
//!   rebinding between check and connection).
//! - **Redirects:** at most [`MAX_REDIRECTS`], each target resolved and checked like the first;
//!   never from HTTPS to HTTP; the credential (`Authorization`) only to the configured host and
//!   port.
//! - **Bounds:** one timeout for the whole request with its redirects, at most [`MAX_BODY`]
//!   bytes of body and [`MAX_HEADER_BYTES`] of headers; only `GET`, HTTP/1.1, one request per
//!   connection.
//! - **TLS:** rustls with the system's roots or the CA file of the configuration (a file the
//!   veto allows); without verification only if the configuration says so explicitly.
//! - **Unix sockets** below `module_sockets` (the Docker API, M5-17).

use std::net::{IpAddr, SocketAddr};
use std::path::{Path, PathBuf};
use std::sync::{Arc, OnceLock};
use std::time::Duration;

use bytes::Bytes;
use http_body_util::{BodyExt, Empty, Limited};
use hyper::client::conn::http1;
use hyper_util::rt::TokioIo;
use rustls::client::danger::{HandshakeSignatureValid, ServerCertVerified, ServerCertVerifier};
use rustls::crypto::{CryptoProvider, verify_tls12_signature, verify_tls13_signature};
use rustls::pki_types::{CertificateDer, ServerName, UnixTime};
use rustls::{ClientConfig, DigitallySignedStruct, RootCertStore, SignatureScheme};
use tokio::io::{AsyncRead, AsyncWrite};
use tokio::net::{TcpStream, UnixStream};
use tokio::time::Instant;

use crate::{BuildError, Refused, RunError, Veto};

/// Most bytes of a response body (ADR 0039).
pub const MAX_BODY: usize = 1024 * 1024;
/// Most redirects followed (ADR 0039).
pub const MAX_REDIRECTS: usize = 3;
/// Most bytes of a response's status line and headers.
pub const MAX_HEADER_BYTES: usize = 64 * 1024;
/// Most headers of a response.
const MAX_HEADERS: usize = 64;
/// Largest CA file read.
const MAX_CA_FILE: u64 = 1024 * 1024;
/// Longest URL taken (a label of the metrics).
pub const MAX_URL: usize = 256;

const USER_AGENT: &str = concat!("sermon-agent/", env!("CARGO_PKG_VERSION"));

/// How the client asks.
#[derive(Clone)]
pub struct Options {
    /// For the whole request with its redirects.
    pub timeout: Duration,
    /// Whether redirects are followed; if not, a redirect is the answer.
    pub follow_redirects: bool,
    /// Whether the certificate is verified (on by default; off only explicitly).
    pub tls_verify: bool,
    /// CA certificates (PEM) to verify with instead of the system's.
    pub ca_file: Option<PathBuf>,
    /// The value of `Authorization`, sent only to the configured host and port.
    pub authorization: Option<String>,
    /// The value of `Accept`.
    pub accept: &'static str,
}

impl std::fmt::Debug for Options {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Options")
            .field("timeout", &self.timeout)
            .field("follow_redirects", &self.follow_redirects)
            .field("tls_verify", &self.tls_verify)
            .field("ca_file", &self.ca_file)
            .field("authorization", &self.authorization.as_ref().map(|_| "…"))
            .finish_non_exhaustive()
    }
}

impl Default for Options {
    fn default() -> Self {
        Self {
            timeout: Duration::from_secs(5),
            follow_redirects: true,
            tls_verify: true,
            ca_file: None,
            authorization: None,
            accept: "*/*",
        }
    }
}

/// Why a request failed.
#[derive(Debug, thiserror::Error)]
pub enum HttpError {
    /// The veto refuses an address or socket.
    #[error(transparent)]
    Vetoed(#[from] Refused),
    /// The service can't be reached, answers nonsense or too much.
    #[error("{0}")]
    Failed(String),
}

impl From<HttpError> for RunError {
    fn from(e: HttpError) -> Self {
        match e {
            HttpError::Vetoed(r) => Self::Vetoed(r),
            HttpError::Failed(why) => Self::Failed(why),
        }
    }
}

fn failed(what: impl std::fmt::Display) -> HttpError {
    HttpError::Failed(what.to_string())
}

/// An `http` or `https` URL without user and password; the fragment dropped.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Url {
    https: bool,
    host: String,
    port: u16,
    path: String,
}

impl Url {
    /// Parses `text`.
    ///
    /// # Errors
    ///
    /// If `text` is no such URL or longer than [`MAX_URL`].
    pub fn parse(text: &str) -> Result<Self, String> {
        if text.len() > MAX_URL {
            return Err(format!("a URL of at most {MAX_URL} bytes"));
        }
        let (https, rest) = if let Some(rest) = text.strip_prefix("https://") {
            (true, rest)
        } else if let Some(rest) = text.strip_prefix("http://") {
            (false, rest)
        } else {
            return Err("an http or https URL".into());
        };
        let rest = rest.split('#').next().unwrap_or_default();
        let end = rest.find(['/', '?']).unwrap_or(rest.len());
        let (authority, path) = rest.split_at(end);
        let (host, port) = authority_parts(authority, if https { 443 } else { 80 })?;
        let path = match path {
            "" => "/".to_owned(),
            p if p.starts_with('?') => format!("/{p}"),
            p => p.to_owned(),
        };
        if !path.bytes().all(|b| b.is_ascii_graphic()) {
            return Err("the path holds spaces or control characters".into());
        }
        Ok(Self {
            https,
            host,
            port,
            path,
        })
    }

    /// The host (an IPv6 address without brackets).
    #[must_use]
    pub fn host(&self) -> &str {
        &self.host
    }

    /// The port.
    #[must_use]
    pub fn port(&self) -> u16 {
        self.port
    }

    /// Whether it is HTTPS.
    #[must_use]
    pub fn https(&self) -> bool {
        self.https
    }

    /// `host[:port]` as in the `Host` header.
    fn authority(&self) -> String {
        let host = if self.host.contains(':') {
            format!("[{}]", self.host)
        } else {
            self.host.clone()
        };
        let default = if self.https { 443 } else { 80 };
        if self.port == default {
            host
        } else {
            format!("{host}:{}", self.port)
        }
    }

    /// The URL without its query: a label of the metrics (a query may hold a token).
    #[must_use]
    pub fn label(&self) -> String {
        let path = self.path.split('?').next().unwrap_or("/");
        let scheme = if self.https { "https" } else { "http" };
        format!("{scheme}://{}{path}", self.authority())
    }

    /// The target of a redirect to `location` from here.
    ///
    /// # Errors
    ///
    /// If the target is no `http` or `https` URL.
    pub fn join(&self, location: &str) -> Result<Self, String> {
        if location.starts_with("http://") || location.starts_with("https://") {
            return Self::parse(location);
        }
        let first = location.find(['/', '?', '#']).unwrap_or(location.len());
        if location[..first].contains(':') {
            return Err("not an http or https URL".into());
        }
        if let Some(rest) = location.strip_prefix("//") {
            let scheme = if self.https { "https" } else { "http" };
            return Self::parse(&format!("{scheme}://{rest}"));
        }
        let path = if location.starts_with('/') {
            location.to_owned()
        } else {
            let base = self.path.split('?').next().unwrap_or("/");
            let dir = &base[..=base.rfind('/').unwrap_or(0)];
            format!("{dir}{location}")
        };
        let next = Self {
            path: path.split('#').next().unwrap_or_default().to_owned(),
            ..self.clone()
        };
        Self::parse(&format!(
            "{}://{}{}",
            if self.https { "https" } else { "http" },
            next.authority(),
            next.path
        ))
    }

    fn same_origin(&self, other: &Self) -> bool {
        self.https == other.https && self.host == other.host && self.port == other.port
    }
}

/// Host and port of an authority (`host`, `host:port`, `[v6]:port`).
fn authority_parts(authority: &str, default: u16) -> Result<(String, u16), String> {
    if authority.is_empty() || authority.contains('@') {
        return Err("a URL with a host, without user and password".into());
    }
    let (host, port) = if let Some(rest) = authority.strip_prefix('[') {
        let (host, port) = rest.split_once(']').ok_or("an IPv6 address in [ ]")?;
        if host.parse::<std::net::Ipv6Addr>().is_err() {
            return Err("not an IPv6 address".into());
        }
        let port = if port.is_empty() {
            None
        } else {
            Some(port.strip_prefix(':').ok_or("not [address]:port")?)
        };
        (host, port)
    } else {
        match authority.split_once(':') {
            Some((host, port)) => (host, Some(port)),
            None => (authority, None),
        }
    };
    let port = match port {
        None => default,
        Some(p) => p
            .parse::<u16>()
            .ok()
            .filter(|p| *p > 0)
            .ok_or("the port is 1 to 65535")?,
    };
    let host_ok = !host.is_empty()
        && host.len() <= 253
        && host
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b".-:".contains(&b));
    if !host_ok {
        return Err("not a host".into());
    }
    Ok((host.to_ascii_lowercase(), port))
}

/// An answer.
#[derive(Debug, Clone)]
pub struct Response {
    /// The status code of the last response.
    pub status: u16,
    /// Its `Content-Type`.
    pub content_type: Option<String>,
    /// Its body (empty unless asked for), at most [`MAX_BODY`] bytes.
    pub body: Bytes,
    /// From the start to the last response's headers (and body, if read).
    pub elapsed: Duration,
    /// The certificate of the first TLS connection (the configured host's).
    pub certificate: Option<CertificateDer<'static>>,
    /// Redirects followed.
    pub redirects: usize,
}

/// The HTTP client of a module.
pub struct Client {
    veto: Veto,
    options: Options,
    roots: Option<Arc<RootCertStore>>,
    tls: OnceLock<Result<Arc<ClientConfig>, String>>,
}

impl std::fmt::Debug for Client {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Client")
            .field("options", &self.options)
            .finish_non_exhaustive()
    }
}

impl Client {
    /// A client bounded by `veto`; a CA file is checked against the veto and read now.
    ///
    /// # Errors
    ///
    /// [`BuildError::Vetoed`] for a CA file outside `module_file_prefixes`,
    /// [`BuildError::Invalid`] for one without certificates.
    pub fn new(veto: &Veto, options: Options) -> Result<Self, BuildError> {
        let roots = match &options.ca_file {
            None => None,
            Some(path) => Some(Arc::new(read_ca_file(&veto.file(path)?)?)),
        };
        Ok(Self {
            veto: veto.clone(),
            options,
            roots,
            tls: OnceLock::new(),
        })
    }

    /// `GET url`, following redirects inside the veto; the body only with `read_body`.
    ///
    /// # Errors
    ///
    /// [`HttpError::Vetoed`] if the veto refuses an address on the way, otherwise
    /// [`HttpError::Failed`].
    pub async fn get(&self, url: &Url, read_body: bool) -> Result<Response, HttpError> {
        let start = Instant::now();
        tokio::time::timeout(self.options.timeout, self.follow(url, read_body, start))
            .await
            .map_err(|_| {
                failed(format!(
                    "{}: no answer within {} ms",
                    url.label(),
                    self.options.timeout.as_millis()
                ))
            })?
    }

    /// `GET path` over the Unix socket `socket` (below `module_sockets`); no redirects.
    ///
    /// # Errors
    ///
    /// [`HttpError::Vetoed`] if the veto refuses the socket, otherwise [`HttpError::Failed`].
    pub async fn get_unix(
        &self,
        socket: &Path,
        path: &str,
        read_body: bool,
    ) -> Result<Response, HttpError> {
        if !path.starts_with('/') || !path.bytes().all(|b| b.is_ascii_graphic()) {
            return Err(failed("the path starts with / and holds no spaces"));
        }
        let socket = self.veto.socket(socket)?;
        let start = Instant::now();
        let exchange = async {
            let stream = UnixStream::connect(&socket)
                .await
                .map_err(|e| failed(format!("{}: {e}", socket.display())))?;
            let answer = self
                .send(stream, "localhost", path, None, read_body)
                .await?;
            Ok(Response {
                status: answer.status,
                content_type: answer.content_type,
                body: answer.body,
                elapsed: start.elapsed(),
                certificate: None,
                redirects: 0,
            })
        };
        tokio::time::timeout(self.options.timeout, exchange)
            .await
            .map_err(|_| {
                failed(format!(
                    "{}: no answer within {} ms",
                    socket.display(),
                    self.options.timeout.as_millis()
                ))
            })?
    }

    async fn follow(
        &self,
        url: &Url,
        read_body: bool,
        start: Instant,
    ) -> Result<Response, HttpError> {
        let mut current = url.clone();
        let mut certificate = None;
        let mut redirects = 0;
        loop {
            let addresses = self.veto.resolve(&current.host, current.port).await?;
            let authorization = if current.same_origin(url) {
                self.options.authorization.as_deref()
            } else {
                None
            };
            let redirecting = self.options.follow_redirects;
            let (answer, cert) = self
                .exchange(&current, &addresses, authorization, read_body)
                .await?;
            if certificate.is_none() {
                certificate = cert;
            }
            match answer.location.filter(|_| redirecting) {
                Some(location) => {
                    if redirects == MAX_REDIRECTS {
                        return Err(failed(format!(
                            "{}: more than {MAX_REDIRECTS} redirects",
                            url.label()
                        )));
                    }
                    let next = current
                        .join(&location)
                        .map_err(|e| failed(format!("{}: a redirect to {e}", current.label())))?;
                    if current.https && !next.https {
                        return Err(failed(format!(
                            "{}: a redirect from https to http",
                            current.label()
                        )));
                    }
                    redirects += 1;
                    current = next;
                }
                None => {
                    return Ok(Response {
                        status: answer.status,
                        content_type: answer.content_type,
                        body: answer.body,
                        elapsed: start.elapsed(),
                        certificate,
                        redirects,
                    });
                }
            }
        }
    }

    /// One request to `url` at the checked `addresses`.
    async fn exchange(
        &self,
        url: &Url,
        addresses: &[SocketAddr],
        authorization: Option<&str>,
        read_body: bool,
    ) -> Result<(Answer, Option<CertificateDer<'static>>), HttpError> {
        let stream = connect(addresses)
            .await
            .map_err(|e| failed(format!("{}: {e}", url.label())))?;
        let authority = url.authority();
        if !url.https {
            let answer = self
                .send(stream, &authority, &url.path, authorization, read_body)
                .await?;
            return Ok((answer, None));
        }
        let config = self.tls_config()?;
        let name = match url.host.parse::<IpAddr>() {
            Ok(ip) => ServerName::IpAddress(ip.into()),
            Err(_) => ServerName::try_from(url.host.clone())
                .map_err(|_| failed(format!("{}: not a host name", url.host)))?,
        };
        let tls = tokio_rustls::TlsConnector::from(config)
            .connect(name, stream)
            .await
            .map_err(|e| failed(format!("{}: TLS: {e}", url.label())))?;
        let certificate = tls
            .get_ref()
            .1
            .peer_certificates()
            .and_then(|c| c.first())
            .map(|c| c.clone().into_owned());
        let answer = self
            .send(tls, &authority, &url.path, authorization, read_body)
            .await?;
        Ok((answer, certificate))
    }

    fn tls_config(&self) -> Result<Arc<ClientConfig>, HttpError> {
        self.tls
            .get_or_init(|| tls_config(&self.options, self.roots.clone()))
            .clone()
            .map_err(HttpError::Failed)
    }

    /// `GET path` over `io`, HTTP/1.1.
    async fn send<T>(
        &self,
        io: T,
        host: &str,
        path: &str,
        authorization: Option<&str>,
        read_body: bool,
    ) -> Result<Answer, HttpError>
    where
        T: AsyncRead + AsyncWrite + Unpin + Send + 'static,
    {
        let (mut sender, connection) = http1::Builder::new()
            .max_buf_size(MAX_HEADER_BYTES)
            .max_headers(MAX_HEADERS)
            .handshake::<_, Empty<Bytes>>(TokioIo::new(io))
            .await
            .map_err(|e| failed(format!("{host}: {e}")))?;
        let _connection = Abort(tokio::spawn(connection));
        let mut request = hyper::Request::get(path)
            .header(hyper::header::HOST, host)
            .header(hyper::header::USER_AGENT, USER_AGENT)
            .header(hyper::header::ACCEPT, self.options.accept)
            .header(hyper::header::CONNECTION, "close");
        if let Some(value) = authorization {
            let mut value = http::HeaderValue::from_str(value)
                .map_err(|_| failed("the credential is no header value"))?;
            value.set_sensitive(true);
            request = request.header(hyper::header::AUTHORIZATION, value);
        }
        let request = request
            .body(Empty::new())
            .map_err(|e| failed(format!("{host}: {e}")))?;
        let response = sender
            .send_request(request)
            .await
            .map_err(|e| failed(format!("{host}: {e}")))?;
        let status = response.status();
        let header = |name| {
            response
                .headers()
                .get(name)
                .and_then(|v| v.to_str().ok())
                .map(str::to_owned)
        };
        let location = status
            .is_redirection()
            .then(|| header(hyper::header::LOCATION))
            .flatten();
        let content_type = header(hyper::header::CONTENT_TYPE);
        let length = header(hyper::header::CONTENT_LENGTH).and_then(|l| l.parse::<u64>().ok());
        let body = if read_body && location.is_none() {
            if length.is_some_and(|l| usize::try_from(l).is_ok_and(|l| l > MAX_BODY)) {
                return Err(too_large(host));
            }
            Limited::new(response.into_body(), MAX_BODY)
                .collect()
                .await
                .map_err(|e| {
                    if e.is::<http_body_util::LengthLimitError>() {
                        too_large(host)
                    } else {
                        failed(format!("{host}: {e}"))
                    }
                })?
                .to_bytes()
        } else {
            Bytes::new()
        };
        Ok(Answer {
            status: status.as_u16(),
            location,
            content_type,
            body,
        })
    }
}

fn too_large(host: &str) -> HttpError {
    failed(format!(
        "{host}: the answer is larger than {MAX_BODY} bytes"
    ))
}

/// A response as one exchange sees it.
struct Answer {
    status: u16,
    location: Option<String>,
    content_type: Option<String>,
    body: Bytes,
}

/// Aborts the connection task with the request.
struct Abort(tokio::task::JoinHandle<Result<(), hyper::Error>>);

impl Drop for Abort {
    fn drop(&mut self) {
        self.0.abort();
    }
}

/// Connects to the first of `addresses` that answers.
async fn connect(addresses: &[SocketAddr]) -> std::io::Result<TcpStream> {
    let mut last = std::io::Error::other("no address");
    for address in addresses {
        match TcpStream::connect(address).await {
            Ok(stream) => {
                stream.set_nodelay(true)?;
                return Ok(stream);
            }
            Err(e) => last = e,
        }
    }
    Err(last)
}

/// The system's CA certificates, read once for all modules.
fn system_roots() -> Result<Arc<RootCertStore>, String> {
    static ROOTS: OnceLock<Result<Arc<RootCertStore>, String>> = OnceLock::new();
    ROOTS
        .get_or_init(|| {
            let mut roots = RootCertStore::empty();
            roots.add_parsable_certificates(rustls_native_certs::load_native_certs().certs);
            if roots.is_empty() {
                Err(
                    "no CA certificates on this host: install ca-certificates or set ca_file"
                        .into(),
                )
            } else {
                Ok(Arc::new(roots))
            }
        })
        .clone()
}

fn read_ca_file(path: &Path) -> Result<RootCertStore, BuildError> {
    use std::io::Read;
    let invalid = |why: String| BuildError::Invalid(format!("{}: {why}", path.display()));
    let mut text = Vec::new();
    std::fs::File::open(path)
        .and_then(|f| f.take(MAX_CA_FILE + 1).read_to_end(&mut text))
        .map_err(|e| invalid(e.to_string()))?;
    if u64::try_from(text.len()).map_or(true, |n| n > MAX_CA_FILE) {
        return Err(invalid(format!("larger than {MAX_CA_FILE} bytes")));
    }
    let mut roots = RootCertStore::empty();
    for block in pem::parse_many(&text).map_err(|e| invalid(e.to_string()))? {
        if block.tag() == "CERTIFICATE" {
            roots
                .add(CertificateDer::from(block.into_contents()))
                .map_err(|e| invalid(e.to_string()))?;
        }
    }
    if roots.is_empty() {
        return Err(invalid("no certificate".into()));
    }
    Ok(roots)
}

fn tls_config(
    options: &Options,
    roots: Option<Arc<RootCertStore>>,
) -> Result<Arc<ClientConfig>, String> {
    let provider = Arc::new(rustls::crypto::ring::default_provider());
    let builder = ClientConfig::builder_with_provider(provider.clone())
        .with_safe_default_protocol_versions()
        .map_err(|e| e.to_string())?;
    let mut config = if options.tls_verify {
        let roots = match roots {
            Some(roots) => roots,
            None => system_roots()?,
        };
        builder.with_root_certificates(roots).with_no_client_auth()
    } else {
        builder
            .dangerous()
            .with_custom_certificate_verifier(Arc::new(Unverified(provider)))
            .with_no_client_auth()
    };
    config.alpn_protocols = vec![b"http/1.1".to_vec()];
    Ok(Arc::new(config))
}

/// Takes any certificate (`tls_verify: false`); the handshake's signatures are still checked.
#[derive(Debug)]
struct Unverified(Arc<CryptoProvider>);

impl ServerCertVerifier for Unverified {
    fn verify_server_cert(
        &self,
        _: &CertificateDer<'_>,
        _: &[CertificateDer<'_>],
        _: &ServerName<'_>,
        _: &[u8],
        _: UnixTime,
    ) -> Result<ServerCertVerified, rustls::Error> {
        Ok(ServerCertVerified::assertion())
    }

    fn verify_tls12_signature(
        &self,
        message: &[u8],
        cert: &CertificateDer<'_>,
        dss: &DigitallySignedStruct,
    ) -> Result<HandshakeSignatureValid, rustls::Error> {
        verify_tls12_signature(
            message,
            cert,
            dss,
            &self.0.signature_verification_algorithms,
        )
    }

    fn verify_tls13_signature(
        &self,
        message: &[u8],
        cert: &CertificateDer<'_>,
        dss: &DigitallySignedStruct,
    ) -> Result<HandshakeSignatureValid, rustls::Error> {
        verify_tls13_signature(
            message,
            cert,
            dss,
            &self.0.signature_verification_algorithms,
        )
    }

    fn supported_verify_schemes(&self) -> Vec<SignatureScheme> {
        self.0.signature_verification_algorithms.supported_schemes()
    }
}

#[cfg(test)]
mod tests;
