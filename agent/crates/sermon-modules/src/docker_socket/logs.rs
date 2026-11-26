//! The output of containers (M5-21): `GET /containers/<id>/logs` with timestamps, multiplexed
//! (8-byte frame heads, stdout and stderr) or raw for a container with a TTY. Each run asks for
//! what came since the last line it took; a container seen for the first time starts at the
//! run's time (no history). The lines go to the agent's log pipeline: redaction, rate, selection
//! and the bound of units per node there, as for every log entry.

/// Most lines asked for per container and run (`tail`).
pub const TAIL: usize = 1000;
/// The `tail` of the second try after an answer that couldn't be read (larger than the HTTP
/// client takes).
pub const SMALL_TAIL: usize = 100;
/// Most containers whose logs are followed.
pub const MAX_FOLLOWED: usize = 16;
/// Most lines a run takes of one container: its share of the run's [`crate::MAX_LOG_LINES`], so
/// that one chatty container can't crowd out the others (M5-33). The rest comes with the next
/// run.
pub const PER_CONTAINER: usize = crate::MAX_LOG_LINES / MAX_FOLLOWED;
/// Messages kept at the newest timestamp, to tell a repeated line from a new one.
const MAX_AT_LAST: usize = 16;

/// A line of a container's output.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Line {
    /// Written to stderr.
    pub stderr: bool,
    /// Seconds since the epoch.
    pub secs: i64,
    /// Nanoseconds of the second.
    pub nanos: u32,
    /// The text, without its line end.
    pub message: String,
}

/// Parses an answer of `/logs?timestamps=1`: frames of the multiplexed stream, or the raw stream
/// of a container with a TTY (all stdout). Lines without a valid timestamp are skipped; a frame
/// cut short ends the answer.
#[must_use]
pub fn parse(body: &[u8], tty: bool) -> Vec<Line> {
    let mut out = Vec::new();
    if tty {
        lines(body, false, &mut out);
        return out;
    }
    let (mut stdout, mut stderr) = (Vec::new(), Vec::new());
    let mut rest = body;
    while rest.len() >= 8 {
        let (head, after) = rest.split_at(8);
        let size = usize::try_from(u32::from_be_bytes([head[4], head[5], head[6], head[7]]))
            .unwrap_or(usize::MAX);
        if after.len() < size {
            break;
        }
        let (payload, next) = after.split_at(size);
        match head[0] {
            1 => stdout.extend_from_slice(payload),
            2 => stderr.extend_from_slice(payload),
            _ => {}
        }
        rest = next;
    }
    lines(&stdout, false, &mut out);
    lines(&stderr, true, &mut out);
    out.sort_by_key(|l| (l.secs, l.nanos));
    out
}

fn lines(stream: &[u8], stderr: bool, out: &mut Vec<Line>) {
    for raw in stream.split(|b| *b == b'\n') {
        let raw = raw.strip_suffix(b"\r").unwrap_or(raw);
        let text = String::from_utf8_lossy(raw);
        let Some((stamp, message)) = text.split_once(' ') else {
            continue;
        };
        if let Some((secs, nanos)) = timestamp(stamp) {
            out.push(Line {
                stderr,
                secs,
                nanos,
                message: message.to_owned(),
            });
        }
    }
}

/// `2026-11-26T20:21:45.943634718Z` (RFC 3339, UTC, up to nanoseconds).
fn timestamp(text: &str) -> Option<(i64, u32)> {
    let text = text.strip_suffix('Z')?;
    let (date, time) = text.split_once('T')?;
    let (clock, fraction) = time.split_once('.').unwrap_or((time, ""));
    let number = |s: &str, len: usize| -> Option<i64> {
        (s.len() == len && s.bytes().all(|b| b.is_ascii_digit())).then(|| s.parse().ok())?
    };
    let mut d = date.split('-');
    let (year, month, day) = (
        number(d.next()?, 4)?,
        number(d.next()?, 2)?,
        number(d.next()?, 2)?,
    );
    let mut c = clock.split(':');
    let (hour, minute, second) = (
        number(c.next()?, 2)?,
        number(c.next()?, 2)?,
        number(c.next()?, 2)?,
    );
    if d.next().is_some()
        || c.next().is_some()
        || !(1..=12).contains(&month)
        || !(1..=31).contains(&day)
        || hour > 23
        || minute > 59
        || second > 59
        || fraction.len() > 9
        || !fraction.bytes().all(|b| b.is_ascii_digit())
    {
        return None;
    }
    let nanos = if fraction.is_empty() {
        0
    } else {
        format!("{fraction:0<9}").parse().ok()?
    };
    let days = crate::x509::days_from_civil(year, month, day);
    Some((days * 86_400 + hour * 3600 + minute * 60 + second, nanos))
}

/// Where the output of a container was taken up to.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Position {
    /// The newest timestamp taken.
    pub secs: i64,
    /// Its nanoseconds.
    pub nanos: u32,
    /// The messages taken at exactly that timestamp.
    at_last: Vec<String>,
}

impl Position {
    /// Starting at `secs` (a container seen first: nothing older).
    #[must_use]
    pub fn at(secs: i64, nanos: u32) -> Self {
        Self {
            secs,
            nanos,
            at_last: Vec::new(),
        }
    }

    /// The `since` of the next request (Docker takes fractional seconds; inclusive).
    #[must_use]
    pub fn since(&self) -> String {
        format!("{}.{:09}", self.secs, self.nanos)
    }

    /// The lines of `got` not taken yet, at most `max`, and moves on to the last one taken: what
    /// is left over comes with the next request.
    pub fn take(&mut self, got: Vec<Line>, max: usize) -> Vec<Line> {
        let mut new = Vec::new();
        for line in got {
            if new.len() >= max {
                break;
            }
            let at = (line.secs, line.nanos);
            let last = (self.secs, self.nanos);
            if at < last || (at == last && self.at_last.contains(&line.message)) {
                continue;
            }
            if at > last {
                (self.secs, self.nanos) = at;
                self.at_last.clear();
            }
            if self.at_last.len() < MAX_AT_LAST {
                self.at_last.push(line.message.clone());
            }
            new.push(line);
        }
        new
    }
}

/// Whether `name` matches `pattern` (`*` for any run of characters).
#[must_use]
pub fn matches(pattern: &str, name: &str) -> bool {
    let parts: Vec<&str> = pattern.split('*').collect();
    if parts.len() == 1 {
        return pattern == name;
    }
    let (first, last) = (parts[0], parts[parts.len() - 1]);
    if !name.starts_with(first) || name.len() < first.len() + last.len() || !name.ends_with(last) {
        return false;
    }
    let mut rest = &name[first.len()..name.len() - last.len()];
    for part in &parts[1..parts.len() - 1] {
        match rest.find(part) {
            Some(i) => rest = &rest[i + part.len()..],
            None => return false,
        }
    }
    true
}

#[cfg(test)]
mod tests {
    use std::path::Path;

    use super::*;

    fn fixture(name: &str) -> Vec<u8> {
        std::fs::read(
            Path::new(env!("CARGO_MANIFEST_DIR"))
                .join("../../fixtures/modules/docker_socket/docker-28.3")
                .join(name),
        )
        .unwrap()
    }

    // The plan's test: the multiplexed format, stdout and stderr apart, in order of time.
    #[test]
    fn reads_the_multiplexed_stream() {
        let lines = parse(&fixture("logs-talker.bin"), false);
        let texts: Vec<(bool, &str)> = lines
            .iter()
            .map(|l| (l.stderr, l.message.as_str()))
            .collect();
        assert_eq!(
            texts,
            [
                (true, "warning: low disk"),
                (false, "starting up"),
                (false, "no newline"),
                (false, "ready")
            ]
        );
        assert!(
            lines
                .windows(2)
                .all(|w| (w[0].secs, w[0].nanos) <= (w[1].secs, w[1].nanos))
        );
        assert!(lines[0].secs > 1_790_000_000, "a time of 2026");
    }

    #[test]
    fn reads_the_raw_stream_of_a_tty() {
        let lines = parse(&fixture("logs-tty.bin"), true);
        let texts: Vec<&str> = lines.iter().map(|l| l.message.as_str()).collect();
        assert_eq!(
            texts,
            ["tty line one", "tty line two"],
            "no \\r, all stdout"
        );
        assert!(lines.iter().all(|l| !l.stderr));
    }

    #[test]
    fn skips_broken_frames_and_lines() {
        let mut body = vec![1, 0, 0, 0, 0, 0, 0, 33];
        body.extend_from_slice(b"2026-01-01T00:00:00.5Z complete\n");
        body.extend_from_slice(&[2, 0, 0, 0, 0, 0, 1, 0, b'x']);
        let lines = parse(&body, false);
        assert_eq!(lines.len(), 1, "the second frame is cut short");
        assert_eq!(
            (lines[0].secs, lines[0].nanos),
            (1_767_225_600, 500_000_000)
        );
        for bad in [
            "no timestamp",
            "2026-13-01T00:00:00Z x",
            "2026-01-01T24:00:00Z x",
            "2026-01-01T00:00:00 x",
            "2026-01-01T00:00:00.1234567890Z x",
            "26-01-01T00:00:00Z x",
        ] {
            assert!(parse(bad.as_bytes(), true).is_empty(), "{bad}");
        }
    }

    // Every line once, also when two runs see the same timestamp.
    #[test]
    fn takes_each_line_once() {
        let line = |secs, nanos, m: &str| Line {
            stderr: false,
            secs,
            nanos,
            message: m.into(),
        };
        let mut p = Position::at(100, 0);
        assert_eq!(p.since(), "100.000000000");
        let first = p.take(
            vec![
                line(99, 9, "old"),
                line(100, 0, "a"),
                line(101, 5, "b"),
                line(101, 5, "c"),
            ],
            100,
        );
        assert_eq!(first.len(), 3, "nothing before the start");
        assert_eq!(p.since(), "101.000000005");
        let again = p.take(
            vec![
                line(101, 5, "b"),
                line(101, 5, "c"),
                line(101, 5, "d"),
                line(102, 0, "e"),
            ],
            100,
        );
        let texts: Vec<&str> = again.iter().map(|l| l.message.as_str()).collect();
        assert_eq!(texts, ["d", "e"]);
        // A new timestamp forgets the texts of the old one.
        let later = p.take(vec![line(103, 0, "y"), line(103, 0, "e")], 100);
        assert_eq!(later.len(), 2, "e again, but at another time");
    }

    // M5-33: beyond its bound a run leaves the rest for the next one instead of moving past it.
    #[test]
    fn takes_at_most_its_bound_and_the_rest_later() {
        let line = |secs, m: &str| Line {
            stderr: false,
            secs,
            nanos: 0,
            message: m.into(),
        };
        let answer = vec![line(1, "a"), line(2, "b"), line(2, "c"), line(3, "d")];
        let mut p = Position::at(0, 0);
        let texts = |lines: Vec<Line>| lines.into_iter().map(|l| l.message).collect::<Vec<_>>();
        assert_eq!(texts(p.take(answer.clone(), 2)), ["a", "b"]);
        assert_eq!(p.since(), "2.000000000");
        assert_eq!(texts(p.take(answer.clone(), 2)), ["c", "d"]);
        assert_eq!(texts(p.take(answer, 2)), Vec::<String>::new());
    }

    #[test]
    fn matches_names() {
        for (p, n) in [
            ("web", "web"),
            ("app-*", "app-1"),
            ("*", "x"),
            ("a*b*c", "aXbYc"),
            ("*-db", "pg-db"),
        ] {
            assert!(matches(p, n), "{p} {n}");
        }
        for (p, n) in [
            ("web", "web2"),
            ("app-*", "ap"),
            ("a*b*c", "acb"),
            ("*-db", "db"),
        ] {
            assert!(!matches(p, n), "{p} {n}");
        }
    }
}
