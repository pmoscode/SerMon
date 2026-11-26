use std::net::SocketAddr;
use std::path::Path;
use std::sync::{Arc, Mutex};

use sermon_core::Value;
use sermon_core::testing::FakeClock;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::{TcpListener, UnixListener};

use super::*;
use crate::veto::Net;
use crate::{Host, Schema, Secrets};

const VERSIONS: [&str; 5] = [
    "redis-6.2",
    "redis-7.2",
    "redis-7.4",
    "redis-8.0",
    "valkey-8.1",
];

fn fixture(version: &str, role: &str) -> String {
    std::fs::read_to_string(
        Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../../fixtures/modules/redis")
            .join(version)
            .join(format!("info-{role}.txt")),
    )
    .unwrap()
}

/// A fake Redis: `requirepass` if `password`, `INFO` answers the current text; it keeps the
/// commands it got.
struct Fake {
    password: Option<&'static str>,
    info: Mutex<String>,
    seen: Mutex<Vec<String>>,
}

impl Fake {
    fn new(password: Option<&'static str>, info: String) -> Arc<Self> {
        Arc::new(Self {
            password,
            info: Mutex::new(info),
            seen: Mutex::default(),
        })
    }

    /// The answers to `commands` (each a list of arguments) and whether to go on.
    fn answer(&self, args: &[String], authed: &mut bool) -> Vec<u8> {
        self.seen.lock().unwrap().push(args.join(" "));
        match args.first().map(String::as_str) {
            Some("AUTH") => match (self.password, args.last()) {
                (Some(p), Some(given)) if p == given => {
                    *authed = true;
                    b"+OK\r\n".to_vec()
                }
                (None, _) => b"-ERR AUTH <password> called without any password configured for the default user.\r\n".to_vec(),
                _ => b"-WRONGPASS invalid username-password pair or user is disabled.\r\n".to_vec(),
            },
            Some("INFO") if self.password.is_some() && !*authed => {
                b"-NOAUTH Authentication required.\r\n".to_vec()
            }
            Some("INFO") => {
                let info = self.info.lock().unwrap().clone();
                format!("${}\r\n{info}\r\n", info.len()).into_bytes()
            }
            _ => b"-ERR unknown command\r\n".to_vec(),
        }
    }
}

/// The commands of a RESP stream (arrays of bulk strings), as far as complete.
fn commands(buf: &[u8]) -> (Vec<Vec<String>>, usize) {
    let text = String::from_utf8_lossy(buf);
    let lines: Vec<&str> = text.split("\r\n").collect();
    let (mut out, mut i, mut used) = (Vec::new(), 0, 0);
    while i < lines.len() {
        let Some(n) = lines[i]
            .strip_prefix('*')
            .and_then(|n| n.parse::<usize>().ok())
        else {
            break;
        };
        if i + 1 + 2 * n >= lines.len() {
            break;
        }
        let args: Vec<String> = (0..n).map(|k| lines[i + 2 + 2 * k].to_owned()).collect();
        used += lines[i..=i + 2 * n]
            .iter()
            .map(|l| l.len() + 2)
            .sum::<usize>();
        out.push(args);
        i += 1 + 2 * n;
    }
    (out, used)
}

async fn talk<S: AsyncReadExt + AsyncWriteExt + Unpin>(mut s: S, fake: Arc<Fake>) {
    let (mut buf, mut authed) = (Vec::new(), false);
    let mut chunk = [0u8; 4096];
    while let Ok(n) = s.read(&mut chunk).await {
        if n == 0 {
            return;
        }
        buf.extend_from_slice(&chunk[..n]);
        let (cmds, used) = commands(&buf);
        buf.drain(..used);
        for args in cmds {
            let answer = fake.answer(&args, &mut authed);
            if s.write_all(&answer).await.is_err() {
                return;
            }
        }
    }
}

async fn serve(fake: Arc<Fake>) -> SocketAddr {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    tokio::spawn(async move {
        while let Ok((s, _)) = listener.accept().await {
            tokio::spawn(talk(s, fake.clone()));
        }
    });
    addr
}

fn build(
    config: &str,
    password: Option<&str>,
    veto: &Veto,
) -> Result<Box<dyn Instance>, BuildError> {
    let secrets = Secrets::new(password.map(|p| ("auth".to_owned(), p.to_owned())));
    let config = Schema::parse(SCHEMA)
        .unwrap()
        .validate(config, &secrets.fields())
        .unwrap();
    Redis.build(
        &config,
        &Setup {
            veto,
            secrets: &secrets,
            host: &Host::default(),
        },
    )
}

fn value(samples: &[Sample], metric: &str) -> Option<Value> {
    samples.iter().find(|s| s.metric == metric).map(|s| s.value)
}

#[test]
fn parses_info_of_every_version() {
    for version in VERSIONS {
        let p = parse_info(&fixture(version, "primary")).unwrap();
        assert_eq!(p.primary, Some(true), "{version}");
        assert_eq!(p.replicas, Some(1), "{version}");
        assert_eq!(p.keys, 51, "{version}: db0 and db1");
        assert_eq!(p.maxmemory, Some(64 * 1024 * 1024), "{version}");
        assert_eq!((p.hits, p.misses), (Some(30), Some(31)), "{version}");
        assert_eq!(p.expired, Some(1), "{version}");
        assert_eq!(p.rdb_ok, Some(true), "{version}");
        assert!(
            p.used_memory.unwrap() > 100_000 && p.commands.unwrap() > 100,
            "{version}"
        );
        assert_eq!(p.master_link_up, None, "{version}");
        let r = parse_info(&fixture(version, "replica")).unwrap();
        assert_eq!(r.primary, Some(false), "{version}");
        assert_eq!(r.master_link_up, Some(true), "{version}");
        assert_eq!(r.keys, 51, "{version}: replicated");
    }
}

// The plan's test: no INFO, broken values.
#[test]
fn refuses_what_is_no_info() {
    let good = fixture("redis-7.4", "primary");
    for bad in [
        String::new(),
        "hello".into(),
        good.replace("used_memory:", "memory:"),
        good.replace("role:master", "rolle:master"),
        format!("{good}{}", "x".repeat(MAX_INFO)),
    ] {
        assert!(parse_info(&bad).is_err(), "{}", &bad[..bad.len().min(40)]);
    }
    let odd = good
        .replace("keyspace_hits:30", "keyspace_hits:-3")
        .replace("db1:keys=1", "db1:keys=x")
        .replace("dbx", "");
    let f = parse_info(&odd).unwrap();
    assert_eq!(f.hits, None);
    assert_eq!(f.keys, 50);
}

#[tokio::test]
async fn reports_info_and_rates() {
    let fake = Fake::new(None, fixture("redis-8.0", "primary"));
    let addr = serve(fake.clone()).await;
    let mut redis = build(
        &format!(r#"{{"address":"{addr}"}}"#),
        None,
        &Veto::default(),
    )
    .unwrap();
    let clock = Arc::new(FakeClock::new(std::time::SystemTime::UNIX_EPOCH));
    let run = Run::new(clock.clone());
    let first = redis.collect(&run).await.unwrap();
    crate::assert_in_catalog("redis", &first);
    assert_eq!(value(&first, "sermon_redis_up"), Some(Value::Int(1)));
    assert_eq!(value(&first, "sermon_redis_keys"), Some(Value::Int(51)));
    assert_eq!(
        value(&first, "sermon_redis_memory_max_bytes"),
        Some(Value::Int(64 << 20))
    );
    assert_eq!(
        value(&first, "sermon_redis_connected_replicas"),
        Some(Value::Int(1))
    );
    assert_eq!(
        value(&first, "sermon_redis_master_link_up"),
        None,
        "a primary"
    );
    assert_eq!(value(&first, "sermon_redis_commands_per_second"), None);
    assert_eq!(*fake.seen.lock().unwrap(), ["INFO"], "nothing but INFO");

    let next = fixture("redis-8.0", "primary")
        .replace("keyspace_hits:30", "keyspace_hits:330")
        .replace("keyspace_misses:31", "keyspace_misses:131")
        .replace("evicted_keys:0", "evicted_keys:60");
    *fake.info.lock().unwrap() = next;
    clock.advance(Duration::from_secs(10));
    let second = redis.collect(&run).await.unwrap();
    crate::assert_in_catalog("redis", &second);
    assert_eq!(
        value(&second, "sermon_redis_keyspace_hits_per_second"),
        Some(Value::Float(30.0))
    );
    assert_eq!(
        value(&second, "sermon_redis_keyspace_misses_per_second"),
        Some(Value::Float(10.0))
    );
    assert_eq!(
        value(&second, "sermon_redis_keyspace_hit_ratio"),
        Some(Value::Int(750_000))
    );
    assert_eq!(
        value(&second, "sermon_redis_evicted_keys_per_second"),
        Some(Value::Float(6.0))
    );
    assert_eq!(
        value(&second, "sermon_redis_commands_per_second"),
        Some(Value::Float(0.0))
    );

    *fake.info.lock().unwrap() = fixture("redis-8.0", "primary")
        .replace("keyspace_hits:30", "keyspace_hits:330")
        .replace("keyspace_misses:31", "keyspace_misses:231")
        .replace("evicted_keys:0", "evicted_keys:60");
    clock.advance(Duration::from_secs(10));
    let only_misses = redis.collect(&run).await.unwrap();
    assert_eq!(
        value(&only_misses, "sermon_redis_keyspace_hit_ratio"),
        Some(Value::Int(0)),
        "misses only"
    );
    clock.advance(Duration::from_secs(10));
    let idle = redis.collect(&run).await.unwrap();
    assert_eq!(
        value(&idle, "sermon_redis_keyspace_hit_ratio"),
        None,
        "no lookups"
    );

    let replica = Fake::new(None, fixture("valkey-8.1", "replica"));
    let addr = serve(replica).await;
    let mut r = build(
        &format!(r#"{{"address":"{addr}"}}"#),
        None,
        &Veto::default(),
    )
    .unwrap();
    let s = r.collect(&run).await.unwrap();
    assert_eq!(
        value(&s, "sermon_redis_master_link_up"),
        Some(Value::Int(1))
    );
    assert_eq!(
        value(&s, "sermon_redis_memory_max_bytes"),
        None,
        "no maxmemory"
    );
}

#[tokio::test]
async fn logs_in_with_the_password() {
    let fake = Fake::new(Some("s3cret-pw"), fixture("redis-7.2", "primary"));
    let addr = serve(fake.clone()).await;
    let config = format!(r#"{{"address":"{addr}"}}"#);
    let mut ok = build(&config, Some("s3cret-pw"), &Veto::default()).unwrap();
    let s = ok.collect(&Run::default()).await.unwrap();
    assert_eq!(value(&s, "sermon_redis_up"), Some(Value::Int(1)));
    assert_eq!(*fake.seen.lock().unwrap(), ["AUTH s3cret-pw", "INFO"]);

    let mut wrong = build(&config, Some("nope"), &Veto::default()).unwrap();
    let e = wrong
        .collect(&Run::default())
        .await
        .err()
        .unwrap()
        .to_string();
    assert!(e.contains("AUTH failed") && !e.contains("nope"), "{e}");
    let mut none = build(&config, None, &Veto::default()).unwrap();
    let e = none
        .collect(&Run::default())
        .await
        .err()
        .unwrap()
        .to_string();
    assert!(e.contains("NOAUTH"), "{e}");

    let acl = build(
        &format!(r#"{{"address":"{addr}","username":"sermon"}}"#),
        Some("s3cret-pw"),
        &Veto::default(),
    );
    let mut acl = acl.unwrap();
    acl.collect(&Run::default()).await.unwrap();
    assert_eq!(
        fake.seen.lock().unwrap().last().map(String::as_str),
        Some("INFO")
    );
    assert!(
        fake.seen
            .lock()
            .unwrap()
            .contains(&"AUTH sermon s3cret-pw".to_owned())
    );
    assert!(matches!(
        build(r#"{"username":"sermon"}"#, None, &Veto::default()),
        Err(BuildError::Invalid(_))
    ));
}

#[tokio::test]
async fn tells_down_from_wrong() {
    let closed = TcpListener::bind("127.0.0.1:0")
        .await
        .unwrap()
        .local_addr()
        .unwrap();
    let mut down = build(
        &format!(r#"{{"address":"{closed}"}}"#),
        None,
        &Veto::default(),
    )
    .unwrap();
    let s = down.collect(&Run::default()).await.unwrap();
    assert_eq!(s.len(), 1);
    assert_eq!(value(&s, "sermon_redis_up"), Some(Value::Int(0)));

    // An HTTP server, a huge or a broken bulk string, a lying length.
    for answer in [
        b"HTTP/1.1 400 Bad Request\r\n\r\n".to_vec(),
        format!("${}\r\n", MAX_INFO + 1).into_bytes(),
        b"$5\r\nhello!!\r\n".to_vec(),
        b"*1\r\n$4\r\nINFO\r\n".to_vec(),
        vec![b'+'; 4096],
    ] {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        tokio::spawn(async move {
            if let Ok((mut s, _)) = listener.accept().await {
                let mut buf = [0u8; 64];
                let _ = s.read(&mut buf).await;
                let _ = s.write_all(&answer).await;
                let _ = s.shutdown().await;
            }
        });
        let mut wrong = build(
            &format!(r#"{{"address":"{addr}"}}"#),
            None,
            &Veto::default(),
        )
        .unwrap();
        assert!(matches!(
            wrong.collect(&Run::default()).await,
            Err(RunError::Failed(_))
        ));
    }

    let veto = Veto {
        targets: vec![Net::parse("127.0.0.2/32").unwrap()],
        ..Veto::default()
    };
    let mut vetoed = build(r#"{"address":"127.0.0.1:6379"}"#, None, &veto).unwrap();
    assert!(matches!(
        vetoed.collect(&Run::default()).await,
        Err(RunError::Vetoed(_))
    ));
}

#[tokio::test(start_paused = true)]
async fn a_silent_redis_is_down_within_the_timeout() {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    tokio::spawn(async move {
        let mut open = Vec::new();
        while let Ok((s, _)) = listener.accept().await {
            open.push(s);
        }
    });
    let mut silent = build(
        &format!(r#"{{"address":"{addr}","timeout":"1s"}}"#),
        None,
        &Veto::default(),
    )
    .unwrap();
    let s = silent.collect(&Run::default()).await.unwrap();
    assert_eq!(value(&s, "sermon_redis_up"), Some(Value::Int(0)));
}

#[tokio::test]
async fn speaks_over_the_unix_socket() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("redis.sock");
    let listener = UnixListener::bind(&path).unwrap();
    let fake = Fake::new(None, fixture("redis-6.2", "primary"));
    tokio::spawn(async move {
        while let Ok((s, _)) = listener.accept().await {
            tokio::spawn(talk(s, fake.clone()));
        }
    });
    let veto = Veto {
        sockets: vec![std::fs::canonicalize(dir.path()).unwrap()],
        ..Veto::default()
    };
    let config = format!(r#"{{"socket":"{}"}}"#, path.display());
    let mut redis = build(&config, None, &veto).unwrap();
    let s = redis.collect(&Run::default()).await.unwrap();
    assert_eq!(value(&s, "sermon_redis_up"), Some(Value::Int(1)));
    assert!(matches!(
        build(&config, None, &Veto::default()),
        Err(BuildError::Vetoed(_))
    ));
}

#[test]
fn is_suggested_where_redis_runs() {
    let modules: Vec<Arc<dyn Module>> = vec![Arc::new(Redis)];
    let facts = HostFacts {
        processes: ["redis-server".to_owned()].into(),
        units: ["redis-server.service".to_owned()].into(),
        listeners: [crate::discovery::Listener {
            ip: "127.0.0.1".parse().unwrap(),
            port: 6379,
        }]
        .into(),
        ..HostFacts::default()
    };
    let hints = crate::discovery::hints(&modules, &facts);
    assert_eq!(
        hints[0].reasons,
        [
            "process:redis-server",
            "unit:redis-server.service",
            "port:6379"
        ]
    );
    assert_eq!(
        hints[0].suggested_config_json,
        r#"{"address":"127.0.0.1:6379"}"#
    );
    let socket_only = HostFacts {
        processes: ["valkey-server".to_owned()].into(),
        sockets: ["/run/redis/redis-server.sock".to_owned()].into(),
        ..HostFacts::default()
    };
    assert_eq!(
        Redis.discover(&socket_only).unwrap().suggested,
        Some(serde_json::json!({"socket": "/run/redis/redis-server.sock"}))
    );
    assert!(Redis.discover(&HostFacts::default()).is_none());
}

#[test]
fn the_password_is_a_credential() {
    let schema = Schema::parse(SCHEMA).unwrap();
    assert_eq!(schema.secrets().collect::<Vec<_>>(), ["auth"]);
    assert!(
        schema.validate(r#"{"auth":"x"}"#, &[]).is_err(),
        "never in the configuration"
    );
    let config = schema.validate("{}", &[]).unwrap();
    assert_eq!(config["address"], "127.0.0.1:6379");
}

// The bounds of a reply, on the bytes alone.
#[tokio::test]
async fn bounds_the_replies() {
    async fn read(bytes: Vec<u8>) -> Result<Reply, String> {
        reply(&mut BufReader::new(&bytes[..])).await
    }
    assert_eq!(
        read(b"+OK\r\n".to_vec()).await,
        Ok(Reply::Simple("OK".into()))
    );
    assert_eq!(read(b"$-1\r\n".to_vec()).await, Ok(Reply::Nil));
    assert_eq!(
        read(b"$2\r\nhi\r\n".to_vec()).await,
        Ok(Reply::Bulk(b"hi".to_vec()))
    );
    let mut huge = format!("${}\r\n", MAX_INFO + 1).into_bytes();
    huge.extend(std::iter::repeat_n(b'x', MAX_INFO + 1));
    huge.extend_from_slice(b"\r\n");
    assert!(read(huge).await.unwrap_err().contains("larger than"));
    let mut exact = format!("${MAX_INFO}\r\n").into_bytes();
    exact.extend(std::iter::repeat_n(b'x', MAX_INFO));
    exact.extend_from_slice(b"\r\n");
    assert!(read(exact).await.is_ok());
    assert!(
        read(b"$5\r\nhello!!\r\n".to_vec()).await.is_err(),
        "no CRLF after the bulk"
    );
    let mut long = vec![b'+'; 2000];
    long.extend_from_slice(b"\r\n");
    assert!(read(long).await.is_err(), "a head longer than 1 KiB");
    assert!(read(b"$x\r\n".to_vec()).await.is_err());
    assert!(read(b"OK\r\n".to_vec()).await.is_err());
}

// Down in between: Redis may have restarted and counted past the old values – no rate over it.
#[tokio::test]
async fn no_rate_across_an_outage() {
    let fake = Fake::new(None, fixture("redis-7.4", "primary"));
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let accept = |listener: TcpListener, fake: Arc<Fake>| async move {
        while let Ok((s, _)) = listener.accept().await {
            tokio::spawn(talk(s, fake.clone()));
        }
    };
    let server = tokio::spawn(accept(listener, fake.clone()));
    let mut redis = build(
        &format!(r#"{{"address":"{addr}"}}"#),
        None,
        &Veto::default(),
    )
    .unwrap();
    let clock = Arc::new(FakeClock::new(std::time::SystemTime::UNIX_EPOCH));
    let run = Run::new(clock.clone());
    redis.collect(&run).await.unwrap();
    server.abort();
    let _ = server.await;
    clock.advance(Duration::from_secs(10));
    let down = redis.collect(&run).await.unwrap();
    assert_eq!(value(&down, "sermon_redis_up"), Some(Value::Int(0)));
    *fake.info.lock().unwrap() =
        fixture("redis-7.4", "primary").replace("keyspace_hits:30", "keyspace_hits:90");
    tokio::spawn(accept(TcpListener::bind(addr).await.unwrap(), fake));
    clock.advance(Duration::from_secs(10));
    let again = redis.collect(&run).await.unwrap();
    assert_eq!(value(&again, "sermon_redis_up"), Some(Value::Int(1)));
    assert_eq!(value(&again, "sermon_redis_keyspace_hits_per_second"), None);
}

// A socket that turns into a link out of module_sockets after the start is refused at the run.
#[tokio::test]
async fn checks_the_socket_at_every_run() {
    let allowed = tempfile::tempdir().unwrap();
    let outside = tempfile::tempdir().unwrap();
    let real = outside.path().join("real.sock");
    let listener = UnixListener::bind(&real).unwrap();
    let fake = Fake::new(None, fixture("redis-6.2", "primary"));
    tokio::spawn(async move {
        while let Ok((s, _)) = listener.accept().await {
            tokio::spawn(talk(s, fake.clone()));
        }
    });
    let veto = Veto {
        sockets: vec![std::fs::canonicalize(allowed.path()).unwrap()],
        ..Veto::default()
    };
    let path = allowed.path().join("redis.sock");
    let mut redis = build(
        &format!(r#"{{"socket":"{}"}}"#, path.display()),
        None,
        &veto,
    )
    .unwrap();
    std::os::unix::fs::symlink(&real, &path).unwrap();
    assert!(matches!(
        redis.collect(&Run::default()).await,
        Err(RunError::Vetoed(_))
    ));
}
