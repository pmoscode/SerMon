//! Smoke tests for the `sermon-helper` command line.

// Test code: panicking on setup failures is the desired behaviour.
#![allow(clippy::expect_used)]

use std::process::Command;

fn run(args: &[&str]) -> std::process::Output {
    Command::new(env!("CARGO_BIN_EXE_sermon-helper"))
        .args(args)
        .output()
        .expect("failed to start sermon-helper")
}

#[test]
fn prints_version() {
    let output = run(&["--version"]);

    assert!(output.status.success());
    assert_eq!(
        String::from_utf8_lossy(&output.stdout).trim(),
        format!("sermon-helper {}", sermon_helper::VERSION)
    );
}

#[test]
fn rejects_unknown_arguments_with_usage_and_exit_code_2() {
    let output = run(&["serve", "--now"]);

    assert_eq!(output.status.code(), Some(2));
    assert!(String::from_utf8_lossy(&output.stderr).contains("usage: sermon-helper"));
}

// Review M4-30: under OpenRC a run writes to the log pipe of the service that started it; after a
// restart of the service nobody reads it any more. A write that fails must not end the helper
// (Rust's eprintln! panics then – in a run, in the middle of an update).
#[test]
fn a_closed_log_doesnt_end_it() {
    let (reader, writer) = std::io::pipe().expect("pipe");
    drop(reader);
    let status = Command::new(env!("CARGO_BIN_EXE_sermon-helper"))
        .args(["serve", "--now"])
        .stderr(writer)
        .status()
        .expect("failed to start sermon-helper");
    assert_eq!(status.code(), Some(2), "it panicked");
}
