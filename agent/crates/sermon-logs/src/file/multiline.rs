//! Lines of a log file as entries (M3-05, KONZEPT §10.1): a Java stack trace or a multi-line
//! message is one entry, not dozens.
//!
//! - **Start of an entry:** with a regex of the configuration, a line it matches; without one, a
//!   line that is not indented. Lines before the first start form an entry of their own; blank
//!   lines never start one and are dropped when no entry is open.
//! - **Bounds:** at most [`MAX_ENTRY_LINES`] lines and [`MAX_ENTRY_BYTES`] per entry; further
//!   continuation lines are dropped and the entry is marked truncated.
//! - **Positions:** [`Assembler::settled`] is the end of the last line that belongs to an emitted
//!   entry (or was dropped); the lines of the open entry come again after a restart.

use super::tail::{Line, Position};

/// Lines of one entry at most.
pub const MAX_ENTRY_LINES: usize = 256;
/// Bytes of one entry at most (the message is cut to 16 KiB later).
pub const MAX_ENTRY_BYTES: usize = 64 * 1024;

/// An entry of several lines.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Entry {
    /// The lines, joined by newlines (invalid UTF-8 replaced).
    pub text: String,
    /// Lines in the entry, including dropped ones.
    pub lines: usize,
    /// A line was cut or lines were dropped.
    pub truncated: bool,
    /// Where its last line ends.
    pub end: Position,
}

/// Whether a line continues the entry before it by indentation.
#[must_use]
pub fn indented(text: &str) -> bool {
    text.starts_with([' ', '\t'])
}

/// Groups lines into entries.
#[derive(Debug, Default)]
pub struct Assembler {
    open: Option<Entry>,
    settled: Option<Position>,
}

impl Assembler {
    /// Takes a line; `starts` says whether it begins a new entry. A completed entry goes to `out`.
    pub fn push(&mut self, line: &Line, starts: impl FnOnce(&str) -> bool, out: &mut Vec<Entry>) {
        let text = String::from_utf8_lossy(&line.text);
        let text = text.strip_suffix('\r').unwrap_or(&text);
        let blank = text.trim().is_empty();
        let begins = !blank && starts(text);
        match &mut self.open {
            Some(entry) if !begins => {
                entry.lines += 1;
                entry.end = line.end;
                if entry.lines > MAX_ENTRY_LINES
                    || entry.text.len() + 1 + text.len() > MAX_ENTRY_BYTES
                {
                    entry.truncated = true;
                } else {
                    entry.text.push('\n');
                    entry.text.push_str(text);
                    entry.truncated |= line.truncated;
                }
            }
            _ if blank => {
                // Nothing open: the line belongs to no entry.
                self.settled = Some(line.end);
            }
            _ => {
                self.emit(out);
                self.open = Some(Entry {
                    text: text.to_owned(),
                    lines: 1,
                    truncated: line.truncated,
                    end: line.end,
                });
            }
        }
    }

    /// Emits the open entry (no line came for a while: it is complete).
    pub fn flush(&mut self, out: &mut Vec<Entry>) {
        self.emit(out);
    }

    /// Whether an entry is open.
    #[must_use]
    pub fn is_open(&self) -> bool {
        self.open.is_some()
    }

    /// The end of the last line that belongs to an emitted entry or to none; `None` before any.
    #[must_use]
    pub fn settled(&self) -> Option<Position> {
        self.settled
    }

    fn emit(&mut self, out: &mut Vec<Entry>) {
        if let Some(entry) = self.open.take() {
            self.settled = Some(entry.end);
            out.push(entry);
        }
    }
}

#[cfg(test)]
mod tests {
    use std::fmt::Write;

    use super::*;
    use proptest::prelude::*;

    fn lines(text: &str) -> Vec<Line> {
        let mut offset = 0;
        text.split_inclusive('\n')
            .map(|l| {
                offset += u64::try_from(l.len()).unwrap();
                Line {
                    text: l.trim_end_matches('\n').as_bytes().to_vec(),
                    truncated: false,
                    end: Position {
                        dev: 1,
                        inode: 2,
                        offset,
                    },
                }
            })
            .collect()
    }

    fn assemble(text: &str, starts: &dyn Fn(&str) -> bool) -> (Vec<Entry>, Assembler) {
        let mut a = Assembler::default();
        let mut out = Vec::new();
        for line in lines(text) {
            a.push(&line, |t| starts(t), &mut out);
        }
        (out, a)
    }

    const JAVA: &str = "2026-11-23 12:00:01 ERROR request failed\n\
java.lang.IllegalStateException: boom\n\
\tat app.Main.run(Main.java:42)\n\
\tat app.Main.main(Main.java:7)\n\
Caused by: java.io.IOException: closed\n\
\t... 2 more\n\
2026-11-23 12:00:02 INFO next request\n";

    #[test]
    fn groups_a_stack_trace_by_a_start_regex() {
        let date = |t: &str| t.len() > 4 && t.as_bytes()[..4].iter().all(u8::is_ascii_digit);
        let (out, mut a) = assemble(JAVA, &date);
        assert_eq!(out.len(), 1);
        assert_eq!(out[0].lines, 6);
        assert!(out[0].text.starts_with("2026-11-23 12:00:01 ERROR"));
        assert!(out[0].text.ends_with("\t... 2 more"));
        // The second entry is still open: not settled.
        assert!(a.is_open());
        assert_eq!(a.settled(), Some(out[0].end));
        let mut rest = Vec::new();
        a.flush(&mut rest);
        assert_eq!(rest[0].text, "2026-11-23 12:00:02 INFO next request");
        assert_eq!(
            a.settled().unwrap().offset,
            u64::try_from(JAVA.len()).unwrap()
        );
    }

    #[test]
    fn groups_by_indentation_without_a_regex() {
        let (mut out, mut a) = assemble(JAVA, &|t| !indented(t));
        a.flush(&mut out);
        let texts: Vec<_> = out.iter().map(|e| e.lines).collect();
        // "Caused by" is not indented: an entry of its own.
        assert_eq!(texts, [1, 3, 2, 1]);
        assert_eq!(
            out[1].text,
            "java.lang.IllegalStateException: boom\n\tat app.Main.run(Main.java:42)\n\tat app.Main.main(Main.java:7)"
        );
    }

    #[test]
    fn lines_before_the_first_start_and_blank_lines() {
        let (mut out, mut a) = assemble("\n\n  orphan\nSTART a\n\n  b\nSTART c\r\n", &|t| {
            t.starts_with("START")
        });
        a.flush(&mut out);
        let texts: Vec<_> = out.iter().map(|e| e.text.as_str()).collect();
        assert_eq!(texts, ["  orphan", "START a\n\n  b", "START c"]);

        // Only blank lines: no entry, but they are consumed.
        let (out, a) = assemble("\n \n\t\n", &|_| true);
        assert_eq!(out, Vec::<Entry>::new());
        assert_eq!(a.settled().map(|p| p.offset), Some(5));
    }

    #[test]
    fn stays_within_its_bounds() {
        let mut text = String::from("START\n");
        for i in 0..300 {
            writeln!(text, "  line {i}").unwrap();
        }
        let (mut out, mut a) = assemble(&text, &|t| t.starts_with("START"));
        a.flush(&mut out);
        assert_eq!(out.len(), 1);
        assert!(out[0].truncated);
        assert_eq!(out[0].lines, 301);
        assert_eq!(out[0].text.lines().count(), MAX_ENTRY_LINES);
        // The dropped lines are consumed: settled at the end.
        assert_eq!(
            a.settled().unwrap().offset,
            u64::try_from(text.len()).unwrap()
        );

        let wide = format!("START\n{}\n{}\n", " ".repeat(40_000), " x".repeat(20_000));
        let (mut out, mut a) = assemble(&wide, &|t| t.starts_with("START"));
        a.flush(&mut out);
        assert!(out[0].truncated && out[0].text.len() <= MAX_ENTRY_BYTES);
    }

    #[test]
    fn a_cut_line_marks_its_entry() {
        let mut a = Assembler::default();
        let mut out = Vec::new();
        let mut line = lines("START\n").remove(0);
        line.truncated = true;
        a.push(&line, |_| true, &mut out);
        a.flush(&mut out);
        assert!(out[0].truncated);
    }

    proptest! {
        // Every line ends up in an entry or is dropped as blank; entries stay within bounds and
        // the settled position only grows.
        #[test]
        fn accounts_for_every_line(text in "([ \\tA-Z0-9a]{0,40}\n){0,600}", start in "[A-Z]?") {
            let starts = |t: &str| start.is_empty() && !indented(t) || !start.is_empty() && t.starts_with(start.as_str());
            let mut a = Assembler::default();
            let mut out = Vec::new();
            let mut last = 0;
            let all = lines(&text);
            for line in &all {
                a.push(line, starts, &mut out);
                let settled = a.settled().map_or(0, |p| p.offset);
                prop_assert!(settled >= last && settled <= line.end.offset);
                last = settled;
            }
            a.flush(&mut out);
            let blank = all.iter().filter(|l| String::from_utf8_lossy(&l.text).trim().is_empty()).count();
            let counted: usize = out.iter().map(|e| e.lines).sum();
            prop_assert!(counted <= all.len() && counted + blank >= all.len());
            for e in &out {
                prop_assert!(e.text.len() <= MAX_ENTRY_BYTES);
                prop_assert!(e.text.lines().count() <= MAX_ENTRY_LINES);
            }
            prop_assert_eq!(a.settled().map_or(0, |p| p.offset), all.last().map_or(0, |l| l.end.offset));
        }
    }
}
