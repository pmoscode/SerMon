//! Docker over the socket (M5-17): state, health and restarts of the containers from the Docker
//! API – also Podman's compatible API. Read only: the module sends two kinds of request, both
//! `GET` (the client knows no other method): the list of all containers and the inspection of
//! one by its checked ID.
//!
//! Whoever may use the socket is root on the host (O4). The veto keeps `docker.sock`,
//! `podman.sock` and `containerd.sock` closed until `allow_docker_socket = true` in `agent.toml`;
//! the socket is checked at every request. The cgroup module `docker` (M5-16) needs no socket.
//!
//! | Metric | Labels | Unit |
//! |---|---|---|
//! | `sermon_docker_socket_up` | – | 1 if the API answered, 0 if not |
//! | `sermon_docker_socket_containers` | `state` | containers by state, all of them |
//! | `sermon_docker_socket_truncated` | – | 1: more containers than reported (`max_containers`) |
//! | `sermon_docker_socket_running` | `container`, `name` | 1 if running |
//! | `sermon_docker_socket_healthy` | `container`, `name` | with a health check: 1 healthy, 0 unhealthy (not while starting) |
//! | `sermon_docker_socket_restarts` | `container`, `name` | restarts by its restart policy (`RestartCount`) |
//! | `sermon_docker_socket_oom_killed` | `container`, `name` | 1 if the OOM killer ended it last |
//!
//! `container` is the short ID of `docker ps` (as in the module `docker`), `name` its name.

use std::path::PathBuf;
use std::time::Duration;

use serde_json::Value;
use sermon_core::Sample;

use crate::fields::timeout;
use crate::http::{Client, HttpError, Options};
use crate::runtime::{MAX_BUDGET, MAX_SAMPLES};
use crate::{BoxFuture, BuildError, Config, Instance, Module, Run, RunError, Setup};

const SCHEMA: &str = include_str!("../schemas/docker_socket.json");

/// The states of a container in the API.
pub const STATES: [&str; 7] = [
    "created",
    "running",
    "paused",
    "restarting",
    "removing",
    "exited",
    "dead",
];
/// Samples of a container at most.
const PER_CONTAINER: usize = 4;
/// Samples besides the containers': up, the states, truncated.
const TOTALS: usize = 2 + STATES.len();
/// Most containers reported.
pub const MAX_CONTAINERS: usize = (MAX_SAMPLES - TOTALS) / PER_CONTAINER;
const _: () = assert!(TOTALS + MAX_CONTAINERS * PER_CONTAINER <= MAX_SAMPLES);
/// The longest name taken (the protocol's bound of a label).
const MAX_NAME: usize = 256;

/// The module `docker_socket`.
#[derive(Debug, Clone, Copy, Default)]
pub struct DockerSocket;

impl Module for DockerSocket {
    fn id(&self) -> &'static str {
        "docker_socket"
    }

    fn schema(&self) -> &'static str {
        SCHEMA
    }

    fn build(&self, config: &Config, setup: &Setup<'_>) -> Result<Box<dyn Instance>, BuildError> {
        let socket = PathBuf::from(
            config
                .get("socket")
                .and_then(Value::as_str)
                .unwrap_or("/var/run/docker.sock"),
        );
        setup.veto.socket(&socket)?;
        let max = config
            .get("max_containers")
            .and_then(Value::as_u64)
            .and_then(|m| usize::try_from(m).ok())
            .unwrap_or(30);
        let timeout = timeout(config, "timeout", Duration::from_secs(5))?;
        let client = Client::new(
            setup.veto,
            Options {
                timeout,
                accept: "application/json",
                ..Options::default()
            },
        )?;
        Ok(Box::new(Api {
            client,
            socket,
            max,
            timeout,
        }))
    }
}

/// A container of the list.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Listed {
    /// The full ID (64 hex digits).
    pub id: String,
    /// The name without its `/`.
    pub name: String,
    /// One of [`STATES`], or `None` for an unknown state.
    pub state: Option<&'static str>,
}

/// What the inspection of a container says.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Inspected {
    /// `State.Running`.
    pub running: bool,
    /// `State.Health.Status` if it has a health check.
    pub health: Option<String>,
    /// `RestartCount`.
    pub restarts: u64,
    /// `State.OOMKilled`.
    pub oom_killed: bool,
}

fn valid_id(id: &str) -> bool {
    id.len() == 64
        && id
            .bytes()
            .all(|b| b.is_ascii_hexdigit() && !b.is_ascii_uppercase())
}

/// The list of `GET /containers/json?all=1`; entries without a valid ID are skipped.
///
/// # Errors
///
/// If it is no JSON array.
pub fn parse_list(body: &[u8]) -> Result<Vec<Listed>, String> {
    let value: Value = serde_json::from_slice(body).map_err(|_| "not JSON of the Docker API")?;
    let list = value.as_array().ok_or("not a list of containers")?;
    Ok(list
        .iter()
        .filter_map(|c| {
            let id = c.get("Id")?.as_str().filter(|id| valid_id(id))?;
            let name = c
                .get("Names")
                .and_then(Value::as_array)
                .and_then(|n| n.first())
                .and_then(Value::as_str)
                .map(|n| n.trim_start_matches('/'))
                .filter(|n| n.len() <= MAX_NAME && !n.chars().any(char::is_control))
                .unwrap_or_default();
            let state = c.get("State").and_then(Value::as_str);
            Some(Listed {
                id: id.to_owned(),
                name: name.to_owned(),
                state: STATES.into_iter().find(|s| Some(*s) == state),
            })
        })
        .collect())
}

/// The inspection of `GET /containers/<id>/json`.
///
/// # Errors
///
/// If it is no JSON object with a `State`.
pub fn parse_inspect(body: &[u8]) -> Result<Inspected, String> {
    let value: Value = serde_json::from_slice(body).map_err(|_| "not JSON of the Docker API")?;
    let state = value.get("State").ok_or("no state of a container")?;
    Ok(Inspected {
        running: state
            .get("Running")
            .and_then(Value::as_bool)
            .unwrap_or(false),
        health: state
            .get("Health")
            .and_then(|h| h.get("Status"))
            .and_then(Value::as_str)
            .filter(|s| ["starting", "healthy", "unhealthy"].contains(s))
            .map(str::to_owned),
        restarts: value
            .get("RestartCount")
            .and_then(Value::as_u64)
            .unwrap_or(0),
        oom_killed: state
            .get("OOMKilled")
            .and_then(Value::as_bool)
            .unwrap_or(false),
    })
}

struct Api {
    client: Client,
    socket: PathBuf,
    max: usize,
    timeout: Duration,
}

impl Api {
    /// `GET path`; `None` if the API doesn't answer.
    async fn get(&self, path: &str) -> Result<Option<Vec<u8>>, RunError> {
        match self.client.get_unix(&self.socket, path, true).await {
            Ok(r) if r.status == 200 => Ok(Some(r.body.to_vec())),
            Ok(r) => Err(RunError::Failed(format!("{path}: status {}", r.status))),
            Err(HttpError::Unreachable(_)) => Ok(None),
            Err(e) => Err(e.into()),
        }
    }
}

impl Instance for Api {
    fn timeout(&self) -> Duration {
        (self.timeout + Duration::from_secs(5)).min(MAX_BUDGET)
    }

    fn collect<'a>(&'a mut self, _: &'a Run) -> BoxFuture<'a, Result<Vec<Sample>, RunError>> {
        Box::pin(async move {
            let Some(body) = self.get("/containers/json?all=1").await? else {
                return Ok(vec![Sample::int("sermon_docker_socket_up", 0)]);
            };
            let mut list = parse_list(&body).map_err(RunError::Failed)?;
            let mut samples = vec![Sample::int("sermon_docker_socket_up", 1)];
            for state in STATES {
                let n = list.iter().filter(|c| c.state == Some(state)).count();
                samples.push(
                    Sample::int(
                        "sermon_docker_socket_containers",
                        i64::try_from(n).unwrap_or(i64::MAX),
                    )
                    .label("state", state),
                );
            }
            // Running ones first, then by name: a stable choice from run to run.
            list.sort_by(|a, b| {
                (a.state != Some("running"), &a.name).cmp(&(b.state != Some("running"), &b.name))
            });
            samples.push(Sample::int(
                "sermon_docker_socket_truncated",
                i64::from(list.len() > self.max),
            ));
            for c in list.iter().take(self.max) {
                // Gone since the list: nothing to report.
                let Ok(Some(body)) = self.get(&format!("/containers/{}/json", c.id)).await else {
                    continue;
                };
                let Ok(i) = parse_inspect(&body) else {
                    continue;
                };
                let short = &c.id[..12];
                let label = |s: Sample| s.label("container", short).label("name", c.name.clone());
                samples.push(label(Sample::int(
                    "sermon_docker_socket_running",
                    i64::from(i.running),
                )));
                if let Some(h) = i.health.as_deref().filter(|h| *h != "starting") {
                    samples.push(label(Sample::int(
                        "sermon_docker_socket_healthy",
                        i64::from(h == "healthy"),
                    )));
                }
                samples.push(label(Sample::int(
                    "sermon_docker_socket_restarts",
                    i64::try_from(i.restarts).unwrap_or(i64::MAX),
                )));
                samples.push(label(Sample::int(
                    "sermon_docker_socket_oom_killed",
                    i64::from(i.oom_killed),
                )));
            }
            Ok(samples)
        })
    }
}

#[cfg(test)]
mod tests;
