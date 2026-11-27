//! Redis and Valkey (M5-15): memory, clients, commands, hit rate, evictions and replication
//! from `INFO`, over TCP inside `module_targets` or a Unix socket below `module_sockets`, over
//! TCP also with TLS (`tls`, `ca_file`; the `tls-port` of Redis 6 and later, V1-13) as the
//! database modules have it ([`crate::sql::Security`]). The module speaks RESP itself: `AUTH` if
//! a password is set, then `INFO` – nothing that changes data. The password (`auth`) is a
//! credential (`writeOnly`), kept in memory only (M5-05) and wiped when dropped.
//!
//! | Metric | Unit |
//! |---|---|
//! | `sermon_redis_up` | 1 if `INFO` was read, 0 if Redis doesn't answer |
//! | `sermon_redis_memory_used_bytes`, `…_rss_bytes`, `…_max_bytes` | `used_memory`, `used_memory_rss`, `maxmemory` (only if set) |
//! | `sermon_redis_clients_connected`, `…_blocked` | clients |
//! | `sermon_redis_commands_per_second` | `total_commands_processed` |
//! | `sermon_redis_keyspace_hits_per_second`, `…_misses_per_second`, `…_hit_ratio` | lookups of keys, hits of them (ppm) |
//! | `sermon_redis_evicted_keys_per_second`, `…_expired_keys_per_second` | keys removed for `maxmemory`, by expiry |
//! | `sermon_redis_rejected_connections_per_second` | `maxclients` reached |
//! | `sermon_redis_keys` | keys of all databases |
//! | `sermon_redis_connected_replicas` | replicas of a primary |
//! | `sermon_redis_master_link_up` | a replica: 1 if the link to its primary is up |
//! | `sermon_redis_rdb_last_save_ok` | the last snapshot (`BGSAVE`) succeeded |
//!
//! No labels: one Redis per module. Rates from the second run, paused after a restart (the
//! counters start at 0) and an outage.

use std::time::Duration;

use serde_json::Value;
use sermon_core::Sample;
use tokio::io::{AsyncBufReadExt, AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt, BufReader};

use crate::discovery::{Discovered, HostFacts};
use crate::fields::timeout;
use crate::rate::Counter;
use crate::sql::{Connection, Security, Target, Tls};
use crate::{BoxFuture, BuildError, Config, Instance, Module, Run, RunError, Setup, Veto};

const SCHEMA: &str = include_str!("../schemas/redis.json");

/// Largest `INFO` taken (the modules' bound for an answer).
pub const MAX_INFO: usize = 1024 * 1024;
/// Longest line of a reply's head (`+OK`, `-ERR …`, `$1234`).
const MAX_HEAD: u64 = 1024;
/// Longest error text of Redis taken into a reason.
const MAX_ERROR: usize = 160;

/// The module `redis`.
#[derive(Debug, Clone, Copy, Default)]
pub struct Redis;

impl Module for Redis {
    fn id(&self) -> &'static str {
        "redis"
    }

    fn schema(&self) -> &'static str {
        SCHEMA
    }

    fn build(&self, config: &Config, setup: &Setup<'_>) -> Result<Box<dyn Instance>, BuildError> {
        let target = Target::of(config, setup.veto)?;
        let security = Security::of(config, &target, setup.veto)?;
        let password = setup.secrets.secret("auth");
        let username = config
            .get("username")
            .and_then(Value::as_str)
            .map(str::to_owned);
        if username.is_some() && password.is_none() {
            return Err(BuildError::Invalid("a user needs its password".into()));
        }
        Ok(Box::new(Info {
            veto: setup.veto.clone(),
            target,
            security,
            login: password.map(|p| Login {
                user: username,
                password: p,
            }),
            timeout: timeout(config, "timeout", Duration::from_secs(5))?,
            counters: Counters::default(),
        }))
    }

    fn discover(&self, facts: &HostFacts) -> Option<Discovered> {
        let mut found = Discovered::evidence(
            facts,
            &["redis-server", "valkey-server"],
            &[
                "redis-server.service",
                "redis.service",
                "valkey.service",
                "valkey-server.service",
            ],
            &[6379],
        )?;
        found.suggested = facts.listener(6379).map(|l| {
            let host = match l.reach() {
                std::net::IpAddr::V6(v6) => format!("[{v6}]"),
                std::net::IpAddr::V4(v4) => v4.to_string(),
            };
            serde_json::json!({ "address": format!("{host}:6379") })
        });
        if found.suggested.is_none() {
            found.suggested = facts
                .sockets
                .iter()
                .find(|s| s.ends_with("/redis.sock") || s.ends_with("/redis-server.sock"))
                .map(|s| serde_json::json!({ "socket": s }));
        }
        Some(found)
    }
}

struct Login {
    user: Option<String>,
    password: crate::Secret,
}

/// What a module needs of `INFO`.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct InfoFields {
    /// `used_memory`.
    pub used_memory: Option<u64>,
    /// `used_memory_rss`.
    pub used_memory_rss: Option<u64>,
    /// `maxmemory` (0: no limit).
    pub maxmemory: Option<u64>,
    /// `connected_clients`.
    pub connected_clients: Option<u64>,
    /// `blocked_clients`.
    pub blocked_clients: Option<u64>,
    /// `total_commands_processed`.
    pub commands: Option<u64>,
    /// `keyspace_hits`.
    pub hits: Option<u64>,
    /// `keyspace_misses`.
    pub misses: Option<u64>,
    /// `evicted_keys`.
    pub evicted: Option<u64>,
    /// `expired_keys`.
    pub expired: Option<u64>,
    /// `rejected_connections`.
    pub rejected: Option<u64>,
    /// `keys=` of all `dbN` lines.
    pub keys: u64,
    /// `role:master` (or `slave`).
    pub primary: Option<bool>,
    /// `connected_slaves`.
    pub replicas: Option<u64>,
    /// `master_link_status` of a replica.
    pub master_link_up: Option<bool>,
    /// `rdb_last_bgsave_status`.
    pub rdb_ok: Option<bool>,
}

/// Parses the text of `INFO` (`key:value` lines in `# Section`s); unknown keys are skipped.
///
/// # Errors
///
/// If it holds none of the fields every Redis has (no `INFO`).
pub fn parse_info(text: &str) -> Result<InfoFields, String> {
    if text.len() > MAX_INFO {
        return Err("INFO is larger than 1 MiB".into());
    }
    let mut f = InfoFields::default();
    for line in text.lines() {
        let line = line.trim_end_matches('\r');
        let Some((key, value)) = line.split_once(':') else {
            continue;
        };
        let n = || value.parse::<u64>().ok();
        match key {
            "used_memory" => f.used_memory = n(),
            "used_memory_rss" => f.used_memory_rss = n(),
            "maxmemory" => f.maxmemory = n(),
            "connected_clients" => f.connected_clients = n(),
            "blocked_clients" => f.blocked_clients = n(),
            "total_commands_processed" => f.commands = n(),
            "keyspace_hits" => f.hits = n(),
            "keyspace_misses" => f.misses = n(),
            "evicted_keys" => f.evicted = n(),
            "expired_keys" => f.expired = n(),
            "rejected_connections" => f.rejected = n(),
            "role" => f.primary = Some(value == "master"),
            "connected_slaves" => f.replicas = n(),
            "master_link_status" => f.master_link_up = Some(value == "up"),
            "rdb_last_bgsave_status" => f.rdb_ok = Some(value == "ok"),
            db if db.len() > 2
                && db.starts_with("db")
                && db[2..].bytes().all(|b| b.is_ascii_digit()) =>
            {
                let keys = value
                    .split(',')
                    .find_map(|kv| kv.strip_prefix("keys="))
                    .and_then(|k| k.parse::<u64>().ok());
                f.keys = f.keys.saturating_add(keys.unwrap_or(0));
            }
            _ => {}
        }
    }
    if f.used_memory.is_none() || f.connected_clients.is_none() || f.primary.is_none() {
        return Err("not the answer of INFO".into());
    }
    Ok(f)
}

/// A reply of Redis.
#[derive(Debug, PartialEq, Eq)]
pub enum Reply {
    Simple(String),
    Error(String),
    Bulk(Vec<u8>),
    Nil,
}

/// Sends `args` as a RESP array; the frame (with `AUTH` the password) is wiped afterwards.
async fn send<S: AsyncWrite + Unpin>(stream: &mut S, args: &[&[u8]]) -> std::io::Result<()> {
    let mut out = zeroize::Zeroizing::new(format!("*{}\r\n", args.len()).into_bytes());
    for a in args {
        out.extend_from_slice(format!("${}\r\n", a.len()).as_bytes());
        out.extend_from_slice(a);
        out.extend_from_slice(b"\r\n");
    }
    stream.write_all(&out).await
}

/// Reads one reply: a simple string, an error or a bulk string of at most [`MAX_INFO`] bytes;
/// the head at most 1 KiB. Public for the fuzz target `module_clients`.
///
/// # Errors
///
/// Why the answer is no reply of Redis, or too large.
pub async fn reply<S: AsyncRead + Unpin>(stream: &mut BufReader<S>) -> Result<Reply, String> {
    let mut head = Vec::new();
    (&mut *stream)
        .take(MAX_HEAD)
        .read_until(b'\n', &mut head)
        .await
        .map_err(|e| e.to_string())?;
    let head = head
        .strip_suffix(b"\r\n")
        .ok_or("no RESP reply (is it Redis?)")?;
    let text = || String::from_utf8_lossy(&head[1..]).into_owned();
    match head.first() {
        Some(b'+') => Ok(Reply::Simple(text())),
        Some(b'-') => Ok(Reply::Error(text())),
        Some(b'$') => {
            let len: i64 = text().parse().map_err(|_| "a broken RESP reply")?;
            if len < 0 {
                return Ok(Reply::Nil);
            }
            let len = usize::try_from(len).map_err(|_| "a broken RESP reply")?;
            if len > MAX_INFO {
                return Err("the answer is larger than 1 MiB".into());
            }
            let mut body = vec![0u8; len + 2];
            stream
                .read_exact(&mut body)
                .await
                .map_err(|e| e.to_string())?;
            if body[len..] != *b"\r\n" {
                return Err("a broken RESP reply".into());
            }
            body.truncate(len);
            Ok(Reply::Bulk(body))
        }
        _ => Err("no RESP reply (is it Redis?)".into()),
    }
}

/// Redis' error text, bounded and without control characters.
fn plain(error: &str) -> String {
    error
        .chars()
        .map(|c| if c.is_control() { ' ' } else { c })
        .take(MAX_ERROR)
        .collect()
}

/// Why a run got no `INFO`.
enum Miss {
    /// Redis doesn't answer.
    Down,
    /// It answers, but not with `INFO`.
    Failed(String),
}

/// `AUTH` (if `login`) and `INFO` over `stream`.
async fn exchange<S: AsyncRead + AsyncWrite + Unpin>(
    stream: S,
    login: Option<&Login>,
) -> Result<String, Miss> {
    let mut stream = BufReader::new(stream);
    let io = |e: std::io::Error| Miss::Failed(e.to_string());
    if let Some(login) = login {
        let mut args: Vec<&[u8]> = vec![b"AUTH"];
        if let Some(user) = &login.user {
            args.push(user.as_bytes());
        }
        args.push(login.password.as_bytes());
        send(stream.get_mut(), &args).await.map_err(io)?;
        match reply(&mut stream).await.map_err(Miss::Failed)? {
            Reply::Simple(_) => {}
            // WRONGPASS, or AUTH without requirepass: no text of the reply (it may name the user).
            Reply::Error(_) => {
                return Err(Miss::Failed("AUTH failed: wrong user or password".into()));
            }
            _ => return Err(Miss::Failed("no RESP reply to AUTH".into())),
        }
    }
    send(stream.get_mut(), &[b"INFO"]).await.map_err(io)?;
    match reply(&mut stream).await.map_err(Miss::Failed)? {
        Reply::Bulk(body) => {
            String::from_utf8(body).map_err(|_| Miss::Failed("INFO is no text".into()))
        }
        Reply::Error(e) if e.starts_with("NOAUTH") => Err(Miss::Failed(
            "Redis wants a password (NOAUTH): set the module's password".into(),
        )),
        Reply::Error(e) => Err(Miss::Failed(format!("INFO: {}", plain(&e)))),
        Reply::Simple(_) | Reply::Nil => Err(Miss::Failed("no answer to INFO".into())),
    }
}

#[derive(Debug, Default)]
struct Counters {
    commands: Counter,
    hits: Counter,
    misses: Counter,
    evicted: Counter,
    expired: Counter,
    rejected: Counter,
}

struct Info {
    veto: Veto,
    target: Target,
    security: Security,
    login: Option<Login>,
    timeout: Duration,
    counters: Counters,
}

impl Info {
    async fn ask(&self, run: &Run) -> Result<String, Result<Miss, RunError>> {
        let connection = match self.target.reach(&self.veto, run).await {
            Ok(Some(connection)) => connection,
            Ok(None) => return Err(Ok(Miss::Down)),
            Err(e) => return Err(Err(e)),
        };
        let stream = match connection {
            Connection::Tcp(stream, host) if self.security.tls != Tls::Off => self
                .security
                .start(stream, &host)
                .await
                .map_err(|e| Ok(Miss::Failed(e.to_string())))?,
            plain => plain.plain(),
        };
        exchange(stream, self.login.as_ref()).await.map_err(Ok)
    }
}

fn gauge(metric: &'static str, n: u64) -> Sample {
    Sample::int(metric, i64::try_from(n).unwrap_or(i64::MAX))
}

impl Instance for Info {
    fn timeout(&self) -> Duration {
        self.timeout + Duration::from_secs(2)
    }

    fn collect<'a>(&'a mut self, run: &'a Run) -> BoxFuture<'a, Result<Vec<Sample>, RunError>> {
        Box::pin(async move {
            let asked = tokio::time::timeout(self.timeout, self.ask(run))
                .await
                .unwrap_or(Err(Ok(Miss::Down)));
            let text = match asked {
                Ok(text) => text,
                Err(Err(e)) => return Err(e),
                Err(Ok(Miss::Failed(why))) => return Err(RunError::Failed(why)),
                Err(Ok(Miss::Down)) => {
                    self.counters = Counters::default();
                    return Ok(vec![Sample::int("sermon_redis_up", 0)]);
                }
            };
            let f = parse_info(&text).map_err(RunError::Failed)?;
            Ok(self.samples(&f, run))
        })
    }
}

impl Info {
    fn samples(&mut self, f: &InfoFields, run: &Run) -> Vec<Sample> {
        let now = run.monotonic();
        let mut out = vec![Sample::int("sermon_redis_up", 1)];
        let gauges = [
            ("sermon_redis_memory_used_bytes", f.used_memory),
            ("sermon_redis_memory_rss_bytes", f.used_memory_rss),
            (
                "sermon_redis_memory_max_bytes",
                f.maxmemory.filter(|m| *m > 0),
            ),
            ("sermon_redis_clients_connected", f.connected_clients),
            ("sermon_redis_clients_blocked", f.blocked_clients),
            ("sermon_redis_keys", Some(f.keys)),
            ("sermon_redis_connected_replicas", f.replicas),
        ];
        out.extend(
            gauges
                .into_iter()
                .filter_map(|(m, v)| v.map(|v| gauge(m, v))),
        );
        let flags = [
            ("sermon_redis_master_link_up", f.master_link_up),
            ("sermon_redis_rdb_last_save_ok", f.rdb_ok),
        ];
        out.extend(
            flags
                .into_iter()
                .filter_map(|(m, v)| v.map(|v| Sample::int(m, i64::from(v)))),
        );
        let c = &mut self.counters;
        let rate = |counter: &mut Counter, value: Option<u64>| {
            value.and_then(|v| counter.per_second(v, now))
        };
        let hits = rate(&mut c.hits, f.hits);
        let misses = rate(&mut c.misses, f.misses);
        let rates = [
            (
                "sermon_redis_commands_per_second",
                rate(&mut c.commands, f.commands),
            ),
            ("sermon_redis_keyspace_hits_per_second", hits),
            ("sermon_redis_keyspace_misses_per_second", misses),
            (
                "sermon_redis_evicted_keys_per_second",
                rate(&mut c.evicted, f.evicted),
            ),
            (
                "sermon_redis_expired_keys_per_second",
                rate(&mut c.expired, f.expired),
            ),
            (
                "sermon_redis_rejected_connections_per_second",
                rate(&mut c.rejected, f.rejected),
            ),
        ];
        out.extend(
            rates
                .into_iter()
                .filter_map(|(m, v)| v.map(|v| Sample::float(m, v))),
        );
        if let (Some(h), Some(m)) = (hits, misses)
            && h + m > 0.0
        {
            let ratio = h / (h + m) * 1_000_000.0;
            #[allow(clippy::cast_possible_truncation, clippy::as_conversions)]
            out.push(Sample::int(
                "sermon_redis_keyspace_hit_ratio",
                ratio.round() as i64,
            ));
        }
        out
    }
}

#[cfg(test)]
mod tests;
