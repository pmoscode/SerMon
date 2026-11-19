//! Tests of the `sermon-agent` binary: command line, exit codes and signal handling.

// Test code: panicking on setup failures is the desired behaviour.
#![allow(clippy::expect_used)]

use std::path::Path;
use std::process::{Command, Output};

const BINARY: &str = env!("CARGO_BIN_EXE_sermon-agent");

fn run(args: &[&str]) -> Output {
    Command::new(BINARY)
        .args(args)
        .env_remove("SERMON_ENROLLMENT_TOKEN")
        .output()
        .expect("failed to start sermon-agent")
}

fn write_config(dir: &Path) -> String {
    let path = dir.join("agent.toml");
    let state = dir.join("state");
    std::fs::write(
        &path,
        format!(
            "[server]\nurl = \"https://sermon.example.com\"\n[paths]\nstate_dir = \"{}\"\n",
            state.display()
        ),
    )
    .expect("write config");
    path.to_str().expect("utf-8 path").to_owned()
}

#[test]
fn prints_version_with_long_flag() {
    let output = run(&["--version"]);

    assert!(output.status.success());
    assert_eq!(
        String::from_utf8_lossy(&output.stdout).trim(),
        format!("sermon-agent {}", env!("CARGO_PKG_VERSION"))
    );
}

#[test]
fn prints_version_with_short_flag_and_subcommand() {
    for args in [&["-V"][..], &["version"][..]] {
        let output = run(args);

        assert!(output.status.success(), "{args:?}");
        assert_eq!(
            String::from_utf8_lossy(&output.stdout).trim(),
            format!("sermon-agent {}", env!("CARGO_PKG_VERSION")),
            "{args:?}"
        );
    }
}

#[test]
fn rejects_unknown_arguments_with_usage_and_exit_code_2() {
    let output = run(&["--bogus"]);

    assert_eq!(output.status.code(), Some(2));
    assert!(String::from_utf8_lossy(&output.stderr).contains("Usage: sermon-agent"));
}

#[test]
fn run_fails_with_the_path_of_a_missing_config() {
    let output = run(&["run", "--config", "/nonexistent/agent.toml"]);

    assert_eq!(output.status.code(), Some(1));
    assert!(String::from_utf8_lossy(&output.stderr).contains("/nonexistent/agent.toml"));
}

#[test]
fn run_lists_all_config_problems() {
    let dir = tempfile::tempdir().expect("tempdir");
    let path = dir.path().join("agent.toml");
    std::fs::write(
        &path,
        "[server]\nurl = \"http://sermon.example.com\"\n[paths]\nstate_dir = \"rel\"\n",
    )
    .expect("write config");

    let output = run(&["run", "--config", path.to_str().expect("utf-8 path")]);

    let stderr = String::from_utf8_lossy(&output.stderr);
    assert_eq!(output.status.code(), Some(1));
    assert!(stderr.contains("server.url"), "{stderr}");
    assert!(stderr.contains("paths.state_dir"), "{stderr}");
}

#[test]
fn run_refuses_to_start_without_enrollment() {
    let dir = tempfile::tempdir().expect("tempdir");
    let config = write_config(dir.path());

    let output = run(&["run", "--config", &config]);

    assert_eq!(output.status.code(), Some(1));
    assert!(String::from_utf8_lossy(&output.stderr).contains("not enrolled"));
}

#[test]
fn uninstall_refuses_without_confirmation() {
    let dir = tempfile::tempdir().expect("tempdir");
    let config = write_config(dir.path());
    let identity = dir.path().join("state/identity");
    std::fs::create_dir_all(&identity).expect("mkdir");

    let output = run(&["uninstall", "--config", &config]);

    assert_eq!(output.status.code(), Some(1));
    assert!(String::from_utf8_lossy(&output.stderr).contains("--yes"));
    assert!(identity.exists());
}

#[test]
fn uninstall_removes_identity_and_buffer() {
    let dir = tempfile::tempdir().expect("tempdir");
    let config = write_config(dir.path());
    let identity = dir.path().join("state/identity");
    let wal = dir.path().join("state/wal");
    std::fs::create_dir_all(&identity).expect("mkdir");
    std::fs::create_dir_all(&wal).expect("mkdir");

    let output = run(&["uninstall", "--yes", "--config", &config]);

    assert!(output.status.success());
    assert!(!identity.exists() && !wal.exists());
    let stdout = String::from_utf8_lossy(&output.stdout);
    assert!(stdout.contains("removed"), "{stdout}");
}
