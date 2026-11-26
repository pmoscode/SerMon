//! The database modules against real servers (M5-13, M5-14): `scripts/test-db-modules.sh`
//! starts them in containers and names them in the environment; without it these tests are
//! skipped (`#[ignore]`, run with `--ignored`).
//!
//! - `SERMON_TEST_POSTGRES`: `version@host:port,…`, users `sermon` (`pg_monitor`) and `plain`
//! - `SERMON_TEST_DB_PASSWORD`: the password of both
//! - `SERMON_FIXTURES_OUT`: if set, the answers are written there as fixtures
//!   (`postgres/<version>/{check,stats}.json`)

// Test code: a failure is a panic with its reason.
#![allow(clippy::unwrap_used, clippy::expect_used, clippy::panic)]

use std::collections::BTreeSet;
use std::time::Duration;

use sermon_core::Sample;
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

async fn collect(
    module: &dyn Module,
    config: &str,
    password: &str,
) -> Result<Vec<Sample>, RunError> {
    let schema = Schema::parse(module.schema()).unwrap();
    let secrets = Secrets::new([("secret".to_owned(), password.to_owned())]);
    let config = schema.validate(config, &secrets.fields()).unwrap();
    let (veto, host) = (Veto::default(), Host::default());
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
        let config = format!(r#"{{"address":"{address}","database":"app"}}"#);
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
        let plain = format!(r#"{{"address":"{address}","role":"plain","database":"app"}}"#);
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
