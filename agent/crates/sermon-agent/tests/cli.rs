//! Smoke tests for the `sermon-agent` command line.

// Test code: panicking on setup failures is the desired behaviour.
#![allow(clippy::expect_used)]

use std::process::Command;

fn run(args: &[&str]) -> std::process::Output {
    Command::new(env!("CARGO_BIN_EXE_sermon-agent"))
        .args(args)
        .output()
        .expect("failed to start sermon-agent")
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
fn prints_version_with_short_flag() {
    let output = run(&["-V"]);

    assert!(output.status.success());
    assert!(String::from_utf8_lossy(&output.stdout).starts_with("sermon-agent "));
}

#[test]
fn rejects_unknown_arguments_with_usage_and_exit_code_2() {
    let output = run(&["--bogus"]);

    assert_eq!(output.status.code(), Some(2));
    assert!(String::from_utf8_lossy(&output.stderr).contains("usage: sermon-agent"));
}
