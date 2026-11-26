//! What the database modules share (M5-13, M5-14): where they connect – a TCP address the host's
//! veto allows (resolved once, every address checked) or a Unix socket below `module_sockets` –
//! and TLS with rustls on demand. Their protocols are small own clients (`postgres::wire`,
//! `mysql::wire`): only what monitoring needs, bounded and fuzzed, without OpenSSL or a driver
//! with dozens of crates.

use std::path::{Path, PathBuf};
use std::sync::{Arc, OnceLock};

use rustls::pki_types::ServerName;
use rustls::{ClientConfig, RootCertStore};
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
    /// A Unix socket, checked against the veto when built and at every connection.
    Unix(PathBuf),
}

/// TLS to the database.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Tls {
    /// None (the default on loopback and for sockets).
    Off,
    /// Required, the certificate verified with the system's roots or `ca_file` (the default for
    /// any other address).
    Verify,
    /// Required, any certificate (only explicitly).
    NoVerify,
}

/// How a module talks to its database: TLS and the roots to verify with.
#[derive(Debug, Clone)]
pub struct Security {
    pub tls: Tls,
    /// The CAs of `ca_file`, if given; the system's otherwise.
    roots: Option<Arc<RootCertStore>>,
}

impl Security {
    /// The settings `tls` and `ca_file` of `config` for `target`: without `tls`, off for the
    /// socket and loopback, verify for any other address (M5-33).
    ///
    /// # Errors
    ///
    /// [`BuildError::Invalid`] for TLS over a socket, or a CA file without `verify` or without
    /// certificates; [`BuildError::Vetoed`] for a CA file outside `module_file_prefixes`.
    pub fn of(config: &Config, target: &Target, veto: &Veto) -> Result<Self, BuildError> {
        let tls = match config.get("tls").and_then(Value::as_str) {
            Some("verify") => Tls::Verify,
            Some("no_verify") => Tls::NoVerify,
            Some(_) => Tls::Off,
            None if target.is_local() => Tls::Off,
            None => Tls::Verify,
        };
        if tls != Tls::Off && matches!(target, Target::Unix(_)) {
            return Err(BuildError::Invalid("tls: not over a Unix socket".into()));
        }
        let roots = match config.get("ca_file").and_then(Value::as_str) {
            None => None,
            Some(_) if tls != Tls::Verify => {
                return Err(BuildError::Invalid("ca_file: only with tls verify".into()));
            }
            Some(path) => Some(Arc::new(crate::http::read_ca_file(veto, Path::new(path))?)),
        };
        Ok(Self { tls, roots })
    }

    /// Whether the peer is known to be the database: over the socket (its path below
    /// `module_sockets`) or TLS with a verified certificate. Only then may a login send the
    /// password itself; `no_verify` encrypts, but to whoever answers (M5-33).
    #[must_use]
    pub fn secure(&self, connection: &Connection) -> bool {
        matches!(connection, Connection::Unix(_)) || self.tls == Tls::Verify
    }

    /// Starts TLS on `io`, a TCP connection to `host`, after the database agreed to.
    ///
    /// # Errors
    ///
    /// [`RunError::Failed`] if the handshake fails.
    pub async fn start<S: AsyncRead + AsyncWrite + Unpin + Send + 'static>(
        &self,
        io: S,
        host: &str,
    ) -> Result<Box<dyn Io>, RunError> {
        tls(io, host, self.tls, self.roots.clone()).await
    }
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
            let path = PathBuf::from(path);
            veto.socket(&path)?;
            return Ok(Self::Unix(path));
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

    /// Whether the target is this host: a socket, `localhost` or a loopback address.
    #[must_use]
    pub fn is_local(&self) -> bool {
        match self {
            Self::Unix(_) => true,
            Self::Tcp { host, .. } => {
                host.eq_ignore_ascii_case("localhost")
                    || host
                        .parse::<std::net::IpAddr>()
                        .is_ok_and(|ip| ip.is_loopback())
            }
        }
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
            Self::Unix(path) => UnixStream::connect(veto.socket(path)?)
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

/// Starts TLS on `io`, a TCP connection to `host`, after the database agreed to; verified with
/// `roots` (a CA file) or the system's.
///
/// # Errors
///
/// [`RunError::Failed`] if the handshake fails.
async fn tls<S: AsyncRead + AsyncWrite + Unpin + Send + 'static>(
    io: S,
    host: &str,
    tls: Tls,
    roots: Option<Arc<RootCertStore>>,
) -> Result<Box<dyn Io>, RunError> {
    static VERIFY: OnceLock<Result<Arc<ClientConfig>, String>> = OnceLock::new();
    static ANY: OnceLock<Result<Arc<ClientConfig>, String>> = OnceLock::new();
    let verify = tls != Tls::NoVerify;
    let options = Options {
        tls_verify: verify,
        ..Options::default()
    };
    let config = match roots {
        Some(roots) if verify => crate::http::tls_config(&options, Some(roots), false),
        _ => if verify { &VERIFY } else { &ANY }
            .get_or_init(|| crate::http::tls_config(&options, None, false))
            .clone(),
    }
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

#[cfg(test)]
mod tests;
