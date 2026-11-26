//! The Prometheus scraper (M5-19): series of `/metrics` endpoints inside `module_targets`,
//! through the modules' HTTP client (veto, redirects, 1 MiB, the bearer token only to its host).
//!
//! The catalog of SerMon is closed (KONZEPT §8: every metric known, labels bounded), so foreign
//! names don't become metrics of their own: every series lands in one of two fixed metrics, the
//! foreign name and labels in the labels `name` and `series` – nothing can collide with the
//! agent's metrics, and the server needs no new catalog per endpoint.
//!
//! | Metric | Labels | Unit |
//! |---|---|---|
//! | `sermon_prometheus_up` | `target` | 1 if the page was read, 0 if the endpoint doesn't answer |
//! | `sermon_prometheus_series` | `target` | series taken |
//! | `sermon_prometheus_dropped_series` | `target` | series left out: over the limit, labels too long, broken lines |
//! | `sermon_prometheus_gauge` | `target`, `name`, `series` | gauges, untyped values, quantiles, info and stateset series |
//! | `sermon_prometheus_rate` | `target`, `name`, `series` | counters and `_count`/`_sum` of histograms and summaries, per second |
//!
//! `target` is the URL without its query, `name` the foreign metric name, `series` its labels
//! sorted and escaped as in the format (`code="200",method="GET"`), at most 256 bytes. Buckets
//! and `_created` are left out; rates start with the second run and pause after a reset.

pub mod expo;

use std::collections::BTreeMap;
use std::sync::Arc;
use std::time::Duration;

use serde_json::Value;
use sermon_core::{Monotonic, Sample};
use tokio::task::JoinSet;

use crate::fields::{strings, timeout};
use crate::http::{Client, HttpError, Options, Url};
use crate::runtime::MAX_SAMPLES;
use crate::{BoxFuture, BuildError, Config, Instance, Module, Run, RunError, Setup};

const SCHEMA: &str = include_str!("../schemas/prometheus.json");

/// Longest value of a label (the protocol's bound).
const MAX_LABEL: usize = 256;
/// Samples of an endpoint besides its series (`up`, `series`, `dropped_series`).
const PER_TARGET: usize = 3;

const ACCEPT: &str =
    "application/openmetrics-text;version=1.0.0;q=0.9,text/plain;version=0.0.4;q=0.5";

/// The module `prometheus`.
#[derive(Debug, Clone, Copy, Default)]
pub struct Prometheus;

impl Module for Prometheus {
    fn id(&self) -> &'static str {
        "prometheus"
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
            return Err(BuildError::Invalid("no endpoint".into()));
        }
        let max_series = config
            .get("max_series")
            .and_then(Value::as_u64)
            .and_then(|n| usize::try_from(n).ok())
            .unwrap_or(50);
        if targets.len() * (max_series + PER_TARGET) > MAX_SAMPLES {
            return Err(BuildError::Invalid(format!(
                "{} endpoints × {max_series} series exceed {MAX_SAMPLES} samples a run: lower max_series",
                targets.len()
            )));
        }
        let timeout = timeout(config, "timeout", Duration::from_secs(5))?;
        let client = Client::new(
            setup.veto,
            Options {
                timeout,
                tls_verify: config
                    .get("tls_verify")
                    .and_then(Value::as_bool)
                    .unwrap_or(true),
                ca_file: config
                    .get("ca_file")
                    .and_then(Value::as_str)
                    .map(Into::into),
                authorization: setup.secrets.get("bearer").map(|t| format!("Bearer {t}")),
                accept: ACCEPT,
                ..Options::default()
            },
        )?;
        Ok(Box::new(Scraper {
            client: Arc::new(client),
            targets,
            filter: strings(config, "metrics").map(str::to_owned).collect(),
            max_series,
            timeout,
            last: BTreeMap::new(),
            known: BTreeMap::new(),
        }))
    }
}

/// Whether `name` is taken by `filter` (exact names, `prefix*`; empty: all).
fn wanted(filter: &[String], name: &str) -> bool {
    filter.is_empty()
        || filter.iter().any(|f| match f.strip_suffix('*') {
            Some(prefix) => name.starts_with(prefix),
            None => f == name,
        })
}

/// How long a series of an endpoint keeps its place while it is absent.
const KEEP_FOR: Duration = Duration::from_secs(3600);

/// The series an endpoint has a place for (name and labels), with when each was last seen.
type Known = BTreeMap<(String, String), Monotonic>;

/// The series of a page within the limits: kept, and how many were left out. The places of an
/// endpoint are a stable set (`known`, at most `max`, M5-33): a known series keeps its place, a
/// new one gets one only while places are free, and a place frees up after [`KEEP_FOR`] without
/// the series. So an exporter whose labels follow outside input (request paths, user IDs) can't
/// fill the node's series budget on the server run after run.
fn select(
    page: expo::Page,
    filter: &[String],
    max: usize,
    known: &mut Known,
    now: Monotonic,
) -> (Vec<(String, String, f64, bool)>, usize) {
    known.retain(|_, seen| now.saturating_duration_since(*seen) <= KEEP_FOR);
    let mut kept = Vec::new();
    let mut dropped = page.broken;
    for s in page.series {
        if !wanted(filter, &s.name) {
            continue;
        }
        let series = expo::canonical(&s.labels);
        let fits = s.name.len() <= MAX_LABEL && series.len() <= MAX_LABEL;
        let key = (s.name, series);
        let duplicate = kept.iter().any(|(n, l, _, _)| *n == key.0 && *l == key.1);
        let place = known.contains_key(&key) || known.len() < max;
        if !fits || duplicate || !place {
            dropped += 1;
            continue;
        }
        known.insert(key.clone(), now);
        kept.push((key.0, key.1, s.value, s.counter));
    }
    (kept, dropped)
}

struct Scraper {
    client: Arc<Client>,
    targets: Vec<(Url, String)>,
    filter: Vec<String>,
    max_series: usize,
    timeout: Duration,
    /// The last value of every counter taken, by endpoint, name and labels.
    last: BTreeMap<(usize, String, String), (f64, Monotonic)>,
    /// The places of the series, by endpoint.
    known: BTreeMap<usize, Known>,
}

impl Instance for Scraper {
    fn timeout(&self) -> Duration {
        self.timeout + Duration::from_secs(2)
    }

    fn collect<'a>(&'a mut self, run: &'a Run) -> BoxFuture<'a, Result<Vec<Sample>, RunError>> {
        Box::pin(async move {
            let mut asks = JoinSet::new();
            for (i, (url, _)) in self.targets.iter().enumerate() {
                let (client, url) = (self.client.clone(), url.clone());
                asks.spawn(async move { (i, client.get(&url, true).await) });
            }
            let mut answers = Vec::with_capacity(self.targets.len());
            while let Some(done) = asks.join_next().await {
                answers.push(done.map_err(|e| RunError::Failed(format!("a scrape failed: {e}")))?);
            }
            answers.sort_by_key(|(i, _)| *i);
            let now = run.monotonic();
            let mut samples = Vec::new();
            let mut seen = BTreeMap::new();
            for (i, answer) in answers {
                let target = self.targets[i].1.clone();
                let text = match answer {
                    Err(HttpError::Vetoed(r)) => return Err(RunError::Vetoed(r)),
                    Err(HttpError::Failed(why)) => return Err(RunError::Failed(why)),
                    Err(HttpError::Unreachable(_)) => {
                        samples
                            .push(Sample::int("sermon_prometheus_up", 0).label("target", target));
                        continue;
                    }
                    Ok(r) if r.status != 200 => {
                        return Err(RunError::Failed(format!("{target}: status {}", r.status)));
                    }
                    Ok(r) => String::from_utf8_lossy(&r.body).into_owned(),
                };
                let page = expo::parse(&text);
                if page.series.is_empty() && page.broken > 0 {
                    return Err(RunError::Failed(format!(
                        "{target}: not the Prometheus text format"
                    )));
                }
                let known = self.known.entry(i).or_default();
                let (kept, dropped) = select(page, &self.filter, self.max_series, known, now);
                let count = |n: usize| i64::try_from(n).unwrap_or(i64::MAX);
                samples
                    .push(Sample::int("sermon_prometheus_up", 1).label("target", target.clone()));
                samples.push(
                    Sample::int("sermon_prometheus_series", count(kept.len()))
                        .label("target", target.clone()),
                );
                samples.push(
                    Sample::int("sermon_prometheus_dropped_series", count(dropped))
                        .label("target", target.clone()),
                );
                for (name, series, value, counter) in kept {
                    let sample = if counter {
                        let key = (i, name.clone(), series.clone());
                        let rate = self.last.get(&key).and_then(|(before, then)| {
                            let elapsed = now.saturating_duration_since(*then).as_secs_f64();
                            (value >= *before && elapsed >= 0.1).then(|| (value - before) / elapsed)
                        });
                        seen.insert(key, (value, now));
                        let Some(rate) = rate else {
                            continue;
                        };
                        Sample::float("sermon_prometheus_rate", rate)
                    } else {
                        Sample::float("sermon_prometheus_gauge", value)
                    };
                    samples.push(
                        sample
                            .label("target", target.clone())
                            .label("name", name)
                            .label("series", series),
                    );
                }
            }
            // Only the counters of this run: an endpoint that is down starts anew.
            self.last = seen;
            Ok(samples)
        })
    }
}

#[cfg(test)]
mod tests;
