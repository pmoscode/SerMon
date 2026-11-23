//! Security events from log lines (KONZEPT §11, M2-06): SSH logins, sudo, accounts, OOM kills and
//! reboots, parsed in the agent so they arrive even when log shipping is off.
//!
//! - **Who sent the line counts.** Any local user can write `logger -t sshd "Failed password for
//!   root from …"`. Lines of sshd, useradd and userdel are believed only from `_UID` 0 (from the
//!   socket credentials). sudo runs setuid with the caller's UID, so its lines also count with a
//!   non-zero `_CAP_EFFECTIVE` – which journald reads from `/proc` afterwards, a race a user can
//!   win by exec'ing a setuid program (ADR 0023). OOM kills only from the kernel transport.
//! - **The attacker controls user names.** Addresses and ports are taken from the end of the line,
//!   so a user named `x from 192.0.2.66 port 22` cannot spoof the source. sshd escapes non-ASCII
//!   user names as octal (`j\303\274rgen`); they are decoded. Fields are cut to fixed lengths.
//! - Patterns use the `regex` crate: linear time on any input.
//! - Raw lines never become event fields; the sudo command (off by default) is redacted.

pub mod redact;

use std::net::IpAddr;
use std::sync::LazyLock;

use regex::Regex;

pub use redact::{Redactor, RuleSpec, redact};

/// Longest user name kept (user names in failed logins are attacker-controlled).
pub const MAX_USER_BYTES: usize = 128;
/// Longest host name kept.
pub const MAX_HOST_BYTES: usize = 253;
/// Longest sudo command kept.
pub const MAX_COMMAND_BYTES: usize = 1024;
/// Longest terminal, key type or fingerprint kept.
pub const MAX_SHORT_BYTES: usize = 128;

/// One log line with the fields the parsers look at.
#[derive(Debug, Clone, Copy)]
pub struct LogLine<'a> {
    /// Time of the line in microseconds since the epoch.
    pub at_us: u64,
    /// `SYSLOG_IDENTIFIER`, e.g. `sshd-session`.
    pub identifier: Option<&'a str>,
    /// `_TRANSPORT`: `kernel` for kernel messages.
    pub transport: Option<&'a str>,
    /// `_UID` of the sender.
    pub uid: Option<u32>,
    /// `_CAP_EFFECTIVE` of the sender.
    pub cap_effective: Option<u64>,
    /// `_BOOT_ID`.
    pub boot_id: Option<&'a str>,
    /// Microseconds since the boot.
    pub monotonic_us: Option<u64>,
    /// Where the line comes from; it decides how far its sender is known.
    pub source: Source,
    /// The message.
    pub message: &'a str,
}

/// Where a line comes from (ADR 0023, ADR 0025).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Source {
    /// The systemd journal: `_UID` and `_CAP_EFFECTIVE` are set by journald.
    Journal,
    /// A classic log file: `uid` only with the SerMon rsyslog template, otherwise unknown.
    File,
}

impl LogLine<'_> {
    /// Root, or – for setuid sudo only – a process with capabilities. `_UID` comes from the
    /// socket credentials; `_CAP_EFFECTIVE` is read from /proc after the line arrived, so a user
    /// who execs a setuid program right after sending shows capabilities too (ADR 0023, M2-28).
    fn privileged(&self, setuid: bool) -> bool {
        self.uid == Some(0) || (setuid && self.cap_effective.is_some_and(|caps| caps != 0))
    }

    /// Whether the line may yield events, and whether they count as verified (ADR 0023, 0025).
    /// `setuid` marks sudo, whose lines carry the UID of the calling user.
    fn trust(&self, setuid: bool) -> Option<bool> {
        let kernel = self.transport == Some("kernel");
        match self.source {
            // journald sets the transport; for everything else the sender must be privileged.
            Source::Journal => (kernel || self.privileged(setuid)).then_some(true),
            Source::File => match self.uid {
                // Classic syslog: anyone could have written it.
                None => Some(false),
                Some(0) => Some(true),
                // rsyslog saw a normal user: forged, but real sudo looks the same.
                Some(_) => (setuid && !kernel).then_some(false),
            },
        }
    }
}

/// A security event.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct SecurityEvent {
    /// When it happened (from the log line), microseconds since the epoch.
    pub at_us: u64,
    /// What happened.
    pub kind: EventKind,
    /// The sender is known to be privileged (ADR 0023); `false` for lines of classic log files
    /// whose sender anyone could be (ADR 0025).
    pub verified: bool,
}

/// The events (names as in KONZEPT §11).
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum EventKind {
    /// `ssh.login.failed`
    SshLoginFailed {
        /// The user name tried.
        user: String,
        /// Where from.
        remote: Remote,
        /// How.
        method: AuthMethod,
        /// The user does not exist.
        invalid_user: bool,
    },
    /// `ssh.login.invalid_user`
    SshInvalidUser {
        /// The user name tried.
        user: String,
        /// Where from.
        remote: Remote,
    },
    /// `ssh.login.success`
    SshLoginSucceeded {
        /// The user.
        user: String,
        /// Where from.
        remote: Remote,
        /// How.
        method: AuthMethod,
        /// The public key, for `publickey`.
        key: Option<PublicKey>,
    },
    /// `ssh.preauth.disconnect`: the connection ended before a login.
    SshPreauthDisconnect {
        /// The user name, if one was tried.
        user: Option<String>,
        /// Where from.
        remote: Remote,
        /// The user does not exist.
        invalid_user: bool,
    },
    /// `sudo.auth.failed`
    SudoAuthFailed {
        /// Whose password failed.
        user: String,
        /// The terminal, if any.
        tty: Option<String>,
    },
    /// `sudo.not_allowed`: not in sudoers, or the command is not allowed.
    SudoNotAllowed {
        /// Who.
        user: String,
        /// As whom.
        target: String,
        /// The terminal, if any.
        tty: Option<String>,
    },
    /// `sudo.command` (only with [`ParserConfig::sudo_commands`]).
    SudoCommand {
        /// Who.
        user: String,
        /// As whom.
        target: String,
        /// The terminal, if any.
        tty: Option<String>,
        /// The command, redacted.
        command: String,
    },
    /// `account.created`
    AccountCreated {
        /// The new user.
        user: String,
        /// Its UID.
        uid: Option<u32>,
    },
    /// `account.deleted`
    AccountDeleted {
        /// The removed user.
        user: String,
    },
    /// `system.reboot`: the boot ID changed.
    SystemReboot {
        /// The boot before.
        previous_boot: String,
        /// How long the previous boot ran, as far as its last line tells.
        uptime_before_us: Option<u64>,
    },
    /// `system.oom_kill`
    OomKill {
        /// The killed process.
        pid: u32,
        /// Its name (at most 15 characters, chosen by whoever started it).
        process: String,
    },
}

impl EventKind {
    /// The event's name, e.g. `ssh.login.failed`.
    #[must_use]
    pub fn name(&self) -> &'static str {
        match self {
            Self::SshLoginFailed { .. } => "ssh.login.failed",
            Self::SshInvalidUser { .. } => "ssh.login.invalid_user",
            Self::SshLoginSucceeded { .. } => "ssh.login.success",
            Self::SshPreauthDisconnect { .. } => "ssh.preauth.disconnect",
            Self::SudoAuthFailed { .. } => "sudo.auth.failed",
            Self::SudoNotAllowed { .. } => "sudo.not_allowed",
            Self::SudoCommand { .. } => "sudo.command",
            Self::AccountCreated { .. } => "account.created",
            Self::AccountDeleted { .. } => "account.deleted",
            Self::SystemReboot { .. } => "system.reboot",
            Self::OomKill { .. } => "system.oom_kill",
        }
    }
}

/// The other end of an SSH connection.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Remote {
    /// The address as logged.
    pub address: Address,
    /// The port; unknown in some PAM messages.
    pub port: Option<u16>,
}

/// An IP address, or a host name where sshd resolved it (`UseDNS yes`).
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Address {
    /// An IP address (IPv4-mapped IPv6 as IPv4).
    Ip(IpAddr),
    /// A host name.
    Host(String),
}

/// How an SSH login was tried.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum AuthMethod {
    /// `password`
    Password,
    /// `publickey`
    PublicKey,
    /// `keyboard-interactive` (usually PAM)
    KeyboardInteractive,
    /// `hostbased`
    HostBased,
    /// `gssapi-*`
    Gssapi,
    /// `none`
    None,
    /// Anything else.
    Other,
}

impl AuthMethod {
    fn parse(method: &str) -> Self {
        match method {
            "password" => Self::Password,
            "publickey" => Self::PublicKey,
            "hostbased" => Self::HostBased,
            "none" => Self::None,
            m if m.starts_with("keyboard-interactive") => Self::KeyboardInteractive,
            m if m.starts_with("gssapi") => Self::Gssapi,
            _ => Self::Other,
        }
    }

    /// The method's name as sshd writes it (without `/pam`).
    #[must_use]
    pub fn name(self) -> &'static str {
        match self {
            Self::Password => "password",
            Self::PublicKey => "publickey",
            Self::KeyboardInteractive => "keyboard-interactive",
            Self::HostBased => "hostbased",
            Self::Gssapi => "gssapi",
            Self::None => "none",
            Self::Other => "other",
        }
    }
}

/// The key of a public-key login.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PublicKey {
    /// E.g. `ED25519`.
    pub key_type: String,
    /// E.g. `SHA256:…`.
    pub fingerprint: String,
}

/// What the parsers report beyond the defaults.
#[derive(Debug, Clone, Copy, Default)]
pub struct ParserConfig {
    /// Report every sudo command (`sudo.command`, redacted); off by default (KONZEPT §11).
    pub sudo_commands: bool,
}

/// Turns log lines into security events. Stateful for reboots (it remembers the boot of the
/// previous line) and for sudo: once a verified PAM failure was seen, sudo's own summary
/// (`N incorrect password attempts`) no longer counts, it repeats the PAM lines.
#[derive(Debug)]
pub struct Parser {
    config: ParserConfig,
    redactor: Redactor,
    boot: Option<(String, Option<u64>)>,
    sudo_pam: bool,
}

impl Parser {
    /// A parser; `last_boot` is the boot (and its monotonic time) of the last line seen before,
    /// e.g. from the stored journal cursor, so a reboot while the agent was stopped is noticed.
    #[must_use]
    pub fn new(config: ParserConfig, last_boot: Option<(String, Option<u64>)>) -> Self {
        Self {
            config,
            redactor: Redactor::builtin(),
            boot: last_boot,
            sudo_pam: false,
        }
    }

    /// Redacts sudo commands with `redactor` (the rules of the organization, M3-02) instead of
    /// only the built-in patterns.
    pub fn set_redactor(&mut self, redactor: Redactor) {
        self.redactor = redactor;
    }

    /// Appends the events of `line` to `out`.
    pub fn parse(&mut self, line: &LogLine<'_>, out: &mut Vec<SecurityEvent>) {
        self.reboot(line, out);
        let kernel = line.transport == Some("kernel");
        let sudo_line = !kernel && line.identifier == Some("sudo");
        let Some(verified) = line.trust(sudo_line) else {
            return;
        };
        let kind = match line.identifier {
            _ if kernel => oom(line.message),
            Some("sshd" | "sshd-session" | "sshd-auth") => ssh(line.message),
            Some("sudo") => {
                let kind = sudo(line.message, self.config, &self.redactor, self.sudo_pam);
                if verified && matches!(kind, Some((_, Pam::Yes))) {
                    self.sudo_pam = true;
                }
                kind.map(|(kind, _)| kind)
            }
            Some("useradd") => account_created(line.message),
            Some("userdel") => account_deleted(line.message),
            _ => None,
        };
        if let Some(kind) = kind {
            out.push(SecurityEvent {
                at_us: line.at_us,
                kind,
                verified,
            });
        }
    }

    fn reboot(&mut self, line: &LogLine<'_>, out: &mut Vec<SecurityEvent>) {
        let Some(boot) = line.boot_id.filter(|b| !b.is_empty()) else {
            return;
        };
        match &mut self.boot {
            Some((previous, monotonic)) if previous == boot => {
                *monotonic = line.monotonic_us.or(*monotonic);
            }
            last => {
                if let Some((previous, uptime)) = last.take() {
                    // Boot IDs come from journald only.
                    out.push(SecurityEvent {
                        at_us: line.at_us,
                        kind: EventKind::SystemReboot {
                            previous_boot: cut(previous, MAX_SHORT_BYTES),
                            uptime_before_us: uptime,
                        },
                        verified: true,
                    });
                }
                *last = Some((boot.to_owned(), line.monotonic_us));
            }
        }
    }
}

fn regex(pattern: &str) -> Regex {
    #[allow(
        clippy::expect_used,
        reason = "constant patterns, covered by the tests"
    )]
    Regex::new(pattern).expect("valid pattern")
}

// The user part is greedy and the address comes last: the real address is the last one.
static FAILED: LazyLock<Regex> = LazyLock::new(|| {
    regex(
        r"^Failed (?P<method>\S+) for (?P<invalid>invalid user )?(?P<user>.*) from (?P<host>\S+) port (?P<port>\d+)(?: ssh2)?(?:: .*)?$",
    )
});
static ACCEPTED: LazyLock<Regex> = LazyLock::new(|| {
    regex(
        r"^Accepted (?P<method>\S+) for (?P<user>.*) from (?P<host>\S+) port (?P<port>\d+)(?: ssh2)?(?:: (?P<keytype>\S+) (?P<fingerprint>\S+))?$",
    )
});
static INVALID: LazyLock<Regex> =
    LazyLock::new(|| regex(r"^Invalid user (?P<user>.*) from (?P<host>\S+) port (?P<port>\d+)$"));
static PREAUTH: LazyLock<Regex> = LazyLock::new(|| {
    regex(
        r"^(?:Connection (?:closed|reset) by|Disconnected from)(?: (?P<kind>authenticating|invalid) user (?P<user>.*))? (?P<host>\S+) port (?P<port>\d+) \[preauth\]$",
    )
});
static PAM_FAILURE: LazyLock<Regex> = LazyLock::new(|| {
    regex(
        r"^error: PAM: Authentication failure for (?P<invalid>(?:illegal|invalid) user )?(?P<user>.*) from (?P<host>\S+)$",
    )
});

fn ssh(message: &str) -> Option<EventKind> {
    if let Some(c) = FAILED.captures(message) {
        return Some(EventKind::SshLoginFailed {
            user: user(&c["user"], 1),
            remote: remote(&c["host"], Some(&c["port"]))?,
            method: AuthMethod::parse(&c["method"]),
            invalid_user: c.name("invalid").is_some(),
        });
    }
    if let Some(c) = ACCEPTED.captures(message) {
        let method = AuthMethod::parse(&c["method"]);
        let key = match (c.name("keytype"), c.name("fingerprint")) {
            (Some(key_type), Some(fingerprint)) if method == AuthMethod::PublicKey => {
                Some(PublicKey {
                    key_type: cut(key_type.as_str().to_owned(), MAX_SHORT_BYTES),
                    fingerprint: cut(fingerprint.as_str().to_owned(), MAX_SHORT_BYTES),
                })
            }
            _ => None,
        };
        return Some(EventKind::SshLoginSucceeded {
            user: user(&c["user"], 1),
            remote: remote(&c["host"], Some(&c["port"]))?,
            method,
            key,
        });
    }
    if let Some(c) = INVALID.captures(message) {
        return Some(EventKind::SshInvalidUser {
            user: user(&c["user"], 1),
            remote: remote(&c["host"], Some(&c["port"]))?,
        });
    }
    if let Some(c) = PREAUTH.captures(message) {
        // sshd escapes the user name of this message twice.
        return Some(EventKind::SshPreauthDisconnect {
            user: c.name("user").map(|u| user(u.as_str(), 2)),
            remote: remote(&c["host"], Some(&c["port"]))?,
            invalid_user: c.name("kind").is_some_and(|k| k.as_str() == "invalid"),
        });
    }
    if let Some(c) = PAM_FAILURE.captures(message) {
        // Logged instead of "Failed keyboard-interactive/pam" (Debian 13, Ubuntu 24.04, Rocky 8/9).
        return Some(EventKind::SshLoginFailed {
            user: user(&c["user"], 1),
            remote: remote(&c["host"], None)?,
            method: AuthMethod::KeyboardInteractive,
            invalid_user: c.name("invalid").is_some(),
        });
    }
    None
}

static SUDO_AUTH: LazyLock<Regex> = LazyLock::new(|| {
    regex(
        r"^pam_unix\(sudo(?:-i)?:auth\): authentication failure;.* tty=(?P<tty>\S*) .* user=(?P<user>\S+)\s*$",
    )
});
static SUDO_ATTEMPTS: LazyLock<Regex> = LazyLock::new(|| {
    regex(
        r"^\s*(?P<user>\S+) : \d+ incorrect password attempts? ; (?:TTY=(?P<tty>\S+) ; )?PWD=.*? ; USER=\S+ ; COMMAND=",
    )
});
static SUDO_DENIED: LazyLock<Regex> = LazyLock::new(|| {
    regex(
        r"^\s*(?P<user>\S+) : (?:user NOT in sudoers|user NOT authorized on host|command not allowed) ; (?:TTY=(?P<tty>\S+) ; )?PWD=.*? ; USER=(?P<target>\S+) ; COMMAND=",
    )
});
static SUDO_COMMAND: LazyLock<Regex> = LazyLock::new(|| {
    regex(
        r"^\s*(?P<user>\S+) : (?:TTY=(?P<tty>\S+) ; )?PWD=.*? ; USER=(?P<target>\S+) ; (?:(?:GROUP|TSID|ENV)=.*? ; )*COMMAND=(?P<command>.*)$",
    )
});

/// Whether a sudo event came from a PAM line.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Pam {
    Yes,
    No,
}

/// `pam_seen`: a verified PAM failure was seen, so sudo's summary repeats what counted already.
/// The target user of a sudo line; `?` when the line names more than one – the working directory
/// before it is the user's choice, and sudo doesn't escape " ; " in it.
fn sudo_target(message: &str, target: &str) -> String {
    if message.matches(" ; USER=").count() > 1 {
        return "?".to_owned();
    }
    cut(target.to_owned(), MAX_USER_BYTES)
}

fn sudo(
    message: &str,
    config: ParserConfig,
    redactor: &Redactor,
    pam_seen: bool,
) -> Option<(EventKind, Pam)> {
    if let Some(c) = SUDO_AUTH.captures(message) {
        let kind = EventKind::SudoAuthFailed {
            user: cut(c["user"].to_owned(), MAX_USER_BYTES),
            tty: tty(c.name("tty").map(|t| t.as_str())),
        };
        return Some((kind, Pam::Yes));
    }
    if let Some(c) = SUDO_ATTEMPTS.captures(message) {
        // sudo without PAM (Alpine) logs only this; with PAM it follows the pam_unix lines.
        let kind = EventKind::SudoAuthFailed {
            user: cut(c["user"].to_owned(), MAX_USER_BYTES),
            tty: tty(c.name("tty").map(|t| t.as_str())),
        };
        return (!pam_seen).then_some((kind, Pam::No));
    }
    if let Some(c) = SUDO_DENIED.captures(message) {
        let kind = EventKind::SudoNotAllowed {
            user: cut(c["user"].to_owned(), MAX_USER_BYTES),
            target: sudo_target(message, &c["target"]),
            tty: tty(c.name("tty").map(|t| t.as_str())),
        };
        return Some((kind, Pam::No));
    }
    if config.sudo_commands
        && let Some(c) = SUDO_COMMAND.captures(message)
    {
        let kind = EventKind::SudoCommand {
            user: cut(c["user"].to_owned(), MAX_USER_BYTES),
            target: sudo_target(message, &c["target"]),
            tty: tty(c.name("tty").map(|t| t.as_str())),
            command: cut(
                redactor.redact(&c["command"]).into_owned(),
                MAX_COMMAND_BYTES,
            ),
        };
        return Some((kind, Pam::No));
    }
    None
}

static USERADD: LazyLock<Regex> =
    LazyLock::new(|| regex(r"^new user: name=(?P<user>[^,]+), UID=(?P<uid>\d+),"));
static USERDEL: LazyLock<Regex> = LazyLock::new(|| regex(r"^delete user '(?P<user>[^']+)'$"));

fn account_created(message: &str) -> Option<EventKind> {
    let c = USERADD.captures(message)?;
    Some(EventKind::AccountCreated {
        user: cut(c["user"].to_owned(), MAX_USER_BYTES),
        uid: c["uid"].parse().ok(),
    })
}

fn account_deleted(message: &str) -> Option<EventKind> {
    let c = USERDEL.captures(message)?;
    Some(EventKind::AccountDeleted {
        user: cut(c["user"].to_owned(), MAX_USER_BYTES),
    })
}

// The process name is at most 15 characters and may contain ") total-vm:" itself; the longest
// name that still leaves the kernel's own ") total-vm:" behind is the real one.
static OOM: LazyLock<Regex> = LazyLock::new(|| {
    regex(
        r"^(?:(?:Memory cgroup )?[Oo]ut of memory: )?Killed process (?P<pid>\d+) \((?P<name>.{0,15})\) total-vm:",
    )
});

fn oom(message: &str) -> Option<EventKind> {
    let c = OOM.captures(message)?;
    Some(EventKind::OomKill {
        pid: c["pid"].parse().ok()?,
        process: c["name"].to_owned(),
    })
}

fn tty(tty: Option<&str>) -> Option<String> {
    tty.filter(|t| !t.is_empty() && *t != "unknown")
        .map(|t| cut(t.to_owned(), MAX_SHORT_BYTES))
}

fn remote(host: &str, port: Option<&str>) -> Option<Remote> {
    let port = match port {
        Some(port) => Some(port.parse::<u16>().ok()?),
        None => None,
    };
    let unscoped = host.split_once('%').map_or(host, |(ip, _)| ip);
    let address = if let Ok(ip) = unscoped.parse::<IpAddr>() {
        Address::Ip(ip.to_canonical())
    } else if host.len() <= MAX_HOST_BYTES
        && host
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'.' || b == b'-')
        && host.bytes().any(|b| b.is_ascii_alphabetic())
    {
        Address::Host(host.to_ascii_lowercase())
    } else {
        return None;
    };
    Some(Remote { address, port })
}

/// A user name as sshd logs it: octal escapes decoded (`passes` times), cut to the limit.
fn user(logged: &str, passes: usize) -> String {
    let mut text = logged.to_owned();
    for _ in 0..passes {
        if !text.contains('\\') {
            break;
        }
        text = unescape(&text);
    }
    cut(text, MAX_USER_BYTES)
}

/// Decodes `\ooo` (octal byte) and `\\` as written by OpenSSH's `strnvis`.
fn unescape(text: &str) -> String {
    let bytes = text.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'\\' {
            let octal = bytes
                .get(i + 1..i + 4)
                .filter(|d| d.iter().all(|b| (b'0'..=b'7').contains(b)));
            if let Some(digits) = octal {
                let value = digits.iter().fold(0u16, |v, d| v * 8 + u16::from(d - b'0'));
                if let Ok(byte) = u8::try_from(value) {
                    out.push(byte);
                    i += 4;
                    continue;
                }
            }
            if bytes.get(i + 1) == Some(&b'\\') {
                out.push(b'\\');
                i += 2;
                continue;
            }
        }
        out.push(bytes[i]);
        i += 1;
    }
    String::from_utf8_lossy(&out).into_owned()
}

/// Cuts `text` to at most `max` bytes at a character boundary.
fn cut(mut text: String, max: usize) -> String {
    if text.len() > max {
        let mut end = max;
        while !text.is_char_boundary(end) {
            end -= 1;
        }
        text.truncate(end);
    }
    text
}

#[cfg(test)]
mod tests {
    use proptest::prelude::*;

    use super::*;

    fn sshd(message: &str) -> LogLine<'_> {
        LogLine {
            at_us: 1,
            identifier: Some("sshd-session"),
            transport: Some("syslog"),
            uid: Some(0),
            cap_effective: Some(0x1ff_ffff_ffff),
            boot_id: None,
            monotonic_us: None,
            source: Source::Journal,
            message,
        }
    }

    fn parse_events(line: &LogLine<'_>) -> Vec<SecurityEvent> {
        let mut out = Vec::new();
        Parser::new(
            ParserConfig {
                sudo_commands: true,
            },
            None,
        )
        .parse(line, &mut out);
        out
    }

    // Classic log files (ADR 0025): without a sender every line counts, but unverified; with the
    // UID from rsyslog root is verified, another UID forged – except for setuid sudo.
    #[test]
    fn file_lines_are_unverified_unless_root_sent_them() {
        let failed = "Failed password for root from 203.0.113.9 port 22 ssh2";
        let sudo = "pam_unix(sudo:auth): authentication failure; logname= uid=1000 euid=0 tty=/dev/pts/1 ruser=alice rhost=  user=alice";
        let oom = "Out of memory: Killed process 42 (stress) total-vm:1kB";
        let file = |identifier, uid, message| LogLine {
            identifier: Some(identifier),
            transport: (identifier == "kernel").then_some("kernel"),
            uid,
            cap_effective: None,
            source: Source::File,
            ..sshd(message)
        };
        let verdict = |line: &LogLine<'_>| -> Option<bool> {
            let events = parse_events(line);
            assert!(events.len() <= 1, "{events:?}");
            events.first().map(|e| e.verified)
        };
        for (identifier, message) in [("sshd", failed), ("sudo", sudo), ("kernel", oom)] {
            assert_eq!(
                verdict(&file(identifier, None, message)),
                Some(false),
                "{identifier} without sender"
            );
            assert_eq!(
                verdict(&file(identifier, Some(0), message)),
                Some(true),
                "{identifier} from root"
            );
        }
        assert_eq!(
            verdict(&file("sshd", Some(1000), failed)),
            None,
            "forged sshd"
        );
        assert_eq!(
            verdict(&file(
                "useradd",
                Some(1000),
                "new user: name=backdoor, UID=0, GID=0, home=/root, shell=/bin/sh"
            )),
            None
        );
        assert_eq!(
            verdict(&file("sudo", Some(1000), sudo)),
            Some(false),
            "setuid sudo"
        );
        assert_eq!(
            verdict(&file("kernel", Some(1000), oom)),
            None,
            "kernel lines from a user"
        );
        // The journal: verified, as before.
        assert_eq!(verdict(&sshd(failed)), Some(true));
    }

    // sudo without PAM (Alpine) logs only its summary; with PAM the pam_unix line counts and the
    // summary after it must not count again. Only a verified PAM line switches that.
    #[test]
    fn sudo_summaries_count_only_without_pam() {
        let summary = "   alice : 1 incorrect password attempt ; TTY=pts/0 ; PWD=/ ; USER=root ; COMMAND=/bin/true";
        let pam = "pam_unix(sudo:auth): authentication failure; logname= uid=1000 euid=0 tty=/dev/pts/1 ruser=alice rhost=  user=alice";
        let file = |message, uid| LogLine {
            identifier: Some("sudo"),
            uid,
            cap_effective: None,
            source: Source::File,
            ..sshd(message)
        };
        let journal = |message| LogLine {
            identifier: Some("sudo"),
            uid: Some(1000),
            ..sshd(message)
        };
        let run = |lines: &[LogLine<'_>]| -> usize {
            let mut parser = Parser::new(ParserConfig::default(), None);
            let mut out = Vec::new();
            for line in lines {
                parser.parse(line, &mut out);
            }
            out.iter()
                .filter(|e| matches!(e.kind, EventKind::SudoAuthFailed { .. }))
                .count()
        };
        assert_eq!(run(&[file(summary, None)]), 1, "busybox file");
        assert_eq!(
            parse(&file(summary, None)),
            [EventKind::SudoAuthFailed {
                user: "alice".into(),
                tty: Some("pts/0".into())
            }]
        );
        assert_eq!(run(&[journal(pam), journal(summary)]), 1, "PAM: once");
        assert_eq!(run(&[journal(pam), journal(summary), journal(summary)]), 1);
        // A forged PAM line in a file must not hide the summaries of later failures.
        assert_eq!(run(&[file(pam, None), file(summary, None)]), 2);
        assert_eq!(run(&[file(pam, Some(1000)), file(summary, Some(1000))]), 2);
    }

    fn parse(line: &LogLine<'_>) -> Vec<EventKind> {
        let mut out = Vec::new();
        Parser::new(
            ParserConfig {
                sudo_commands: true,
            },
            None,
        )
        .parse(line, &mut out);
        out.into_iter().map(|e| e.kind).collect()
    }

    fn ip(s: &str) -> Address {
        Address::Ip(s.parse().unwrap())
    }

    // The attacker picks the user name; the address is the one sshd appended last.
    #[test]
    fn user_names_cannot_spoof_the_source() {
        for message in [
            "Failed password for invalid user root from 6.6.6.6 port 22 from 192.0.2.7 port 4711 ssh2",
            "Failed password for invalid user a from 6.6.6.6 port 22 ssh2: x from 192.0.2.7 port 4711 ssh2",
            "Invalid user root from 6.6.6.6 port 22 from 192.0.2.7 port 4711",
            "Connection closed by invalid user root 6.6.6.6 port 22 192.0.2.7 port 4711 [preauth]",
        ] {
            let events = parse(&sshd(message));
            let remote = match events.as_slice() {
                [
                    EventKind::SshLoginFailed { remote, .. }
                    | EventKind::SshInvalidUser { remote, .. }
                    | EventKind::SshPreauthDisconnect { remote, .. },
                ] => remote,
                other => panic!("{message}: {other:?}"),
            };
            assert_eq!(remote.address, ip("192.0.2.7"), "{message}");
            assert_eq!(remote.port, Some(4711), "{message}");
        }
    }

    // `logger -t sshd …` by a normal user: uid 1000 without capabilities (or none known).
    #[test]
    fn forged_senders_are_ignored() {
        let message = "Failed password for root from 203.0.113.9 port 22 ssh2";
        for (uid, caps) in [(Some(1000), Some(0)), (Some(1000), None), (None, None)] {
            let line = LogLine {
                uid,
                cap_effective: caps,
                ..sshd(message)
            };
            assert!(parse(&line).is_empty(), "uid {uid:?}, caps {caps:?}");
        }
        for identifier in ["sudo", "useradd", "userdel"] {
            let line = LogLine {
                identifier: Some(identifier),
                uid: Some(1000),
                cap_effective: Some(0),
                ..sshd("delete user 'root'")
            };
            assert!(parse(&line).is_empty(), "{identifier}");
        }
        // journald reads _CAP_EFFECTIVE from /proc after the line arrived: a user who sends a line
        // and execs a setuid program at once (passwd) shows capabilities under its own UID. Only
        // sudo, which logs with the caller's UID, may come that way (ADR 0023, M2-28).
        for identifier in ["sshd", "sshd-session", "sshd-auth", "useradd", "userdel"] {
            let line = LogLine {
                identifier: Some(identifier),
                uid: Some(1000),
                cap_effective: Some(0x1ff_ffff_ffff),
                ..sshd(message)
            };
            assert!(parse(&line).is_empty(), "{identifier} with capabilities");
            let line = LogLine {
                identifier: Some(identifier),
                uid: Some(1000),
                cap_effective: Some(0x1ff_ffff_ffff),
                ..sshd(
                    "new user: name=eve, UID=1001, GID=1001, home=/home/eve, shell=/bin/sh, from=/dev/pts/0",
                )
            };
            assert!(parse(&line).is_empty(), "{identifier} with capabilities");
        }
        let fake_kernel = LogLine {
            identifier: Some("kernel"),
            ..sshd("Out of memory: Killed process 1 (systemd) total-vm:1kB")
        };
        assert!(
            parse(&fake_kernel).is_empty(),
            "kernel lines only from the kernel transport"
        );
        // sudo is setuid: the user's UID, but capabilities.
        let sudo = LogLine {
            identifier: Some("sudo"),
            uid: Some(1000),
            ..sshd(
                "pam_unix(sudo:auth): authentication failure; logname= uid=1000 euid=0 tty=/dev/pts/1 ruser=alice rhost=  user=alice",
            )
        };
        assert_eq!(
            parse(&sudo),
            [EventKind::SudoAuthFailed {
                user: "alice".into(),
                tty: Some("/dev/pts/1".into())
            }]
        );
    }

    #[test]
    fn addresses_and_ports() {
        let remote = |message: &str| match parse(&sshd(message)).as_slice() {
            [EventKind::SshInvalidUser { remote, .. }] => Some(remote.clone()),
            _ => None,
        };
        assert_eq!(
            remote("Invalid user a from ::ffff:192.0.2.1 port 1")
                .unwrap()
                .address,
            ip("192.0.2.1")
        );
        assert_eq!(
            remote("Invalid user a from fe80::1%eth0 port 1")
                .unwrap()
                .address,
            ip("fe80::1")
        );
        assert_eq!(
            remote("Invalid user a from Host.Example.org port 1")
                .unwrap()
                .address,
            Address::Host("host.example.org".into())
        );
        assert_eq!(
            remote("Invalid user a from 192.0.2.1 port 65536"),
            None,
            "no such port"
        );
        assert_eq!(remote("Invalid user a from <script> port 22"), None);
        assert_eq!(
            remote("Invalid user a from 12345 port 22"),
            None,
            "a number is no host name"
        );
    }

    #[test]
    fn methods_keys_and_messages_that_are_no_event() {
        let events = parse(&sshd(
            "Accepted publickey for alice from 2001:db8::1 port 22 ssh2: RSA SHA256:abc",
        ));
        assert_eq!(
            events,
            [EventKind::SshLoginSucceeded {
                user: "alice".into(),
                remote: Remote {
                    address: ip("2001:db8::1"),
                    port: Some(22)
                },
                method: AuthMethod::PublicKey,
                key: Some(PublicKey {
                    key_type: "RSA".into(),
                    fingerprint: "SHA256:abc".into()
                }),
            }]
        );
        let failed = parse(&sshd(
            "Failed publickey for alice from 192.0.2.1 port 22 ssh2: ED25519 SHA256:abc",
        ));
        assert!(matches!(
            failed.as_slice(),
            [EventKind::SshLoginFailed {
                method: AuthMethod::PublicKey,
                ..
            }]
        ));
        let none = parse(&sshd(
            "Failed none for invalid user admin from 192.0.2.1 port 22 ssh2",
        ));
        assert!(matches!(
            none.as_slice(),
            [EventKind::SshLoginFailed {
                method: AuthMethod::None,
                invalid_user: true,
                ..
            }]
        ));
        for message in [
            "Server listening on :: port 22.",
            "Received disconnect from 192.0.2.1 port 22:11: disconnected by user",
            "Disconnected from user alice 192.0.2.1 port 22",
            "pam_unix(sshd:session): session opened for user alice(uid=1000) by (uid=0)",
            "Connection closed by 192.0.2.1 port 22",
        ] {
            assert!(parse(&sshd(message)).is_empty(), "{message}");
        }
        let no_user = parse(&sshd("Connection closed by 192.0.2.1 port 22 [preauth]"));
        assert!(matches!(
            no_user.as_slice(),
            [EventKind::SshPreauthDisconnect { user: None, .. }]
        ));
    }

    #[test]
    fn user_names_are_decoded_and_cut() {
        assert_eq!(user(r"j\303\274rgen", 1), "jürgen");
        assert_eq!(user(r"j\\303\\274rgen", 2), "jürgen");
        assert_eq!(user(r"a\\b", 1), r"a\b");
        assert_eq!(user(r"\777x", 1), r"\777x", "not a byte");
        assert_eq!(user(r"\303", 1), "\u{fffd}", "a lone byte");
        let long = "ä".repeat(100);
        assert_eq!(user(&long, 1).len(), MAX_USER_BYTES);
    }

    #[test]
    fn oom_process_names_cannot_hide_the_kernel_text() {
        let kernel = |message| LogLine {
            identifier: Some("kernel"),
            transport: Some("kernel"),
            uid: None,
            cap_effective: None,
            ..sshd(message)
        };
        for (message, pid, name) in [
            (
                "Out of memory: Killed process 4242 (stress-ng) total-vm:1kB, anon-rss:1kB",
                4242,
                "stress-ng",
            ),
            (
                "Memory cgroup out of memory: Killed process 7 (a) total-vm:x) total-vm:1kB",
                7,
                "a) total-vm:x",
            ),
            (
                "Killed process 9 (old kernel) total-vm:1kB, anon-rss:0kB",
                9,
                "old kernel",
            ),
        ] {
            assert_eq!(
                parse(&kernel(message)),
                [EventKind::OomKill {
                    pid,
                    process: name.into()
                }],
                "{message}"
            );
        }
        for message in [
            "oom_reaper: reaped process 4242 (stress-ng), now anon-rss:0kB",
            "python3 invoked oom-killer: gfp_mask=0xcc0(GFP_KERNEL), order=0, oom_score_adj=0",
            "Out of memory: Kill process 1 (x) score 1 or sacrifice child",
        ] {
            assert!(parse(&kernel(message)).is_empty(), "{message}");
        }
    }

    #[test]
    fn a_new_boot_id_is_a_reboot() {
        let at = |boot, monotonic, at_us| LogLine {
            boot_id: Some(boot),
            monotonic_us: Some(monotonic),
            at_us,
            ..sshd("hello")
        };
        let mut parser = Parser::new(ParserConfig::default(), None);
        let mut out = Vec::new();
        parser.parse(&at("a", 10, 1), &mut out);
        parser.parse(&at("a", 500, 2), &mut out);
        assert!(out.is_empty(), "the first boot seen is no reboot");
        parser.parse(&at("b", 3, 3), &mut out);
        parser.parse(&at("b", 4, 4), &mut out);
        assert_eq!(
            out,
            [SecurityEvent {
                at_us: 3,
                kind: EventKind::SystemReboot {
                    previous_boot: "a".into(),
                    uptime_before_us: Some(500)
                },
                verified: true,
            }]
        );

        // A reboot while the agent was stopped: the boot of the stored cursor.
        let mut resumed = Parser::new(ParserConfig::default(), Some(("a".into(), Some(900))));
        let mut out = Vec::new();
        resumed.parse(&at("b", 1, 7), &mut out);
        assert_eq!(
            out[0].kind,
            EventKind::SystemReboot {
                previous_boot: "a".into(),
                uptime_before_us: Some(900)
            }
        );
    }

    #[test]
    fn sudo_lines() {
        let sudo = |message| LogLine {
            identifier: Some("sudo"),
            uid: Some(1000),
            ..sshd(message)
        };
        assert_eq!(
            parse(&sudo(
                "   bob : user NOT in sudoers ; TTY=pts/1 ; PWD=/home/bob ; USER=root ; COMMAND=/bin/sh"
            )),
            [EventKind::SudoNotAllowed {
                user: "bob".into(),
                target: "root".into(),
                tty: Some("pts/1".into())
            }]
        );
        assert_eq!(
            parse(&sudo(
                "  alice : TTY=pts/0 ; PWD=/ ; USER=root ; COMMAND=/usr/bin/mysql --password=hunter2 -e 'select 1'"
            )),
            [EventKind::SudoCommand {
                user: "alice".into(),
                target: "root".into(),
                tty: Some("pts/0".into()),
                command: "/usr/bin/mysql --password=*** -e 'select 1'".into()
            }]
        );
        // A failed attempt is no command; without a PAM line before it, it is the failure itself.
        assert_eq!(
            parse(&sudo(
                "   alice : 3 incorrect password attempts ; TTY=pts/0 ; PWD=/ ; USER=root ; COMMAND=/bin/true"
            )),
            [EventKind::SudoAuthFailed {
                user: "alice".into(),
                tty: Some("pts/0".into())
            }]
        );
        let mut off = Vec::new();
        Parser::new(ParserConfig::default(), None).parse(
            &sudo("  alice : TTY=pts/0 ; PWD=/ ; USER=root ; COMMAND=/bin/id"),
            &mut off,
        );
        assert!(off.is_empty(), "commands are off by default");
        // The rules of the organization apply to commands too (M3-02).
        let mut parser = Parser::new(
            ParserConfig {
                sudo_commands: true,
            },
            None,
        );
        parser.set_redactor(
            Redactor::with_rules(&[RuleSpec {
                pattern: r"--customer=(?P<secret>\d+)".into(),
            }])
            .unwrap(),
        );
        let mut ruled = Vec::new();
        parser.parse(
            &sudo("  alice : TTY=pts/0 ; PWD=/ ; USER=root ; COMMAND=/opt/export --customer=4711 --password=x"),
            &mut ruled,
        );
        assert!(
            matches!(ruled.as_slice(), [SecurityEvent { kind: EventKind::SudoCommand { command, .. }, .. }]
                if command == "/opt/export --customer=*** --password=***"),
            "{ruled:?}"
        );
        // The user chooses the working directory, and sudo doesn't escape " ; " in it: a second
        // "USER=" makes the target unknown instead of the one the user planted (M2-28).
        assert_eq!(
            parse(&sudo(
                "   bob : user NOT in sudoers ; TTY=pts/1 ; PWD=/tmp/x ; USER=nobody ; COMMAND=/bin/true ; /q ; USER=root ; COMMAND=/bin/bash"
            )),
            [EventKind::SudoNotAllowed {
                user: "bob".into(),
                target: "?".into(),
                tty: Some("pts/1".into())
            }]
        );
        assert!(matches!(
            parse(&sudo(
                "  alice : TTY=pts/0 ; PWD=/tmp/x ; USER=nobody ; COMMAND=/bin/true ; /q ; USER=root ; COMMAND=/bin/bash"
            ))
            .as_slice(),
            [EventKind::SudoCommand { target, .. }] if target == "?"
        ));
    }

    proptest! {
        #[test]
        fn never_panics(identifier in prop::sample::select(vec!["sshd", "sshd-session", "sudo", "useradd", "userdel", "kernel"]), message in "\\PC{0,200}") {
            let line = LogLine { identifier: Some(identifier), transport: Some(if identifier == "kernel" { "kernel" } else { "syslog" }), ..sshd(&message) };
            let _ = parse(&line);
        }

        #[test]
        fn never_panics_on_sshd_like_lines(user in "\\PC{0,40}", host in "[0-9a-f:.%a-z]{0,40}", port in "[0-9]{0,7}") {
            for message in [
                format!("Failed password for invalid user {user} from {host} port {port} ssh2"),
                format!("Connection closed by invalid user {user} {host} port {port} [preauth]"),
                format!("Accepted publickey for {user} from {host} port {port} ssh2: {user} {user}"),
            ] {
                let _ = parse(&sshd(&message));
            }
        }
    }
}
