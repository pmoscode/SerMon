use std::path::Path;

use sermon_core::Value as V;

use super::*;
use crate::discovery::Listener;
use crate::{Schema, Secrets};

fn fixture(version: &str, name: &str) -> Vec<ResultSet> {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../fixtures/modules/postgres")
        .join(version)
        .join(format!("{name}.json"));
    let text = std::fs::read_to_string(&path).unwrap_or_else(|e| panic!("{}: {e}", path.display()));
    let sets: Vec<serde_json::Value> = serde_json::from_str(&text).unwrap();
    sets.iter()
        .map(|s| ResultSet {
            columns: s["columns"]
                .as_array()
                .unwrap()
                .iter()
                .map(|c| c.as_str().unwrap().to_owned())
                .collect(),
            rows: s["rows"]
                .as_array()
                .unwrap()
                .iter()
                .map(|r| {
                    r.as_array()
                        .unwrap()
                        .iter()
                        .map(|v| v.as_str().map(str::to_owned))
                        .collect()
                })
                .collect(),
        })
        .collect()
}

fn read(version: &str) -> Stats {
    let (standby, max) = check(&fixture(version, "check"), "sermon").unwrap();
    stats(&fixture(version, "stats"), standby, max)
}

fn value(samples: &[Sample], metric: &str, label: Option<(&str, &str)>) -> Option<V> {
    samples
        .iter()
        .find(|s| {
            s.metric == metric
                && label.is_none_or(|(k, v)| s.labels.iter().any(|(n, x)| *n == k && x == v))
        })
        .map(|s| s.value)
}

#[test]
fn its_schema_is_in_the_subset() {
    let schema = Schema::parse(SCHEMA).unwrap();
    assert_eq!(schema.secrets().collect::<Vec<_>>(), ["secret"]);
    let config = schema.validate("{}", &[]).unwrap();
    assert_eq!(
        (config["address"].as_str(), config["role"].as_str()),
        (Some("127.0.0.1:5432"), Some("sermon"))
    );
    for bad in [
        r#"{"role":"a b"}"#,
        r#"{"tls":"maybe"}"#,
        r#"{"secret":"x"}"#,
        r#"{"address":"host"}"#,
    ] {
        assert!(schema.validate(bad, &[]).is_err(), "{bad}");
    }
}

// The answers of every supported version, recorded under load (scripts/test-db-modules.sh).
#[test]
fn reads_every_version() {
    for version in ["13", "14", "15", "16", "17"] {
        let s = read(version);
        assert!(!s.standby, "{version}");
        assert_eq!(s.max_connections, Some(100), "{version}");
        assert!(
            s.connections["active"] >= 1,
            "{version}: our own session at least"
        );
        assert_eq!(s.connections.len(), 4, "{version}: every state, 0 without");
        assert_eq!(
            s.databases
                .iter()
                .map(|d| d.name.as_str())
                .collect::<Vec<_>>(),
            ["app", "postgres"],
            "{version}"
        );
        assert!(
            s.databases[0].size > 1 << 20 && s.databases[0].transactions > 0,
            "{version}"
        );
        assert!(s.locks.is_some_and(|(held, _)| held > 0), "{version}");
        assert_eq!(s.replay_lag, None, "{version}: no standby");
    }
    let primary = read("17");
    assert_eq!(primary.replicas.len(), 1);
    assert_eq!(
        primary.replicas[0].0, "192.0.2.7",
        "walreceiver is no name: the address"
    );
    let standby = read("17-standby");
    assert!(standby.standby && standby.replay_lag.is_some_and(|l| l < 60.0));
    let samples = samples(&standby, None);
    assert_eq!(
        value(&samples, "sermon_postgres_standby", None),
        Some(V::Int(1))
    );
    crate::assert_in_catalog("postgres", &samples);
}

#[test]
fn rates_come_from_two_runs() {
    let before = read("16");
    let mut now = before.clone();
    now.databases[0].transactions += 500;
    now.databases[0].blocks_hit += 990;
    now.databases[0].blocks_read += 10;
    let first = samples(&before, None);
    assert_eq!(
        value(&first, "sermon_postgres_transactions_per_second", None),
        None
    );
    let second = samples(&now, Some((&before, Duration::from_secs(10))));
    let app = Some(("database", "app"));
    assert_eq!(
        value(&second, "sermon_postgres_transactions_per_second", app),
        Some(V::Int(50))
    );
    assert_eq!(
        value(&second, "sermon_postgres_cache_hit_ratio", app),
        Some(V::Int(990_000))
    );
    assert_eq!(
        value(
            &second,
            "sermon_postgres_cache_hit_ratio",
            Some(("database", "postgres"))
        ),
        None,
        "no reads, no ratio"
    );
    assert_eq!(
        value(
            &second,
            "sermon_postgres_connections",
            Some(("state", "idle"))
        ),
        Some(V::Int(0))
    );
    crate::assert_in_catalog("postgres", &second);
    // A counter that went back (the statistics were reset): no rate.
    let mut reset = now.clone();
    reset.databases[0].transactions = 1;
    let after = samples(&reset, Some((&now, Duration::from_secs(10))));
    assert_eq!(
        value(&after, "sermon_postgres_transactions_per_second", app),
        None
    );
}

// Missing rights stop the run with a reason that says what to grant; odd values are left out.
#[test]
fn takes_only_what_holds() {
    let mut no_role = fixture("17", "check");
    no_role[0].rows[0][3] = Some("f".into());
    let Err(RunError::Failed(why)) = check(&no_role, "alice") else {
        panic!("taken without pg_monitor");
    };
    assert!(why.contains("GRANT pg_monitor TO alice"), "{why}");
    assert!(check(&[], "alice").is_err());
    let odd = |rows: Vec<Vec<Option<&str>>>| ResultSet {
        columns: Vec::new(),
        rows: rows
            .into_iter()
            .map(|r| r.into_iter().map(|v| v.map(str::to_owned)).collect())
            .collect(),
    };
    let sets = vec![
        odd(vec![
            vec![Some("idle in transaction (aborted)"), Some("2")],
            vec![Some("fastpath function call"), Some("x")],
        ]),
        odd(vec![
            vec![
                Some("evil\u{1b}[2J"),
                Some("1"),
                Some("1"),
                Some("1"),
                Some("1"),
            ],
            vec![
                Some(&"d".repeat(100)),
                Some("1"),
                Some("1"),
                Some("1"),
                Some("1"),
            ],
            vec![Some("short"), Some("-1"), Some("1"), Some("1"), Some("1")],
            vec![None, Some("1"), Some("1"), Some("1"), Some("1")],
        ]),
        odd(vec![vec![Some("x"), Some("y")]]),
        odd(vec![
            vec![Some(""), Some("192.0.2.9"), Some("NaN")],
            vec![Some("node2"), None, Some("-1")],
            vec![Some("node3"), None, Some("1.5")],
        ]),
        odd(vec![vec![Some("4.2")]]),
    ];
    let s = stats(&sets, false, None);
    assert_eq!(
        (s.connections["idle_in_transaction"], s.connections["other"]),
        (2, 0)
    );
    assert_eq!(s.databases.len(), 2, "unsigned counters and a name only");
    assert_eq!(s.databases[0].name, "evil[2J");
    assert_eq!(s.databases[1].name.len(), MAX_LABEL);
    assert_eq!(s.locks, None);
    assert_eq!(s.replicas, [("node3".to_owned(), 1.5)]);
    assert_eq!(s.replay_lag, None, "only on a standby");
    let many: Vec<Vec<Option<&str>>> = (0..40)
        .map(|_| vec![Some("db"), Some("1"), Some("1"), Some("1"), Some("1")])
        .collect();
    assert_eq!(
        stats(&[odd(vec![]), odd(many)], false, None)
            .databases
            .len(),
        MAX_DATABASES
    );
    let samples = samples(&s, None);
    assert!(samples.len() <= 4 + 3 + 2 + MAX_REPLICAS + 1 + MAX_DATABASES * 3);
}

#[test]
fn builds_from_its_configuration() {
    let schema = Schema::parse(SCHEMA).unwrap();
    let host = crate::Host::default();
    let veto = Veto::default().any_port();
    let secrets = Secrets::new([("secret".to_owned(), "pencil".to_owned())]);
    let setup = Setup {
        veto: &veto,
        secrets: &secrets,
        host: &host,
    };
    let build = |json: &str| Postgres.build(&schema.validate(json, &["secret"]).unwrap(), &setup);
    assert!(build("{}").is_ok());
    assert!(build(r#"{"socket":"/run/postgresql/.s.PGSQL.5432"}"#).is_ok());
    assert!(matches!(
        build(r#"{"socket":"/etc/sermon/x.sock"}"#),
        Err(BuildError::Vetoed(_))
    ));
    assert!(matches!(
        build(r#"{"socket":"/run/postgresql/.s.PGSQL.5432","tls":"verify"}"#),
        Err(BuildError::Invalid(_))
    ));
}

// An address outside the veto: vetoed before anything is sent.
#[tokio::test]
async fn connects_only_inside_the_veto() {
    let schema = Schema::parse(SCHEMA).unwrap();
    let host = crate::Host::default();
    let veto = Veto::default().any_port();
    let secrets = Secrets::default();
    let setup = Setup {
        veto: &veto,
        secrets: &secrets,
        host: &host,
    };
    let mut outside = Postgres
        .build(
            &schema
                .validate(r#"{"address":"192.0.2.10:5432"}"#, &[])
                .unwrap(),
            &setup,
        )
        .unwrap();
    assert!(matches!(
        outside.collect(&Run::default()).await,
        Err(RunError::Vetoed(_))
    ));
}

#[test]
fn postgres_suggests_itself_with_its_socket_or_port() {
    assert!(Postgres.discover(&HostFacts::default()).is_none());
    let mut facts = HostFacts {
        processes: ["postgres".to_owned()].into(),
        ..HostFacts::default()
    };
    facts.listeners.insert(Listener {
        ip: "127.0.0.1".parse().unwrap(),
        port: 5432,
    });
    let found = Postgres.discover(&facts).unwrap();
    assert_eq!(
        found.suggested,
        Some(serde_json::json!({ "address": "127.0.0.1:5432" }))
    );
    facts
        .sockets
        .insert("/run/postgresql/.s.PGSQL.5432".to_owned());
    let found = Postgres.discover(&facts).unwrap();
    assert_eq!(
        found.suggested,
        Some(serde_json::json!({ "socket": "/run/postgresql/.s.PGSQL.5432" }))
    );
}
