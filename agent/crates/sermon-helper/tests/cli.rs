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
        format!("sermon-helper {}", env!("CARGO_PKG_VERSION"))
    );
}

#[test]
fn rejects_unknown_arguments_with_usage_and_exit_code_2() {
    let output = run(&["serve", "--now"]);

    assert_eq!(output.status.code(), Some(2));
    assert!(String::from_utf8_lossy(&output.stderr).contains("usage: sermon-helper"));
}
