//! The security parsers against real journal lines of every target distribution
//! (agent/fixtures/logs, captured with agent/fixtures/logs/capture): expected events, nothing from
//! the other lines, nothing from forged lines of an unprivileged user.

// Test code: panicking on setup failures is the desired behaviour.
#![allow(clippy::expect_used, clippy::unwrap_used, clippy::panic)]

use std::path::PathBuf;

use sermon_logs::journal::{Entry, parse_line};
use sermon_security::{Address, EventKind, LogLine, Parser, ParserConfig, SecurityEvent};

const DISTROS: [&str; 4] = ["debian-13", "ubuntu-24.04", "rockylinux-9", "rockylinux-8"];

fn entries(distro: &str, file: &str) -> Vec<Entry> {
    let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../fixtures/logs")
        .join(distro)
        .join(file);
    std::fs::read(&path)
        .unwrap_or_else(|e| panic!("{}: {e}", path.display()))
        .split(|b| *b == b'\n')
        .filter(|l| !l.is_empty())
        .map(|l| parse_line(l).unwrap())
        .collect()
}

fn line(entry: &Entry) -> LogLine<'_> {
    LogLine {
        at_us: entry.realtime_us,
        identifier: entry.identifier.as_deref(),
        transport: entry.transport.as_deref(),
        uid: entry.uid,
        cap_effective: entry.cap_effective,
        boot_id: entry.boot_id.as_deref(),
        monotonic_us: entry.monotonic_us,
        message: &entry.message,
    }
}

fn events(distro: &str, file: &str, config: ParserConfig) -> Vec<SecurityEvent> {
    let mut parser = Parser::new(config, None);
    let mut out = Vec::new();
    for entry in entries(distro, file) {
        parser.parse(&line(&entry), &mut out);
    }
    out
}

fn address(remote: &sermon_security::Remote) -> String {
    match &remote.address {
        Address::Ip(ip) => ip.to_string(),
        Address::Host(host) => format!("host:{host}"),
    }
}

/// The event without its port and time, which differ per capture.
fn summary(event: &SecurityEvent) -> String {
    let name = event.kind.name();
    match &event.kind {
        EventKind::SshLoginFailed {
            user,
            remote,
            method,
            invalid_user,
        } => {
            format!(
                "{name} {user:?} {} {} invalid={invalid_user}",
                address(remote),
                method.name()
            )
        }
        EventKind::SshInvalidUser { user, remote } => {
            format!("{name} {user:?} {}", address(remote))
        }
        EventKind::SshLoginSucceeded {
            user,
            remote,
            method,
            key,
        } => format!(
            "{name} {user:?} {} {}{}",
            address(remote),
            method.name(),
            key.as_ref()
                .map_or_else(String::new, |k| format!(" {}", k.key_type))
        ),
        EventKind::SshPreauthDisconnect {
            user,
            remote,
            invalid_user,
        } => {
            format!("{name} {user:?} {} invalid={invalid_user}", address(remote))
        }
        EventKind::SudoAuthFailed { user, tty } => format!("{name} {user:?} tty={tty:?}"),
        EventKind::SudoNotAllowed { user, target, .. } => format!("{name} {user:?} as {target:?}"),
        EventKind::SudoCommand {
            user,
            target,
            command,
            ..
        } => format!("{name} {user:?} as {target:?}: {command}"),
        EventKind::AccountCreated { user, .. } | EventKind::AccountDeleted { user } => {
            format!("{name} {user:?}")
        }
        EventKind::SystemReboot { previous_boot, .. } => format!("{name} {previous_boot}"),
        EventKind::OomKill { process, .. } => format!("{name} {process:?}"),
    }
}

fn summaries(events: &[SecurityEvent]) -> Vec<String> {
    events.iter().map(summary).collect()
}

#[test]
fn the_spike_fixtures_on_every_distro() {
    let want = [
        r#"ssh.login.failed "alice" 127.0.0.1 password invalid=false"#,
        r#"ssh.preauth.disconnect Some("alice") 127.0.0.1 invalid=false"#,
        r#"ssh.login.success "alice" 127.0.0.1 password"#,
        r#"ssh.login.invalid_user "mallory" 127.0.0.1"#,
        r#"ssh.login.failed "mallory" 127.0.0.1 password invalid=true"#,
        r#"ssh.preauth.disconnect Some("mallory") 127.0.0.1 invalid=true"#,
        r#"sudo.auth.failed "alice" tty=None"#,
        r#"account.created "bob""#,
        r#"account.deleted "bob""#,
    ];
    for distro in DISTROS {
        let got = events(distro, "journal-security.jsonl", ParserConfig::default());
        assert_eq!(summaries(&got), want, "{distro}");
        for event in &got {
            if let EventKind::AccountCreated { uid, .. } = event.kind {
                assert!(uid.is_some_and(|u| u >= 1000), "{distro}: {uid:?}");
            }
            assert!(
                event.at_us > 1_700_000_000_000_000,
                "{distro}: time from the log"
            );
        }
    }
}

// IPv6, root, public keys, keyboard-interactive, user names chosen by the attacker (a fake
// "from … port", unicode, empty), sudo without the right – and forged lines, which yield nothing.
#[test]
fn the_variants_on_every_distro() {
    let want = [
        r#"ssh.login.failed "alice" ::1 password invalid=false"#,
        r#"ssh.preauth.disconnect Some("alice") ::1 invalid=false"#,
        r#"ssh.login.success "alice" ::1 password"#,
        r#"ssh.login.success "root" 127.0.0.1 password"#,
        r#"ssh.login.failed "root" ::1 password invalid=false"#,
        r#"ssh.preauth.disconnect Some("root") ::1 invalid=false"#,
        r#"ssh.login.invalid_user "x from 192.0.2.66 port 22" 127.0.0.1"#,
        r#"ssh.login.failed "x from 192.0.2.66 port 22" 127.0.0.1 password invalid=true"#,
        r#"ssh.preauth.disconnect Some("x from 192.0.2.66 port 22") 127.0.0.1 invalid=true"#,
        r#"ssh.login.invalid_user "jürgen müller" ::1"#,
        r#"ssh.login.failed "jürgen müller" ::1 password invalid=true"#,
        r#"ssh.preauth.disconnect Some("jürgen müller") ::1 invalid=true"#,
        r#"ssh.login.invalid_user "" 127.0.0.1"#,
        r#"ssh.login.failed "" 127.0.0.1 password invalid=true"#,
        r#"ssh.preauth.disconnect Some("") 127.0.0.1 invalid=true"#,
        r#"ssh.login.failed "alice" 127.0.0.1 password invalid=false"#,
        r#"ssh.preauth.disconnect Some("alice") 127.0.0.1 invalid=false"#,
        r#"ssh.login.success "alice" ::1 publickey ED25519"#,
        r#"ssh.preauth.disconnect Some("alice") 127.0.0.1 invalid=false"#,
        r#"ssh.login.failed "alice" 127.0.0.1 keyboard-interactive invalid=false"#,
        r#"ssh.preauth.disconnect Some("alice") 127.0.0.1 invalid=false"#,
        r#"ssh.login.success "alice" ::1 keyboard-interactive"#,
        r#"sudo.not_allowed "carol" as "root""#,
    ];
    for distro in DISTROS {
        let got = events(
            distro,
            "journal-security-variants.jsonl",
            ParserConfig::default(),
        );
        assert_eq!(summaries(&got), want, "{distro}");
        for event in &got {
            let text = format!("{event:?}");
            assert!(
                !text.contains("203.0.113.9") && !text.contains("backdoor"),
                "{distro}: forged: {text}"
            );
            if let EventKind::SshLoginSucceeded { key: Some(key), .. } = &event.kind {
                assert!(key.fingerprint.starts_with("SHA256:"), "{distro}: {key:?}");
            }
        }
    }
}

#[test]
fn ports_are_known_except_for_the_pam_message() {
    for distro in DISTROS {
        for event in events(
            distro,
            "journal-security-variants.jsonl",
            ParserConfig::default(),
        ) {
            let (remote, kbd) = match &event.kind {
                EventKind::SshLoginFailed { remote, method, .. } => {
                    (remote, method.name() == "keyboard-interactive")
                }
                EventKind::SshInvalidUser { remote, .. }
                | EventKind::SshLoginSucceeded { remote, .. }
                | EventKind::SshPreauthDisconnect { remote, .. } => (remote, false),
                _ => continue,
            };
            assert_eq!(remote.port.is_none(), kbd, "{distro}: {event:?}");
        }
    }
}

#[test]
fn sudo_commands_only_when_switched_on() {
    let on = ParserConfig {
        sudo_commands: true,
    };
    for distro in DISTROS {
        let commands: Vec<String> = summaries(&events(distro, "journal-security.jsonl", on))
            .into_iter()
            .filter(|s| s.starts_with("sudo.command"))
            .collect();
        assert_eq!(
            commands.len(),
            1,
            "{distro}: {commands:?} (the failed attempt is no command)"
        );
        assert!(commands[0].ends_with("/true"), "{distro}: {commands:?}");
        let variants = events(distro, "journal-security-variants.jsonl", on);
        let id = variants.iter().find_map(|e| match &e.kind {
            EventKind::SudoCommand { tty, command, .. } => Some((tty.clone(), command.clone())),
            _ => None,
        });
        let (tty, command) = id.unwrap_or_else(|| panic!("{distro}: no command"));
        assert!(command.ends_with("/id"), "{distro}: {command}");
        // script(1) gives sudo a terminal where it reports one (Debian, Ubuntu).
        if distro.starts_with("debian") || distro.starts_with("ubuntu") {
            assert_eq!(tty.as_deref(), Some("pts/0"), "{distro}");
        }
    }
}

#[test]
fn oom_kills_from_the_kernel() {
    for distro in DISTROS {
        let got = events(distro, "journal-oom.jsonl", ParserConfig::default());
        assert_eq!(
            summaries(&got),
            [r#"system.oom_kill "python3""#],
            "{distro}"
        );
        let EventKind::OomKill { pid, .. } = got[0].kind else {
            unreachable!()
        };
        assert!(pid > 1, "{distro}");
    }
}

// The format fixtures (binary messages, long fields) hold nothing security-relevant.
#[test]
fn other_lines_yield_nothing() {
    for distro in DISTROS {
        assert!(
            events(distro, "journal-format.jsonl", ParserConfig::default()).is_empty(),
            "{distro}"
        );
    }
}
