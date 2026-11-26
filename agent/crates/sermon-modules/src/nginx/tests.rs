use std::net::SocketAddr;
use std::path::Path;
use std::sync::Mutex;

use sermon_core::Value;
use sermon_core::testing::FakeClock;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpListener;

use super::*;
use crate::veto::Net;
use crate::{Host, Schema, Secrets, Veto};
use sermon_core::ProcRoot;

const VERSIONS: [&str; 5] = ["1.22", "1.24", "1.26", "1.28", "1.29"];

fn fixture(version: &str) -> String {
    std::fs::read_to_string(
        Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../../fixtures/modules/nginx")
            .join(version)
            .join("stub_status.txt"),
    )
    .unwrap()
}

/// A server answering `status` and the current page.
async fn serve(status: &'static str, page: Arc<Mutex<String>>) -> SocketAddr {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    tokio::spawn(answer_on(listener, status, page));
    addr
}

async fn answer_on(listener: TcpListener, status: &'static str, page: Arc<Mutex<String>>) {
    while let Ok((mut s, _)) = listener.accept().await {
        let body = page.lock().unwrap().clone();
        tokio::spawn(async move {
            let mut buf = [0u8; 4096];
            let _ = s.read(&mut buf).await;
            let answer = format!(
                "HTTP/1.1 {status}\r\nContent-Type: text/plain\r\nContent-Length: {}\r\n\r\n{body}",
                body.len()
            );
            let _ = s.write_all(answer.as_bytes()).await;
        });
    }
}

fn build(config: &str, veto: &Veto) -> Result<Box<dyn Instance>, BuildError> {
    let config = Schema::parse(SCHEMA)
        .unwrap()
        .validate(config, &[])
        .unwrap();
    Nginx.build(
        &config,
        &Setup {
            veto,
            secrets: &Secrets::default(),
            host: &Host::default(),
        },
    )
}

fn value(samples: &[Sample], metric: &str) -> Option<Value> {
    samples.iter().find(|s| s.metric == metric).map(|s| s.value)
}

#[test]
fn parses_the_page_of_every_version() {
    for version in VERSIONS {
        assert_eq!(
            parse_stub_status(&fixture(version)),
            Ok(StubStatus {
                active: 3,
                accepts: 13,
                handled: 13,
                requests: 13,
                reading: 1,
                writing: 1,
                waiting: 1,
            }),
            "{version}"
        );
    }
}

// The plan's test: broken and too large answers.
#[test]
fn refuses_what_is_no_stub_status_page() {
    let good = fixture("1.26");
    for bad in [
        String::new(),
        "<html>404 Not Found</html>".into(),
        good.replace("Active connections: 3", "Active connections: x"),
        good.replace("server accepts handled requests", "server accepts"),
        good.replace(" 13 13 13 ", " 13 13 "),
        good.replace(" 13 13 13 ", " 13 13 13 13 "),
        good.replace(" 13 13 13 ", " 13 -1 13 "),
        good.replace(" 13 13 13 ", " 13 99999999999999999999999 13 "),
        good.replace("Waiting: 1", "Idle: 1"),
        good.replace("Writing: 1", "Writing: one"),
        format!("{good}extra line\n"),
        good.lines().take(3).collect::<Vec<_>>().join("\n"),
        format!("{good}{}", " ".repeat(MAX_PAGE)),
    ] {
        assert!(parse_stub_status(&bad).is_err(), "{bad:?}");
    }
    assert!(
        parse_stub_status(&good.replace('\n', "\r\n")).is_ok(),
        "CRLF"
    );
}

// Gauges at once, rates from the second run; a restart of nginx pauses them.
#[tokio::test]
async fn reports_connections_and_rates() {
    let page = Arc::new(Mutex::new(fixture("1.28")));
    let addr = serve("200 OK", page.clone()).await;
    let mut nginx = build(
        &format!(r#"{{"url":"http://{addr}/nginx_status"}}"#),
        &Veto::default(),
    )
    .unwrap();
    let clock = Arc::new(FakeClock::new(std::time::SystemTime::UNIX_EPOCH));
    let run = Run::new(clock.clone());
    let first = nginx.collect(&run).await.unwrap();
    crate::assert_in_catalog("nginx", &first);
    assert_eq!(value(&first, "sermon_nginx_up"), Some(Value::Int(1)));
    assert_eq!(
        value(&first, "sermon_nginx_connections_active"),
        Some(Value::Int(3))
    );
    assert_eq!(
        value(&first, "sermon_nginx_connections_waiting"),
        Some(Value::Int(1))
    );
    assert_eq!(value(&first, "sermon_nginx_requests_per_second"), None);
    assert_eq!(first.len(), 5);

    *page.lock().unwrap() = "Active connections: 7 \nserver accepts handled requests\n 73 63 313 \nReading: 0 Writing: 2 Waiting: 5 \n".into();
    clock.advance(Duration::from_secs(30));
    let second = nginx.collect(&run).await.unwrap();
    crate::assert_in_catalog("nginx", &second);
    assert_eq!(
        value(&second, "sermon_nginx_requests_per_second"),
        Some(Value::Float(10.0))
    );
    assert_eq!(
        value(&second, "sermon_nginx_connections_accepted_per_second"),
        Some(Value::Float(2.0))
    );
    assert_eq!(
        value(&second, "sermon_nginx_connections_handled_per_second"),
        Some(Value::Float(50.0 / 30.0))
    );
    assert_eq!(
        value(&second, "sermon_nginx_connections_dropped_per_second"),
        Some(Value::Float(10.0 / 30.0))
    );
    assert_eq!(second.len(), 9);

    *page.lock().unwrap() = fixture("1.28");
    clock.advance(Duration::from_secs(30));
    let restarted = nginx.collect(&run).await.unwrap();
    assert_eq!(
        value(&restarted, "sermon_nginx_requests_per_second"),
        None,
        "counters went back"
    );
}

// Down in between: nginx may have restarted and counted past the old values – no rate over it.
#[tokio::test]
async fn no_rate_across_an_outage() {
    let page = Arc::new(Mutex::new(fixture("1.28")));
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let server = tokio::spawn(answer_on(listener, "200 OK", page.clone()));
    let mut nginx = build(
        &format!(r#"{{"url":"http://{addr}/nginx_status"}}"#),
        &Veto::default(),
    )
    .unwrap();
    let clock = Arc::new(FakeClock::new(std::time::SystemTime::UNIX_EPOCH));
    let run = Run::new(clock.clone());
    nginx.collect(&run).await.unwrap();
    server.abort();
    let _ = server.await;
    clock.advance(Duration::from_secs(30));
    let down = nginx.collect(&run).await.unwrap();
    assert_eq!(value(&down, "sermon_nginx_up"), Some(Value::Int(0)));
    *page.lock().unwrap() = "Active connections: 1 \nserver accepts handled requests\n 90 90 90 \nReading: 0 Writing: 1 Waiting: 0 \n".into();
    let back = TcpListener::bind(addr).await.unwrap();
    tokio::spawn(answer_on(back, "200 OK", page));
    clock.advance(Duration::from_secs(30));
    let again = nginx.collect(&run).await.unwrap();
    assert_eq!(value(&again, "sermon_nginx_up"), Some(Value::Int(1)));
    assert_eq!(value(&again, "sermon_nginx_requests_per_second"), None);
}

#[tokio::test]
async fn tells_down_from_misconfigured() {
    let closed = TcpListener::bind("127.0.0.1:0")
        .await
        .unwrap()
        .local_addr()
        .unwrap();
    let mut down = build(
        &format!(r#"{{"url":"http://{closed}/nginx_status"}}"#),
        &Veto::default(),
    )
    .unwrap();
    let s = down.collect(&Run::default()).await.unwrap();
    crate::assert_in_catalog("nginx", &s);
    assert_eq!(s.len(), 1);
    assert_eq!(value(&s, "sermon_nginx_up"), Some(Value::Int(0)));

    // A page that looks right but comes with 404 is no status page either.
    let not_found = serve("404 Not Found", Arc::new(Mutex::new(fixture("1.26")))).await;
    let mut missing = build(
        &format!(r#"{{"url":"http://{not_found}/nginx_status"}}"#),
        &Veto::default(),
    )
    .unwrap();
    assert!(matches!(
        missing.collect(&Run::default()).await,
        Err(RunError::Failed(why)) if why.contains("status 404")
    ));
    let other = serve("200 OK", Arc::new(Mutex::new("hello".into()))).await;
    let mut wrong = build(&format!(r#"{{"url":"http://{other}/"}}"#), &Veto::default()).unwrap();
    assert!(matches!(
        wrong.collect(&Run::default()).await,
        Err(RunError::Failed(_))
    ));
    let big = serve(
        "200 OK",
        Arc::new(Mutex::new("x".repeat(crate::http::MAX_BODY + 1))),
    )
    .await;
    let mut huge = build(&format!(r#"{{"url":"http://{big}/"}}"#), &Veto::default()).unwrap();
    assert!(matches!(
        huge.collect(&Run::default()).await,
        Err(RunError::Failed(_))
    ));

    let veto = Veto {
        targets: vec![Net::parse("127.0.0.2/32").unwrap()],
        ..Veto::default()
    };
    let mut refused = build(&format!(r#"{{"url":"http://{other}/"}}"#), &veto).unwrap();
    assert!(matches!(
        refused.collect(&Run::default()).await,
        Err(RunError::Vetoed(_))
    ));
}

// The demo's way (M5-32): nginx found on every recorded host, with the URL for the form.
#[test]
fn is_suggested_where_nginx_runs() {
    let modules: Vec<Arc<dyn Module>> = vec![Arc::new(Nginx)];
    for distro in [
        "debian-13",
        "ubuntu-24.04",
        "rockylinux-9",
        "almalinux-10",
        "fedora-44",
        "alpine-3.22",
    ] {
        let root = ProcRoot::new(
            Path::new(env!("CARGO_MANIFEST_DIR"))
                .join("../../fixtures/discovery")
                .join(distro),
        );
        let facts = HostFacts::read(&root, ["nginx.service".to_owned()]);
        let hints = crate::discovery::hints(&modules, &facts);
        assert_eq!(hints.len(), 1, "{distro}");
        assert_eq!(
            hints[0].reasons[..2],
            ["process:nginx", "unit:nginx.service"]
        );
        assert!(hints[0].reasons.contains(&"port:80".to_owned()));
        assert_eq!(
            hints[0].suggested_config_json,
            r#"{"url":"http://127.0.0.1/nginx_status"}"#
        );
        let schema = Schema::parse(SCHEMA).unwrap();
        assert!(
            schema
                .validate(&hints[0].suggested_config_json, &[])
                .is_ok(),
            "the suggestion fits the form"
        );
    }
    let v6 = HostFacts {
        processes: ["nginx".to_owned()].into(),
        listeners: [crate::discovery::Listener {
            ip: "::".parse().unwrap(),
            port: 80,
        }]
        .into(),
        ..HostFacts::default()
    };
    let found = Nginx.discover(&v6).unwrap();
    assert_eq!(
        found.suggested,
        Some(serde_json::json!({"url": "http://[::1]/nginx_status"}))
    );
    assert!(Nginx.discover(&HostFacts::default()).is_none());
}

#[test]
fn checks_the_configuration() {
    let schema = Schema::parse(SCHEMA).unwrap();
    let config = schema.validate("{}", &[]).unwrap();
    assert_eq!(config["url"], "http://127.0.0.1/nginx_status");
    assert!(build("{}", &Veto::default()).is_ok());
    assert!(matches!(
        build(r#"{"timeout":"26s"}"#, &Veto::default()),
        Err(BuildError::Invalid(_))
    ));
}
