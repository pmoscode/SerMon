//! TLS certificates (M5-09): how long the certificates of files and endpoints have left, whether
//! their chain leads to a trusted CA and whether an endpoint's fits its name. Files only below
//! `module_file_prefixes` (by default `/etc/ssl`, `/etc/pki`, `/etc/letsencrypt`), endpoints only
//! inside `module_targets`; only metadata leaves the host. A file may hold a private key too
//! (haproxy): only its certificates are read.
//!
//! | Metric | Labels | Unit |
//! |---|---|---|
//! | `sermon_tls_up` | `target` | 1 if certificates were read (file) or presented (endpoint) |
//! | `sermon_tls_cert_expires_in_seconds` | `target` | until the first certificate (the leaf) expires; negative once expired |
//! | `sermon_tls_chain_expires_in_seconds` | `target` | until the first certificate of the chain expires (intermediates included) |
//! | `sermon_tls_chain_valid` | `target` | 1 if the chain leads to a CA of the system or of `ca_file` |
//! | `sermon_tls_name_valid` | `target` | endpoints: 1 if the leaf is valid for the host (or `server_name`) |
//!
//! `target` is the configured path or `host:port`, at most 32 files and 16 endpoints. The chain
//! is checked at the leaf's last valid second once it expired: an expired leaf shows in its
//! expiry, not as a broken chain. Without CA certificates on the host and without `ca_file`,
//! `sermon_tls_chain_valid` is left out.

use std::io::Read;
use std::path::PathBuf;
use std::sync::Arc;
use std::time::{Duration, UNIX_EPOCH};

use rustls::RootCertStore;
use rustls::client::{verify_server_cert_signed_by_trust_anchor, verify_server_name};
use rustls::pki_types::{CertificateDer, ServerName, UnixTime};
use rustls::server::ParsedCertificate;
use serde_json::Value;
use sermon_core::Sample;
use tokio::task::JoinSet;

use crate::fields::{strings, timeout};
use crate::http::{self, HttpError};
use crate::{
    BoxFuture, BuildError, Config, Instance, Module, Refused, Run, RunError, Setup, Veto, schema,
};

const SCHEMA: &str = include_str!("../schemas/tls.json");

/// Largest certificate file read.
const MAX_FILE: u64 = 1024 * 1024;
/// Most certificates of a chain looked at.
const MAX_CHAIN: usize = 16;

/// The module `tls`.
#[derive(Debug, Clone, Copy, Default)]
pub struct Tls;

impl Module for Tls {
    fn id(&self) -> &'static str {
        "tls"
    }

    fn schema(&self) -> &'static str {
        SCHEMA
    }

    fn default_interval(&self) -> Duration {
        // Expiry is a matter of days.
        Duration::from_mins(5)
    }

    fn build(&self, config: &Config, setup: &Setup<'_>) -> Result<Box<dyn Instance>, BuildError> {
        let mut files: Vec<PathBuf> = Vec::new();
        for text in strings(config, "files") {
            let path = PathBuf::from(text);
            setup.veto.file(&path)?;
            if files.contains(&path) {
                return Err(BuildError::Invalid(format!("{text}: twice")));
            }
            files.push(path);
        }
        let mut endpoints: Vec<Endpoint> = Vec::new();
        for text in strings(config, "endpoints") {
            let (host, port) =
                schema::host_port(text).map_err(|e| BuildError::Invalid(format!("{text}: {e}")))?;
            if endpoints.iter().any(|e| e.label == text) {
                return Err(BuildError::Invalid(format!("{text}: twice")));
            }
            endpoints.push(Endpoint {
                host: host.to_owned(),
                port,
                label: text.to_owned(),
            });
        }
        if files.is_empty() && endpoints.is_empty() {
            return Err(BuildError::Invalid("no file and no endpoint".into()));
        }
        let roots = match config.get("ca_file").and_then(Value::as_str) {
            Some(path) => Some(Arc::new(http::read_ca_file(
                &setup.veto.file(std::path::Path::new(path))?,
            )?)),
            None => None,
        };
        Ok(Box::new(Certificates {
            veto: setup.veto.clone(),
            files,
            endpoints,
            server_name: config
                .get("server_name")
                .and_then(Value::as_str)
                .map(str::to_owned),
            roots,
            timeout: timeout(config, "timeout", Duration::from_secs(5))?,
        }))
    }
}

#[derive(Debug, Clone)]
struct Endpoint {
    host: String,
    port: u16,
    label: String,
}

struct Certificates {
    veto: Veto,
    files: Vec<PathBuf>,
    endpoints: Vec<Endpoint>,
    server_name: Option<String>,
    roots: Option<Arc<RootCertStore>>,
    timeout: Duration,
}

/// The certificates of a PEM file below the veto, at most [`MAX_CHAIN`].
fn read_file(veto: &Veto, path: &std::path::Path) -> Result<Vec<CertificateDer<'static>>, Unread> {
    let resolved = veto.file(path).map_err(Unread::Vetoed)?;
    let mut text = Vec::new();
    std::fs::File::open(&resolved)
        .and_then(|f| f.take(MAX_FILE + 1).read_to_end(&mut text))
        .map_err(|_| Unread::Unreadable)?;
    if u64::try_from(text.len()).map_or(true, |n| n > MAX_FILE) {
        return Err(Unread::Unreadable);
    }
    let blocks = pem::parse_many(&text).map_err(|_| Unread::Unreadable)?;
    Ok(blocks
        .into_iter()
        .filter(|b| b.tag() == "CERTIFICATE")
        .take(MAX_CHAIN)
        .map(|b| CertificateDer::from(b.into_contents()))
        .collect())
}

/// Why a file gave no certificates.
enum Unread {
    Vetoed(Refused),
    Unreadable,
}

/// What the metrics of one target say.
#[derive(Debug, Default, PartialEq, Eq)]
struct Verdict {
    cert_expires_in: Option<i64>,
    chain_expires_in: Option<i64>,
    chain_valid: Option<bool>,
    name_valid: Option<bool>,
}

/// Judges `chain` (leaf first) at `now` (seconds since the epoch) against `roots`; `name` for
/// an endpoint.
fn judge(
    chain: &[CertificateDer<'_>],
    now: i64,
    roots: Option<&RootCertStore>,
    name: Option<&ServerName<'_>>,
) -> Verdict {
    let Some(leaf) = chain.first() else {
        return Verdict::default();
    };
    let Some(validity) = crate::x509::validity(leaf) else {
        return Verdict::default();
    };
    let chain_end = chain
        .iter()
        .take(MAX_CHAIN)
        .filter_map(|c| crate::x509::validity(c))
        .map(|v| v.not_after)
        .min()
        .unwrap_or(validity.not_after);
    let parsed = ParsedCertificate::try_from(leaf).ok();
    let at = now.min(validity.not_after.saturating_sub(1)).max(0);
    let at = UnixTime::since_unix_epoch(Duration::from_secs(u64::try_from(at).unwrap_or(0)));
    let algorithms = rustls::crypto::ring::default_provider().signature_verification_algorithms;
    let chain_valid = roots.map(|roots| {
        parsed.as_ref().is_some_and(|p| {
            verify_server_cert_signed_by_trust_anchor(
                p,
                roots,
                &chain[1..chain.len().min(MAX_CHAIN)],
                at,
                algorithms.all,
            )
            .is_ok()
        })
    });
    let name_valid = name.map(|n| {
        parsed
            .as_ref()
            .is_some_and(|p| verify_server_name(p, n).is_ok())
    });
    Verdict {
        cert_expires_in: Some(validity.not_after.saturating_sub(now)),
        chain_expires_in: Some(chain_end.saturating_sub(now)),
        chain_valid,
        name_valid,
    }
}

fn samples(label: &str, verdict: &Verdict, out: &mut Vec<Sample>) {
    let up = verdict.cert_expires_in.is_some();
    out.push(Sample::int("sermon_tls_up", i64::from(up)).label("target", label));
    let metrics = [
        (
            "sermon_tls_cert_expires_in_seconds",
            verdict.cert_expires_in,
        ),
        (
            "sermon_tls_chain_expires_in_seconds",
            verdict.chain_expires_in,
        ),
        ("sermon_tls_chain_valid", verdict.chain_valid.map(i64::from)),
        ("sermon_tls_name_valid", verdict.name_valid.map(i64::from)),
    ];
    for (metric, value) in metrics {
        if let Some(value) = value {
            out.push(Sample::int(metric, value).label("target", label));
        }
    }
}

impl Instance for Certificates {
    fn timeout(&self) -> Duration {
        self.timeout + Duration::from_secs(2)
    }

    fn collect<'a>(&'a mut self, run: &'a Run) -> BoxFuture<'a, Result<Vec<Sample>, RunError>> {
        Box::pin(async move {
            let now = run
                .now()
                .duration_since(UNIX_EPOCH)
                .map_or(0, |d| i64::try_from(d.as_secs()).unwrap_or(i64::MAX));
            let roots = match &self.roots {
                Some(roots) => Some(roots.clone()),
                None => http::system_roots().ok(),
            };
            let mut asks = JoinSet::new();
            for (i, e) in self.endpoints.iter().enumerate() {
                let (veto, e, limit) = (self.veto.clone(), e.clone(), self.timeout);
                let name = self.server_name.clone().unwrap_or_else(|| e.host.clone());
                asks.spawn(async move {
                    let chain = http::peer_chain(&veto, &e.host, e.port, &name, limit).await;
                    (i, name, chain)
                });
            }
            let (veto, files) = (self.veto.clone(), self.files.clone());
            let read = run
                .blocking(move || {
                    files
                        .iter()
                        .map(|f| read_file(&veto, f))
                        .collect::<Vec<_>>()
                })
                .await?;
            let mut out = Vec::new();
            for (path, chain) in self.files.iter().zip(read) {
                let chain = match chain {
                    Ok(chain) => chain,
                    Err(Unread::Vetoed(r)) => return Err(RunError::Vetoed(r)),
                    Err(Unread::Unreadable) => Vec::new(),
                };
                let verdict = judge(&chain, now, roots.as_deref(), None);
                samples(&path.to_string_lossy(), &verdict, &mut out);
            }
            let mut answers = Vec::new();
            while let Some(done) = asks.join_next().await {
                answers.push(done.map_err(|e| RunError::Failed(format!("a check failed: {e}")))?);
            }
            answers.sort_by_key(|(i, _, _)| *i);
            for (i, name, chain) in answers {
                let chain = match chain {
                    Ok(chain) => chain,
                    Err(HttpError::Vetoed(r)) => return Err(RunError::Vetoed(r)),
                    Err(HttpError::Failed(_) | HttpError::Unreachable(_)) => Vec::new(),
                };
                let server_name = match name.parse::<std::net::IpAddr>() {
                    Ok(ip) => Some(ServerName::IpAddress(ip.into())),
                    Err(_) => ServerName::try_from(name).ok(),
                };
                let verdict = judge(&chain, now, roots.as_deref(), server_name.as_ref());
                samples(&self.endpoints[i].label, &verdict, &mut out);
            }
            Ok(out)
        })
    }
}

#[cfg(test)]
mod tests;
