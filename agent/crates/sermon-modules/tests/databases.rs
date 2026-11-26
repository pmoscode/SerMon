//! The database modules against real servers (M5-13, M5-14): `scripts/test-db-modules.sh`
//! starts them in containers and names them in the environment; without it these tests are
//! skipped (`#[ignore]`, run with `--ignored`).
//!
//! - `SERMON_TEST_POSTGRES`: `version@host:port,…`, users `sermon` (`pg_monitor`) and `plain`
//! - `SERMON_TEST_DB_PASSWORD`: the password of both
//! - `SERMON_TEST_DB_CA`: the CA of the MySQL and MariaDB servers' certificates (for 127.0.0.1)
//! - `SERMON_FIXTURES_OUT`: if set, the answers are written there as fixtures
//!   (`postgres/<version>/{check,stats}.json`)

// Test code: a failure is a panic with its reason.
#![allow(clippy::unwrap_used, clippy::expect_used, clippy::panic)]

use std::collections::BTreeSet;
use std::time::Duration;

use sermon_core::Sample;
use sermon_modules::mysql;
use sermon_modules::postgres::{self, wire};
use sermon_modules::{Host, Module, Run, RunError, Schema, Secrets, Setup, Veto};

fn servers(var: &str) -> Vec<(String, String)> {
    std::env::var(var)
        .unwrap_or_default()
        .split(',')
        .filter_map(|s| s.split_once('@'))
        .map(|(v, a)| (v.to_owned(), a.to_owned()))
        .collect()
}

fn password() -> String {
    std::env::var("SERMON_TEST_DB_PASSWORD").expect("SERMON_TEST_DB_PASSWORD")
}

fn ca() -> String {
    std::env::var("SERMON_TEST_DB_CA").expect("SERMON_TEST_DB_CA")
}

/// The default veto, and the directory of the servers' CA for `ca_file`.
fn veto() -> Veto {
    let mut veto = Veto::default().any_port();
    if let Ok(ca) = std::env::var("SERMON_TEST_DB_CA") {
        let dir = std::path::Path::new(&ca).parent().unwrap();
        // As configured and resolved: the temporary directory may lie behind a link (macOS).
        veto.file_prefixes.push(dir.to_path_buf());
        veto.file_prefixes.push(std::fs::canonicalize(dir).unwrap());
    }
    veto
}

async fn collect(
    module: &dyn Module,
    config: &str,
    password: &str,
) -> Result<Vec<Sample>, RunError> {
    let schema = Schema::parse(module.schema()).unwrap();
    let secrets = Secrets::new([("secret".to_owned(), password.to_owned())]);
    let config = schema.validate(config, &secrets.fields()).unwrap();
    let (veto, host) = (veto(), Host::default());
    let setup = Setup {
        veto: &veto,
        secrets: &secrets,
        host: &host,
    };
    let mut instance = module
        .build(&config, &setup)
        .map_err(|e| RunError::Failed(e.to_string()))?;
    let run = Run::default();
    instance.collect(&run).await?;
    tokio::time::sleep(Duration::from_millis(1100)).await;
    instance.collect(&run).await
}

fn metrics(samples: &[Sample]) -> BTreeSet<&'static str> {
    samples.iter().map(|s| s.metric).collect()
}

fn json(sets: &[wire::ResultSet]) -> serde_json::Value {
    serde_json::Value::Array(
        sets.iter()
            .map(|s| serde_json::json!({ "columns": s.columns, "rows": s.rows }))
            .collect(),
    )
}

#[tokio::test]
#[ignore = "needs the servers of scripts/test-db-modules.sh"]
async fn postgres_against_every_version() {
    let primaries = servers("SERMON_TEST_POSTGRES");
    assert!(
        !primaries.is_empty(),
        "SERMON_TEST_POSTGRES names no server"
    );
    let pw = password();
    let standby = servers("SERMON_TEST_POSTGRES_STANDBY");
    for (version, address) in primaries {
        // PostgreSQL 13 stores md5 hashes (password_encryption): taken only when allowed (M5-33).
        let auth = if version == "13" {
            r#","auth":"md5""#
        } else {
            ""
        };
        if !auth.is_empty() {
            let scram_only = format!(r#"{{"address":"{address}","database":"app"}}"#);
            let Err(RunError::Failed(why)) = collect(&postgres::Postgres, &scram_only, &pw).await
            else {
                panic!("{version}: md5 taken without auth = md5");
            };
            assert!(why.contains("md5"), "{version}: {why}");
        }
        let config = format!(r#"{{"address":"{address}","database":"app"{auth}}}"#);
        let samples = collect(&postgres::Postgres, &config, &pw)
            .await
            .unwrap_or_else(|e| panic!("{version}: {e}"));
        let seen = metrics(&samples);
        for want in [
            "sermon_postgres_connections",
            "sermon_postgres_connections_limit",
            "sermon_postgres_database_size_bytes",
            "sermon_postgres_transactions_per_second",
            "sermon_postgres_locks",
            "sermon_postgres_standby",
        ] {
            assert!(seen.contains(want), "{version}: no {want} in {seen:?}");
        }
        assert!(
            samples
                .iter()
                .any(|s| s.metric == "sermon_postgres_database_size_bytes"
                    && s.labels.contains(&("database", "app".to_owned()))),
            "{version}"
        );
        // A wrong password and missing rights are the module's health with the reason.
        let Err(RunError::Failed(why)) = collect(&postgres::Postgres, &config, "wrong").await
        else {
            panic!("{version}: a wrong password was taken");
        };
        assert!(
            why.contains("login refused") && !why.contains(&pw),
            "{version}: {why}"
        );
        let plain = format!(r#"{{"address":"{address}","role":"plain","database":"app"{auth}}}"#);
        let Err(RunError::Failed(why)) = collect(&postgres::Postgres, &plain, &pw).await else {
            panic!("{version}: a user without pg_monitor");
        };
        assert!(why.contains("pg_monitor"), "{version}: {why}");

        if let Some((_, at)) = standby.iter().find(|(v, _)| *v == version) {
            assert!(
                samples
                    .iter()
                    .any(|s| s.metric == "sermon_postgres_replication_lag_seconds"),
                "{version}: the primary names no replica"
            );
            let config = format!(r#"{{"address":"{at}","database":"app"}}"#);
            let on_standby = collect(&postgres::Postgres, &config, &pw).await.unwrap();
            assert!(
                on_standby
                    .iter()
                    .any(|s| s.metric == "sermon_postgres_standby"
                        && s.value == sermon_core::Value::Int(1)),
                "{version}: the standby"
            );
            assert!(
                on_standby
                    .iter()
                    .any(|s| s.metric == "sermon_postgres_replay_lag_seconds"),
                "{version}: the standby's lag"
            );
            write_fixtures(&format!("{version}-standby"), at, &pw).await;
        }
        write_fixtures(&version, &address, &pw).await;
    }
}

/// Writes the answers of the server at `address` as fixtures below `SERMON_FIXTURES_OUT`.
async fn write_fixtures(version: &str, address: &str, pw: &str) {
    let Some(out) = std::env::var("SERMON_FIXTURES_OUT")
        .ok()
        .filter(|o| !o.is_empty())
    else {
        return;
    };
    let (host, port) = address.rsplit_once(':').unwrap();
    let io = tokio::net::TcpStream::connect((host, port.parse::<u16>().unwrap()))
        .await
        .unwrap();
    let startup = wire::Startup {
        user: "sermon",
        database: "app",
        password: Some(pw),
        options: "",
        allow: wire::Allow::Md5,
        secure: false,
    };
    let mut session = wire::start(io, startup).await.unwrap();
    let check = session.query(postgres::CHECK).await.unwrap();
    let stats = session.query(postgres::STATS).await.unwrap();
    let dir = std::path::Path::new(&out).join("postgres").join(version);
    std::fs::create_dir_all(&dir).unwrap();
    for (name, sets) in [("check", &check), ("stats", &stats)] {
        let text = serde_json::to_string_pretty(&json(sets)).unwrap();
        std::fs::write(dir.join(format!("{name}.json")), text + "\n").unwrap();
    }
}

fn has(samples: &[Sample], metric: &str) -> Option<sermon_core::Value> {
    samples.iter().find(|s| s.metric == metric).map(|s| s.value)
}

// MySQL 8.4 and 9, MariaDB 10.11, 11.4 and 11.8 (`SERMON_TEST_MYSQL`, `name@host:port`), users
// `sermon` (PROCESS and the replication's right), `fresh` (the same, never logged in) and
// `plain` (no rights); replicas of some in `SERMON_TEST_MYSQL_REPLICAS`.
#[tokio::test]
#[ignore = "needs the servers of scripts/test-db-modules.sh"]
async fn mysql_against_every_version() {
    let primaries = servers("SERMON_TEST_MYSQL");
    assert!(!primaries.is_empty(), "SERMON_TEST_MYSQL names no server");
    let replicas = servers("SERMON_TEST_MYSQL_REPLICAS");
    let pw = password();
    let verify = format!(r#""verify","ca_file":"{}""#, ca());
    for (version, address) in primaries {
        let config = |user: &str, tls: &str| {
            let tls = if tls == "verify" {
                verify.clone()
            } else {
                format!(r#""{tls}""#)
            };
            format!(r#"{{"address":"{address}","user":"{user}","tls":{tls}}}"#)
        };
        if version.starts_with("mysql") {
            // caching_sha2_password: the first login sends the password, only over the socket or
            // verified TLS – not to whoever answers no_verify (M5-33).
            for tls in ["off", "no_verify"] {
                let Err(RunError::Failed(why)) =
                    collect(&mysql::Mysql, &config("fresh", tls), &pw).await
                else {
                    panic!("{version}: a full login over {tls}");
                };
                assert!(why.contains("tls verify"), "{version} {tls}: {why}");
            }
            collect(&mysql::Mysql, &config("sermon", "verify"), &pw)
                .await
                .unwrap_or_else(|e| panic!("{version} over TLS: {e}"));
        }
        // From then on the fast login (MySQL) or mysql_native_password (MariaDB) without TLS.
        let samples = collect(&mysql::Mysql, &config("sermon", "off"), &pw)
            .await
            .unwrap_or_else(|e| panic!("{version}: {e}"));
        let seen = metrics(&samples);
        for want in [
            "sermon_mysql_connections",
            "sermon_mysql_connections_limit",
            "sermon_mysql_queries_per_second",
            "sermon_mysql_slow_queries_per_second",
            "sermon_mysql_innodb_buffer_pool_hit_ratio",
            "sermon_mysql_innodb_buffer_pool_utilization_ratio",
            "sermon_mysql_long_queries",
        ] {
            assert!(seen.contains(want), "{version}: no {want} in {seen:?}");
        }
        assert_eq!(
            has(&samples, "sermon_mysql_replica"),
            Some(sermon_core::Value::Int(0)),
            "{version}"
        );
        // A wrong password: refused over TLS; without, caching_sha2_password asks for the full
        // login, which needs TLS – the reason says both.
        let tls = if version.starts_with("mysql") {
            "verify"
        } else {
            "off"
        };
        let Err(RunError::Failed(why)) =
            collect(&mysql::Mysql, &config("sermon", tls), "wrong").await
        else {
            panic!("{version}: a wrong password was taken");
        };
        assert!(
            why.contains("login refused") && !why.contains(&pw),
            "{version}: {why}"
        );
        if version.starts_with("mysql") {
            let Err(RunError::Failed(why)) =
                collect(&mysql::Mysql, &config("sermon", "off"), "wrong").await
            else {
                panic!("{version}: a wrong password was taken");
            };
            assert!(why.contains("password is wrong"), "{version}: {why}");
        }
        let Err(RunError::Failed(why)) = collect(&mysql::Mysql, &config("plain", tls), &pw).await
        else {
            panic!("{version}: a user without rights");
        };
        assert!(why.contains("PROCESS"), "{version}: {why}");
        write_mysql_fixtures(&version, &address, &pw).await;
        if let Some((_, at)) = replicas.iter().find(|(v, _)| *v == version) {
            let config = format!(r#"{{"address":"{at}","user":"sermon","tls":{verify}}}"#);
            let on_replica = collect(&mysql::Mysql, &config, &pw)
                .await
                .unwrap_or_else(|e| panic!("{version} replica: {e}"));
            assert_eq!(
                has(&on_replica, "sermon_mysql_replica"),
                Some(sermon_core::Value::Int(1)),
                "{version}"
            );
            assert_eq!(
                has(&on_replica, "sermon_mysql_replication_running"),
                Some(sermon_core::Value::Int(1)),
                "{version}"
            );
            assert!(
                has(&on_replica, "sermon_mysql_replication_lag_seconds").is_some(),
                "{version}"
            );
            write_mysql_fixtures(&format!("{version}-replica"), at, &pw).await;
        }
    }
}

/// Writes the handshake and the answers of the server at `address` below `SERMON_FIXTURES_OUT`
/// (`mysql/<version>/`); the user `sermon` logs in fast (its login is cached by then).
async fn write_mysql_fixtures(version: &str, address: &str, pw: &str) {
    let Some(out) = std::env::var("SERMON_FIXTURES_OUT")
        .ok()
        .filter(|o| !o.is_empty())
    else {
        return;
    };
    let (host, port) = address.rsplit_once(':').unwrap();
    let io = tokio::net::TcpStream::connect((host, port.parse::<u16>().unwrap()))
        .await
        .unwrap();
    let mut packets = mysql::wire::Packets::new(io);
    let greeting = packets.read().await.unwrap();
    let handshake = mysql::wire::Handshake::parse(&greeting).unwrap();
    let login = mysql::wire::Login {
        user: "sermon",
        password: pw,
        database: "",
        secure: false,
    };
    packets
        .write(&mysql::wire::handshake_response(&handshake, &login, false).unwrap())
        .await
        .unwrap();
    mysql::wire::finish_login(&mut packets, &login)
        .await
        .unwrap();
    let dir = std::path::Path::new(&out).join("mysql").join(version);
    std::fs::create_dir_all(&dir).unwrap();
    std::fs::write(dir.join("handshake.bin"), &greeting).unwrap();
    let replica = match packets.query(mysql::REPLICA).await {
        Err(_) => packets.query(mysql::REPLICA_OLD).await.unwrap(),
        Ok(set) => set,
    };
    for (name, sql) in [
        ("privileges", mysql::PRIVILEGES),
        ("status", mysql::STATUS),
        ("variables", mysql::VARIABLES),
        ("long", mysql::LONG),
    ] {
        let set = packets.query(sql).await.unwrap();
        let text = serde_json::to_string_pretty(
            &serde_json::json!({ "columns": set.columns, "rows": set.rows }),
        )
        .unwrap();
        std::fs::write(dir.join(format!("{name}.json")), text + "\n").unwrap();
    }
    let text = serde_json::to_string_pretty(
        &serde_json::json!({ "columns": replica.columns, "rows": replica.rows }),
    )
    .unwrap();
    std::fs::write(dir.join("replica.json"), text + "\n").unwrap();
}
