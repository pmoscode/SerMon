//! Security events in the agent (M2-06, KONZEPT §11): journal entries through the parsers of
//! `sermon-security`, and the events as protocol messages. Sending them (through the WAL) follows
//! with M2-07.

use sermon_logs::journal::{Boot, Entry};
use sermon_security::{Address, AuthMethod, EventKind, LogLine, Remote, SecurityEvent};
use sermon_transport::proto::agent::v1 as proto;

/// The parsers' view of a journal entry.
#[must_use]
pub fn line(entry: &Entry) -> LogLine<'_> {
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

/// The boot of the last entry before a restart, from the stored cursor: a reboot while the agent
/// was stopped is noticed with the first entry of the new boot.
#[must_use]
pub fn last_boot(cursor: Option<&str>) -> Option<(String, Option<u64>)> {
    cursor
        .and_then(Boot::from_cursor)
        .map(|boot| (boot.id, boot.monotonic_us))
}

fn timestamp(us: u64) -> prost_types::Timestamp {
    prost_types::Timestamp {
        seconds: i64::try_from(us / 1_000_000).unwrap_or(i64::MAX),
        // < 1 000 000 000, fits.
        nanos: i32::try_from(us % 1_000_000 * 1000).unwrap_or(0),
    }
}

fn remote(remote: &Remote) -> proto::SshRemote {
    proto::SshRemote {
        address: Some(match &remote.address {
            Address::Ip(ip) => proto::ssh_remote::Address::Ip(ip.to_string()),
            Address::Host(host) => proto::ssh_remote::Address::Host(host.clone()),
        }),
        port: remote.port.map_or(0, u32::from),
    }
}

fn method(method: AuthMethod) -> i32 {
    i32::from(match method {
        AuthMethod::Password => proto::SshAuthMethod::Password,
        AuthMethod::PublicKey => proto::SshAuthMethod::Publickey,
        AuthMethod::KeyboardInteractive => proto::SshAuthMethod::KeyboardInteractive,
        AuthMethod::HostBased => proto::SshAuthMethod::Hostbased,
        AuthMethod::Gssapi => proto::SshAuthMethod::Gssapi,
        AuthMethod::None => proto::SshAuthMethod::None,
        AuthMethod::Other => proto::SshAuthMethod::Other,
    })
}

/// The event as a protocol message.
#[must_use]
pub fn to_proto(event: &SecurityEvent) -> proto::SecurityEvent {
    use proto::security_event::Event;
    let tty = |tty: &Option<String>| tty.clone().unwrap_or_default();
    let message = match &event.kind {
        EventKind::SshLoginFailed {
            user,
            remote: r,
            method: m,
            invalid_user,
        } => Event::SshLoginFailed(proto::SshLoginFailed {
            user: user.clone(),
            remote: Some(remote(r)),
            method: method(*m),
            invalid_user: *invalid_user,
        }),
        EventKind::SshInvalidUser { user, remote: r } => {
            Event::SshInvalidUser(proto::SshInvalidUser {
                user: user.clone(),
                remote: Some(remote(r)),
            })
        }
        EventKind::SshLoginSucceeded {
            user,
            remote: r,
            method: m,
            key,
        } => Event::SshLoginSucceeded(proto::SshLoginSucceeded {
            user: user.clone(),
            remote: Some(remote(r)),
            method: method(*m),
            key_type: key
                .as_ref()
                .map_or_else(String::new, |k| k.key_type.clone()),
            key_fingerprint: key
                .as_ref()
                .map_or_else(String::new, |k| k.fingerprint.clone()),
        }),
        EventKind::SshPreauthDisconnect {
            user,
            remote: r,
            invalid_user,
        } => Event::SshPreauthDisconnect(proto::SshPreauthDisconnect {
            user: user.clone(),
            remote: Some(remote(r)),
            invalid_user: *invalid_user,
        }),
        EventKind::SudoAuthFailed { user, tty: t } => {
            Event::SudoAuthFailed(proto::SudoAuthFailed {
                user: user.clone(),
                tty: tty(t),
            })
        }
        EventKind::SudoNotAllowed {
            user,
            target,
            tty: t,
        } => Event::SudoNotAllowed(proto::SudoNotAllowed {
            user: user.clone(),
            target: target.clone(),
            tty: tty(t),
        }),
        EventKind::SudoCommand {
            user,
            target,
            tty: t,
            command,
        } => Event::SudoCommand(proto::SudoCommand {
            user: user.clone(),
            target: target.clone(),
            tty: tty(t),
            command: command.clone(),
        }),
        EventKind::AccountCreated { user, uid } => Event::AccountCreated(proto::AccountCreated {
            user: user.clone(),
            uid: *uid,
        }),
        EventKind::AccountDeleted { user } => {
            Event::AccountDeleted(proto::AccountDeleted { user: user.clone() })
        }
        EventKind::SystemReboot {
            previous_boot,
            uptime_before_us,
        } => Event::SystemReboot(proto::SystemReboot {
            previous_boot_id: previous_boot.clone(),
            uptime_before: uptime_before_us.map(|us| prost_types::Duration {
                seconds: i64::try_from(us / 1_000_000).unwrap_or(i64::MAX),
                nanos: i32::try_from(us % 1_000_000 * 1000).unwrap_or(0),
            }),
        }),
        EventKind::OomKill { pid, process } => Event::OomKill(proto::OomKill {
            pid: *pid,
            process: process.clone(),
        }),
    };
    proto::SecurityEvent {
        occurred_at: Some(timestamp(event.at_us)),
        event: Some(message),
    }
}

#[cfg(test)]
mod tests {
    use std::path::PathBuf;

    use prost::Message;
    use sermon_logs::journal::parse_line;
    use sermon_security::{Parser, ParserConfig};

    use super::*;

    fn fixture_events() -> Vec<SecurityEvent> {
        let root = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../fixtures/logs");
        let mut parser = Parser::new(
            ParserConfig {
                sudo_commands: true,
            },
            None,
        );
        let mut events = Vec::new();
        for distro in ["debian-13", "ubuntu-24.04", "rockylinux-9", "rockylinux-8"] {
            for file in [
                "journal-security.jsonl",
                "journal-security-variants.jsonl",
                "journal-oom.jsonl",
            ] {
                for raw in std::fs::read(root.join(distro).join(file))
                    .unwrap()
                    .split(|b| *b == b'\n')
                    .filter(|l| !l.is_empty())
                {
                    parser.parse(&line(&parse_line(raw).unwrap()), &mut events);
                }
            }
        }
        // sshd with UseDNS logs host names (none in the captures).
        events.push(SecurityEvent {
            at_us: 1_795_000_000_000_000,
            kind: EventKind::SshInvalidUser {
                user: "admin".into(),
                remote: Remote {
                    address: Address::Host("client-7.example.org".into()),
                    port: Some(2222),
                },
            },
        });
        // A reboot with an unknown uptime.
        events.push(SecurityEvent {
            at_us: 1_795_000_000_000_000,
            kind: EventKind::SystemReboot {
                previous_boot: "cc8f81a486f84386b00bc31030b4e1bb".into(),
                uptime_before_us: None,
            },
        });
        events
    }

    fn batch() -> proto::SecurityEventBatch {
        proto::SecurityEventBatch {
            events: fixture_events().iter().map(to_proto).collect(),
        }
    }

    #[test]
    fn every_kind_converts_and_roundtrips() {
        let batch = batch();
        let mut kinds: Vec<&str> = batch
            .events
            .iter()
            .map(|e| match e.event.as_ref().unwrap() {
                proto::security_event::Event::SshLoginFailed(_) => "ssh_login_failed",
                proto::security_event::Event::SshInvalidUser(_) => "ssh_invalid_user",
                proto::security_event::Event::SshLoginSucceeded(_) => "ssh_login_succeeded",
                proto::security_event::Event::SshPreauthDisconnect(_) => "ssh_preauth_disconnect",
                proto::security_event::Event::SudoAuthFailed(_) => "sudo_auth_failed",
                proto::security_event::Event::SudoNotAllowed(_) => "sudo_not_allowed",
                proto::security_event::Event::SudoCommand(_) => "sudo_command",
                proto::security_event::Event::AccountCreated(_) => "account_created",
                proto::security_event::Event::AccountDeleted(_) => "account_deleted",
                proto::security_event::Event::SystemReboot(_) => "system_reboot",
                proto::security_event::Event::OomKill(_) => "oom_kill",
            })
            .collect();
        kinds.sort_unstable();
        kinds.dedup();
        assert_eq!(kinds.len(), 11, "{kinds:?}");
        let decoded = proto::SecurityEventBatch::decode(batch.encode_to_vec().as_slice()).unwrap();
        assert_eq!(decoded, batch);
        let first = &batch.events[0];
        assert!(first.occurred_at.unwrap().seconds > 1_700_000_000);
    }

    #[test]
    fn times_keep_their_microseconds() {
        assert_eq!(
            timestamp(1_795_163_620_776_631),
            prost_types::Timestamp {
                seconds: 1_795_163_620,
                nanos: 776_631_000
            }
        );
    }

    // The server validates what the agent sends (protovalidate, Go): the encoded batch of every
    // fixture event is checked there (server/internal/transport/agentapi/schema_test.go).
    // SERMON_UPDATE_GOLDEN=1 rewrites the file after a deliberate change.
    #[test]
    fn golden_batch_for_the_server_schema_test() {
        let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
            .join("../../../proto/testdata/security_events.binpb");
        let encoded = batch().encode_to_vec();
        if std::env::var_os("SERMON_UPDATE_GOLDEN").is_some() {
            std::fs::create_dir_all(path.parent().unwrap()).unwrap();
            std::fs::write(&path, &encoded).unwrap();
        }
        let golden = std::fs::read(&path).unwrap_or_default();
        assert!(
            golden == encoded,
            "{} is stale – run with SERMON_UPDATE_GOLDEN=1",
            path.display()
        );
    }

    #[test]
    fn the_boot_of_the_stored_cursor() {
        assert_eq!(
            last_boot(Some("s=a;i=1;b=cc8f;m=10;t=1;x=2")),
            Some(("cc8f".into(), Some(16)))
        );
        assert_eq!(last_boot(None), None);
    }
}
