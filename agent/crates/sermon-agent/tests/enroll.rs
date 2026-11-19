//! `sermon-agent enroll` end to end: the real binary against an in-process fake server.

// Test code: panicking on setup failures is the desired behaviour.
#![allow(clippy::expect_used, clippy::unwrap_used)]

use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::process::{Command, Output};
use std::sync::Arc;

use sermon_agent::identity;
use sermon_agent::state::StateLayout;
use sermon_testserver::{
    FakeEnrollment, NODE_ID, RunningServer, TestPki, serve_tls, tls_server_config,
};
use sermon_transport::fingerprint::Fingerprint;
use tonic::Status;

const TOKEN: &str = "sme_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_abcde";

struct Host {
    dir: tempfile::TempDir,
}

impl Host {
    fn new(server_url: &str) -> Self {
        let dir = tempfile::tempdir().unwrap();
        let fixture = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../fixtures/debian-13");
        std::fs::write(
            dir.path().join("agent.toml"),
            format!(
                "[server]\nurl = \"{server_url}\"\n[paths]\nstate_dir = \"{}\"\nhost_root = \"{}\"\n",
                dir.path().join("state").display(),
                fixture.display()
            ),
        )
        .unwrap();
        Self { dir }
    }

    fn layout(&self) -> StateLayout {
        StateLayout::new(self.dir.path().join("state"))
    }

    fn config(&self) -> PathBuf {
        self.dir.path().join("agent.toml")
    }

    /// Runs `sermon-agent enroll` with the token in the environment (as install.sh does).
    async fn enroll(&self, token: &str, extra: &[&str]) -> Output {
        let mut command = Command::new(env!("CARGO_BIN_EXE_sermon-agent"));
        command
            .arg("enroll")
            .arg("--config")
            .arg(self.config())
            .args(extra)
            .env("SERMON_ENROLLMENT_TOKEN", token);
        tokio::task::spawn_blocking(move || command.output().unwrap())
            .await
            .unwrap()
    }
}

struct Server {
    service: FakeEnrollment,
    running: RunningServer,
    leaf: Fingerprint,
}

async fn server(service: FakeEnrollment) -> Server {
    let web = TestPki::new("Web");
    let (chain, key) = web.server_identity(&["localhost"]);
    let leaf = Fingerprint::of(&chain[0]);
    let running = serve_tls(service.routes(), tls_server_config(chain, key)).await;
    Server {
        service,
        running,
        leaf,
    }
}

fn issuing() -> FakeEnrollment {
    FakeEnrollment::issuing(Arc::new(TestPki::new("Agent")))
}

fn text(bytes: &[u8]) -> String {
    String::from_utf8_lossy(bytes).into_owned()
}

fn mode(path: &Path) -> u32 {
    std::fs::metadata(path).unwrap().permissions().mode() & 0o777
}

#[tokio::test(flavor = "multi_thread")]
async fn enrolls_and_stores_a_private_identity() {
    let server = server(issuing()).await;
    let host = Host::new(&server.running.https_url());

    let output = host
        .enroll(TOKEN, &["--ca-fingerprint", &server.leaf.to_string()])
        .await;

    assert!(output.status.success(), "{}", text(&output.stderr));
    assert!(
        text(&output.stdout).contains(NODE_ID),
        "{}",
        text(&output.stdout)
    );
    let layout = host.layout();
    assert_eq!(mode(&layout.identity_dir()), 0o700);
    assert_eq!(mode(&layout.key_file()), 0o600);
    let stored = identity::load(&layout).unwrap();
    assert_eq!(stored.identity.node_id, NODE_ID);
    let requests = server.service.requests();
    assert_eq!(requests.len(), 1);
    let facts = requests[0].host_facts.clone().unwrap();
    assert_eq!(facts.hostname, "web-01");
    assert_eq!(facts.os_id, "debian");
}

#[tokio::test(flavor = "multi_thread")]
async fn wrong_fingerprint_aborts_without_sending_or_storing() {
    let server = server(issuing()).await;
    let host = Host::new(&server.running.https_url());
    let wrong = Fingerprint::of(b"someone else").to_string();

    let output = host.enroll(TOKEN, &["--ca-fingerprint", &wrong]).await;

    assert_eq!(output.status.code(), Some(1));
    let stderr = text(&output.stderr);
    assert!(stderr.contains("pinned fingerprint"), "{stderr}");
    assert!(stderr.contains(&server.leaf.to_string()), "{stderr}");
    assert_eq!(server.service.requests().len(), 0);
    assert!(!host.layout().identity_dir().exists());
}

#[tokio::test(flavor = "multi_thread")]
async fn existing_enrollment_is_kept_unless_forced() {
    let server = server(issuing()).await;
    let host = Host::new(&server.running.https_url());
    let pin = server.leaf.to_string();
    assert!(
        host.enroll(TOKEN, &["--ca-fingerprint", &pin])
            .await
            .status
            .success()
    );
    let first = identity::load(&host.layout())
        .unwrap()
        .key
        .public_key_info();

    let refused = host.enroll(TOKEN, &["--ca-fingerprint", &pin]).await;

    assert_eq!(refused.status.code(), Some(1));
    assert!(
        text(&refused.stderr).contains("--force"),
        "{}",
        text(&refused.stderr)
    );
    assert_eq!(server.service.requests().len(), 1);

    let forced = host
        .enroll(TOKEN, &["--ca-fingerprint", &pin, "--force"])
        .await;

    assert!(forced.status.success(), "{}", text(&forced.stderr));
    let second = identity::load(&host.layout())
        .unwrap()
        .key
        .public_key_info();
    assert_ne!(first, second, "a re-enrollment must use a new key");
}

#[tokio::test(flavor = "multi_thread")]
async fn refused_enrollment_stores_nothing() {
    let server = server(FakeEnrollment::failing(Status::unauthenticated(
        "invalid enrollment token",
    )))
    .await;
    let host = Host::new(&server.running.https_url());

    let output = host
        .enroll(TOKEN, &["--ca-fingerprint", &server.leaf.to_string()])
        .await;

    assert_eq!(output.status.code(), Some(1));
    assert!(text(&output.stderr).contains("create a new one in the UI"));
    assert!(!host.layout().identity_dir().exists());
}

#[tokio::test(flavor = "multi_thread")]
async fn malformed_token_is_refused_locally_and_not_echoed() {
    let server = server(issuing()).await;
    let host = Host::new(&server.running.https_url());
    let mistyped = "sme_AbCdEfGh IjKl";

    let output = host
        .enroll(mistyped, &["--ca-fingerprint", &server.leaf.to_string()])
        .await;

    assert_eq!(output.status.code(), Some(1));
    let stderr = text(&output.stderr);
    assert!(stderr.contains("not an enrollment token"), "{stderr}");
    assert!(!stderr.contains("IjKl"), "{stderr}");
    assert_eq!(server.service.requests().len(), 0);
}

#[tokio::test(flavor = "multi_thread")]
async fn fingerprint_requires_https() {
    let host = Host::new("http://127.0.0.1:9");
    let pin = Fingerprint::of(b"x").to_string();

    let output = host.enroll(TOKEN, &["--ca-fingerprint", &pin]).await;

    assert_eq!(output.status.code(), Some(1));
    assert!(text(&output.stderr).contains("needs an https://"));
}
