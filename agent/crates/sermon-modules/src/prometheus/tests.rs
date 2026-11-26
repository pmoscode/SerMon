use std::net::SocketAddr;
use std::path::Path;
use std::sync::Mutex;

use sermon_core::Value;
use sermon_core::testing::FakeClock;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpListener;

use super::*;
use crate::veto::{Net, Veto};
use crate::{Host, Schema, Secrets};

fn fixture(source: &str, format: &str) -> String {
    std::fs::read_to_string(
        Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../../fixtures/modules/prometheus")
            .join(source)
            .join(format!("metrics-{format}.txt")),
    )
    .unwrap()
}

/// An endpoint answering the current page; it keeps the requests.
async fn serve(page: Arc<Mutex<String>>, requests: Arc<Mutex<Vec<String>>>) -> SocketAddr {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    tokio::spawn(answer_on(listener, page, requests));
    addr
}

async fn answer_on(
    listener: TcpListener,
    page: Arc<Mutex<String>>,
    requests: Arc<Mutex<Vec<String>>>,
) {
    while let Ok((mut s, _)) = listener.accept().await {
        let (body, requests) = (page.lock().unwrap().clone(), requests.clone());
        tokio::spawn(async move {
            let mut buf = vec![0u8; 8192];
            let n = s.read(&mut buf).await.unwrap_or(0);
            requests
                .lock()
                .unwrap()
                .push(String::from_utf8_lossy(&buf[..n]).into_owned());
            let answer = format!(
                "HTTP/1.1 200 OK\r\nContent-Type: text/plain; version=0.0.4\r\nContent-Length: {}\r\n\r\n{body}",
                body.len()
            );
            let _ = s.write_all(answer.as_bytes()).await;
        });
    }
}

fn build(config: &str, bearer: Option<&str>, veto: &Veto) -> Result<Box<dyn Instance>, BuildError> {
    let secrets = Secrets::new(bearer.map(|t| ("bearer".to_owned(), t.to_owned())));
    let config = Schema::parse(SCHEMA)
        .unwrap()
        .validate(config, &secrets.fields())
        .unwrap();
    Prometheus.build(
        &config,
        &Setup {
            veto,
            secrets: &secrets,
            host: &Host::default(),
        },
    )
}

fn get(samples: &[Sample], metric: &str, name: &str, series: &str) -> Option<Value> {
    samples
        .iter()
        .find(|s| {
            s.metric == metric
                && s.labels.iter().any(|(k, v)| *k == "name" && v == name)
                && s.labels.iter().any(|(k, v)| *k == "series" && v == series)
        })
        .map(|s| s.value)
}

fn of_target(samples: &[Sample], metric: &str) -> Option<Value> {
    samples.iter().find(|s| s.metric == metric).map(|s| s.value)
}

#[tokio::test]
async fn maps_series_onto_gauges_and_rates() {
    let page = Arc::new(Mutex::new(fixture("python-client-0", "openmetrics")));
    let requests = Arc::new(Mutex::new(Vec::new()));
    let addr = serve(page.clone(), requests.clone()).await;
    let mut scraper = build(
        &format!(r#"{{"urls":["http://{addr}/metrics"]}}"#),
        None,
        &Veto::default().any_port(),
    )
    .unwrap();
    let clock = Arc::new(FakeClock::new(std::time::SystemTime::UNIX_EPOCH));
    let run = Run::new(clock.clone());
    let first = scraper.collect(&run).await.unwrap();
    crate::assert_in_catalog("prometheus", &first);
    assert_eq!(
        of_target(&first, "sermon_prometheus_up"),
        Some(Value::Int(1))
    );
    assert_eq!(
        get(&first, "sermon_prometheus_gauge", "queue_depth", ""),
        Some(Value::Float(7.0))
    );
    assert_eq!(
        get(
            &first,
            "sermon_prometheus_gauge",
            "build_info",
            r#"version="1.2.3""#
        ),
        Some(Value::Float(1.0))
    );
    assert_eq!(
        get(
            &first,
            "sermon_prometheus_rate",
            "http_requests_total",
            r#"code="200",method="GET""#
        ),
        None,
        "a rate needs two runs"
    );
    let request = requests.lock().unwrap()[0].to_ascii_lowercase();
    assert!(
        request.contains("accept: application/openmetrics-text"),
        "{request}"
    );

    *page.lock().unwrap() = fixture("python-client-0", "openmetrics").replace(
        "http_requests_total{code=\"200\",method=\"GET\"} 42.0",
        "http_requests_total{code=\"200\",method=\"GET\"} 642.0",
    );
    clock.advance(Duration::from_secs(60));
    let second = scraper.collect(&run).await.unwrap();
    crate::assert_in_catalog("prometheus", &second);
    assert_eq!(
        get(
            &second,
            "sermon_prometheus_rate",
            "http_requests_total",
            r#"code="200",method="GET""#
        ),
        Some(Value::Float(10.0))
    );
    assert_eq!(
        get(
            &second,
            "sermon_prometheus_rate",
            "http_requests_total",
            r#"code="500",method="POST""#
        ),
        Some(Value::Float(0.0))
    );
    assert_eq!(
        of_target(&second, "sermon_prometheus_dropped_series"),
        Some(Value::Int(0))
    );

    *page.lock().unwrap() = fixture("python-client-0", "openmetrics");
    clock.advance(Duration::from_secs(60));
    let reset = scraper.collect(&run).await.unwrap();
    assert_eq!(
        get(
            &reset,
            "sermon_prometheus_rate",
            "http_requests_total",
            r#"code="200",method="GET""#
        ),
        None,
        "the counter went back"
    );
}

// The plan's test: too many series – the first are taken, the rest counted.
#[tokio::test]
async fn bounds_the_series() {
    let page = Arc::new(Mutex::new(fixture("node-exporter-1", "text")));
    let addr = serve(page, Arc::default()).await;
    let mut scraper = build(
        &format!(r#"{{"urls":["http://{addr}/metrics"],"max_series":20}}"#),
        None,
        &Veto::default().any_port(),
    )
    .unwrap();
    let s = scraper.collect(&Run::default()).await.unwrap();
    crate::assert_in_catalog("prometheus", &s);
    assert_eq!(
        of_target(&s, "sermon_prometheus_series"),
        Some(Value::Int(20))
    );
    assert!(matches!(
        of_target(&s, "sermon_prometheus_dropped_series"),
        Some(Value::Int(n)) if n > 1000
    ));
    assert!(s.len() <= 20 + PER_TARGET);
    assert!(
        s.iter()
            .all(|x| x.labels.iter().all(|(_, v)| v.len() <= MAX_LABEL))
    );

    let mut filtered = build(
        &format!(
            r#"{{"urls":["http://{addr}/metrics"],"metrics":["node_load1","node_memory_Mem*"]}}"#
        ),
        None,
        &Veto::default().any_port(),
    )
    .unwrap();
    let f = filtered.collect(&Run::default()).await.unwrap();
    let names: std::collections::BTreeSet<&str> = f
        .iter()
        .flat_map(|x| {
            x.labels
                .iter()
                .filter(|(k, _)| *k == "name")
                .map(|(_, v)| v.as_str())
        })
        .collect();
    assert!(
        names.contains("node_load1") && names.contains("node_memory_MemFree_bytes"),
        "{names:?}"
    );
    assert!(
        names
            .iter()
            .all(|n| *n == "node_load1" || n.starts_with("node_memory_Mem"))
    );
    assert_eq!(
        of_target(&f, "sermon_prometheus_dropped_series"),
        Some(Value::Int(0)),
        "filtered out is not dropped"
    );

    for bad in [
        r#"{"urls":["http://127.0.0.1:1/a","http://127.0.0.1:1/b","http://127.0.0.1:1/c"],"max_series":100}"#,
        r#"{"urls":[]}"#,
        r#"{"urls":["http://127.0.0.1:1/a","http://127.0.0.1:1/a?x"]}"#,
    ] {
        assert!(
            matches!(
                build(bad, None, &Veto::default().any_port()),
                Err(BuildError::Invalid(_))
            ),
            "{bad}"
        );
    }
}

#[test]
fn drops_labels_beyond_the_bounds() {
    let long = "v".repeat(MAX_LABEL);
    let page = expo::parse(&format!("ok 1\nlong{{a=\"{long}\"}} 2\nok 3\n"));
    let (kept, dropped) = select(page, &[], 10, &mut Known::default(), Monotonic::default());
    assert_eq!(
        kept.len(),
        1,
        "the label is too long, the duplicate counts once"
    );
    assert_eq!(dropped, 2);
    assert!(wanted(&["go_*".into()], "go_goroutines"));
    assert!(!wanted(&["go_*".into()], "process_cpu"));
    assert!(wanted(&["up".into()], "up") && !wanted(&["up".into()], "upx"));
}

#[tokio::test]
async fn sends_the_token_to_its_endpoint_only() {
    let requests = Arc::new(Mutex::new(Vec::new()));
    let addr = serve(Arc::new(Mutex::new("up 1\n".into())), requests.clone()).await;
    let mut scraper = build(
        &format!(r#"{{"urls":["http://{addr}/metrics"]}}"#),
        Some("t0ken-xyz"),
        &Veto::default().any_port(),
    )
    .unwrap();
    scraper.collect(&Run::default()).await.unwrap();
    let request = requests.lock().unwrap()[0].to_ascii_lowercase();
    assert!(
        request.contains("authorization: bearer t0ken-xyz"),
        "{request}"
    );
    let schema = Schema::parse(SCHEMA).unwrap();
    assert_eq!(schema.secrets().collect::<Vec<_>>(), ["bearer"]);
}

#[tokio::test]
async fn tells_down_from_wrong() {
    let closed = TcpListener::bind("127.0.0.1:0")
        .await
        .unwrap()
        .local_addr()
        .unwrap();
    let mut down = build(
        &format!(r#"{{"urls":["http://{closed}/metrics"]}}"#),
        None,
        &Veto::default().any_port(),
    )
    .unwrap();
    let s = down.collect(&Run::default()).await.unwrap();
    assert_eq!(s.len(), 1);
    assert_eq!(of_target(&s, "sermon_prometheus_up"), Some(Value::Int(0)));

    let html = serve(
        Arc::new(Mutex::new("<html>\n<body>hi</body>\n</html>\n".into())),
        Arc::default(),
    )
    .await;
    let mut wrong = build(
        &format!(r#"{{"urls":["http://{html}/"]}}"#),
        None,
        &Veto::default().any_port(),
    )
    .unwrap();
    assert!(matches!(
        wrong.collect(&Run::default()).await,
        Err(RunError::Failed(why)) if why.contains("not the Prometheus text format")
    ));
    let veto = Veto {
        targets: vec![Net::parse("127.0.0.2/32").unwrap()],
        ..Veto::default().any_port()
    };
    let mut vetoed = build(&format!(r#"{{"urls":["http://{html}/"]}}"#), None, &veto).unwrap();
    assert!(matches!(
        vetoed.collect(&Run::default()).await,
        Err(RunError::Vetoed(_))
    ));
}

// Down in between: the endpoint may have restarted and counted past the old values.
#[tokio::test]
async fn no_rate_across_an_outage() {
    let page = Arc::new(Mutex::new("jobs_total 5\n".to_owned()));
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let server = tokio::spawn(answer_on(listener, page.clone(), Arc::default()));
    let mut scraper = build(
        &format!(r#"{{"urls":["http://{addr}/metrics"]}}"#),
        None,
        &Veto::default().any_port(),
    )
    .unwrap();
    let clock = Arc::new(FakeClock::new(std::time::SystemTime::UNIX_EPOCH));
    let run = Run::new(clock.clone());
    scraper.collect(&run).await.unwrap();
    server.abort();
    let _ = server.await;
    clock.advance(Duration::from_secs(30));
    let down = scraper.collect(&run).await.unwrap();
    assert_eq!(
        of_target(&down, "sermon_prometheus_up"),
        Some(Value::Int(0))
    );
    *page.lock().unwrap() = "jobs_total 50\n".into();
    let back = TcpListener::bind(addr).await.unwrap();
    tokio::spawn(answer_on(back, page, Arc::default()));
    clock.advance(Duration::from_secs(30));
    let again = scraper.collect(&run).await.unwrap();
    assert_eq!(
        of_target(&again, "sermon_prometheus_up"),
        Some(Value::Int(1))
    );
    assert_eq!(
        get(&again, "sermon_prometheus_rate", "jobs_total", ""),
        None
    );
}

// M5-33: an exporter whose labels follow outside input (a request path, a user ID) must not
// fill the node's series budget run after run: the series of an endpoint are a stable set of at
// most `max_series`; new ones get a place only when a known one was absent for an hour.
#[test]
fn the_series_of_an_endpoint_stay_a_stable_set() {
    let at = |s: u64| Monotonic::from_origin(Duration::from_secs(s));
    let mut known = Known::default();
    let page = |text: &str| expo::parse(text);
    let names = |kept: &[(String, String, f64, bool)]| {
        kept.iter()
            .map(|(_, l, _, _)| l.clone())
            .collect::<Vec<_>>()
    };
    let (kept, dropped) = select(
        page("r{p=\"/a\"} 1\nr{p=\"/b\"} 1\nr{p=\"/c\"} 1\n"),
        &[],
        2,
        &mut known,
        at(0),
    );
    assert_eq!(names(&kept), [r#"p="/a""#, r#"p="/b""#]);
    assert_eq!(dropped, 1);
    // New paths every run: none of them takes a place while /a and /b are known.
    for (i, path) in ["/x", "/y", "/z"].iter().enumerate() {
        let (kept, dropped) = select(
            page(&format!("r{{p=\"{path}\"}} 1\nr{{p=\"/a\"}} 1\n")),
            &[],
            2,
            &mut known,
            at(10 * (u64::try_from(i).unwrap() + 1)),
        );
        assert_eq!(names(&kept), [r#"p="/a""#], "{path}");
        assert_eq!(dropped, 1, "{path}");
    }
    assert_eq!(
        known.get(&("r".to_owned(), r#"p="/a""#.to_owned())),
        Some(&at(30)),
        "seen last at 30 s"
    );
    // /b has been absent for an hour: its place is free again.
    let (kept, _) = select(
        page("r{p=\"/new\"} 1\nr{p=\"/a\"} 1\n"),
        &[],
        2,
        &mut known,
        at(3600 + 1),
    );
    assert_eq!(names(&kept), [r#"p="/new""#, r#"p="/a""#]);
    let (kept, dropped) = select(
        page("r{p=\"/b\"} 1\nr{p=\"/a\"} 1\nr{p=\"/new\"} 1\n"),
        &[],
        2,
        &mut known,
        at(3602),
    );
    assert_eq!(
        names(&kept),
        [r#"p="/a""#, r#"p="/new""#],
        "/b came too late"
    );
    assert_eq!(dropped, 1);
}
