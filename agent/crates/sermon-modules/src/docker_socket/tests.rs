use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};

use sermon_core::Value;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::UnixListener;

use super::*;
use crate::{Host, Schema, Secrets, Veto};

const VERSION: &str = "docker-28.3";

fn fixtures() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../fixtures/modules/docker_socket")
        .join(VERSION)
}

/// The answers of the fake API by path; it keeps the request lines.
struct Fake {
    answers: Mutex<BTreeMap<String, (u16, Vec<u8>)>>,
    seen: Mutex<Vec<String>>,
}

impl Fake {
    /// The recorded list and inspections.
    fn recorded() -> Arc<Self> {
        let mut answers = BTreeMap::new();
        let list = std::fs::read(fixtures().join("containers.json")).unwrap();
        for c in parse_list(&list).unwrap() {
            let inspect =
                std::fs::read(fixtures().join(format!("inspect-{}.json", &c.id[..12]))).unwrap();
            answers.insert(format!("/containers/{}/json", c.id), (200, inspect));
        }
        for (name, file) in [("talker", "logs-talker.bin"), ("tty", "logs-tty.bin")] {
            let c = parse_list(&list)
                .unwrap()
                .into_iter()
                .find(|c| c.name == name)
                .unwrap();
            answers.insert(
                format!("/containers/{}/logs", c.id),
                (200, std::fs::read(fixtures().join(file)).unwrap()),
            );
        }
        answers.insert("/containers/json?all=1".into(), (200, list));
        Arc::new(Self {
            answers: Mutex::new(answers),
            seen: Mutex::default(),
        })
    }
}

/// A fake API at `<dir>/docker.sock`.
fn serve(dir: &Path, fake: Arc<Fake>) -> PathBuf {
    let path = dir.join("docker.sock");
    let listener = UnixListener::bind(&path).unwrap();
    tokio::spawn(async move {
        while let Ok((mut s, _)) = listener.accept().await {
            let fake = fake.clone();
            tokio::spawn(async move {
                let mut buf = vec![0u8; 4096];
                let n = s.read(&mut buf).await.unwrap_or(0);
                let request = String::from_utf8_lossy(&buf[..n]).into_owned();
                let line = request.lines().next().unwrap_or_default().to_owned();
                fake.seen.lock().unwrap().push(line.clone());
                let full = line.split_whitespace().nth(1).unwrap_or_default();
                // The logs by their path alone: the query names the position.
                let path = if full.contains("/logs?") {
                    full.split('?').next().unwrap_or_default()
                } else {
                    full
                };
                let (status, body) = fake
                    .answers
                    .lock()
                    .unwrap()
                    .get(path)
                    .cloned()
                    .unwrap_or((404, b"{\"message\":\"No such container\"}".to_vec()));
                let head = format!(
                    "HTTP/1.1 {status} X\r\nContent-Type: application/json\r\nContent-Length: {}\r\n\r\n",
                    body.len()
                );
                let _ = s.write_all(head.as_bytes()).await;
                let _ = s.write_all(&body).await;
            });
        }
    });
    path
}

fn allowed(dir: &Path) -> Veto {
    Veto {
        sockets: vec![std::fs::canonicalize(dir).unwrap()],
        docker_socket: true,
        ..Veto::default()
    }
}

fn build(config: &str, veto: &Veto) -> Result<Box<dyn Instance>, BuildError> {
    let config = Schema::parse(SCHEMA)
        .unwrap()
        .validate(config, &[])
        .unwrap();
    DockerSocket.build(
        &config,
        &Setup {
            veto,
            secrets: &Secrets::default(),
            host: &Host::default(),
        },
    )
}

fn value(samples: &[Sample], metric: &str, name: &str) -> Option<Value> {
    samples
        .iter()
        .find(|s| s.metric == metric && s.labels.iter().any(|(k, v)| *k == "name" && v == name))
        .map(|s| s.value)
}

fn state(samples: &[Sample], state: &str) -> Option<Value> {
    samples
        .iter()
        .find(|s| s.metric == "sermon_docker_socket_containers" && s.labels[0].1 == state)
        .map(|s| s.value)
}

#[test]
fn parses_the_recorded_answers() {
    let list = parse_list(&std::fs::read(fixtures().join("containers.json")).unwrap()).unwrap();
    let names: Vec<&str> = list.iter().map(|c| c.name.as_str()).collect();
    assert_eq!(names.len(), 7);
    for n in ["web", "sick", "flappy", "done", "plain", "talker", "tty"] {
        assert!(names.contains(&n), "{names:?}");
    }
    let flappy = list.iter().find(|c| c.name == "flappy").unwrap();
    assert_eq!(flappy.state, Some("restarting"));
    let inspect =
        std::fs::read(fixtures().join(format!("inspect-{}.json", &flappy.id[..12]))).unwrap();
    let i = parse_inspect(&inspect).unwrap();
    assert!(i.restarts >= 2 && i.health.is_none(), "{i:?}");
    let sick = list.iter().find(|c| c.name == "sick").unwrap();
    let inspect =
        std::fs::read(fixtures().join(format!("inspect-{}.json", &sick.id[..12]))).unwrap();
    assert_eq!(
        parse_inspect(&inspect).unwrap().health.as_deref(),
        Some("unhealthy")
    );
}

#[test]
fn skips_what_is_no_container() {
    let list = br#"[{"Id":"../../../images/json","Names":["/x"]},{"Id":"ABCDEF","Names":["/y"]},
        {"Id":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","Names":["/ok"],"State":"weird"},
        {"Names":["/no-id"]},
        {"Id":"1123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","Names":["/bell\u0007"]}]"#;
    let parsed = parse_list(list).unwrap();
    assert_eq!(parsed.len(), 2);
    assert_eq!(parsed[0].name, "ok");
    assert_eq!(parsed[1].name, "", "a control character");
    assert_eq!(parsed[0].state, None, "an unknown state");
    for bad in [&b"{}"[..], b"not json", b"[1"] {
        assert!(parse_list(bad).is_err());
    }
    assert!(parse_inspect(b"{}").is_err());
    assert!(parse_inspect(b"[]").is_err());
}

// The plan's tests: the fake API, read paths only.
#[tokio::test]
async fn reports_state_health_and_restarts() {
    let dir = tempfile::tempdir().unwrap();
    let fake = Fake::recorded();
    // web is still starting its health checks this time: no health value yet.
    let list = parse_list(&std::fs::read(fixtures().join("containers.json")).unwrap()).unwrap();
    let web = list.iter().find(|c| c.name == "web").unwrap().id.clone();
    let starting = br#"{"State":{"Running":true,"OOMKilled":false,"Health":{"Status":"starting"}},"RestartCount":0}"#;
    fake.answers
        .lock()
        .unwrap()
        .insert(format!("/containers/{web}/json"), (200, starting.to_vec()));
    let socket = serve(dir.path(), fake.clone());
    let mut api = build(
        &format!(r#"{{"socket":"{}"}}"#, socket.display()),
        &allowed(dir.path()),
    )
    .unwrap();
    let s = api.collect(&Run::default()).await.unwrap();
    crate::assert_in_catalog("docker_socket", &s);
    assert_eq!(s[0].value, Value::Int(1));
    assert_eq!(state(&s, "running"), Some(Value::Int(4)));
    assert_eq!(state(&s, "exited"), Some(Value::Int(2)));
    assert_eq!(state(&s, "restarting"), Some(Value::Int(1)));
    assert_eq!(state(&s, "dead"), Some(Value::Int(0)));
    assert_eq!(
        value(&s, "sermon_docker_socket_healthy", "web"),
        None,
        "starting"
    );
    assert_eq!(
        value(&s, "sermon_docker_socket_healthy", "sick"),
        Some(Value::Int(0))
    );
    assert_eq!(
        value(&s, "sermon_docker_socket_healthy", "plain"),
        None,
        "no health check"
    );
    assert_eq!(
        value(&s, "sermon_docker_socket_running", "done"),
        Some(Value::Int(0))
    );
    assert!(matches!(
        value(&s, "sermon_docker_socket_restarts", "flappy"),
        Some(Value::Int(n)) if n >= 2
    ));
    assert_eq!(
        value(&s, "sermon_docker_socket_oom_killed", "web"),
        Some(Value::Int(0))
    );
    let label = s
        .iter()
        .find(|x| x.metric == "sermon_docker_socket_running")
        .unwrap();
    assert_eq!(label.labels[0].1.len(), 12, "the short ID");
    let seen = fake.seen.lock().unwrap().clone();
    assert_eq!(seen[0], "GET /containers/json?all=1 HTTP/1.1");
    assert_eq!(seen.len(), 8);
    for line in &seen[1..] {
        let path = line
            .strip_prefix("GET /containers/")
            .and_then(|p| p.strip_suffix("/json HTTP/1.1"))
            .unwrap_or_else(|| panic!("not a read path: {line}"));
        assert!(valid_id(path), "{line}");
    }
}

#[tokio::test]
async fn bounds_the_containers_and_the_answers() {
    let dir = tempfile::tempdir().unwrap();
    let fake = Fake::recorded();
    let socket = serve(dir.path(), fake.clone());
    let veto = allowed(dir.path());
    let config = format!(r#"{{"socket":"{}","max_containers":2}}"#, socket.display());
    let s = build(&config, &veto)
        .unwrap()
        .collect(&Run::default())
        .await
        .unwrap();
    let names: Vec<&str> = s
        .iter()
        .filter(|x| x.metric == "sermon_docker_socket_running")
        .flat_map(|x| {
            x.labels
                .iter()
                .filter(|(k, _)| *k == "name")
                .map(|(_, v)| v.as_str())
        })
        .collect();
    assert_eq!(names, ["plain", "sick"], "running ones first, by name");
    let truncated = |s: &[Sample]| {
        s.iter()
            .find(|x| x.metric == "sermon_docker_socket_truncated")
            .map(|x| x.value)
    };
    assert_eq!(truncated(&s), Some(Value::Int(1)));
    let exactly = format!(r#"{{"socket":"{}","max_containers":7}}"#, socket.display());
    let all = build(&exactly, &veto)
        .unwrap()
        .collect(&Run::default())
        .await
        .unwrap();
    assert_eq!(truncated(&all), Some(Value::Int(0)), "seven of seven");

    // A container gone between list and inspection is left out.
    let gone: Vec<String> = fake
        .answers
        .lock()
        .unwrap()
        .keys()
        .filter(|k| k.ends_with("/json") && k.len() > 30)
        .cloned()
        .collect();
    fake.answers.lock().unwrap().remove(&gone[0]);
    let s = build(&format!(r#"{{"socket":"{}"}}"#, socket.display()), &veto)
        .unwrap()
        .collect(&Run::default())
        .await
        .unwrap();
    let running = s
        .iter()
        .filter(|x| x.metric == "sermon_docker_socket_running")
        .count();
    assert_eq!(running, 6);

    let list = "/containers/json?all=1".to_owned();
    for (status, body) in [
        (200, vec![b' '; crate::http::MAX_BODY + 1]),
        (200, b"<html>".to_vec()),
        (500, b"{}".to_vec()),
    ] {
        fake.answers
            .lock()
            .unwrap()
            .insert(list.clone(), (status, body));
        let r = build(&format!(r#"{{"socket":"{}"}}"#, socket.display()), &veto)
            .unwrap()
            .collect(&Run::default())
            .await;
        assert!(matches!(r, Err(RunError::Failed(_))), "{status}");
    }
}

// O4: the socket is root-equivalent – only with allow_docker_socket.
#[tokio::test]
async fn needs_the_hosts_permission() {
    let dir = tempfile::tempdir().unwrap();
    let socket = dir.path().join("docker.sock");
    let config = format!(r#"{{"socket":"{}"}}"#, socket.display());
    let not_allowed = Veto {
        docker_socket: false,
        ..allowed(dir.path())
    };
    assert!(matches!(
        build(&config, &not_allowed),
        Err(BuildError::Vetoed(_))
    ));
    assert!(matches!(
        build(
            r#"{"socket":"/run/sermon/helper.sock"}"#,
            &Veto {
                docker_socket: true,
                ..Veto::default()
            }
        ),
        Err(BuildError::Vetoed(_))
    ));
    let mut down = build(&config, &allowed(dir.path())).unwrap();
    let s = down.collect(&Run::default()).await.unwrap();
    assert_eq!(s.len(), 1, "no daemon: down");
    assert_eq!(s[0].value, Value::Int(0));
    let schema = Schema::parse(SCHEMA).unwrap();
    assert_eq!(
        schema.validate("{}", &[]).unwrap()["socket"],
        "/var/run/docker.sock"
    );
}

#[test]
fn stays_within_the_samples_of_a_run() {
    let schema: serde_json::Value = serde_json::from_str(SCHEMA).unwrap();
    let max = schema["properties"]["max_containers"]["maximum"]
        .as_u64()
        .unwrap();
    assert!(
        usize::try_from(max).unwrap() <= MAX_CONTAINERS,
        "the form's bound fits a run"
    );
}

// M5-21: the output of the chosen containers goes to the run's log lines, each line once; a
// container seen first starts at the run's time.
#[tokio::test]
async fn hands_the_output_of_chosen_containers_to_the_logs() {
    let dir = tempfile::tempdir().unwrap();
    let fake = Fake::recorded();
    let socket = serve(dir.path(), fake.clone());
    let config = format!(
        r#"{{"socket":"{}","logs":["talk*","tty"]}}"#,
        socket.display()
    );
    let mut api = build(&config, &allowed(dir.path())).unwrap();
    // Before the fixtures were written.
    let clock = Arc::new(sermon_core::testing::FakeClock::new(
        UNIX_EPOCH + Duration::from_secs(1_790_000_000),
    ));
    let run = Run::new(clock.clone());
    api.collect(&run).await.unwrap();
    assert_eq!(run.take_logs(), [], "a container seen first starts now");
    assert!(
        !fake
            .seen
            .lock()
            .unwrap()
            .iter()
            .any(|l| l.contains("/logs"))
    );

    api.collect(&run).await.unwrap();
    let lines = run.take_logs();
    let of = |name: &str| -> Vec<(bool, String)> {
        lines
            .iter()
            .filter(|l| l.name == name)
            .map(|l| (l.stderr, l.message.clone()))
            .collect()
    };
    assert!(
        of("talker").contains(&(true, "warning: low disk".into())),
        "{lines:?}"
    );
    assert!(of("talker").contains(&(false, "ready".into())));
    assert_eq!(of("tty").len(), 2);
    assert!(
        lines
            .iter()
            .all(|l| l.at > UNIX_EPOCH + Duration::from_secs(1_790_000_000))
    );
    let requests: Vec<String> = fake
        .seen
        .lock()
        .unwrap()
        .iter()
        .filter(|l| l.contains("/logs"))
        .cloned()
        .collect();
    assert_eq!(requests.len(), 2, "talker and tty only");
    for r in &requests {
        assert!(
            r.contains("?stdout=1&stderr=1&timestamps=1&since=1790000000.000000000&tail=1000"),
            "{r}"
        );
    }

    api.collect(&run).await.unwrap();
    assert_eq!(run.take_logs(), [], "nothing twice");
    let last = fake.seen.lock().unwrap().last().cloned().unwrap();
    assert!(
        !last.contains("since=1790000000.000000000"),
        "it moved on: {last}"
    );
}
