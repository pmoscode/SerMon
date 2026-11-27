use std::sync::atomic::{AtomicUsize, Ordering};

use sermon_core::SystemClock;

use super::*;
use crate::{BoxFuture, Config, Instance, Refused};

/// What a fake module does in a run.
#[derive(Debug, Clone, Copy)]
enum Act {
    Samples(usize),
    Fail,
    Panic,
    /// Never returns (an async wait).
    Pend,
    /// Blocks a thread for this long.
    Block(Duration),
    Refused,
    /// A sample of another module's name.
    Foreign,
    /// Log lines, then a failure (logs of a failed run go out too).
    Logs(usize),
    /// A label value with a bidi control.
    Bidi,
}

struct Fake {
    id: &'static str,
    act: Arc<Mutex<Act>>,
    runs: Arc<AtomicUsize>,
    builds: Arc<AtomicUsize>,
    built_on: Arc<Mutex<Vec<std::thread::ThreadId>>>,
}

const SCHEMA: &str = r#"{"type":"object","additionalProperties":false,"properties":{
  "port":{"type":"integer","minimum":1,"maximum":65535,"default":80},
  "password":{"type":"string","writeOnly":true,"maxLength":64}}}"#;

impl Module for Fake {
    fn id(&self) -> &'static str {
        self.id
    }
    fn schema(&self) -> &'static str {
        SCHEMA
    }
    fn build(&self, config: &Config, setup: &Setup<'_>) -> Result<Box<dyn Instance>, BuildError> {
        self.builds.fetch_add(1, Ordering::SeqCst);
        self.built_on
            .lock()
            .unwrap()
            .push(std::thread::current().id());
        if config["port"] == 5 {
            std::thread::sleep(Duration::from_millis(500));
        }
        if config["port"] == 1 {
            return Err(BuildError::Invalid("port 1 is reserved".into()));
        }
        if config["port"] == 2 {
            return Err(setup
                .veto
                .address("192.0.2.1".parse().unwrap())
                .unwrap_err()
                .into());
        }
        assert!(config["port"] != 3, "a bug in build");
        Ok(Box::new(FakeRun {
            id: self.id,
            act: self.act.clone(),
            runs: self.runs.clone(),
            password: setup.secrets.get("password").map(str::to_owned),
            panics_in_timeout: config["port"] == 4,
        }))
    }
}

struct FakeRun {
    id: &'static str,
    act: Arc<Mutex<Act>>,
    runs: Arc<AtomicUsize>,
    password: Option<String>,
    panics_in_timeout: bool,
}

impl Instance for FakeRun {
    fn timeout(&self) -> Duration {
        assert!(!self.panics_in_timeout, "a bug in timeout");
        Duration::from_secs(5)
    }

    fn collect<'a>(&'a mut self, run: &'a Run) -> BoxFuture<'a, Result<Vec<Sample>, RunError>> {
        Box::pin(async move {
            self.runs.fetch_add(1, Ordering::SeqCst);
            let act = *self.act.lock().unwrap();
            let metric = if self.id == "a" {
                "sermon_a_up"
            } else {
                "sermon_b_up"
            };
            match act {
                Act::Samples(n) => {
                    let value = i64::from(self.password.is_some());
                    Ok((0..n)
                        .map(|i| Sample::int(metric, value).label("n", i.to_string()))
                        .collect())
                }
                Act::Fail => Err(RunError::Failed("connection refused".into())),
                Act::Panic => panic!("a bug in the module"),
                Act::Pend => std::future::pending().await,
                Act::Block(d) => {
                    run.blocking(move || std::thread::sleep(d)).await?;
                    Ok(Vec::new())
                }
                Act::Refused => Err(Refused("10.0.0.1 is not in module_targets".into()).into()),
                Act::Foreign => Ok(vec![Sample::int("sermon_cpu_hijack", 1)]),
                Act::Bidi => Ok(vec![Sample::int(metric, 1).label("n", "x\u{202e}y")]),
                Act::Logs(n) => {
                    for i in 0..n {
                        run.log(crate::LogLine {
                            at: std::time::UNIX_EPOCH,
                            name: "web".into(),
                            stderr: false,
                            message: format!("line {i}"),
                        });
                    }
                    Err(RunError::Failed("after the logs".into()))
                }
            }
        })
    }
}

#[derive(Default)]
struct Out {
    samples: Mutex<Vec<Sample>>,
    reports: Mutex<Vec<Vec<Health>>>,
    logs: Mutex<Vec<(String, crate::LogLine)>>,
}

impl Sink for Out {
    fn samples(&self, samples: Vec<Sample>, _: SystemTime) {
        self.samples.lock().unwrap().extend(samples);
    }
    fn health(&self, report: Vec<Health>, _: SystemTime) {
        self.reports.lock().unwrap().push(report);
    }
    fn logs(&self, module: &str, lines: Vec<crate::LogLine>) {
        let mut logs = self.logs.lock().unwrap();
        logs.extend(lines.into_iter().map(|l| (module.to_owned(), l)));
    }
}

impl Out {
    fn state(&self, module: &str) -> Option<(State, String)> {
        self.reports
            .lock()
            .unwrap()
            .last()?
            .iter()
            .find(|h| h.module == module)
            .map(|h| (h.state, h.reason.clone()))
    }
}

struct World {
    out: Arc<Out>,
    runtime: Runtime,
    act: BTreeMap<&'static str, Arc<Mutex<Act>>>,
    runs: BTreeMap<&'static str, Arc<AtomicUsize>>,
    builds: BTreeMap<&'static str, Arc<AtomicUsize>>,
    built_on: BTreeMap<&'static str, Arc<Mutex<Vec<std::thread::ThreadId>>>>,
}

fn world(veto: Veto) -> World {
    let out = Arc::new(Out::default());
    let (mut act, mut runs, mut builds) = (BTreeMap::new(), BTreeMap::new(), BTreeMap::new());
    let mut built_on = BTreeMap::new();
    let mut modules: Vec<Arc<dyn Module>> = Vec::new();
    for id in ["a", "b"] {
        let fake = Fake {
            id,
            act: Arc::new(Mutex::new(Act::Samples(2))),
            runs: Arc::new(AtomicUsize::new(0)),
            builds: Arc::new(AtomicUsize::new(0)),
            built_on: Arc::default(),
        };
        built_on.insert(id, fake.built_on.clone());
        act.insert(id, fake.act.clone());
        runs.insert(id, fake.runs.clone());
        builds.insert(id, fake.builds.clone());
        modules.push(Arc::new(fake));
    }
    let runtime = Runtime::new(modules, veto, out.clone(), Arc::new(SystemClock::new()))
        .with_pauses(Pauses {
            first: Duration::from_secs(30),
            max: Duration::from_secs(120),
        });
    World {
        out,
        runtime,
        act,
        runs,
        builds,
        built_on,
    }
}

fn spec(id: &str, config: &str) -> Spec {
    Spec {
        id: id.into(),
        enabled: true,
        interval: Some(Duration::from_secs(10)),
        config_json: config.into(),
    }
}

impl World {
    fn set(&self, id: &str, act: Act) {
        *self.act[id].lock().unwrap() = act;
    }
    fn runs(&self, id: &str) -> usize {
        self.runs[id].load(Ordering::SeqCst)
    }
}

/// Moves the paused clock on by hand: with a blocking job running, tokio doesn't do it alone.
async fn advance(d: Duration) {
    tokio::time::advance(d).await;
    for _ in 0..10 {
        tokio::task::yield_now().await;
    }
}

/// Yields until `done` holds, in real time (at most 10 s): `build` and blocking work run on real
/// threads, which the paused clock doesn't wait for – on a slow runner they take a while.
async fn until(mut done: impl FnMut() -> bool) {
    let deadline = std::time::Instant::now() + Duration::from_secs(10);
    while !done() {
        assert!(std::time::Instant::now() < deadline, "timed out waiting");
        std::thread::sleep(Duration::from_millis(5));
        tokio::task::yield_now().await;
    }
}

async fn settle(d: Duration) {
    tokio::time::sleep(d).await;
    // Let tasks woken at the end run.
    for _ in 0..10 {
        tokio::task::yield_now().await;
    }
}

// M5-21: the log lines of a run go to the sink, each once, at most MAX_LOG_LINES a run.
#[tokio::test(start_paused = true)]
async fn the_log_lines_of_a_run_go_out() {
    let mut w = world(Veto::default().any_port());
    *w.act["a"].lock().unwrap() = Act::Logs(crate::MAX_LOG_LINES + 7);
    w.runtime.apply(&[spec("a", "{}")], &BTreeMap::new());
    settle(Duration::from_secs(15)).await;
    let logs = w.out.logs.lock().unwrap().clone();
    assert_eq!(
        logs.len(),
        2 * crate::MAX_LOG_LINES,
        "two runs, bounded each"
    );
    assert_eq!(logs[0].0, "a");
    assert_eq!(logs[0].1.message, "line 0");
    assert_eq!(logs[crate::MAX_LOG_LINES].1.message, "line 0", "not again");
}

// ADR 0039: an enabled module runs at its interval and its samples go out; its state is ok.
#[tokio::test(start_paused = true)]
async fn an_enabled_module_runs_at_its_interval() {
    let mut w = world(Veto::default().any_port());
    w.runtime.apply(
        &[
            spec("a", "{}"),
            Spec {
                enabled: false,
                ..spec("b", "{}")
            },
        ],
        &BTreeMap::new(),
    );
    settle(Duration::from_secs(25)).await;
    assert_eq!(w.runs("a"), 3, "at 0, 10 and 20 s");
    assert_eq!(w.runs("b"), 0, "off");
    assert_eq!(w.out.samples.lock().unwrap().len(), 6);
    assert_eq!(w.out.state("a"), Some((State::Ok, String::new())));
    assert_eq!(w.out.state("b"), None);
    // Unchanged health is not sent again with every run.
    assert_eq!(w.out.reports.lock().unwrap().len(), 1);
}

// ADR 0039: a module that hangs, panics or times out stays alone with it: the other module goes
// on running, the state says why.
#[tokio::test(start_paused = true)]
async fn a_failing_module_doesnt_touch_the_others() {
    for (act, state) in [
        (Act::Pend, State::Timeout),
        (Act::Fail, State::Error),
        (Act::Panic, State::Crashed),
        (Act::Refused, State::Vetoed),
        (Act::Foreign, State::Error),
        (Act::Samples(MAX_SAMPLES + 1), State::Error),
    ] {
        let mut w = world(Veto::default().any_port());
        w.set("a", act);
        w.runtime
            .apply(&[spec("a", "{}"), spec("b", "{}")], &BTreeMap::new());
        settle(Duration::from_secs(25)).await;
        assert_eq!(w.out.state("a").map(|s| s.0), Some(state), "{act:?}");
        assert_eq!(w.out.state("b").map(|s| s.0), Some(State::Ok), "{act:?}");
        assert_eq!(w.runs("b"), 3, "{act:?}");
        assert!(
            w.out
                .samples
                .lock()
                .unwrap()
                .iter()
                .all(|s| s.metric == "sermon_b_up"),
            "{act:?}: samples of a failed run went out"
        );
    }
}

// A crashed module is built anew after a pause that doubles; a run that succeeds resets it.
#[tokio::test(start_paused = true)]
async fn a_crashed_module_starts_again_after_a_pause() {
    let mut w = world(Veto::default().any_port());
    w.set("a", Act::Panic);
    w.runtime.apply(&[spec("a", "{}")], &BTreeMap::new());
    settle(Duration::from_secs(1)).await;
    assert_eq!((w.runs("a"), w.builds["a"].load(Ordering::SeqCst)), (1, 1));
    settle(Duration::from_secs(31)).await;
    assert_eq!(
        (w.runs("a"), w.builds["a"].load(Ordering::SeqCst)),
        (2, 2),
        "after 30 s"
    );
    settle(Duration::from_secs(31)).await;
    assert_eq!(w.runs("a"), 2, "the second pause is 60 s");
    settle(Duration::from_secs(30)).await;
    assert_eq!(w.runs("a"), 3);
    w.set("a", Act::Samples(1));
    settle(Duration::from_secs(121)).await;
    assert_eq!(w.out.state("a").map(|s| s.0), Some(State::Ok));
}

// A panic in `build` or `timeout` is a crash like one in a run: reported, built anew after the
// pause; the other module runs on (M5-33).
#[tokio::test(start_paused = true)]
async fn a_panic_outside_the_run_is_a_crash_too() {
    for port in [3, 4] {
        let mut w = world(Veto::default().any_port());
        let config = format!(r#"{{"port":{port}}}"#);
        w.runtime
            .apply(&[spec("a", &config), spec("b", "{}")], &BTreeMap::new());
        settle(Duration::from_secs(1)).await;
        let (state, why) = w.out.state("a").expect("a report");
        assert_eq!(state, State::Crashed, "port {port}");
        assert!(why.contains("30 s"), "{why}");
        assert_eq!(w.builds["a"].load(Ordering::SeqCst), 1);
        settle(Duration::from_secs(31)).await;
        assert_eq!(
            w.builds["a"].load(Ordering::SeqCst),
            2,
            "port {port}: again"
        );
        assert_eq!(w.out.state("b").map(|s| s.0), Some(State::Ok));
        assert_eq!(w.runs("a"), 0);
    }
}

// V1-13: `build` checks paths of the veto (canonicalize, metadata) and reads CA files: it runs on
// a blocking thread, not on the executor.
#[tokio::test(start_paused = true)]
async fn the_build_runs_off_the_executor() {
    let mut w = world(Veto::default().any_port());
    w.runtime.apply(&[spec("a", "{}")], &BTreeMap::new());
    settle(Duration::from_secs(1)).await;
    let threads = w.built_on["a"].lock().unwrap().clone();
    assert_eq!(threads.len(), 1);
    assert_ne!(threads[0], std::thread::current().id(), "on the executor");
    assert_eq!(w.out.state("a").map(|s| s.0), Some(State::Ok));
}

// V1-13: a build that doesn't return (a path on a hung mount) is reported hung after the
// longest budget; once it returns, the module runs.
#[tokio::test(start_paused = true)]
async fn a_build_that_doesnt_return_is_hung() {
    let mut w = world(Veto::default().any_port());
    w.runtime
        .apply(&[spec("a", r#"{"port":5}"#)], &BTreeMap::new());
    advance(Duration::ZERO).await;
    advance(Duration::from_secs(29)).await;
    assert_eq!(w.out.state("a"), None, "not yet");
    advance(Duration::from_secs(2)).await;
    let (state, why) = w.out.state("a").expect("a report");
    assert_eq!(state, State::Hung);
    assert!(why.contains("build"), "{why}");
    std::thread::sleep(Duration::from_millis(700));
    advance(Duration::from_secs(1)).await;
    assert_eq!(w.runs("a"), 1);
    assert_eq!(w.out.state("a").map(|s| s.0), Some(State::Ok));
}

// Blocking work that doesn't return: the run times out, no further run starts while it blocks;
// once it returns, the module runs again.
#[tokio::test(start_paused = true)]
async fn a_hung_module_starts_no_new_run() {
    let mut w = world(Veto::default().any_port());
    w.set("a", Act::Block(Duration::from_millis(500)));
    w.runtime.apply(&[spec("a", "{}")], &BTreeMap::new());
    advance(Duration::ZERO).await;
    // The module is built on a blocking thread; its first run starts once that returns.
    until(|| w.runs("a") == 1).await;
    // Virtual time runs on while the thread blocks for real.
    advance(Duration::from_secs(6)).await;
    assert_eq!(w.out.state("a").map(|s| s.0), Some(State::Timeout));
    w.set("a", Act::Samples(1));
    advance(Duration::from_secs(4)).await;
    assert_eq!(w.out.state("a").map(|s| s.0), Some(State::Hung));
    assert_eq!(w.runs("a"), 1);
    std::thread::sleep(Duration::from_millis(700));
    advance(Duration::from_secs(10)).await;
    assert_eq!(w.out.state("a").map(|s| s.0), Some(State::Ok));
}

// The configuration is checked against the schema: a module outside it, one the module can't
// use and an unknown one don't run; the reasons go out.
#[tokio::test(start_paused = true)]
async fn an_invalid_configuration_keeps_the_module_off() {
    let mut w = world(Veto::default().any_port());
    w.runtime.apply(
        &[
            spec("a", r#"{"port":"eighty"}"#),
            spec("b", r#"{"port":1}"#),
            spec("nginx", "{}"),
        ],
        &BTreeMap::new(),
    );
    settle(Duration::from_secs(1)).await;
    let (state, why) = w.out.state("a").unwrap();
    assert_eq!(state, State::InvalidConfig);
    assert!(why.contains("port: not an integer"), "{why}");
    assert_eq!(
        w.out.state("b"),
        Some((State::InvalidConfig, "port 1 is reserved".into()))
    );
    assert_eq!(
        w.out.state("nginx").map(|s| s.0),
        Some(State::InvalidConfig)
    );
    assert_eq!(w.runs("a") + w.runs("b"), 0);
    // A credential doesn't belong in the configuration.
    w.runtime
        .apply(&[spec("a", r#"{"password":"x"}"#)], &BTreeMap::new());
    settle(Duration::from_secs(1)).await;
    assert_eq!(w.out.state("a").map(|s| s.0), Some(State::InvalidConfig));
    // M5-33: nor does a credential outside its property (maxLength 64 in the schema).
    let long = BTreeMap::from([(
        "b".to_owned(),
        Secrets::new([("password".to_owned(), "x".repeat(65))]),
    )]);
    w.runtime.apply(&[spec("b", "{}")], &long);
    settle(Duration::from_secs(1)).await;
    let (state, why) = w.out.state("b").unwrap();
    assert_eq!(state, State::InvalidConfig);
    assert!(why.starts_with("password: length"), "{why}");
    assert_eq!(w.runs("b"), 0);
}

// The local veto: allow_modules = false runs nothing, a target outside module_targets is vetoed.
#[tokio::test(start_paused = true)]
async fn the_local_veto_wins() {
    let mut w = world(Veto {
        allow: false,
        ..Veto::default().any_port()
    });
    w.runtime.apply(&[spec("a", "{}")], &BTreeMap::new());
    settle(Duration::from_secs(25)).await;
    assert_eq!(w.runs("a"), 0);
    let (state, why) = w.out.state("a").unwrap();
    assert_eq!(state, State::Vetoed);
    assert!(why.contains("allow_modules"), "{why}");
    let mut w = world(Veto::default().any_port());
    w.runtime
        .apply(&[spec("a", r#"{"port":2}"#)], &BTreeMap::new());
    settle(Duration::from_secs(1)).await;
    let (state, why) = w.out.state("a").unwrap();
    assert_eq!(state, State::Vetoed);
    assert!(why.contains("module_targets"), "{why}");
}

// A change restarts only the module it touches; credentials count as a change; a module turned
// off stops and leaves the report.
#[tokio::test(start_paused = true)]
async fn a_change_restarts_what_it_touches() {
    let mut w = world(Veto::default().any_port());
    w.runtime
        .apply(&[spec("a", "{}"), spec("b", "{}")], &BTreeMap::new());
    settle(Duration::from_secs(1)).await;
    w.runtime.apply(
        &[spec("a", r#"{"port":8080}"#), spec("b", "{}")],
        &BTreeMap::new(),
    );
    settle(Duration::from_secs(1)).await;
    assert_eq!(w.builds["a"].load(Ordering::SeqCst), 2);
    assert_eq!(w.builds["b"].load(Ordering::SeqCst), 1, "b unchanged");
    let secrets = BTreeMap::from([(
        "a".to_owned(),
        Secrets::new([("password".to_owned(), "pw".to_owned())]),
    )]);
    w.runtime
        .apply(&[spec("a", r#"{"port":8080}"#), spec("b", "{}")], &secrets);
    settle(Duration::from_secs(1)).await;
    assert_eq!(w.builds["a"].load(Ordering::SeqCst), 3);
    assert!(
        w.out
            .samples
            .lock()
            .unwrap()
            .iter()
            .any(|s| s.metric == "sermon_a_up" && s.value == sermon_core::Value::Int(1))
    );
    w.runtime.apply(&[spec("b", "{}")], &BTreeMap::new());
    settle(Duration::from_secs(30)).await;
    let runs = w.runs("a");
    settle(Duration::from_secs(30)).await;
    assert_eq!(w.runs("a"), runs, "a still runs");
    assert_eq!(w.out.state("a"), None);
    assert!(w.out.state("b").is_some());
}

#[test]
fn reasons_are_plain_and_bounded() {
    assert_eq!(bounded("a\nb\u{1b}[31m"), "a b [31m");
    let long = "ä".repeat(200);
    let cut = bounded(&long);
    assert!(cut.len() <= MAX_REASON && cut.chars().all(|c| c == 'ä'));
}

#[test]
fn samples_are_checked_against_the_protocol() {
    assert!(
        check(
            "nginx",
            &[Sample::int("sermon_nginx_up", 1).label("port", "80")]
        )
        .is_ok()
    );
    for bad in [
        Sample::int("sermon_cpu_up", 1),
        Sample::int("sermon_nginx_Up", 1),
        Sample::int("sermon_nginx_up", 1).label("Port", "80"),
        Sample::int("sermon_nginx_up", 1).label("port", "x\u{202e}\n"),
        Sample::int("sermon_nginx_up", 1).label("port", "x".repeat(257)),
    ] {
        assert!(
            check("nginx", std::slice::from_ref(&bad)).is_err(),
            "{bad:?}"
        );
    }
    let nine = (0..9).fold(Sample::int("sermon_nginx_up", 1), |s, _| s.label("l", "x"));
    assert!(check("nginx", &[nine]).is_err());
}

// M5-33: bidi controls, zero-width and other invisible characters in label values (a database
// name, a Prometheus label, a container's comm) could disguise one series as another; they become
// U+FFFD, visible, as the server's textsafe does.
#[test]
fn label_values_lose_invisible_characters() {
    let mut samples = vec![
        Sample::int("sermon_nginx_up", 1).label("port", "post\u{202e}gres"),
        Sample::int("sermon_nginx_up", 1).label("port", "a\u{200b}b\u{2066}c\u{2069}"),
        Sample::int("sermon_nginx_up", 1).label("port", "l\u{2028}m\u{2029}n\u{feff}"),
        Sample::int("sermon_nginx_up", 1).label("port", "\u{00ad}\u{180e}\u{3164}\u{e0041}"),
        Sample::int("sermon_nginx_up", 1).label("port", "Grüße 🐘 ü"),
    ];
    visible(&mut samples);
    let values: Vec<&str> = samples.iter().map(|s| s.labels[0].1.as_str()).collect();
    assert_eq!(
        values,
        [
            "post\u{fffd}gres",
            "a\u{fffd}b\u{fffd}c\u{fffd}",
            "l\u{fffd}m\u{fffd}n\u{fffd}",
            "\u{fffd}\u{fffd}\u{fffd}\u{fffd}",
            "Grüße 🐘 ü",
        ]
    );
    assert!(check("nginx", &samples).is_ok());
}

#[tokio::test(start_paused = true)]
async fn the_runtime_sends_label_values_visible() {
    let mut w = world(Veto::default().any_port());
    w.set("a", Act::Bidi);
    w.runtime.apply(&[spec("a", "{}")], &BTreeMap::new());
    settle(Duration::from_secs(1)).await;
    let samples = w.out.samples.lock().unwrap();
    assert!(!samples.is_empty());
    assert!(
        samples
            .iter()
            .all(|s| s.labels == [("n", "x\u{fffd}y".to_owned())]),
        "{samples:?}"
    );
}
