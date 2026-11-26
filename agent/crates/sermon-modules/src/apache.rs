//! Apache httpd (M5-12): workers, connections and traffic from `server-status?auto`
//! (`mod_status`), through the modules' HTTP client (inside `module_targets`, at most 1 MiB).
//! Debian enables the page for localhost; on the RHEL family it needs a `<Location>` – the help
//! text says how.
//!
//! | Metric | Unit |
//! |---|---|
//! | `sermon_apache_up` | 1 if the page was read, 0 if Apache doesn't answer |
//! | `sermon_apache_workers_busy`, `…_idle` | workers serving a request, waiting for one |
//! | `sermon_apache_workers_max` | slots of the scoreboard (`ServerLimit` × threads): busy at max means requests queue |
//! | `sermon_apache_processes` | child processes (event and worker MPM) |
//! | `sermon_apache_connections` | open connections (event MPM) |
//! | `sermon_apache_requests_per_second` | requests (`Total Accesses`, with `ExtendedStatus`) |
//! | `sermon_apache_sent_bytes_per_second` | bytes sent (`Total kBytes`) |
//!
//! No labels: one Apache per node. Rates start with the second run and pause after a restart
//! (the counters start at 0) and an outage. What the prefork MPM or `ExtendedStatus Off` don't
//! show is left out.

use std::sync::Arc;
use std::time::Duration;

use sermon_core::Sample;

use crate::discovery::{Discovered, HostFacts};
use crate::http::{Client, Url};
use crate::page;
use crate::rate::Counter;
use crate::{BoxFuture, BuildError, Config, Instance, Module, Run, RunError, Setup};

const SCHEMA: &str = include_str!("../schemas/apache.json");

/// Larger answers are no `server-status?auto` page.
const MAX_PAGE: usize = 64 * 1024;
/// The characters of the scoreboard (`mod_status`).
const SCOREBOARD: &[u8] = b"_SRWKDCLGI.";

/// The module `apache`.
#[derive(Debug, Clone, Copy, Default)]
pub struct Apache;

impl Module for Apache {
    fn id(&self) -> &'static str {
        "apache"
    }

    fn schema(&self) -> &'static str {
        SCHEMA
    }

    fn build(&self, config: &Config, setup: &Setup<'_>) -> Result<Box<dyn Instance>, BuildError> {
        let (client, url, timeout) = page::client(
            config,
            setup,
            "http://127.0.0.1/server-status?auto",
            Some("auto"),
            "text/plain",
        )?;
        Ok(Box::new(Status {
            client: Arc::new(client),
            url,
            timeout,
            counters: Counters::default(),
        }))
    }

    fn discover(&self, facts: &HostFacts) -> Option<Discovered> {
        let mut found = Discovered::evidence(
            facts,
            &["apache2", "httpd"],
            &["apache2.service", "httpd.service"],
            &[80, 443],
        )?;
        found.suggested = facts
            .listener(80)
            .map(|l| serde_json::json!({ "url": l.http_url("/server-status?auto") }));
        Some(found)
    }
}

/// A `server-status?auto` page.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct ServerStatus {
    /// `BusyWorkers`.
    pub busy: u64,
    /// `IdleWorkers`.
    pub idle: u64,
    /// The slots of the scoreboard.
    pub slots: u64,
    /// `Processes` (not with prefork).
    pub processes: Option<u64>,
    /// `ConnsTotal` (event MPM).
    pub connections: Option<u64>,
    /// `Total Accesses` (with `ExtendedStatus`).
    pub accesses: Option<u64>,
    /// `Total kBytes`.
    pub kbytes: Option<u64>,
}

/// Parses `server-status?auto` (Apache 2.4).
///
/// # Errors
///
/// What makes it no such page – most often the HTML page without `?auto`.
pub fn parse_server_status(text: &str) -> Result<ServerStatus, String> {
    if text.len() > MAX_PAGE {
        return Err("not a server-status?auto page".into());
    }
    if text.trim_start().starts_with('<') {
        return Err("an HTML page: add ?auto to the URL".into());
    }
    let mut status = ServerStatus::default();
    let (mut busy, mut idle, mut scoreboard) = (None, None, None);
    for line in text.lines() {
        let Some((key, value)) = line.split_once(": ") else {
            continue;
        };
        let value = value.trim();
        let number = || value.parse::<u64>().ok();
        match key {
            "BusyWorkers" => busy = number(),
            "IdleWorkers" => idle = number(),
            "Processes" => status.processes = number(),
            "ConnsTotal" => status.connections = number(),
            "Total Accesses" => status.accesses = number(),
            "Total kBytes" => status.kbytes = number(),
            "Scoreboard" if value.bytes().all(|b| SCOREBOARD.contains(&b)) => {
                scoreboard = u64::try_from(value.len()).ok();
            }
            _ => {}
        }
    }
    match (busy, idle, scoreboard) {
        (Some(busy), Some(idle), Some(slots)) if slots > 0 => Ok(ServerStatus {
            busy,
            idle,
            slots,
            ..status
        }),
        _ => Err("not a server-status?auto page".into()),
    }
}

#[derive(Debug, Default)]
struct Counters {
    requests: Counter,
    bytes: Counter,
}

struct Status {
    client: Arc<Client>,
    url: Url,
    timeout: Duration,
    counters: Counters,
}

fn gauge(metric: &'static str, n: u64) -> Sample {
    Sample::int(metric, i64::try_from(n).unwrap_or(i64::MAX))
}

impl Instance for Status {
    fn timeout(&self) -> Duration {
        self.timeout + Duration::from_secs(2)
    }

    fn collect<'a>(&'a mut self, run: &'a Run) -> BoxFuture<'a, Result<Vec<Sample>, RunError>> {
        Box::pin(async move {
            let hint = "is mod_status enabled there, for this host?";
            let Some(text) = page::read(&self.client, &self.url, hint).await? else {
                self.counters = Counters::default();
                return Ok(vec![Sample::int("sermon_apache_up", 0)]);
            };
            let s = parse_server_status(&text)
                .map_err(|e| RunError::Failed(format!("{}: {e}", self.url.label())))?;
            let now = run.monotonic();
            let mut samples = vec![
                Sample::int("sermon_apache_up", 1),
                gauge("sermon_apache_workers_busy", s.busy),
                gauge("sermon_apache_workers_idle", s.idle),
                gauge("sermon_apache_workers_max", s.slots),
            ];
            if let Some(n) = s.processes {
                samples.push(gauge("sermon_apache_processes", n));
            }
            if let Some(n) = s.connections {
                samples.push(gauge("sermon_apache_connections", n));
            }
            let c = &mut self.counters;
            if let Some(rate) = s.accesses.and_then(|n| c.requests.per_second(n, now)) {
                samples.push(Sample::float("sermon_apache_requests_per_second", rate));
            }
            let bytes = s.kbytes.map(|k| k.saturating_mul(1024));
            if let Some(rate) = bytes.and_then(|n| c.bytes.per_second(n, now)) {
                samples.push(Sample::float("sermon_apache_sent_bytes_per_second", rate));
            }
            Ok(samples)
        })
    }
}

#[cfg(test)]
mod tests;
