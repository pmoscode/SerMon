//! MySQL and MariaDB (M5-14): connections, queries, slow and long-running queries, traffic, the
//! InnoDB buffer pool and replication, read with a user that has `PROCESS` and the right to see
//! the replication (`REPLICATION CLIENT`; MariaDB from 10.5: `REPLICA MONITOR`). Over the
//! module's own client ([`wire`]): no driver, no OpenSSL. A wrong password or a missing right is
//! the module's health with the server's reason.
//!
//! Metrics:
//!
//! | Metric | Labels | Meaning |
//! |---|---|---|
//! | `sermon_mysql_connections` | – | `Threads_connected` |
//! | `sermon_mysql_connections_running` | – | `Threads_running` |
//! | `sermon_mysql_connections_limit` | – | `max_connections` |
//! | `sermon_mysql_aborted_connects_per_second` | – | failed connection attempts |
//! | `sermon_mysql_queries_per_second` | – | statements (`Queries`) |
//! | `sermon_mysql_slow_queries_per_second` | – | statements over `long_query_time` |
//! | `sermon_mysql_long_queries` | – | statements running longer than 60 s now |
//! | `sermon_mysql_received_bytes_per_second`, `sermon_mysql_sent_bytes_per_second` | – | traffic of the clients |
//! | `sermon_mysql_innodb_buffer_pool_bytes` | – | `innodb_buffer_pool_size` |
//! | `sermon_mysql_innodb_buffer_pool_utilization_ratio` | – | pages in use, ppm |
//! | `sermon_mysql_innodb_buffer_pool_hit_ratio` | – | reads served from the pool, ppm |
//! | `sermon_mysql_replica` | – | 1 if the server replicates from a source |
//! | `sermon_mysql_replication_running` | – | 1 if both replication threads run (replica) |
//! | `sermon_mysql_replication_lag_seconds` | – | `Seconds_Behind_Source` (replica) |
//!
//! Rates need two runs.

pub mod wire;

use std::collections::BTreeMap;
use std::time::Duration;

use sermon_core::{Monotonic, Sample};

use crate::discovery::{Discovered, HostFacts};
use crate::sql::{self, Target, Tls};
use crate::{BoxFuture, BuildError, Config, Instance, Module, Run, RunError, Setup, Veto};
use wire::{Error, Login, Packets, ResultSet};

const SCHEMA: &str = include_str!("../schemas/mysql.json");

/// The status counters read (`SHOW GLOBAL STATUS`).
pub const STATUS: &str = "SHOW GLOBAL STATUS WHERE Variable_name IN ('Threads_connected', \
     'Threads_running', 'Aborted_connects', 'Queries', 'Slow_queries', 'Bytes_received', \
     'Bytes_sent', 'Innodb_buffer_pool_pages_total', 'Innodb_buffer_pool_pages_free', \
     'Innodb_buffer_pool_reads', 'Innodb_buffer_pool_read_requests')";
/// The settings read.
pub const VARIABLES: &str = "SHOW GLOBAL VARIABLES WHERE Variable_name IN ('max_connections', \
     'innodb_buffer_pool_size', 'version')";
/// The global rights of the module's user.
pub const PRIVILEGES: &str = "SELECT PRIVILEGE_TYPE FROM information_schema.USER_PRIVILEGES \
     WHERE GRANTEE = CONCAT('''', SUBSTRING_INDEX(CURRENT_USER(), '@', 1), '''@''', \
     SUBSTRING_INDEX(CURRENT_USER(), '@', -1), '''')";
/// Statements running longer than a minute (with `PROCESS`: of all users).
pub const LONG: &str = "SELECT COUNT(*) FROM information_schema.PROCESSLIST WHERE TIME > 60 \
     AND COMMAND NOT IN ('Sleep', 'Daemon', 'Binlog Dump', 'Binlog Dump GTID', 'Connect') \
     AND USER NOT IN ('system user', 'event_scheduler')";
/// The replication (MySQL from 8.0.22 and MariaDB from 10.5.1; [`REPLICA_OLD`] before).
pub const REPLICA: &str = "SHOW REPLICA STATUS";
/// The replication before the renaming.
pub const REPLICA_OLD: &str = "SHOW SLAVE STATUS";

/// The module `mysql`.
#[derive(Debug, Clone, Copy, Default)]
pub struct Mysql;

impl Module for Mysql {
    fn id(&self) -> &'static str {
        "mysql"
    }

    fn schema(&self) -> &'static str {
        SCHEMA
    }

    fn build(&self, config: &Config, setup: &Setup<'_>) -> Result<Box<dyn Instance>, BuildError> {
        let target = Target::of(config, setup.veto)?;
        let tls = sql::tls_of(config);
        if tls != Tls::Off && matches!(target, Target::Unix(_)) {
            return Err(BuildError::Invalid("tls: not over a Unix socket".into()));
        }
        Ok(Box::new(Reader {
            veto: setup.veto.clone(),
            target,
            tls,
            user: sql::text(config, "user", "sermon").to_owned(),
            password: setup.secrets.get("secret").unwrap_or("").to_owned(),
            last: None,
        }))
    }

    fn discover(&self, facts: &HostFacts) -> Option<Discovered> {
        let mut found = Discovered::evidence(
            facts,
            &["mysqld", "mariadbd"],
            &["mysql.service", "mysqld.service", "mariadb.service"],
            &[3306],
        )?;
        let socket = [
            "/run/mysqld/mysqld.sock",
            "/var/run/mysqld/mysqld.sock",
            "/var/lib/mysql/mysql.sock",
        ]
        .into_iter()
        .find(|s| facts.has_socket(s));
        found.suggested = match socket {
            Some(s) => Some(serde_json::json!({ "socket": s })),
            None => facts.listener(3306).map(|l| {
                let ip = l.reach();
                let host = if ip.is_ipv6() {
                    format!("[{ip}]")
                } else {
                    ip.to_string()
                };
                serde_json::json!({ "address": format!("{host}:3306") })
            }),
        };
        Some(found)
    }
}

struct Reader {
    veto: Veto,
    target: Target,
    tls: Tls,
    user: String,
    password: String,
    last: Option<(Monotonic, Stats)>,
}

/// What one run read.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Stats {
    /// `SHOW GLOBAL STATUS` by name.
    pub status: BTreeMap<String, u64>,
    pub max_connections: Option<u64>,
    pub buffer_pool_size: Option<u64>,
    pub long_queries: Option<u64>,
    pub replica: Option<Replica>,
}

/// The replication of a replica.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Replica {
    pub running: bool,
    /// `None` while not replicating.
    pub lag: Option<u64>,
}

/// The name-value rows of `SHOW … STATUS/VARIABLES`, numbers only.
#[must_use]
pub fn values(set: &ResultSet) -> BTreeMap<String, u64> {
    set.rows
        .iter()
        .filter_map(|r| {
            let name = r.first()?.as_deref()?;
            let value = r.get(1)?.as_deref()?.trim().parse().ok()?;
            Some((name.to_owned(), value))
        })
        .collect()
}

/// Whether the global rights suffice: `PROCESS` (or `SUPER`).
///
/// # Errors
///
/// [`RunError::Failed`] without it, naming the grant.
pub fn check_rights(set: &ResultSet, user: &str) -> Result<(), RunError> {
    let has = |p: &str| {
        set.rows
            .iter()
            .any(|r| r.first().and_then(Option::as_deref) == Some(p))
    };
    if has("PROCESS") || has("SUPER") {
        Ok(())
    } else {
        Err(RunError::Failed(format!(
            "the user {user} lacks the right PROCESS: GRANT PROCESS, REPLICATION CLIENT ON *.* TO {user} (MariaDB: REPLICA MONITOR)"
        )))
    }
}

/// The replication of a `SHOW REPLICA/SLAVE STATUS` answer; `None` without a source.
#[must_use]
pub fn replica(set: &ResultSet) -> Option<Replica> {
    let row = set.rows.first()?;
    let column = |names: &[&str]| {
        set.columns
            .iter()
            .position(|c| names.contains(&c.as_str()))
            .and_then(|i| row.get(i)?.as_deref())
    };
    let yes = |names: &[&str]| column(names) == Some("Yes");
    let running = yes(&["Replica_IO_Running", "Slave_IO_Running"])
        && yes(&["Replica_SQL_Running", "Slave_SQL_Running"]);
    let lag =
        column(&["Seconds_Behind_Source", "Seconds_Behind_Master"]).and_then(|v| v.parse().ok());
    Some(Replica { running, lag })
}

/// The samples of `now`; rates against `before` (taken `elapsed` earlier).
#[must_use]
pub fn samples(now: &Stats, before: Option<(&Stats, Duration)>) -> Vec<Sample> {
    let get = |name: &str| now.status.get(name).copied();
    let int = |m, v: Option<u64>| Some(Sample::int(m, i64::try_from(v?).ok()?));
    let mut out: Vec<Sample> = [
        int("sermon_mysql_connections", get("Threads_connected")),
        int("sermon_mysql_connections_running", get("Threads_running")),
        int("sermon_mysql_connections_limit", now.max_connections),
        int("sermon_mysql_long_queries", now.long_queries),
        int(
            "sermon_mysql_innodb_buffer_pool_bytes",
            now.buffer_pool_size,
        ),
        int(
            "sermon_mysql_replica",
            Some(u64::from(now.replica.is_some())),
        ),
    ]
    .into_iter()
    .flatten()
    .collect();
    if let (Some(total), Some(free)) = (
        get("Innodb_buffer_pool_pages_total"),
        get("Innodb_buffer_pool_pages_free"),
    ) {
        out.extend(
            sermon_collectors::rate::ppm(total.saturating_sub(free), total)
                .map(|r| Sample::int("sermon_mysql_innodb_buffer_pool_utilization_ratio", r)),
        );
    }
    if let Some(r) = &now.replica {
        out.push(Sample::int(
            "sermon_mysql_replication_running",
            i64::from(r.running),
        ));
        out.extend(int("sermon_mysql_replication_lag_seconds", r.lag));
    }
    let Some((was, elapsed)) = before else {
        return out;
    };
    let rate = |m, name: &str| {
        let (b, n) = (was.status.get(name)?, now.status.get(name)?);
        Some(Sample::float(
            m,
            sermon_collectors::rate::per_second_float(*b, *n, elapsed)?,
        ))
    };
    out.extend(
        [
            rate(
                "sermon_mysql_aborted_connects_per_second",
                "Aborted_connects",
            ),
            rate("sermon_mysql_queries_per_second", "Queries"),
            rate("sermon_mysql_slow_queries_per_second", "Slow_queries"),
            rate("sermon_mysql_received_bytes_per_second", "Bytes_received"),
            rate("sermon_mysql_sent_bytes_per_second", "Bytes_sent"),
        ]
        .into_iter()
        .flatten(),
    );
    let delta = |name: &str| now.status.get(name)?.checked_sub(*was.status.get(name)?);
    if let (Some(requests), Some(reads)) = (
        delta("Innodb_buffer_pool_read_requests"),
        delta("Innodb_buffer_pool_reads"),
    ) {
        out.extend(
            sermon_collectors::rate::ppm(requests.saturating_sub(reads), requests)
                .map(|r| Sample::int("sermon_mysql_innodb_buffer_pool_hit_ratio", r)),
        );
    }
    out
}

/// What a server's refusal means for the module.
fn failed(e: Error) -> RunError {
    RunError::Failed(match e {
        Error::Server {
            code: 1045,
            message,
        } => format!("login refused: {message}"),
        Error::Server {
            code: 1227,
            message,
        } => format!("a right is missing: {message}"),
        other => other.to_string(),
    })
}

impl Reader {
    async fn session(&self) -> Result<Packets<Box<dyn sql::Io>>, RunError> {
        let connection = self.target.connect(&self.veto).await?;
        let unix = matches!(connection, sql::Connection::Unix(_));
        let host = match &connection {
            sql::Connection::Tcp(_, host) => host.clone(),
            sql::Connection::Unix(_) => String::new(),
        };
        let io = connection.plain();
        let mut packets = Packets::new(io);
        let handshake =
            wire::Handshake::parse(&packets.read().await.map_err(failed)?).map_err(failed)?;
        let tls = self.tls != Tls::Off;
        if tls && !handshake.tls() {
            return Err(RunError::Failed("the server offers no TLS".into()));
        }
        let mut packets = if tls {
            packets
                .write(&wire::ssl_request(handshake.capabilities))
                .await
                .map_err(failed)?;
            let (io, seq) = packets.into_inner();
            Packets::resume(sql::tls(io, &host, self.tls).await?, seq)
        } else {
            packets
        };
        let login = Login {
            user: &self.user,
            password: &self.password,
            database: "",
            secure: tls || unix,
        };
        let response = wire::handshake_response(&handshake, &login, tls).map_err(failed)?;
        packets.write(&response).await.map_err(failed)?;
        wire::finish_login(&mut packets, &login)
            .await
            .map_err(failed)?;
        Ok(packets)
    }

    async fn read(&self) -> Result<Stats, RunError> {
        let mut s = self.session().await?;
        check_rights(&s.query(PRIVILEGES).await.map_err(failed)?, &self.user)?;
        let replicating = match s.query(REPLICA).await {
            // 1064: a syntax error – a version before SHOW REPLICA STATUS.
            Err(Error::Server { code: 1064, .. }) => s.query(REPLICA_OLD).await,
            other => other,
        }
        .map_err(failed)?;
        let status = values(&s.query(STATUS).await.map_err(failed)?);
        let variables = values(&s.query(VARIABLES).await.map_err(failed)?);
        let long = s.query(LONG).await.map_err(failed)?;
        s.close().await;
        Ok(Stats {
            status,
            max_connections: variables.get("max_connections").copied(),
            buffer_pool_size: variables.get("innodb_buffer_pool_size").copied(),
            long_queries: long
                .rows
                .first()
                .and_then(|r| r.first()?.as_deref()?.parse().ok()),
            replica: replica(&replicating),
        })
    }
}

impl Instance for Reader {
    fn collect<'a>(&'a mut self, run: &'a Run) -> BoxFuture<'a, Result<Vec<Sample>, RunError>> {
        Box::pin(async move {
            let at = run.monotonic();
            let now = self.read().await?;
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
