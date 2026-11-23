//! Lines of classic log files (M2-20, ADR 0025). The format belongs to the file, it is never
//! guessed from a line: a forged line cannot claim another one.
//!
//! - [`Format::Syslog`]: `Nov 21 19:33:13 host [auth.info] sshd[23]: text`, as busybox `syslogd`
//!   (with facility and priority), rsyslog and syslog-ng (without) write it. Local time without a
//!   year; nothing says who sent the line.
//! - [`Format::Trusted`]: the SerMon template for rsyslog,
//!   `2026-11-21T19:34:29.181507+00:00 imuxsock uid=0 sshd[23]: text`. rsyslog takes the input
//!   and the sender's UID (`SO_PASSCRED`) from the kernel; both stand before everything a sender
//!   controls.

use jiff::civil::DateTime;
use jiff::tz::TimeZone;
use jiff::{Span, Timestamp};

/// The longest tag (`identifier[pid]`) taken as one.
const MAX_TAG_BYTES: usize = 64;

/// How the lines of a file look.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Format {
    /// Classic syslog, without the sender.
    Syslog,
    /// The SerMon template for rsyslog, with input and UID of the sender.
    Trusted,
}

/// Who sent a line, as far as the file says.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Sender {
    /// The file does not say (classic syslog, or a line from the network).
    Unknown,
    /// A local process with this UID (rsyslog `imuxsock`).
    Uid(u32),
    /// The kernel (rsyslog `imklog`).
    Kernel,
}

/// A parsed line.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Record<'a> {
    /// Microseconds since the epoch.
    pub at_us: u64,
    /// The host field of a classic syslog line; `None` in the trusted format, which only local
    /// senders write (M2-39).
    pub host: Option<&'a str>,
    /// The tag without the PID, e.g. `sshd-session`.
    pub identifier: Option<&'a str>,
    /// The PID from the tag.
    pub pid: Option<u32>,
    /// Who sent it.
    pub sender: Sender,
    /// The syslog facility (0 kern … 23 local7), where the line names it (busybox).
    pub facility: Option<u8>,
    /// The syslog priority (0 emergency … 7 debug), where the line names it (busybox).
    pub priority: Option<u8>,
    /// The text after the tag.
    pub message: &'a str,
}

/// Why a line is not a record.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum SyslogError {
    /// The time at the start is missing or invalid.
    #[error("no valid time at the start of the line")]
    Time,
    /// A field of the trusted format is missing.
    #[error("not the SerMon rsyslog format")]
    Trusted,
}

/// What reading local times needs: the host's time zone and the current time (for the year).
#[derive(Debug, Clone)]
pub struct LocalTime {
    zone: TimeZone,
    now: Timestamp,
}

impl LocalTime {
    /// The zone from the content of `/etc/localtime` (`TZif`); UTC without one or if it is invalid.
    #[must_use]
    pub fn new(localtime: Option<&[u8]>, now_us: u64) -> Self {
        let zone = localtime
            .and_then(|data| TimeZone::tzif("localtime", data).ok())
            .unwrap_or(TimeZone::UTC);
        let now = i64::try_from(now_us)
            .ok()
            .and_then(|us| Timestamp::from_microsecond(us).ok())
            .unwrap_or(Timestamp::UNIX_EPOCH);
        Self { zone, now }
    }

    /// `Nov 21 19:33:13` as a time: the year of now, or the one before if that lies more than a
    /// day ahead (a line from December read in January).
    fn resolve(&self, text: &str) -> Option<u64> {
        let now = self.now.to_zoned(self.zone.clone());
        let at = |year: i16| -> Option<Timestamp> {
            let parsed = DateTime::strptime("%Y %b %e %H:%M:%S", format!("{year} {text}")).ok()?;
            Some(parsed.to_zoned(self.zone.clone()).ok()?.timestamp())
        };
        let ahead = self.now.checked_add(Span::new().hours(24)).ok()?;
        let mut stamp = at(now.year())?;
        if stamp > ahead {
            stamp = at(now.year() - 1)?;
        }
        u64::try_from(stamp.as_microsecond()).ok()
    }
}

/// Parses a line of a file in `format`.
///
/// # Errors
///
/// The line has no valid time, or misses a field of the trusted format.
pub fn parse<'a>(
    format: Format,
    line: &'a str,
    time: &LocalTime,
) -> Result<Record<'a>, SyslogError> {
    match format {
        Format::Syslog => syslog(line, time),
        Format::Trusted => trusted(line),
    }
}

fn syslog<'a>(line: &'a str, time: &LocalTime) -> Result<Record<'a>, SyslogError> {
    // `Nov 21 19:33:13` or `Nov  1 19:33:13`: always 15 bytes.
    let stamp = line.get(..15).ok_or(SyslogError::Time)?;
    let at_us = time.resolve(stamp).ok_or(SyslogError::Time)?;
    let rest = line[15..].trim_start_matches(' ');
    let (host, rest) = rest
        .split_once(' ')
        .map_or((None, ""), |(host, rest)| (Some(host), rest));
    // busybox writes `facility.priority` before the tag.
    let (facility, priority, rest) = match rest.split_once(' ') {
        Some((first, after)) if facility_priority(first) => {
            let (f, p) = first.split_once('.').unwrap_or_default();
            (facility_number(f), priority_number(p), after)
        }
        _ => (None, None, rest),
    };
    let (identifier, pid, message) = tag(rest);
    Ok(Record {
        at_us,
        host,
        identifier,
        pid,
        sender: Sender::Unknown,
        facility,
        priority,
        message,
    })
}

fn facility_priority(token: &str) -> bool {
    token.split_once('.').is_some_and(|(f, p)| {
        !f.is_empty()
            && !p.is_empty()
            && f.bytes()
                .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit())
            && p.bytes().all(|b| b.is_ascii_lowercase())
    })
}

/// The names of `syslog.h` (`facilitynames`), as busybox prints them.
fn facility_number(name: &str) -> Option<u8> {
    Some(match name {
        "kern" => 0,
        "user" => 1,
        "mail" => 2,
        "daemon" => 3,
        "auth" | "security" => 4,
        "syslog" => 5,
        "lpr" => 6,
        "news" => 7,
        "uucp" => 8,
        "cron" => 9,
        "authpriv" => 10,
        "ftp" => 11,
        _ => {
            let n: u8 = name.strip_prefix("local")?.parse().ok()?;
            return (n <= 7 && name.len() == 6).then_some(16 + n);
        }
    })
}

/// The names of `syslog.h` (`prioritynames`), as busybox prints them.
fn priority_number(name: &str) -> Option<u8> {
    Some(match name {
        "emerg" | "panic" => 0,
        "alert" => 1,
        "crit" => 2,
        "err" | "error" => 3,
        "warn" | "warning" => 4,
        "notice" => 5,
        "info" => 6,
        "debug" => 7,
        _ => return None,
    })
}

fn trusted(line: &str) -> Result<Record<'_>, SyslogError> {
    let (stamp, rest) = line.split_once(' ').ok_or(SyslogError::Trusted)?;
    let at: Timestamp = stamp.parse().map_err(|_| SyslogError::Time)?;
    let at_us = u64::try_from(at.as_microsecond()).map_err(|_| SyslogError::Time)?;
    let (input, rest) = rest.split_once(' ').ok_or(SyslogError::Trusted)?;
    let (uid, rest) = rest.split_once(' ').ok_or(SyslogError::Trusted)?;
    let uid = uid.strip_prefix("uid=").ok_or(SyslogError::Trusted)?;
    let sender = match input {
        "imklog" => Sender::Kernel,
        "imuxsock" => uid.parse().map_or(Sender::Unknown, Sender::Uid),
        // From the network or another input: rsyslog knows no sender.
        _ => Sender::Unknown,
    };
    let (identifier, pid, message) = tag(rest);
    Ok(Record {
        at_us,
        host: None,
        identifier,
        pid,
        sender,
        facility: None,
        priority: None,
        message,
    })
}

/// `sshd[23]: text` → (`sshd`, 23, `text`); without a tag the whole rest is the message.
fn tag(rest: &str) -> (Option<&str>, Option<u32>, &str) {
    let Some((tag, message)) = rest.split_once(':') else {
        return (None, None, rest);
    };
    if tag.is_empty() || tag.len() > MAX_TAG_BYTES || tag.contains(' ') {
        return (None, None, rest);
    }
    let message = message.strip_prefix(' ').unwrap_or(message);
    match tag.strip_suffix(']').and_then(|t| t.split_once('[')) {
        Some((identifier, pid)) => (
            Some(identifier).filter(|i| !i.is_empty()),
            pid.parse().ok(),
            message,
        ),
        None => (Some(tag), None, message),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 2026-11-21T20:00:00Z.
    const NOW: u64 = 1_795_291_200_000_000;

    fn utc() -> LocalTime {
        LocalTime::new(None, NOW)
    }

    fn us(text: &str) -> u64 {
        u64::try_from(text.parse::<Timestamp>().unwrap().as_microsecond()).unwrap()
    }

    #[test]
    fn reads_busybox_lines_with_facility() {
        let r = parse(
            Format::Syslog,
            "Nov 21 19:33:14 45a848315440 auth.info sshd-session[23]: Connection closed by invalid user alice 127.0.0.1 port 52462 [preauth]",
            &utc(),
        )
        .unwrap();
        assert_eq!(r.at_us, us("2026-11-21T19:33:14Z"));
        assert_eq!(r.host, Some("45a848315440"));
        assert_eq!((r.identifier, r.pid), (Some("sshd-session"), Some(23)));
        assert_eq!(r.sender, Sender::Unknown);
        assert_eq!(
            r.message,
            "Connection closed by invalid user alice 127.0.0.1 port 52462 [preauth]"
        );
        // Without a PID.
        let r = parse(
            Format::Syslog,
            "Nov 21 19:33:13 web-01 user.notice sudo: forged by alice",
            &utc(),
        )
        .unwrap();
        assert_eq!(
            (r.identifier, r.pid, r.message),
            (Some("sudo"), None, "forged by alice")
        );
    }

    #[test]
    fn reads_classic_lines_without_facility_and_padded_days() {
        let r = parse(
            Format::Syslog,
            "Nov  1 08:00:00 web-01 sshd[7]: Failed password for root from 192.0.2.9 port 22 ssh2",
            &utc(),
        )
        .unwrap();
        assert_eq!(r.at_us, us("2026-11-01T08:00:00Z"));
        assert_eq!(r.identifier, Some("sshd"));
        assert_eq!(
            r.message,
            "Failed password for root from 192.0.2.9 port 22 ssh2"
        );
    }

    #[test]
    fn converts_local_time_with_the_zone_of_the_host() {
        let berlin = std::fs::read("/usr/share/zoneinfo/Europe/Berlin");
        let Ok(berlin) = berlin else {
            return; // no zoneinfo on this machine
        };
        let time = LocalTime::new(Some(&berlin), NOW);
        let winter = parse(Format::Syslog, "Nov 21 20:00:00 h kernel: x", &time).unwrap();
        assert_eq!(winter.at_us, us("2026-11-21T19:00:00Z"));
        let summer = parse(Format::Syslog, "Jul  1 12:00:00 h kernel: x", &time).unwrap();
        assert_eq!(summer.at_us, us("2026-07-01T10:00:00Z"));
        // Invalid zone data: UTC.
        let broken = LocalTime::new(Some(b"not tzif"), NOW);
        let r = parse(Format::Syslog, "Nov 21 20:00:00 h kernel: x", &broken).unwrap();
        assert_eq!(r.at_us, us("2026-11-21T20:00:00Z"));
    }

    #[test]
    fn puts_lines_from_the_future_into_last_year() {
        let r = parse(Format::Syslog, "Dec 31 23:59:59 h kernel: x", &utc()).unwrap();
        assert_eq!(r.at_us, us("2025-12-31T23:59:59Z"));
        // Up to a day ahead stays (clocks differ a little).
        let r = parse(Format::Syslog, "Nov 22 10:00:00 h kernel: x", &utc()).unwrap();
        assert_eq!(r.at_us, us("2026-11-22T10:00:00Z"));
    }

    #[test]
    fn rejects_lines_without_a_time() {
        for line in [
            "",
            "garbage",
            "Foo 21 19:33:13 h sshd: x",
            "Feb 30 10:00:00 h sshd: x",
        ] {
            assert_eq!(
                parse(Format::Syslog, line, &utc()),
                Err(SyslogError::Time),
                "{line:?}"
            );
        }
    }

    #[test]
    fn reads_facility_and_priority_of_busybox_lines() {
        let r = parse(
            Format::Syslog,
            "Nov 21 19:33:14 h auth.info sshd[23]: Accepted password for alice",
            &utc(),
        )
        .unwrap();
        assert_eq!((r.facility, r.priority), (Some(4), Some(6)));
        for (token, facility, priority) in [
            ("kern.warn", Some(0), Some(4)),
            ("kern.warning", Some(0), Some(4)),
            ("daemon.err", Some(3), Some(3)),
            ("daemon.error", Some(3), Some(3)),
            ("authpriv.notice", Some(10), Some(5)),
            ("security.crit", Some(4), Some(2)),
            ("cron.emerg", Some(9), Some(0)),
            ("user.panic", Some(1), Some(0)),
            ("local7.debug", Some(23), Some(7)),
            ("syslog.alert", Some(5), Some(1)),
            ("local8.info", None, Some(6)),
            ("user.loud", Some(1), None),
        ] {
            let line = format!("Nov 21 19:33:14 h {token} app: text");
            let r = parse(Format::Syslog, &line, &utc()).unwrap();
            assert_eq!((r.facility, r.priority), (facility, priority), "{token}");
            assert_eq!((r.identifier, r.message), (Some("app"), "text"), "{token}");
        }
        // Classic files and the trusted format don't tell.
        let r = parse(Format::Syslog, "Nov 21 19:33:13 h sshd[7]: x", &utc()).unwrap();
        assert_eq!((r.facility, r.priority), (None, None));
        let r = parse(
            Format::Trusted,
            "2026-11-21T19:34:29+00:00 imuxsock uid=0 auth.info sshd: x",
            &utc(),
        )
        .unwrap();
        assert_eq!((r.facility, r.priority), (None, None));
    }

    #[test]
    fn keeps_odd_tags_in_the_message() {
        let r = parse(Format::Syslog, "Nov 21 19:33:13 h no tag here", &utc()).unwrap();
        assert_eq!((r.identifier, r.message), (None, "no tag here"));
        let r = parse(
            Format::Syslog,
            "Nov 21 19:33:13 h user.notice two words: x",
            &utc(),
        )
        .unwrap();
        assert_eq!((r.identifier, r.message), (None, "two words: x"));
    }

    #[test]
    fn reads_the_trusted_format_with_the_sender() {
        let r = parse(
            Format::Trusted,
            "2026-11-21T19:34:29.908937+00:00 imuxsock uid=0 sshd-session[37]: User nobody not allowed because account is locked",
            &utc(),
        )
        .unwrap();
        assert_eq!(r.at_us, us("2026-11-21T19:34:29.908937Z"));
        assert_eq!(r.sender, Sender::Uid(0));
        assert_eq!((r.identifier, r.pid), (Some("sshd-session"), Some(37)));
        assert_eq!(
            r.message,
            "User nobody not allowed because account is locked"
        );

        let forged = parse(
            Format::Trusted,
            "2026-11-21T19:34:29+01:00 imuxsock uid=1000 sshd: Failed password for root from 192.0.2.9 port 22 ssh2",
            &utc(),
        )
        .unwrap();
        assert_eq!(forged.sender, Sender::Uid(1000));
        assert_eq!(forged.at_us, us("2026-11-21T18:34:29Z"));

        let kernel = parse(
            Format::Trusted,
            "2026-11-21T19:34:29+00:00 imklog uid= kernel: Out of memory: Killed process 42 (stress)",
            &utc(),
        )
        .unwrap();
        assert_eq!(kernel.sender, Sender::Kernel);
        let remote = parse(
            Format::Trusted,
            "2026-11-21T19:34:29+00:00 imudp uid=0 sshd: x",
            &utc(),
        )
        .unwrap();
        assert_eq!(remote.sender, Sender::Unknown, "no sender from the network");
        let empty = parse(
            Format::Trusted,
            "2026-11-21T19:34:29+00:00 imuxsock uid= sshd: x",
            &utc(),
        )
        .unwrap();
        assert_eq!(empty.sender, Sender::Unknown);
    }

    #[test]
    fn a_sender_cannot_claim_fields_of_the_trusted_format() {
        // The message repeats the format: the fields before it count.
        let r = parse(
            Format::Trusted,
            "2026-11-21T19:34:29+00:00 imuxsock uid=1000 x: 2026-11-21T19:34:29+00:00 imuxsock uid=0 sshd: Accepted password for root",
            &utc(),
        )
        .unwrap();
        assert_eq!((r.sender, r.identifier), (Sender::Uid(1000), Some("x")));
        // A classic line is no trusted one.
        assert!(parse(Format::Trusted, "Nov 21 19:33:13 h sshd: x", &utc()).is_err());
        assert_eq!(
            parse(
                Format::Trusted,
                "2026-11-21T19:34:29+00:00 imuxsock 0 sshd: x",
                &utc()
            ),
            Err(SyslogError::Trusted)
        );
    }
}
