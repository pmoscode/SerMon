//! The HTTP check (M5-08): whether endpoints the host's veto allows answer, how fast, with which
//! status, and how long the certificate of an HTTPS endpoint has left. Up to 16 URLs, asked at
//! once, each within its timeout; the body is not read.
//!
//! | Metric | Labels | Unit |
//! |---|---|---|
//! | `sermon_http_check_up` | `target` | 1 for an answer with an expected status, else 0 |
//! | `sermon_http_check_status_code` | `target` | status of the last answer (after redirects) |
//! | `sermon_http_check_duration_seconds` | `target` | until the last answer's headers, redirects included |
//! | `sermon_http_check_cert_expires_in_seconds` | `target` | HTTPS: until the certificate of the configured host expires; negative once expired |
//!
//! `target` is the URL without its query (at most 16 per node). An endpoint that doesn't answer
//! is `up` 0 without the other metrics; an address outside the veto (also on a redirect) is no
//! answer but a refusal: the module reports `vetoed`.

use std::sync::Arc;
use std::time::{Duration, UNIX_EPOCH};

use serde_json::Value;
use sermon_core::Sample;
use tokio::task::JoinSet;

use crate::fields::{strings, timeout};
use crate::http::{Client, HttpError, Options, Url};
use crate::{BoxFuture, BuildError, Config, Instance, Module, Run, RunError, Setup};

const SCHEMA: &str = include_str!("../schemas/http_check.json");

/// The module `http_check`.
#[derive(Debug, Clone, Copy, Default)]
pub struct HttpCheck;

impl Module for HttpCheck {
    fn id(&self) -> &'static str {
        "http_check"
    }

    fn schema(&self) -> &'static str {
        SCHEMA
    }

    fn build(&self, config: &Config, setup: &Setup<'_>) -> Result<Box<dyn Instance>, BuildError> {
        let mut targets: Vec<(Url, String)> = Vec::new();
        for text in strings(config, "urls") {
            let url = Url::parse(text).map_err(|e| BuildError::Invalid(format!("{text}: {e}")))?;
            let label = url.label();
            if targets.iter().any(|(_, l)| *l == label) {
                return Err(BuildError::Invalid(format!("{label}: twice")));
            }
            targets.push((url, label));
        }
        if targets.is_empty() {
            return Err(BuildError::Invalid("no URL to check".into()));
        }
        let timeout = timeout(config, "timeout", Duration::from_secs(5))?;
        let expect = config
            .get("expect_status")
            .and_then(Value::as_array)
            .map_or_else(Vec::new, |a| {
                a.iter()
                    .filter_map(Value::as_u64)
                    .filter_map(|n| u16::try_from(n).ok())
                    .collect()
            });
        let flag = |name: &str| config.get(name).and_then(Value::as_bool).unwrap_or(true);
        let client = Client::new(
            setup.veto,
            Options {
                timeout,
                follow_redirects: flag("follow_redirects"),
                tls_verify: flag("tls_verify"),
                ca_file: config
                    .get("ca_file")
                    .and_then(Value::as_str)
                    .map(Into::into),
                ..Options::default()
            },
        )?;
        Ok(Box::new(Checks {
            client: Arc::new(client),
            targets,
            expect,
            timeout,
        }))
    }
}

struct Checks {
    client: Arc<Client>,
    targets: Vec<(Url, String)>,
    expect: Vec<u16>,
    timeout: Duration,
}

impl Checks {
    fn expected(&self, status: u16) -> bool {
        if self.expect.is_empty() {
            (200..400).contains(&status)
        } else {
            self.expect.contains(&status)
        }
    }
}

impl Instance for Checks {
    fn timeout(&self) -> Duration {
        self.timeout + Duration::from_secs(2)
    }

    fn collect<'a>(&'a mut self, run: &'a Run) -> BoxFuture<'a, Result<Vec<Sample>, RunError>> {
        Box::pin(async move {
            let mut asks = JoinSet::new();
            for (i, (url, _)) in self.targets.iter().enumerate() {
                let (client, url) = (self.client.clone(), url.clone());
                asks.spawn(async move { (i, client.get(&url, false).await) });
            }
            let mut answers = Vec::with_capacity(self.targets.len());
            while let Some(done) = asks.join_next().await {
                answers.push(done.map_err(|e| RunError::Failed(format!("a check failed: {e}")))?);
            }
            answers.sort_by_key(|(i, _)| *i);
            let now = run
                .now()
                .duration_since(UNIX_EPOCH)
                .map_or(0, |d| i64::try_from(d.as_secs()).unwrap_or(i64::MAX));
            let mut samples = Vec::new();
            for (i, answer) in answers {
                let label = &self.targets[i].1;
                let response = match answer {
                    Ok(response) => response,
                    Err(HttpError::Vetoed(r)) => return Err(RunError::Vetoed(r)),
                    Err(HttpError::Failed(_)) => {
                        samples.push(Sample::int("sermon_http_check_up", 0).label("target", label));
                        continue;
                    }
                };
                let up = i64::from(self.expected(response.status));
                samples.push(Sample::int("sermon_http_check_up", up).label("target", label));
                samples.push(
                    Sample::int("sermon_http_check_status_code", i64::from(response.status))
                        .label("target", label),
                );
                samples.push(
                    Sample::float(
                        "sermon_http_check_duration_seconds",
                        response.elapsed.as_secs_f64(),
                    )
                    .label("target", label),
                );
                if let Some(validity) = response
                    .certificate
                    .as_ref()
                    .and_then(|c| crate::x509::validity(c))
                {
                    samples.push(
                        Sample::int(
                            "sermon_http_check_cert_expires_in_seconds",
                            validity.not_after.saturating_sub(now),
                        )
                        .label("target", label),
                    );
                }
            }
            Ok(samples)
        })
    }
}

#[cfg(test)]
mod tests;
