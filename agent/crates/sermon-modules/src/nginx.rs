//! nginx (M5-11): connections and requests from its `stub_status` page, through the modules'
//! HTTP client (inside `module_targets`, at most 1 MiB, redirects checked). The page must be
//! enabled in nginx (a `location` with `stub_status;`); the help text says how.
//!
//! | Metric | Unit |
//! |---|---|
//! | `sermon_nginx_up` | 1 if the page was read, 0 if nginx doesn't answer |
//! | `sermon_nginx_connections_active` | open client connections (`Active connections`) |
//! | `sermon_nginx_connections_reading`, `…_writing`, `…_waiting` | connections reading a request, writing an answer, idle (keep-alive) |
//! | `sermon_nginx_connections_accepted_per_second`, `…_handled_per_second` | new connections |
//! | `sermon_nginx_connections_dropped_per_second` | accepted but not handled (`worker_connections` exhausted) |
//! | `sermon_nginx_requests_per_second` | requests |
//!
//! No labels: one nginx per node. Rates start with the second run and pause after a restart of
//! nginx (its counters start at 0). An answer that is no `stub_status` page (404: not enabled) is
//! an error of the module, not nginx being down.

use std::sync::Arc;
use std::time::Duration;

use sermon_core::Sample;

use crate::discovery::{Discovered, HostFacts};
use crate::http::{Client, Url};
use crate::page;
use crate::rate::Counter;
use crate::{BoxFuture, BuildError, Config, Instance, Module, Run, RunError, Setup};

const SCHEMA: &str = include_str!("../schemas/nginx.json");

/// Larger answers are no `stub_status` page.
const MAX_PAGE: usize = 4096;

/// The module `nginx`.
#[derive(Debug, Clone, Copy, Default)]
pub struct Nginx;

impl Module for Nginx {
    fn id(&self) -> &'static str {
        "nginx"
    }

    fn schema(&self) -> &'static str {
        SCHEMA
    }

    fn build(&self, config: &Config, setup: &Setup<'_>) -> Result<Box<dyn Instance>, BuildError> {
        let (client, url, timeout) =
            page::client(config, setup, "http://127.0.0.1/nginx_status", "text/plain")?;
        Ok(Box::new(Status {
            client: Arc::new(client),
            url,
            timeout,
            counters: Counters::default(),
        }))
    }

    fn discover(&self, facts: &HostFacts) -> Option<Discovered> {
        let mut found = Discovered::evidence(facts, &["nginx"], &["nginx.service"], &[80, 443])?;
        found.suggested = facts
            .listener(80)
            .map(|l| serde_json::json!({ "url": l.http_url("/nginx_status") }));
        Some(found)
    }
}

/// A `stub_status` page.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct StubStatus {
    /// `Active connections`.
    pub active: u64,
    /// Connections accepted, since nginx started.
    pub accepts: u64,
    /// Connections handled.
    pub handled: u64,
    /// Requests.
    pub requests: u64,
    /// Connections reading a request.
    pub reading: u64,
    /// Connections writing an answer.
    pub writing: u64,
    /// Idle connections.
    pub waiting: u64,
}

/// Parses a `stub_status` page (the same format since nginx 1.x).
///
/// # Errors
///
/// What makes it no `stub_status` page.
pub fn parse_stub_status(text: &str) -> Result<StubStatus, String> {
    let not = || "not a stub_status page".to_owned();
    if text.len() > MAX_PAGE {
        return Err(not());
    }
    let mut lines = text.lines().map(str::trim).filter(|l| !l.is_empty());
    let active = lines
        .next()
        .and_then(|l| l.strip_prefix("Active connections:"))
        .and_then(|n| n.trim().parse().ok())
        .ok_or_else(not)?;
    if lines.next() != Some("server accepts handled requests") {
        return Err(not());
    }
    let counters: Vec<u64> = lines
        .next()
        .ok_or_else(not)?
        .split_whitespace()
        .map(str::parse)
        .collect::<Result<_, _>>()
        .map_err(|_| not())?;
    let [accepts, handled, requests] = counters[..] else {
        return Err(not());
    };
    let words: Vec<&str> = lines.next().ok_or_else(not)?.split_whitespace().collect();
    let ["Reading:", read, "Writing:", write, "Waiting:", idle] = words[..] else {
        return Err(not());
    };
    if lines.next().is_some() {
        return Err(not());
    }
    let number = |s: &str| s.parse::<u64>().map_err(|_| not());
    Ok(StubStatus {
        active,
        accepts,
        handled,
        requests,
        reading: number(read)?,
        writing: number(write)?,
        waiting: number(idle)?,
    })
}

#[derive(Debug, Default)]
struct Counters {
    accepted: Counter,
    handled: Counter,
    dropped: Counter,
    requests: Counter,
}

struct Status {
    client: Arc<Client>,
    url: Url,
    timeout: Duration,
    counters: Counters,
}

impl Instance for Status {
    fn timeout(&self) -> Duration {
        self.timeout + Duration::from_secs(2)
    }

    fn collect<'a>(&'a mut self, run: &'a Run) -> BoxFuture<'a, Result<Vec<Sample>, RunError>> {
        Box::pin(async move {
            let hint = "is stub_status enabled there?";
            let Some(text) = page::read(&self.client, &self.url, hint).await? else {
                // nginx doesn't answer: it is down (or the URL points nowhere).
                self.counters = Counters::default();
                return Ok(vec![Sample::int("sermon_nginx_up", 0)]);
            };
            let s = parse_stub_status(&text)
                .map_err(|e| RunError::Failed(format!("{}: {e}", self.url.label())))?;
            let now = run.monotonic();
            let gauge = |metric, n: u64| Sample::int(metric, i64::try_from(n).unwrap_or(i64::MAX));
            let mut samples = vec![
                Sample::int("sermon_nginx_up", 1),
                gauge("sermon_nginx_connections_active", s.active),
                gauge("sermon_nginx_connections_reading", s.reading),
                gauge("sermon_nginx_connections_writing", s.writing),
                gauge("sermon_nginx_connections_waiting", s.waiting),
            ];
            let c = &mut self.counters;
            let rates = [
                (
                    "sermon_nginx_connections_accepted_per_second",
                    c.accepted.per_second(s.accepts, now),
                ),
                (
                    "sermon_nginx_connections_handled_per_second",
                    c.handled.per_second(s.handled, now),
                ),
                (
                    "sermon_nginx_connections_dropped_per_second",
                    c.dropped
                        .per_second(s.accepts.saturating_sub(s.handled), now),
                ),
                (
                    "sermon_nginx_requests_per_second",
                    c.requests.per_second(s.requests, now),
                ),
            ];
            for (metric, rate) in rates {
                if let Some(rate) = rate {
                    samples.push(Sample::float(metric, rate));
                }
            }
            Ok(samples)
        })
    }
}

#[cfg(test)]
mod tests;
