//! `sermon-agent run` end to end: the real binary enrolls and then talks to an in-process fake
//! agent API over mTLS – session, trust bundle, rotation, clean stop on SIGTERM.

// Test code: panicking on setup failures is the desired behaviour.
#![allow(clippy::expect_used, clippy::unwrap_used)]

use std::io::{BufRead, BufReader};
use std::path::PathBuf;
use std::process::{Child, Command, Stdio};
use std::sync::{Arc, mpsc};
use std::time::Duration;

use sermon_agent::identity;
use sermon_agent::state::StateLayout;
use sermon_testserver::{
    FakeAgentApi, FakeEnrollment, RunningServer, TestPki, mtls_server_config, serve_plain,
    serve_tls,
};

const TOKEN: &str = "sme_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_abcde";
const WAIT: Duration = Duration::from_secs(15);

struct World {
    dir: tempfile::TempDir,
    api: FakeAgentApi,
    next_root: Vec<u8>,
    _enrollment: RunningServer,
    _agent_api: RunningServer,
}

impl World {
    async fn start() -> Self {
        let pki = Arc::new(TestPki::new("Agent"));
        let enrollment = serve_plain(FakeEnrollment::issuing(pki.clone()).routes()).await;
        let api = FakeAgentApi::new(pki.clone());
        // A CA rollover in progress: the bundle has the current and the next root.
        let next_root = TestPki::new("Next").root().to_vec();
        api.set_trust_roots(vec![pki.root().to_vec(), next_root.clone()]);
        let (chain, key) = pki.server_identity(&["localhost"]);
        let agent_api =
            serve_tls(api.routes(), mtls_server_config(chain, key, &[pki.root()])).await;

        let dir = tempfile::tempdir().unwrap();
        let fixture = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../fixtures/debian-13");
        std::fs::write(
            dir.path().join("agent.toml"),
            format!(
                "[server]\nurl = \"{}\"\nagent_url = \"{}\"\n[paths]\nstate_dir = \"{}\"\nhost_root = \"{}\"\n",
                enrollment.http_url(),
                agent_api.https_url(),
                dir.path().join("state").display(),
                fixture.display()
            ),
        )
        .unwrap();
        Self {
            dir,
            api,
            next_root,
            _enrollment: enrollment,
            _agent_api: agent_api,
        }
    }

    fn command(&self, args: &[&str]) -> Command {
        let mut command = Command::new(env!("CARGO_BIN_EXE_sermon-agent"));
        command
            .args(args)
            .arg("--config")
            .arg(self.dir.path().join("agent.toml"))
            .env("SERMON_ENROLLMENT_TOKEN", TOKEN);
        command
    }

    fn layout(&self) -> StateLayout {
        StateLayout::new(self.dir.path().join("state"))
    }

    async fn enroll(&self) {
        let mut command = self.command(&["enroll"]);
        let output = tokio::task::spawn_blocking(move || command.output().unwrap())
            .await
            .unwrap();
        assert!(
            output.status.success(),
            "{}",
            String::from_utf8_lossy(&output.stderr)
        );
    }

    /// Starts `run` and returns the process with a channel of its log lines.
    fn run(&self) -> (Child, mpsc::Receiver<String>) {
        let mut child = self
            .command(&["run"])
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .spawn()
            .unwrap();
        let stdout = child.stdout.take().unwrap();
        let (lines, received) = mpsc::channel();
        std::thread::spawn(move || {
            for line in BufReader::new(stdout).lines().map_while(Result::ok) {
                if lines.send(line).is_err() {
                    break;
                }
            }
        });
        (child, received)
    }
}

async fn stop(mut child: Child, lines: &mpsc::Receiver<String>) -> Vec<String> {
    // The shell builtin: minimal images have no /bin/kill.
    let status = Command::new("sh")
        .args(["-c", &format!("kill -TERM {}", child.id())])
        .status()
        .unwrap();
    assert!(status.success());
    let exit = tokio::task::spawn_blocking(move || child.wait().unwrap())
        .await
        .unwrap();
    let rest: Vec<String> = lines.try_iter().collect();
    assert!(exit.success(), "exit {exit:?}, output {rest:?}");
    rest
}

#[tokio::test(flavor = "multi_thread")]
async fn runs_a_session_stores_the_trust_bundle_and_stops_on_sigterm() {
    let world = World::start().await;
    world.enroll().await;
    let certificate = identity::load(&world.layout()).unwrap().certificate;

    let (child, lines) = world.run();

    assert!(
        world
            .api
            .wait_for(WAIT, |s| s.first().is_some_and(|s| s.hello.is_some()))
            .await
    );
    let session = &world.api.sessions()[0];
    assert_eq!(session.client_cert, Some(certificate));
    assert_eq!(
        session.hello.clone().unwrap().host_facts.unwrap().hostname,
        "web-01"
    );
    let layout = world.layout();
    let deadline = std::time::Instant::now() + WAIT;
    while identity::load(&layout).unwrap().trust_anchors.len() < 2 {
        assert!(
            std::time::Instant::now() < deadline,
            "trust bundle not stored"
        );
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    assert_eq!(
        identity::load(&layout).unwrap().trust_anchors[1].to_vec(),
        world.next_root
    );

    let rest = stop(child, &lines).await;
    assert!(
        rest.iter().any(|line| line.contains("agent stopped")),
        "{rest:?}"
    );
}

#[tokio::test(flavor = "multi_thread")]
async fn rotates_a_certificate_that_is_due_and_keeps_running() {
    let world = World::start().await;
    world.enroll().await;
    // Pretend 25 of 30 days are over: two thirds of the lifetime have passed.
    let metadata = world.layout().identity_file();
    let text = std::fs::read_to_string(&metadata).unwrap();
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_secs();
    let text: String = text
        .lines()
        .map(|line| {
            if line.starts_with("issued_at") {
                format!("issued_at = {}\n", now - 25 * 24 * 3600)
            } else if line.starts_with("not_after") {
                format!("not_after = {}\n", now + 5 * 24 * 3600)
            } else {
                format!("{line}\n")
            }
        })
        .collect();
    std::fs::write(&metadata, text).unwrap();
    let old = identity::load(&world.layout()).unwrap().certificate;

    let (child, lines) = world.run();

    let reconnected = world
        .api
        .wait_for(WAIT, |s| s.len() >= 2 && s[1].hello.is_some())
        .await;
    assert!(
        reconnected,
        "{:?}\n{:?}",
        world.api.sessions(),
        lines.try_iter().collect::<Vec<_>>()
    );
    let sessions = world.api.sessions();
    assert_eq!(sessions[0].rotations, 1);
    let new = identity::load(&world.layout()).unwrap().certificate;
    assert_ne!(new, old);
    assert_eq!(sessions[1].client_cert, Some(new));
    stop(child, &lines).await;
}
