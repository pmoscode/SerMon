//! What the database modules share (M5-13, M5-14): where they connect – a TCP address the host's
//! veto allows (resolved once, every address checked) or a Unix socket below `module_sockets` –
//! and TLS with rustls on demand. Their protocols are small own clients (`postgres::wire`,
//! `mysql::wire`): only what monitoring needs, bounded and fuzzed, without OpenSSL or a driver
//! with dozens of crates.

use std::path::PathBuf;
use std::sync::{Arc, OnceLock};

use rustls::ClientConfig;
use rustls::pki_types::ServerName;
use serde_json::Value;
use tokio::io::{AsyncRead, AsyncWrite};
use tokio::net::{TcpStream, UnixStream};

use crate::http::Options;
use crate::{BuildError, Config, RunError, Veto, schema};

/// Where a database listens.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Target {
    /// `host:port`; the host is resolved through the veto at every connection.
    Tcp { host: String, port: u16 },
    /// A Unix socket, checked against the veto when built.
    Unix(PathBuf),
}

/// TLS to the database.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Tls {
    /// None (the default on loopback and for sockets).
    Off,
    /// Required, the certificate verified with the system's roots.
    Verify,
    /// Required, any certificate (only explicitly).
    NoVerify,
}

/// A connection, plain or over TLS.
pub trait Io: AsyncRead + AsyncWrite + Unpin + Send {}
impl<T: AsyncRead + AsyncWrite + Unpin + Send> Io for T {}

impl Target {
    /// The target of `config`: `socket` (a path) wins over `address` (`host:port`).
    ///
    /// # Errors
    ///
    /// [`BuildError`] for no target, or a socket the veto refuses.
    pub fn of(config: &Config, veto: &Veto) -> Result<Self, BuildError> {
        if let Some(path) = config.get("socket").and_then(Value::as_str) {
            let checked = veto.socket(std::path::Path::new(path))?;
            return Ok(Self::Unix(checked));
        }
        let text = config
            .get("address")
            .and_then(Value::as_str)
            .ok_or_else(|| BuildError::Invalid("address or socket required".into()))?;
        let (host, port) =
            schema::host_port(text).map_err(|e| BuildError::Invalid(format!("address: {e}")))?;
        Ok(Self::Tcp {
            host: host.to_owned(),
            port,
        })
    }

    /// The label of the target: the address or the socket's path.
    #[must_use]
    pub fn label(&self) -> String {
        match self {
            Self::Tcp { host, port } if host.contains(':') => format!("[{host}]:{port}"),
            Self::Tcp { host, port } => format!("{host}:{port}"),
            Self::Unix(path) => path.display().to_string(),
        }
    }

    /// Connects; a TCP address only if the veto allows every address of the name.
    ///
    /// # Errors
    ///
    /// [`RunError::Vetoed`] for an address outside the veto, [`RunError::Failed`] if nothing
    /// answers.
    pub async fn connect(&self, veto: &Veto) -> Result<Connection, RunError> {
        match self {
            Self::Tcp { host, port } => {
                let mut last = String::from("no address");
                for address in veto.resolve(host, *port).await? {
                    match TcpStream::connect(address).await {
                        Ok(stream) => {
                            let _ = stream.set_nodelay(true);
                            return Ok(Connection::Tcp(stream, host.clone()));
                        }
                        Err(e) => last = e.to_string(),
                    }
                }
                Err(RunError::Failed(format!("{}: {last}", self.label())))
            }
            Self::Unix(path) => UnixStream::connect(path)
                .await
                .map(Connection::Unix)
                .map_err(|e| RunError::Failed(format!("{}: {e}", path.display()))),
        }
    }
}

/// A fresh connection, before TLS.
#[derive(Debug)]
pub enum Connection {
    /// Over TCP to the host named.
    Tcp(TcpStream, String),
    /// Over a Unix socket.
    Unix(UnixStream),
}

impl Connection {
    /// The connection as it is.
    #[must_use]
    pub fn plain(self) -> Box<dyn Io> {
        match self {
            Self::Tcp(s, _) => Box::new(s),
            Self::Unix(s) => Box::new(s),
        }
    }
}

/// The TLS setting of `config` (`tls`: `off`, `verify`, `no_verify`).
#[must_use]
pub fn tls_of(config: &Config) -> Tls {
    match config.get("tls").and_then(Value::as_str) {
        Some("verify") => Tls::Verify,
        Some("no_verify") => Tls::NoVerify,
        _ => Tls::Off,
    }
}

/// Starts TLS on `io`, a TCP connection to `host`, after the database agreed to.
///
/// # Errors
///
/// [`RunError::Failed`] if the handshake fails.
pub async fn tls<S: AsyncRead + AsyncWrite + Unpin + Send + 'static>(
    io: S,
    host: &str,
    tls: Tls,
) -> Result<Box<dyn Io>, RunError> {
    static VERIFY: OnceLock<Result<Arc<ClientConfig>, String>> = OnceLock::new();
    static ANY: OnceLock<Result<Arc<ClientConfig>, String>> = OnceLock::new();
    let verify = tls != Tls::NoVerify;
    let options = Options {
        tls_verify: verify,
        ..Options::default()
    };
    let config = if verify { &VERIFY } else { &ANY }
        .get_or_init(|| crate::http::tls_config(&options, None, false))
        .clone()
        .map_err(RunError::Failed)?;
    let name = match host.parse::<std::net::IpAddr>() {
        Ok(ip) => ServerName::IpAddress(ip.into()),
        Err(_) => ServerName::try_from(host.to_owned())
            .map_err(|_| RunError::Failed(format!("{host}: not a host name")))?,
    };
    let stream = tokio_rustls::TlsConnector::from(config)
        .connect(name, io)
        .await
        .map_err(|e| RunError::Failed(format!("TLS: {e}")))?;
    Ok(Box::new(stream))
}

/// The text of a string field of `config`, or `default`.
#[must_use]
pub fn text<'a>(config: &'a Config, name: &str, default: &'a str) -> &'a str {
    config.get(name).and_then(Value::as_str).unwrap_or(default)
}
