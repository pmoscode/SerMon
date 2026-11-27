//! PostgreSQL (M5-13): connections, transactions, cache hits, database sizes, locks and
//! replication, read with a user of the role `pg_monitor` over the module's own client
//! ([`wire`]: no OpenSSL, no driver). Each run opens one session, asks two queries under a
//! `statement_timeout` and closes it. A wrong password or a user without `pg_monitor` is the
//! module's health with the server's reason, not a crash.
//!
//! Metrics:
//!
//! | Metric | Labels | Meaning |
//! |---|---|---|
//! | `sermon_postgres_up` | – | 1 if the server answered, 0 if nothing accepts the connection |
//! | `sermon_postgres_connections` | `state` (`active`, `idle`, `idle_in_transaction`, `other`) | client connections |
//! | `sermon_postgres_connections_limit` | – | `max_connections` |
//! | `sermon_postgres_database_size_bytes` | `database` | size, the 16 largest |
//! | `sermon_postgres_transactions_per_second` | `database` | commits and rollbacks |
//! | `sermon_postgres_cache_hit_ratio` | `database` | blocks from the buffer cache, ppm (only with reads) |
//! | `sermon_postgres_locks`, `sermon_postgres_locks_waiting` | – | locks held, locks waited for |
//! | `sermon_postgres_standby` | – | 1 on a standby (in recovery) |
//! | `sermon_postgres_replication_lag_seconds` | `replica` | replay lag of each standby (primary, at most 8) |
//! | `sermon_postgres_replay_lag_seconds` | – | since the last replayed transaction (standby) |
//!
//! Rates need two runs.

pub mod wire;

use std::collections::BTreeMap;
use std::time::Duration;

use sermon_core::{Monotonic, Sample};

use crate::discovery::{Discovered, HostFacts};
use crate::sql::{self, Security, Target, Tls};
use crate::{BoxFuture, BuildError, Config, Instance, Module, Run, RunError, Setup, Veto};
use wire::{Error, ResultSet};

const SCHEMA: &str = include_str!("../schemas/postgres.json");
/// Most databases reported.
pub const MAX_DATABASES: usize = 16;
/// Most replicas reported.
pub const MAX_REPLICAS: usize = 8;
const MAX_LABEL: usize = 64;

/// Whether the user may read what the module asks: `pg_monitor` (or a superuser).
pub const CHECK: &str = "SELECT current_setting('server_version_num'), pg_is_in_recovery(), \
     current_setting('max_connections'), \
     pg_has_role(current_user, 'pg_monitor', 'USAGE') \
     OR (SELECT rolsuper FROM pg_roles WHERE rolname = current_user)";

/// What the module reads, one result set each.
pub const STATS: &str = "\
SELECT coalesce(state, 'other'), count(*) FROM pg_stat_activity \
 WHERE backend_type = 'client backend' GROUP BY 1;\n\
SELECT d.datname, s.xact_commit + s.xact_rollback, s.blks_hit, s.blks_read, pg_database_size(d.oid) \
 FROM pg_database d JOIN pg_stat_database s ON s.datid = d.oid \
 WHERE NOT d.datistemplate AND d.datallowconn ORDER BY 5 DESC, 1 LIMIT 16;\n\
SELECT count(*), count(*) FILTER (WHERE NOT granted) FROM pg_locks;\n\
SELECT coalesce(application_name, ''), coalesce(host(client_addr), ''), \
 coalesce(extract(epoch FROM replay_lag), 0) FROM pg_stat_replication ORDER BY 1, 2 LIMIT 8;\n\
SELECT greatest(0, extract(epoch FROM clock_timestamp() - pg_last_xact_replay_timestamp()))";

/// The module `postgres`.
#[derive(Debug, Clone, Copy, Default)]
pub struct Postgres;

impl Module for Postgres {
    fn id(&self) -> &'static str {
        "postgres"
    }

    fn schema(&self) -> &'static str {
        SCHEMA
    }

    fn build(&self, config: &Config, setup: &Setup<'_>) -> Result<Box<dyn Instance>, BuildError> {
        let target = Target::of(config, setup.veto)?;
        let security = Security::of(config, &target, setup.veto)?;
        Ok(Box::new(Reader {
            veto: setup.veto.clone(),
            target,
            security,
            user: sql::text(config, "role", "sermon").to_owned(),
            database: sql::text(config, "database", "postgres").to_owned(),
            password: setup.secrets.secret("secret"),
            allow: wire::Allow::of(sql::text(config, "auth", "scram-sha-256")),
            last: None,
        }))
    }

    fn discover(&self, facts: &HostFacts) -> Option<Discovered> {
        let mut found = Discovered::evidence(
            facts,
            &["postgres", "postmaster"],
            &["postgresql.service"],
            &[5432],
        )?;
        let socket = "/run/postgresql/.s.PGSQL.5432";
        found.suggested =
            if facts.has_socket(socket) || facts.has_socket("/var/run/postgresql/.s.PGSQL.5432") {
                Some(serde_json::json!({ "socket": socket }))
            } else {
                facts.listener(5432).map(
                    |l| serde_json::json!({ "address": format!("{}:5432", bracket(l.reach())) }),
                )
            };
        Some(found)
    }
}

fn bracket(ip: std::net::IpAddr) -> String {
    if ip.is_ipv6() {
        format!("[{ip}]")
    } else {
        ip.to_string()
    }
}

struct Reader {
    veto: Veto,
    target: Target,
    security: Security,
    user: String,
    database: String,
    password: Option<crate::Secret>,
    allow: wire::Allow,
    last: Option<(Monotonic, Stats)>,
}

/// What one run read.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Stats {
    pub standby: bool,
    pub max_connections: Option<i64>,
    /// By state label.
    pub connections: BTreeMap<&'static str, i64>,
    pub databases: Vec<Database>,
    pub locks: Option<(i64, i64)>,
    /// Replica label and lag in seconds.
    pub replicas: Vec<(String, f64)>,
    pub replay_lag: Option<f64>,
}

/// One database's counters.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Database {
    pub name: String,
    pub transactions: u64,
    pub blocks_hit: u64,
    pub blocks_read: u64,
    pub size: u64,
}

/// Why the answer to [`CHECK`] stops the run: not `pg_monitor`.
///
/// # Errors
///
/// [`RunError::Failed`] without the role or for a broken answer.
pub fn check(sets: &[ResultSet], user: &str) -> Result<(bool, Option<i64>), RunError> {
    let row = sets
        .first()
        .and_then(|s| s.rows.first())
        .filter(|r| r.len() == 4)
        .ok_or_else(|| RunError::Failed("no answer to the check of the rights".into()))?;
    if row[3].as_deref() != Some("t") {
        return Err(RunError::Failed(format!(
            "the user {user} lacks the role pg_monitor: GRANT pg_monitor TO {user}"
        )));
    }
    Ok((row[1].as_deref() == Some("t"), number(row[2].as_ref())))
}

/// The statistics of the answer to [`STATS`].
#[must_use]
pub fn stats(sets: &[ResultSet], standby: bool, max_connections: Option<i64>) -> Stats {
    let set = |i: usize| sets.get(i).map_or(&[][..], |s| s.rows.as_slice());
    let mut s = Stats {
        standby,
        max_connections,
        ..Stats::default()
    };
    for row in set(0) {
        let state = match row.first().and_then(Option::as_deref) {
            Some("active") => "active",
            Some("idle") => "idle",
            Some(s) if s.starts_with("idle in transaction") => "idle_in_transaction",
            _ => "other",
        };
        if let Some(n) = number(row.get(1).and_then(Option::as_ref)) {
            *s.connections.entry(state).or_default() += n;
        }
    }
    for state in ["active", "idle", "idle_in_transaction", "other"] {
        s.connections.entry(state).or_default();
    }
    s.databases = set(1)
        .iter()
        .take(MAX_DATABASES)
        .filter_map(|row| {
            let n = |i: usize| unsigned(row.get(i).and_then(Option::as_ref));
            Some(Database {
                name: label(row.first()?.as_deref()?),
                transactions: n(1)?,
                blocks_hit: n(2)?,
                blocks_read: n(3)?,
                size: n(4)?,
            })
        })
        .collect();
    s.locks = set(2)
        .first()
        .and_then(|r| Some((number(r.first()?.as_ref())?, number(r.get(1)?.as_ref())?)));
    s.replicas = set(3)
        .iter()
        .take(MAX_REPLICAS)
        .filter_map(|r| {
            let name = r.first()?.as_deref().unwrap_or("");
            let addr = r.get(1)?.as_deref().unwrap_or("");
            let replica = if name.is_empty() || name == "walreceiver" {
                addr
            } else {
                name
            };
            Some((label(replica), seconds(r.get(2)?.as_deref())?))
        })
        .collect();
    s.replay_lag = set(4)
        .first()
        .and_then(|r| seconds(r.first()?.as_deref()))
        .filter(|_| standby);
    s
}

fn number(v: Option<&String>) -> Option<i64> {
    v?.trim().parse().ok()
}

fn unsigned(v: Option<&String>) -> Option<u64> {
    v?.trim().parse().ok()
}

fn seconds(v: Option<&str>) -> Option<f64> {
    v?.trim()
        .parse::<f64>()
        .ok()
        .filter(|s| s.is_finite() && *s >= 0.0)
}

/// A name from the server as a label: printable, at most [`MAX_LABEL`] bytes.
fn label(name: &str) -> String {
    let mut out = String::new();
    for c in name.chars().filter(|c| !c.is_control()) {
        if out.len() + c.len_utf8() > MAX_LABEL {
            break;
        }
        out.push(c);
    }
    out
}

/// The samples of `now`; rates against `before` (taken `elapsed` earlier).
#[must_use]
pub fn samples(now: &Stats, before: Option<(&Stats, Duration)>) -> Vec<Sample> {
    let mut out = vec![Sample::int("sermon_postgres_up", 1)];
    out.extend(
        now.connections.iter().map(|(state, n)| {
            Sample::int("sermon_postgres_connections", *n).label("state", *state)
        }),
    );
    out.extend(
        now.max_connections
            .map(|n| Sample::int("sermon_postgres_connections_limit", n)),
    );
    out.push(Sample::int(
        "sermon_postgres_standby",
        i64::from(now.standby),
    ));
    if let Some((held, waiting)) = now.locks {
        out.push(Sample::int("sermon_postgres_locks", held));
        out.push(Sample::int("sermon_postgres_locks_waiting", waiting));
    }
    for (replica, lag) in &now.replicas {
        out.push(
            Sample::float("sermon_postgres_replication_lag_seconds", *lag)
                .label("replica", replica.clone()),
        );
    }
    out.extend(
        now.replay_lag
            .map(|l| Sample::float("sermon_postgres_replay_lag_seconds", l)),
    );
    for db in &now.databases {
        let with = |s: Sample| s.label("database", db.name.clone());
        out.push(with(Sample::int(
            "sermon_postgres_database_size_bytes",
            i64::try_from(db.size).unwrap_or(i64::MAX),
        )));
        let Some((was, elapsed)) = before else {
            continue;
        };
        let Some(old) = was.databases.iter().find(|d| d.name == db.name) else {
            continue;
        };
        if let Some(tps) =
            sermon_collectors::rate::per_second(old.transactions, db.transactions, elapsed)
        {
            out.push(with(Sample::int(
                "sermon_postgres_transactions_per_second",
                tps,
            )));
        }
        let hit = db.blocks_hit.checked_sub(old.blocks_hit);
        let read = db.blocks_read.checked_sub(old.blocks_read);
        if let Some(ratio) = hit
            .zip(read)
            .and_then(|(h, r)| sermon_collectors::rate::ppm(h, h.checked_add(r)?))
        {
            out.push(with(Sample::int("sermon_postgres_cache_hit_ratio", ratio)));
        }
    }
    out
}

/// What a server's refusal means for the module: plain, with the server's reason.
fn failed(e: Error) -> RunError {
    RunError::Failed(match e {
        Error::Server { code, message } if code == "28P01" || code == "28000" => {
            format!("login refused: {message}")
        }
        other => other.to_string(),
    })
}

impl Reader {
    async fn read(&self, connection: sql::Connection) -> Result<Stats, RunError> {
        let secure = self.security.secure(&connection);
        let io = match (connection, self.security.tls) {
            (sql::Connection::Tcp(mut tcp, host), Tls::Verify | Tls::NoVerify) => {
                if !wire::ssl_request(&mut tcp).await.map_err(failed)? {
                    return Err(RunError::Failed("the server offers no TLS".into()));
                }
                self.security.start(tcp, &host).await?
            }
            (c, _) => c.plain(),
        };
        let startup = wire::Startup {
            user: &self.user,
            database: &self.database,
            password: self.password.as_ref().map(|p| p.as_str()),
            options: "-c statement_timeout=3000 -c lock_timeout=1000 -c default_transaction_read_only=on",
            allow: self.allow,
            secure,
        };
        let mut session = wire::start(io, startup).await.map_err(failed)?;
        let (standby, max) = check(&session.query(CHECK).await.map_err(failed)?, &self.user)?;
        let sets = session.query(STATS).await.map_err(failed)?;
        session.close().await;
        Ok(stats(&sets, standby, max))
    }
}

impl Instance for Reader {
    fn collect<'a>(&'a mut self, run: &'a Run) -> BoxFuture<'a, Result<Vec<Sample>, RunError>> {
        Box::pin(async move {
            let at = run.monotonic();
            let Some(connection) = self.target.reach(&self.veto, run).await? else {
                self.last = None;
                return Ok(vec![Sample::int("sermon_postgres_up", 0)]);
            };
            let now = self.read(connection).await?;
            let before = self
                .last
                .as_ref()
                .map(|(t, s)| (s, at.saturating_duration_since(*t)));
            let out = samples(&now, before);
            self.last = Some((at, now));
            Ok(out)
        })
    }
}

#[cfg(test)]
mod tests;
