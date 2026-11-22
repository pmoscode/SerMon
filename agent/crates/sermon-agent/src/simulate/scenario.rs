//! Scenarios of the simulator (M2-30): problems the virtual nodes play through on a fixed cycle,
//! so the default alert rules can be tried by hand (`just simulate N --scenario cpu-high`). Each
//! scenario names the rule it is made for; times count from the start of the simulator, so all
//! nodes go through the same phase at the same time.

use std::time::Duration;

use sermon_security::{LogLine, Parser, ParserConfig, SecurityEvent, Source};

/// A problem the virtual nodes play through.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, clap::ValueEnum)]
pub enum Scenario {
    /// CPU load over the threshold, then just under it but above the clear level (the alert holds
    /// at warning: hysteresis), then calm (`system.cpu.usage_high`).
    CpuHigh,
    /// The root file system fills up within [`FILL_TIME`] and stays full
    /// (`system.filesystem.full_soon`).
    DiskFilling,
    /// Failed SSH logins from one address on every node, as the log parser reports them
    /// (`security.ssh.bruteforce`, with three or more nodes `security.ssh.fleet_correlation`).
    SshBruteforce,
    /// The usage of the root file system goes back and forth across the threshold
    /// (`system.filesystem.usage_high` fires and resolves until it counts as flapping).
    Flapping,
    /// The node falls silent – no connection, the samples wait in its buffer – and comes back
    /// (`system.node.offline`).
    Offline,
}

const MINUTE: u64 = 60;

/// Length of one `cpu-high` cycle: [`CPU_OVER`] over the threshold, [`CPU_HOLD`] between
/// threshold and clear, the rest calm.
pub const CPU_CYCLE: Duration = Duration::from_secs(40 * MINUTE);
/// How long `cpu-high` keeps the CPU over the critical threshold.
pub const CPU_OVER: Duration = Duration::from_secs(15 * MINUTE);
/// How long `cpu-high` then stays between threshold and clear.
pub const CPU_HOLD: Duration = Duration::from_secs(10 * MINUTE);
/// Busy share over the threshold: 3 % idle, under the critical 5 %.
const CPU_BUSY_OVER: f64 = 0.97;
/// Busy share between threshold and clear: 17 % idle, above the warning 15 % and under the
/// clear 20 %.
const CPU_BUSY_HOLD: f64 = 0.83;
/// Most busy share in the calm phase: idle clearly above the clear level.
const CPU_BUSY_CALM: f64 = 0.5;

/// How long `disk-filling` takes from the node's fill level to a full disk. A full disk stays
/// full: a cleanup would bend the trend over the rule's lookback of 24 hours.
pub const FILL_TIME: Duration = Duration::from_secs(4 * 60 * MINUTE);

/// Length of one `flapping` cycle: [`FLAP_HIGH`] over the warning threshold, the rest under the
/// clear level.
pub const FLAP_CYCLE: Duration = Duration::from_secs(5 * MINUTE);
/// How long `flapping` stays over the threshold (longer than the rule's `for` of 2 minutes).
pub const FLAP_HIGH: Duration = Duration::from_secs(3 * MINUTE);
/// Usage over the warning threshold of 80 %.
const FLAP_OVER: f64 = 0.85;
/// Usage under the clear level of 75 %.
const FLAP_UNDER: f64 = 0.70;

/// Length of one `ssh-bruteforce` cycle: [`ATTACK`] of failed logins, then quiet.
pub const ATTACK_CYCLE: Duration = Duration::from_secs(30 * MINUTE);
/// How long the attack lasts.
pub const ATTACK: Duration = Duration::from_secs(10 * MINUTE);
/// One failed login every this many seconds per node: 150 in 5 minutes, over the critical 50.
pub const ATTACK_EVERY: u64 = 2;
/// The attacker (documentation range, RFC 5737).
pub const ATTACKER: &str = "192.0.2.66";
/// User names a brute force typically tries.
const TRIED_USERS: [&str; 5] = ["root", "admin", "oracle", "test", "ubuntu"];

/// Length of one `offline` cycle: [`ONLINE`] connected, the rest silent.
pub const OFFLINE_CYCLE: Duration = Duration::from_secs(2 * MINUTE);
/// How long the node is connected at the start of each cycle. The silence (90 s) is longer than
/// the rule's 60 s plus the server's 30 s for writing the last contact.
pub const ONLINE: Duration = Duration::from_secs(30);

/// The busy share the scenarios set for the CPU.
#[derive(Debug, Clone, Copy, PartialEq)]
pub enum Busy {
    /// Exactly this share (with little noise).
    Fixed(f64),
    /// The node's own pattern, but at most this share.
    AtMost(f64),
}

fn phase(elapsed: Duration, cycle: Duration) -> Duration {
    Duration::from_nanos(
        // The remainder is below the cycle, which fits into u64 nanoseconds.
        u64::try_from(elapsed.as_nanos() % cycle.as_nanos()).unwrap_or_default(),
    )
}

/// The scenarios a simulation plays (none: the plain fleet of M1-34).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Scenarios(Vec<Scenario>);

impl Scenarios {
    /// The given scenarios; duplicates count once.
    #[must_use]
    pub fn new(scenarios: &[Scenario]) -> Self {
        let mut out: Vec<Scenario> = Vec::new();
        for s in scenarios {
            if !out.contains(s) {
                out.push(*s);
            }
        }
        Self(out)
    }

    fn has(&self, s: Scenario) -> bool {
        self.0.contains(&s)
    }

    /// Whether no scenario runs.
    #[must_use]
    pub fn is_empty(&self) -> bool {
        self.0.is_empty()
    }

    /// The CPU busy share `cpu-high` sets at `elapsed` since the start.
    #[must_use]
    pub fn busy(&self, elapsed: Duration) -> Option<Busy> {
        if !self.has(Scenario::CpuHigh) {
            return None;
        }
        let at = phase(elapsed, CPU_CYCLE);
        Some(if at < CPU_OVER {
            Busy::Fixed(CPU_BUSY_OVER)
        } else if at < CPU_OVER + CPU_HOLD {
            Busy::Fixed(CPU_BUSY_HOLD)
        } else {
            Busy::AtMost(CPU_BUSY_CALM)
        })
    }

    /// How far `disk-filling` has filled the free space at `elapsed`: 0 at the start, 1 for a
    /// full disk.
    #[must_use]
    pub fn filled(&self, elapsed: Duration) -> Option<f64> {
        self.has(Scenario::DiskFilling)
            .then(|| (elapsed.as_secs_f64() / FILL_TIME.as_secs_f64()).min(1.0))
    }

    /// The usage of the root file system `flapping` sets at `elapsed` (it overrides
    /// `disk-filling`).
    #[must_use]
    pub fn usage(&self, elapsed: Duration) -> Option<f64> {
        self.has(Scenario::Flapping).then(|| {
            if phase(elapsed, FLAP_CYCLE) < FLAP_HIGH {
                FLAP_OVER
            } else {
                FLAP_UNDER
            }
        })
    }

    /// Whether the node is connected at `elapsed`.
    #[must_use]
    pub fn online(&self, elapsed: Duration) -> bool {
        !self.has(Scenario::Offline) || phase(elapsed, OFFLINE_CYCLE) < ONLINE
    }

    /// Whether `ssh-bruteforce` attacks at `elapsed`.
    #[must_use]
    pub fn attacking(&self, elapsed: Duration) -> bool {
        self.has(Scenario::SshBruteforce) && phase(elapsed, ATTACK_CYCLE) < ATTACK
    }
}

/// Makes the security events of the scenarios the way the agent finds them: as sshd log lines
/// that go through the real parser.
#[derive(Debug)]
pub struct Attacker {
    parser: Parser,
    attempts: u64,
}

impl Default for Attacker {
    fn default() -> Self {
        Self {
            parser: Parser::new(ParserConfig::default(), None),
            attempts: 0,
        }
    }
}

impl Attacker {
    /// The events of second `elapsed` (whole seconds since the start), stamped with `at_us`
    /// (microseconds since the epoch). `port` varies the source port between nodes.
    pub fn events(
        &mut self,
        scenarios: &Scenarios,
        elapsed: Duration,
        at_us: u64,
        port: u16,
    ) -> Vec<SecurityEvent> {
        let mut out = Vec::new();
        if !scenarios.attacking(elapsed) || !elapsed.as_secs().is_multiple_of(ATTACK_EVERY) {
            return out;
        }
        let user = TRIED_USERS[usize::try_from(self.attempts % 5).unwrap_or(0)];
        self.attempts += 1;
        let port = port.wrapping_add(u16::try_from(self.attempts % 1000).unwrap_or(0));
        let message = format!("Failed password for {user} from {ATTACKER} port {port} ssh2");
        self.parser.parse(
            &LogLine {
                at_us,
                identifier: Some("sshd-session"),
                transport: Some("syslog"),
                uid: Some(0),
                cap_effective: None,
                boot_id: None,
                monotonic_us: None,
                source: Source::Journal,
                message: &message,
            },
            &mut out,
        );
        out
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use sermon_security::{Address, EventKind};

    const fn secs(s: u64) -> Duration {
        Duration::from_secs(s)
    }

    #[test]
    fn without_scenarios_nothing_changes() {
        let none = Scenarios::default();
        for at in [secs(0), secs(600), secs(5000)] {
            assert_eq!(none.busy(at), None);
            assert_eq!(none.filled(at), None);
            assert_eq!(none.usage(at), None);
            assert!(none.online(at));
            assert!(!none.attacking(at));
        }
        assert!(none.is_empty());
        assert_eq!(
            Scenarios::new(&[Scenario::Offline, Scenario::Offline]),
            Scenarios::new(&[Scenario::Offline])
        );
    }

    // cpu-high: over the critical threshold (idle < 5 %), between warning and clear (15–20 %
    // idle), calm (idle > 20 %); then again.
    #[test]
    fn cpu_high_goes_over_holds_and_calms_down() {
        let s = Scenarios::new(&[Scenario::CpuHigh]);
        let idle = |at| match s.busy(at) {
            Some(Busy::Fixed(busy)) => 1.0 - busy,
            other => panic!("{other:?} at {at:?}"),
        };
        assert!(idle(secs(0)) < 0.05 && idle(CPU_OVER.saturating_sub(secs(1))) < 0.05);
        let hold = idle(CPU_OVER);
        assert!(hold > 0.15 && hold < 0.2, "{hold}");
        assert_eq!(s.busy(CPU_OVER + CPU_HOLD), Some(Busy::AtMost(0.5)));
        assert_eq!(
            s.busy(CPU_CYCLE.saturating_sub(secs(1))),
            Some(Busy::AtMost(0.5))
        );
        assert!(idle(CPU_CYCLE) < 0.05, "the next cycle starts over");
    }

    #[test]
    fn disk_filling_fills_up_and_stays_full() {
        let s = Scenarios::new(&[Scenario::DiskFilling]);
        assert_eq!(s.filled(secs(0)), Some(0.0));
        assert_eq!(s.filled(FILL_TIME / 2), Some(0.5));
        assert_eq!(s.filled(FILL_TIME), Some(1.0));
        assert_eq!(s.filled(FILL_TIME * 3), Some(1.0));
    }

    #[test]
    fn flapping_crosses_threshold_and_clear() {
        let s = Scenarios::new(&[Scenario::Flapping]);
        assert!(s.usage(secs(0)).unwrap() > 0.8);
        assert!(s.usage(FLAP_HIGH).unwrap() < 0.75);
        assert!(s.usage(FLAP_CYCLE).unwrap() > 0.8);
        // Over the threshold longer than the rule's `for`, so it fires every cycle.
        assert!(FLAP_HIGH > secs(2 * 60));
    }

    #[test]
    fn offline_is_silent_longer_than_the_rule_allows() {
        let s = Scenarios::new(&[Scenario::Offline]);
        assert!(s.online(secs(0)) && s.online(ONLINE.saturating_sub(secs(1))));
        assert!(!s.online(ONLINE) && !s.online(OFFLINE_CYCLE.saturating_sub(secs(1))));
        assert!(s.online(OFFLINE_CYCLE));
        assert!(OFFLINE_CYCLE.saturating_sub(ONLINE) >= secs(90));
    }

    // The events are what the parser makes of sshd's lines: verified, from the attacker, users
    // and ports varying; 150 in five minutes, nothing in the quiet phase.
    #[test]
    fn ssh_bruteforce_sends_parsed_failed_logins() {
        let s = Scenarios::new(&[Scenario::SshBruteforce]);
        let mut attacker = Attacker::default();
        let mut events = Vec::new();
        for second in 0..300 {
            events.extend(attacker.events(&s, secs(second), second * 1_000_000, 40_000));
        }
        assert_eq!(events.len(), 150);
        let mut users = Vec::new();
        for e in &events {
            assert!(e.verified);
            let EventKind::SshLoginFailed { user, remote, .. } = &e.kind else {
                panic!("{e:?}");
            };
            assert_eq!(remote.address, Address::Ip(ATTACKER.parse().unwrap()));
            assert!(remote.port.is_some());
            users.push(user.clone());
        }
        users.sort();
        users.dedup();
        assert_eq!(users.len(), 5);
        assert_eq!(
            attacker.events(&s, ATTACK, ATTACK.as_secs() * 1_000_000, 40_000),
            []
        );
        assert!(
            !attacker.events(&s, ATTACK_CYCLE, 0, 40_000).is_empty(),
            "the next attack"
        );
    }
}
