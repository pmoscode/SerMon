use std::path::Path;

use sermon_core::Value as V;

use super::*;
use crate::discovery::Listener;
use crate::{Schema, Secrets};

fn fixture(version: &str, name: &str) -> ResultSet {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../fixtures/modules/mysql")
        .join(version)
        .join(format!("{name}.json"));
    let text = std::fs::read_to_string(&path).unwrap_or_else(|e| panic!("{}: {e}", path.display()));
    let s: serde_json::Value = serde_json::from_str(&text).unwrap();
    ResultSet {
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
    }
}

fn read(version: &str) -> Stats {
    check_rights(&fixture(version, "privileges"), "sermon").unwrap();
    let variables = values(&fixture(version, "variables"));
    Stats {
        status: values(&fixture(version, "status")),
        max_connections: variables.get("max_connections").copied(),
        buffer_pool_size: variables.get("innodb_buffer_pool_size").copied(),
        long_queries: fixture(version, "long").rows[0][0]
            .as_deref()
            .and_then(|v| v.parse().ok()),
        replica: replica(&fixture(version, "replica")),
    }
}

fn value(samples: &[Sample], metric: &str) -> Option<V> {
    samples.iter().find(|s| s.metric == metric).map(|s| s.value)
}

const VERSIONS: [&str; 5] = [
    "mysql-8.4",
    "mysql-9",
    "mariadb-10.11",
    "mariadb-11.4",
    "mariadb-11.8",
];

#[test]
fn its_schema_is_in_the_subset() {
    let schema = Schema::parse(SCHEMA).unwrap();
    assert_eq!(schema.secrets().collect::<Vec<_>>(), ["secret"]);
    let config = schema.validate("{}", &[]).unwrap();
    assert_eq!(config["address"].as_str(), Some("127.0.0.1:3306"));
    for bad in [
        r#"{"user":"a b"}"#,
        r#"{"tls":"maybe"}"#,
        r#"{"secret":"x"}"#,
    ] {
        assert!(schema.validate(bad, &[]).is_err(), "{bad}");
    }
}

// The answers of every supported version, recorded under load (scripts/test-db-modules.sh).
#[test]
fn reads_every_version() {
    for version in VERSIONS {
        let s = read(version);
        assert_eq!(s.status.len(), 11, "{version}: every counter asked for");
        assert_eq!(s.max_connections, Some(151), "{version}");
        assert_eq!(s.buffer_pool_size, Some(128 << 20), "{version}");
        assert_eq!(s.long_queries, Some(0), "{version}");
        assert_eq!(s.replica, None, "{version}: no source");
        let samples = samples(&s, None);
        assert!(
            value(&samples, "sermon_mysql_connections").is_some_and(|v| v != V::Int(0)),
            "{version}"
        );
        assert!(
            value(
                &samples,
                "sermon_mysql_innodb_buffer_pool_utilization_ratio"
            )
            .is_some_and(|v| matches!(v, V::Int(r) if r > 0 && r < 1_000_000)),
            "{version}"
        );
        assert_eq!(value(&samples, "sermon_mysql_replica"), Some(V::Int(0)));
        crate::assert_in_catalog("mysql", &samples);
    }
    for version in ["mysql-8.4-replica", "mariadb-11.4-replica"] {
        let s = read(version);
        let r = s.replica.clone().unwrap();
        assert!(
            r.running && r.lag.is_some_and(|l| l < 60),
            "{version}: {r:?}"
        );
        let samples = samples(&s, None);
        assert_eq!(
            value(&samples, "sermon_mysql_replication_running"),
            Some(V::Int(1))
        );
        crate::assert_in_catalog("mysql", &samples);
    }
}

#[test]
fn rates_come_from_two_runs() {
    let before = read("mariadb-11.8");
    let mut now = before.clone();
    for (name, more) in [
        ("Queries", 600),
        ("Slow_queries", 3),
        ("Innodb_buffer_pool_read_requests", 1000),
        ("Innodb_buffer_pool_reads", 10),
    ] {
        *now.status.get_mut(name).unwrap() += more;
    }
    assert_eq!(
        value(&samples(&before, None), "sermon_mysql_queries_per_second"),
        None
    );
    let s = samples(&now, Some((&before, Duration::from_secs(30))));
    assert_eq!(
        value(&s, "sermon_mysql_queries_per_second"),
        Some(V::Float(20.0))
    );
    assert_eq!(
        value(&s, "sermon_mysql_slow_queries_per_second"),
        Some(V::Float(0.1))
    );
    assert_eq!(
        value(&s, "sermon_mysql_innodb_buffer_pool_hit_ratio"),
        Some(V::Int(990_000))
    );
    crate::assert_in_catalog("mysql", &s);
    let mut restarted = now.clone();
    *restarted.status.get_mut("Queries").unwrap() = 5;
    *restarted
        .status
        .get_mut("Innodb_buffer_pool_reads")
        .unwrap() = 0;
    let s = samples(&restarted, Some((&now, Duration::from_secs(30))));
    assert_eq!(
        value(&s, "sermon_mysql_queries_per_second"),
        None,
        "a restart: no rate"
    );
    assert_eq!(value(&s, "sermon_mysql_innodb_buffer_pool_hit_ratio"), None);
}

// Missing rights stop the run with the grant; odd values are left out.
#[test]
fn takes_only_what_holds() {
    let usage = ResultSet {
        columns: vec!["PRIVILEGE_TYPE".into()],
        rows: vec![vec![Some("USAGE".into())]],
    };
    let Err(RunError::Failed(why)) = check_rights(&usage, "alice") else {
        panic!("taken without PROCESS");
    };
    assert!(
        why.contains("GRANT PROCESS") && why.contains("alice"),
        "{why}"
    );
    let mut sup = usage.clone();
    sup.rows.push(vec![Some("SUPER".into())]);
    assert!(check_rights(&sup, "alice").is_ok());
    let odd = ResultSet {
        columns: vec!["Variable_name".into(), "Value".into()],
        rows: vec![
            vec![Some("Queries".into()), Some("-1".into())],
            vec![Some("Threads_connected".into()), Some("x".into())],
            vec![None, Some("1".into())],
            vec![Some("Threads_running".into())],
            vec![Some("Slow_queries".into()), Some(" 7 ".into())],
        ],
    };
    assert_eq!(values(&odd), [("Slow_queries".to_owned(), 7)].into());
    let mut stopped = fixture("mysql-8.4-replica", "replica");
    let io = stopped
        .columns
        .iter()
        .position(|c| c == "Replica_IO_Running")
        .unwrap();
    let lag = stopped
        .columns
        .iter()
        .position(|c| c == "Seconds_Behind_Source")
        .unwrap();
    stopped.rows[0][io] = Some("Connecting".into());
    stopped.rows[0][lag] = None;
    assert_eq!(
        replica(&stopped),
        Some(Replica {
            running: false,
            lag: None
        })
    );
    assert_eq!(replica(&ResultSet::default()), None);
    let empty_pool = Stats {
        status: [
            ("Innodb_buffer_pool_pages_total".to_owned(), 0),
            ("Innodb_buffer_pool_pages_free".to_owned(), 0),
        ]
        .into(),
        ..Stats::default()
    };
    assert_eq!(
        value(
            &samples(&empty_pool, None),
            "sermon_mysql_innodb_buffer_pool_utilization_ratio"
        ),
        None
    );
}

#[test]
fn builds_from_its_configuration() {
    let schema = Schema::parse(SCHEMA).unwrap();
    let (host, veto) = (crate::Host::default(), Veto::default().any_port());
    let secrets = Secrets::new([("secret".to_owned(), "pencil".to_owned())]);
    let setup = Setup {
        veto: &veto,
        secrets: &secrets,
        host: &host,
    };
    let build = |json: &str| Mysql.build(&schema.validate(json, &["secret"]).unwrap(), &setup);
    assert!(build("{}").is_ok());
    assert!(build(r#"{"socket":"/run/mysqld/mysqld.sock"}"#).is_ok());
    assert!(matches!(
        build(r#"{"socket":"/var/lib/mysql/mysql.sock"}"#),
        Err(BuildError::Vetoed(_))
    ));
    assert!(matches!(
        build(r#"{"socket":"/run/mysqld/mysqld.sock","tls":"verify"}"#),
        Err(BuildError::Invalid(_))
    ));
}

#[tokio::test]
async fn connects_only_inside_the_veto() {
    let schema = Schema::parse(SCHEMA).unwrap();
    let (host, veto, secrets) = (
        crate::Host::default(),
        Veto::default().any_port(),
        Secrets::default(),
    );
    let setup = Setup {
        veto: &veto,
        secrets: &secrets,
        host: &host,
    };
    let mut outside = Mysql
        .build(
            &schema
                .validate(r#"{"address":"192.0.2.10:3306"}"#, &[])
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
fn mysqld_or_mariadbd_suggest_the_module() {
    assert!(Mysql.discover(&HostFacts::default()).is_none());
    let mut facts = HostFacts {
        processes: ["mariadbd".to_owned()].into(),
        ..HostFacts::default()
    };
    facts.listeners.insert(Listener {
        ip: "127.0.0.1".parse().unwrap(),
        port: 3306,
    });
    assert_eq!(
        Mysql.discover(&facts).unwrap().suggested,
        Some(serde_json::json!({ "address": "127.0.0.1:3306" }))
    );
    facts.sockets.insert("/run/mysqld/mysqld.sock".to_owned());
    assert_eq!(
        Mysql.discover(&facts).unwrap().suggested,
        Some(serde_json::json!({ "socket": "/run/mysqld/mysqld.sock" }))
    );
}

/// A MySQL 8.4 that asks for the full login of `caching_sha2_password` and refuses whatever
/// comes; returns the packet after the handshake response, if any.
async fn asks_for_the_full_login(io: Box<dyn sql::Io>) -> Option<Vec<u8>> {
    let greeting = std::fs::read(
        Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../../fixtures/modules/mysql/mysql-8.4/handshake.bin"),
    )
    .unwrap();
    let mut p = Packets::new(io);
    p.write(&greeting).await.unwrap();
    p.read().await.unwrap();
    p.write(&[1, 4]).await.unwrap();
    let sent = tokio::time::timeout(Duration::from_secs(2), p.read())
        .await
        .ok()
        .and_then(Result::ok);
    let denied = [&[0xffu8, 0x15, 0x04][..], b"#28000Access denied"].concat();
    let _ = p.write(&denied).await;
    sent
}

// M5-33: the password of the full login goes over the socket, not over plain TCP.
#[tokio::test]
async fn the_full_login_only_over_the_socket_or_verified_tls() {
    let dir = crate::TestDir::new();
    let socket = dir.path().join("mysqld.sock");
    let veto = Veto {
        sockets: vec![dir.path().to_path_buf()],
        ..Veto::default().any_port()
    };
    let host = crate::Host::default();
    let secrets = Secrets::new([("secret".to_owned(), "pencil".to_owned())]);
    let setup = Setup {
        veto: &veto,
        secrets: &secrets,
        host: &host,
    };
    let schema = Schema::parse(SCHEMA).unwrap();
    let unix = tokio::net::UnixListener::bind(&socket).unwrap();
    let tcp = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let port = tcp.local_addr().unwrap().port();
    for over_socket in [true, false] {
        let config = if over_socket {
            format!(r#"{{"socket":"{}"}}"#, socket.display())
        } else {
            format!(r#"{{"address":"127.0.0.1:{port}"}}"#)
        };
        let mut reader = Mysql
            .build(&schema.validate(&config, &["secret"]).unwrap(), &setup)
            .unwrap();
        let server = async {
            let io: Box<dyn sql::Io> = if over_socket {
                Box::new(unix.accept().await.unwrap().0)
            } else {
                Box::new(tcp.accept().await.unwrap().0)
            };
            asks_for_the_full_login(io).await
        };
        let run = Run::default();
        let (sent, result) = tokio::join!(server, reader.collect(&run));
        let Err(RunError::Failed(why)) = result else {
            panic!("{config}: {result:?}");
        };
        if over_socket {
            assert_eq!(sent.as_deref(), Some(&b"pencil\0"[..]), "{config}");
            assert!(why.contains("login refused"), "{why}");
        } else {
            assert_eq!(sent, None, "{config}: the password stayed");
            assert!(why.contains("tls verify"), "{why}");
        }
    }
}

// M5-20: a server that doesn't answer is down (`sermon_mysql_up` 0), not a failed module; one
// that answers is up.
#[tokio::test]
async fn a_server_that_does_not_answer_is_down() {
    let dir = crate::TestDir::new();
    let veto = Veto {
        sockets: vec![dir.path().to_path_buf()],
        ..Veto::default().any_port()
    };
    let host = crate::Host::default();
    let secrets = Secrets::default();
    let setup = Setup {
        veto: &veto,
        secrets: &secrets,
        host: &host,
    };
    let schema = Schema::parse(SCHEMA).unwrap();
    let closed = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let port = closed.local_addr().unwrap().port();
    drop(closed);
    let socket = dir.path().join("mysqld.sock");
    for config in [
        format!(r#"{{"address":"127.0.0.1:{port}"}}"#),
        format!(r#"{{"socket":"{}"}}"#, socket.display()),
    ] {
        let mut reader = Mysql
            .build(&schema.validate(&config, &[]).unwrap(), &setup)
            .unwrap();
        let samples = reader.collect(&Run::default()).await.unwrap();
        assert_eq!(samples.len(), 1, "{config}");
        assert_eq!(
            value(&samples, "sermon_mysql_up"),
            Some(V::Int(0)),
            "{config}"
        );
        crate::assert_in_catalog("mysql", &samples);
    }
    let up = samples(&read("mysql-8.4"), None);
    assert_eq!(value(&up, "sermon_mysql_up"), Some(V::Int(1)));
}
