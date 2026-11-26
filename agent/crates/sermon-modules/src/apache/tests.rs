use std::net::SocketAddr;
use std::path::Path;
use std::sync::Mutex;

use sermon_core::testing::FakeClock;
use sermon_core::{ProcRoot, Value};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpListener;

use super::*;
use crate::veto::Net;
use crate::{Host, Schema, Secrets, Veto};

/// The recorded pages: busy, idle, slots, processes, connections, accesses, kBytes.
const PAGES: [(&str, [Option<u64>; 7]); 5] = [
    (
        "debian-13-event",
        [
            Some(1),
            Some(49),
            Some(150),
            Some(2),
            Some(0),
            Some(10),
            Some(107),
        ],
    ),
    (
        "debian-13-prefork",
        [Some(1), Some(4), Some(150), None, None, Some(10), Some(107)],
    ),
    (
        "rockylinux-9",
        [
            Some(1),
            Some(74),
            Some(400),
            Some(3),
            Some(0),
            Some(10),
            Some(77),
        ],
    ),
    (
        "almalinux-10",
        [
            Some(1),
            Some(74),
            Some(400),
            Some(3),
            Some(0),
            Some(10),
            Some(59),
        ],
    ),
    (
        "fedora-44",
        [
            Some(1),
            Some(74),
            Some(400),
            Some(3),
            Some(0),
            Some(10),
            Some(86),
        ],
    ),
];

fn fixture(name: &str) -> String {
    std::fs::read_to_string(
        Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../../fixtures/modules/apache")
            .join(name)
            .join("server-status.txt"),
    )
    .unwrap()
}

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
                "HTTP/1.1 {status}\r\nContent-Length: {}\r\n\r\n{body}",
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
    Apache.build(
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
fn parses_the_page_of_every_distro_and_mpm() {
    for (name, [busy, idle, slots, processes, connections, accesses, kbytes]) in PAGES {
        assert_eq!(
            parse_server_status(&fixture(name)),
            Ok(ServerStatus {
                busy: busy.unwrap(),
                idle: idle.unwrap(),
                slots: slots.unwrap(),
                processes,
                connections,
                accesses,
                kbytes,
            }),
            "{name}"
        );
    }
}

// The plan's test: broken answers.
#[test]
fn refuses_what_is_no_server_status_page() {
    let good = fixture("debian-13-event");
    let html = "<!DOCTYPE HTML PUBLIC \"-//W3C//DTD HTML 3.2 Final//EN\">\n<html><head>";
    assert!(parse_server_status(html).unwrap_err().contains("add ?auto"));
    for bad in [
        String::new(),
        "hello".into(),
        good.replace("BusyWorkers: 1", "BusyWorkers: x"),
        good.replace("IdleWorkers: 49", "Idle: 49"),
        good.replace("Scoreboard: ___", "Scoreboard: _<script>"),
        good.lines()
            .filter(|l| !l.starts_with("Scoreboard"))
            .collect::<Vec<_>>()
            .join("\n"),
        good.replace(
            good.lines().find(|l| l.starts_with("Scoreboard")).unwrap(),
            "Scoreboard: ",
        ),
        format!("{good}{}", "_".repeat(MAX_PAGE)),
    ] {
        assert!(parse_server_status(&bad).is_err(), "{bad:?}");
    }
    let without_extended = good
        .lines()
        .filter(|l| !l.starts_with("Total "))
        .collect::<Vec<_>>()
        .join("\n");
    let s = parse_server_status(&without_extended).unwrap();
    assert_eq!((s.accesses, s.kbytes), (None, None), "ExtendedStatus Off");
}

#[tokio::test]
async fn reports_workers_and_rates() {
    let page = Arc::new(Mutex::new(fixture("debian-13-event")));
    let addr = serve("200 OK", page.clone()).await;
    let mut apache = build(
        &format!(r#"{{"url":"http://{addr}/server-status?auto"}}"#),
        &Veto::default(),
    )
    .unwrap();
    let clock = Arc::new(FakeClock::new(std::time::SystemTime::UNIX_EPOCH));
    let run = Run::new(clock.clone());
    let first = apache.collect(&run).await.unwrap();
    crate::assert_in_catalog("apache", &first);
    assert_eq!(value(&first, "sermon_apache_up"), Some(Value::Int(1)));
    assert_eq!(
        value(&first, "sermon_apache_workers_idle"),
        Some(Value::Int(49))
    );
    assert_eq!(
        value(&first, "sermon_apache_workers_max"),
        Some(Value::Int(150))
    );
    assert_eq!(
        value(&first, "sermon_apache_processes"),
        Some(Value::Int(2))
    );
    assert_eq!(
        value(&first, "sermon_apache_connections"),
        Some(Value::Int(0))
    );
    assert_eq!(value(&first, "sermon_apache_requests_per_second"), None);
    assert_eq!(first.len(), 6);

    let next = fixture("debian-13-event")
        .replace("Total Accesses: 10", "Total Accesses: 610")
        .replace("Total kBytes: 107", "Total kBytes: 407");
    *page.lock().unwrap() = next;
    clock.advance(Duration::from_secs(60));
    let second = apache.collect(&run).await.unwrap();
    crate::assert_in_catalog("apache", &second);
    assert_eq!(
        value(&second, "sermon_apache_requests_per_second"),
        Some(Value::Float(10.0))
    );
    assert_eq!(
        value(&second, "sermon_apache_sent_bytes_per_second"),
        Some(Value::Float(300.0 * 1024.0 / 60.0))
    );

    *page.lock().unwrap() = fixture("debian-13-prefork");
    clock.advance(Duration::from_secs(60));
    let prefork = apache.collect(&run).await.unwrap();
    assert_eq!(value(&prefork, "sermon_apache_processes"), None);
    assert_eq!(value(&prefork, "sermon_apache_connections"), None);
    assert_eq!(
        value(&prefork, "sermon_apache_requests_per_second"),
        None,
        "counters went back"
    );
}

// Down in between: Apache may have restarted and counted past the old values – no rate over it.
#[tokio::test]
async fn no_rate_across_an_outage() {
    let page = Arc::new(Mutex::new(fixture("rockylinux-9")));
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let server = tokio::spawn(answer_on(listener, "200 OK", page.clone()));
    let mut apache = build(
        &format!(r#"{{"url":"http://{addr}/server-status?auto"}}"#),
        &Veto::default(),
    )
    .unwrap();
    let clock = Arc::new(FakeClock::new(std::time::SystemTime::UNIX_EPOCH));
    let run = Run::new(clock.clone());
    apache.collect(&run).await.unwrap();
    server.abort();
    let _ = server.await;
    clock.advance(Duration::from_secs(30));
    let down = apache.collect(&run).await.unwrap();
    assert_eq!(value(&down, "sermon_apache_up"), Some(Value::Int(0)));
    *page.lock().unwrap() =
        fixture("rockylinux-9").replace("Total Accesses: 10", "Total Accesses: 90");
    tokio::spawn(answer_on(
        TcpListener::bind(addr).await.unwrap(),
        "200 OK",
        page,
    ));
    clock.advance(Duration::from_secs(30));
    let again = apache.collect(&run).await.unwrap();
    assert_eq!(value(&again, "sermon_apache_up"), Some(Value::Int(1)));
    assert_eq!(value(&again, "sermon_apache_requests_per_second"), None);
}

#[tokio::test]
async fn tells_down_from_misconfigured() {
    let closed = TcpListener::bind("127.0.0.1:0")
        .await
        .unwrap()
        .local_addr()
        .unwrap();
    let mut down = build(
        &format!(r#"{{"url":"http://{closed}/server-status?auto"}}"#),
        &Veto::default(),
    )
    .unwrap();
    let s = down.collect(&Run::default()).await.unwrap();
    assert_eq!(s.len(), 1);
    assert_eq!(value(&s, "sermon_apache_up"), Some(Value::Int(0)));

    let forbidden = serve("403 Forbidden", Arc::new(Mutex::new(fixture("fedora-44")))).await;
    let mut refused = build(
        &format!(r#"{{"url":"http://{forbidden}/server-status?auto"}}"#),
        &Veto::default(),
    )
    .unwrap();
    assert!(matches!(
        refused.collect(&Run::default()).await,
        Err(RunError::Failed(why)) if why.contains("status 403") && why.contains("mod_status")
    ));
    let html = serve("200 OK", Arc::new(Mutex::new("<html>status</html>".into()))).await;
    let mut no_auto = build(
        &format!(r#"{{"url":"http://{html}/server-status"}}"#),
        &Veto::default(),
    )
    .unwrap();
    assert!(matches!(
        no_auto.collect(&Run::default()).await,
        Err(RunError::Failed(why)) if why.contains("add ?auto")
    ));
    let veto = Veto {
        targets: vec![Net::parse("127.0.0.2/32").unwrap()],
        ..Veto::default()
    };
    let mut vetoed = build(&format!(r#"{{"url":"http://{html}/"}}"#), &veto).unwrap();
    assert!(matches!(
        vetoed.collect(&Run::default()).await,
        Err(RunError::Vetoed(_))
    ));
}

#[test]
fn is_suggested_for_apache2_and_httpd() {
    let modules: Vec<Arc<dyn Module>> = vec![Arc::new(Apache)];
    for (process, unit) in [("apache2", "apache2.service"), ("httpd", "httpd.service")] {
        let facts = HostFacts {
            processes: [process.to_owned()].into(),
            units: [unit.to_owned()].into(),
            listeners: [crate::discovery::Listener {
                ip: "0.0.0.0".parse().unwrap(),
                port: 80,
            }]
            .into(),
            ..HostFacts::default()
        };
        let hints = crate::discovery::hints(&modules, &facts);
        assert_eq!(
            hints[0].reasons,
            [
                format!("process:{process}"),
                format!("unit:{unit}"),
                "port:80".into()
            ]
        );
        assert_eq!(
            hints[0].suggested_config_json,
            r#"{"url":"http://127.0.0.1/server-status?auto"}"#
        );
    }
    // nginx on the recorded hosts is no Apache, though it listens on 80.
    let root = ProcRoot::new(
        Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures/discovery/debian-13"),
    );
    let facts = HostFacts::read(&root, ["nginx.service".to_owned()]);
    assert!(Apache.discover(&facts).is_none());
}

#[test]
fn defaults_to_the_local_auto_page() {
    let config = Schema::parse(SCHEMA).unwrap().validate("{}", &[]).unwrap();
    assert_eq!(config["url"], "http://127.0.0.1/server-status?auto");
    assert!(build("{}", &Veto::default()).is_ok());
}
