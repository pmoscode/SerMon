//! gRPC channels to the server, with TLS under the agent's control.
//!
//! tonic's built-in TLS can't use a custom certificate verifier (pinning) and would pull in a
//! second TLS stack setup. The agent therefore opens TCP and TLS itself and hands the stream to
//! tonic.

use std::io;
use std::sync::Arc;
use std::time::Duration;

use http::Uri;
use hyper_util::rt::TokioIo;
use rustls::ClientConfig;
use rustls::pki_types::ServerName;
use tokio::net::TcpStream;
use tokio_rustls::TlsConnector;
use tonic::transport::{Channel, Endpoint};

/// Time for TCP and TLS setup.
pub const CONNECT_TIMEOUT: Duration = Duration::from_secs(10);
/// HTTP/2 keepalive pings on idle connections (KONZEPT §5).
pub const KEEPALIVE_INTERVAL: Duration = Duration::from_secs(15);
/// A ping unanswered for this long ends the connection.
pub const KEEPALIVE_TIMEOUT: Duration = Duration::from_secs(20);

/// Errors when opening a channel.
#[derive(Debug, thiserror::Error)]
pub enum ConnectError {
    /// The URL has no usable host.
    #[error("invalid server address {0}")]
    InvalidAddress(String),
    /// TCP, TLS or HTTP/2 setup failed; the message includes the cause (e.g. certificate errors).
    #[error("cannot connect to {uri}: {}", error_chain(.source))]
    Connect {
        /// The server.
        uri: Uri,
        /// Cause.
        #[source]
        source: tonic::transport::Error,
    },
}

/// Opens a channel to `uri`. With `tls`, `uri` must be `https`; without, it is plain HTTP/2 (only
/// used for loopback development setups, see the agent configuration). `request_timeout` bounds
/// unary calls; long-lived streams pass `None` and rely on the keepalive pings.
///
/// # Errors
///
/// [`ConnectError`] if the server can't be reached or the TLS handshake fails.
pub async fn connect(
    uri: Uri,
    tls: Option<Arc<ClientConfig>>,
    request_timeout: Option<Duration>,
) -> Result<Channel, ConnectError> {
    let (host, port) = host_and_port(&uri)?;
    let mut endpoint = Endpoint::from(uri.clone())
        .connect_timeout(CONNECT_TIMEOUT)
        .http2_keep_alive_interval(KEEPALIVE_INTERVAL)
        .keep_alive_timeout(KEEPALIVE_TIMEOUT)
        .keep_alive_while_idle(true);
    if let Some(timeout) = request_timeout {
        endpoint = endpoint.timeout(timeout);
    }
    let result = match tls {
        None => endpoint.connect().await,
        Some(config) => {
            let server_name = ServerName::try_from(host.clone())
                .map_err(|_| ConnectError::InvalidAddress(uri.to_string()))?;
            let connector = TlsConnector::from(config);
            endpoint
                .connect_with_connector(tower::service_fn(move |_: Uri| {
                    let (host, connector, server_name) =
                        (host.clone(), connector.clone(), server_name.clone());
                    async move {
                        let tcp = TcpStream::connect((host.as_str(), port)).await?;
                        tcp.set_nodelay(true)?;
                        let stream = connector.connect(server_name, tcp).await?;
                        Ok::<_, io::Error>(TokioIo::new(stream))
                    }
                }))
                .await
        }
    };
    result.map_err(|source| ConnectError::Connect { uri, source })
}

/// Host without IPv6 brackets and port (default from the scheme).
fn host_and_port(uri: &Uri) -> Result<(String, u16), ConnectError> {
    let host = uri
        .host()
        .filter(|host| !host.is_empty())
        .ok_or_else(|| ConnectError::InvalidAddress(uri.to_string()))?;
    let host = host
        .strip_prefix('[')
        .and_then(|inner| inner.strip_suffix(']'))
        .unwrap_or(host);
    let default_port = if uri.scheme_str() == Some("https") {
        443
    } else {
        80
    };
    Ok((host.to_owned(), uri.port_u16().unwrap_or(default_port)))
}

/// Text from the server for logs and terminals: control characters (newlines, escape sequences)
/// become spaces so that it can't forge log lines, and it is cut at `max_chars`.
pub(crate) fn printable(text: &str, max_chars: usize) -> String {
    text.chars()
        .map(|c| if c.is_control() { ' ' } else { c })
        .take(max_chars)
        .collect()
}

/// The error and all its causes, joined with ": ".
pub(crate) fn error_chain(error: &(dyn std::error::Error + 'static)) -> String {
    let mut text = error.to_string();
    let mut source = error.source();
    while let Some(cause) = source {
        let cause_text = cause.to_string();
        if !text.ends_with(&cause_text) {
            text.push_str(": ");
            text.push_str(&cause_text);
        }
        source = cause.source();
    }
    text
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn splits_host_and_port_with_scheme_defaults() {
        let cases = [
            ("https://sermon.example.com", ("sermon.example.com", 443)),
            ("http://localhost", ("localhost", 80)),
            ("https://192.0.2.10:8443", ("192.0.2.10", 8443)),
            ("https://[2001:db8::1]:8443", ("2001:db8::1", 8443)),
        ];
        for (uri, (host, port)) in cases {
            let parsed = host_and_port(&uri.parse().unwrap()).unwrap();
            assert_eq!(parsed, (host.to_owned(), port), "{uri}");
        }
    }

    #[test]
    fn printable_removes_control_characters_and_bounds_the_length() {
        assert_eq!(printable("a\nb\u{1b}[2Jc", 100), "a b [2Jc");
        assert_eq!(printable("abcdef", 3), "abc");
    }

    #[test]
    fn error_chain_lists_causes_once() {
        #[derive(Debug, thiserror::Error)]
        #[error("outer")]
        struct Outer(#[source] io::Error);

        let error = Outer(io::Error::other("inner"));

        assert_eq!(error_chain(&error), "outer: inner");
    }
}
