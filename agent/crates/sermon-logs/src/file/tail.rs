//! Following a log file across rotation and restarts (M2-20, ADR 0025).
//!
//! The file is read by polling: every call of [`Tail::read`] returns the complete lines written
//! since the last one. A [`Position`] (device, inode, offset) names where a line ends; committed
//! and stored, it lets the next start continue there:
//!
//! - **Rotation by renaming** (logrotate `create`, busybox `syslogd -s`): the old file is read to
//!   its end, then the new one from its start. The old one is read once more on the next call,
//!   for lines its writer wrote before it reopened the path.
//! - **Truncation** (logrotate `copytruncate`): the file is read again from its start.
//! - **Restart:** with the same file the source continues at the stored offset; rotated while
//!   the agent was stopped, it finds the old file as `<name>.0` or `<name>.1`, reads its rest and
//!   then the new file from its start.
//! - **First start:** at the end of the file; old lines are not read again.
//!
//! Every open – the first, after a rotation, of a rotated `.0`/`.1` – is non-blocking and only of
//! a regular file (a FIFO or device swapped in would stall the agent), and with a [`Guard`] the
//! file opened must be the one its check allowed (M3-28): a symlink swapped in after the glob was
//! expanded reads nothing outside the allowed prefixes.

use std::fmt;
use std::fs::{self, File};
use std::io::{self, Read, Seek, SeekFrom};
use std::os::unix::fs::{MetadataExt, OpenOptionsExt};
use std::path::{Path, PathBuf};
use std::sync::Arc;

/// Lines longer than this are cut (and marked).
pub const MAX_LINE_BYTES: usize = 16 * 1024;
/// At most so many bytes per call of [`Tail::read`]; the rest comes with the next call.
pub const MAX_READ_BYTES: usize = 1024 * 1024;
/// About so many lines per call of [`Tail::read`] (a read chunk more at most): a megabyte of
/// empty lines would otherwise be a million of them in memory at once (M3-28).
pub const MAX_READ_LINES: usize = 8192;
/// Bytes read at once.
const CHUNK_BYTES: usize = 16 * 1024;

/// The access check of a path before every open: the device and inode of the regular file it may
/// read there; `NotFound` if there is none, another error if it is refused.
#[derive(Clone)]
pub struct Guard(Arc<GuardFn>);

/// The device and inode of the file the check allows at a path.
type GuardFn = dyn Fn(&Path) -> io::Result<(u64, u64)> + Send + Sync;

impl Guard {
    /// A guard calling `check`.
    pub fn new(check: impl Fn(&Path) -> io::Result<(u64, u64)> + Send + Sync + 'static) -> Self {
        Self(Arc::new(check))
    }
}

impl fmt::Debug for Guard {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("Guard")
    }
}

/// Opens a regular file without blocking (FIFOs, devices) and, with a guard, only the file the
/// guard allowed.
fn open_checked(path: &Path, guard: Option<&Guard>) -> io::Result<File> {
    let allowed = guard.map(|g| (g.0)(path)).transpose()?;
    let flags = rustix::fs::OFlags::NONBLOCK | rustix::fs::OFlags::NOCTTY;
    let file = fs::OpenOptions::new()
        .read(true)
        .custom_flags(i32::try_from(flags.bits()).unwrap_or(0))
        .open(path)?;
    let meta = file.metadata()?;
    if !meta.file_type().is_file() {
        return Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            "not a regular file",
        ));
    }
    if allowed.is_some_and(|ids| ids != (meta.dev(), meta.ino())) {
        return Err(io::Error::new(
            io::ErrorKind::PermissionDenied,
            "the file changed after its access check",
        ));
    }
    Ok(file)
}

/// Where a line ends: the file by device and inode, and the offset after its newline.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Position {
    /// `st_dev` of the file.
    pub dev: u64,
    /// `st_ino` of the file.
    pub inode: u64,
    /// Bytes from the start of the file.
    pub offset: u64,
}

impl Position {
    /// The position as stored: `dev:inode:offset`.
    #[must_use]
    pub fn encode(self) -> String {
        format!("{}:{}:{}", self.dev, self.inode, self.offset)
    }

    /// A stored position; `None` if it is malformed.
    #[must_use]
    pub fn decode(text: &str) -> Option<Self> {
        let mut parts = text.trim().split(':');
        let position = Self {
            dev: parts.next()?.parse().ok()?,
            inode: parts.next()?.parse().ok()?,
            offset: parts.next()?.parse().ok()?,
        };
        parts.next().is_none().then_some(position)
    }
}

/// A complete line of the file, without its newline.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Line {
    /// The bytes of the line (at most [`MAX_LINE_BYTES`]).
    pub text: Vec<u8>,
    /// The line was longer and is cut.
    pub truncated: bool,
    /// Where it ends: commit this once the line's results are stored.
    pub end: Position,
}

/// What happened to the file, for the agent's own metrics.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct TailStats {
    /// The file was replaced by a new one.
    pub rotations: u64,
    /// The file became shorter than what was read.
    pub truncations: u64,
    /// Bytes of a rotated file left unread because the next rotation came first (M2-39).
    pub skipped_bytes: u64,
}

#[derive(Debug)]
struct Open {
    file: File,
    dev: u64,
    inode: u64,
    offset: u64,
    /// The start of a line whose newline is not written yet.
    partial: Vec<u8>,
    /// The partial line was longer than [`MAX_LINE_BYTES`].
    cut: bool,
}

impl Open {
    fn at(file: File, offset: u64) -> io::Result<Self> {
        let meta = file.metadata()?;
        let mut open = Self {
            file,
            dev: meta.dev(),
            inode: meta.ino(),
            offset,
            partial: Vec::new(),
            cut: false,
        };
        open.file.seek(SeekFrom::Start(offset))?;
        Ok(open)
    }

    fn same(&self, meta: &fs::Metadata) -> bool {
        self.dev == meta.dev() && self.inode == meta.ino()
    }

    fn position(&self, offset: u64) -> Position {
        Position {
            dev: self.dev,
            inode: self.inode,
            offset,
        }
    }

    /// Reads up to `budget` bytes and returns the complete lines among them; `true` once it
    /// reached the end of the file.
    fn read(&mut self, budget: &mut usize, out: &mut Vec<Line>) -> io::Result<bool> {
        let mut buffer = vec![0u8; CHUNK_BYTES];
        while *budget > 0 && out.len() < MAX_READ_LINES {
            let want = buffer.len().min(*budget);
            let n = self.file.read(&mut buffer[..want])?;
            if n == 0 {
                return Ok(true);
            }
            *budget -= n;
            // The offset of the first byte of `partial`, to compute each line's end.
            let mut start = self.offset + u64::try_from(self.partial.len()).unwrap_or(u64::MAX);
            for chunk in buffer[..n].split_inclusive(|b| *b == b'\n') {
                start += u64::try_from(chunk.len()).unwrap_or(u64::MAX);
                let (body, complete) = match chunk.strip_suffix(b"\n") {
                    Some(body) => (body, true),
                    None => (chunk, false),
                };
                let room = MAX_LINE_BYTES.saturating_sub(self.partial.len());
                self.cut |= body.len() > room;
                self.partial
                    .extend_from_slice(&body[..body.len().min(room)]);
                if complete {
                    out.push(Line {
                        text: std::mem::take(&mut self.partial),
                        truncated: std::mem::take(&mut self.cut),
                        end: self.position(start),
                    });
                    self.offset = start;
                }
            }
        }
        Ok(false)
    }

    /// Bytes not read yet.
    fn rest(&self) -> u64 {
        self.file
            .metadata()
            .map_or(0, |meta| meta.len().saturating_sub(self.offset))
    }
}

/// Follows the file at a path.
#[derive(Debug)]
pub struct Tail {
    path: PathBuf,
    current: Option<Open>,
    /// The file before the last rotation, read until its end.
    previous: Option<Open>,
    /// Where to continue at the first call (from the stored position).
    resume: Resume,
    stats: TailStats,
    guard: Option<Guard>,
}

#[derive(Debug, Clone, Copy)]
enum Resume {
    /// First start: at the end.
    End,
    /// At a stored position.
    At(Position),
    /// Started already: a file that appears is new, from its start.
    Start,
}

impl Tail {
    /// Follows `path`, continuing at `resume` (the last committed position) or, without one, at
    /// the end of the file.
    #[must_use]
    pub fn new(path: impl Into<PathBuf>, resume: Option<Position>) -> Self {
        Self::start(path.into(), resume.map_or(Resume::End, Resume::At), None)
    }

    /// Follows a file found after the start (e.g. by a glob): everything in it is new.
    #[must_use]
    pub fn new_file(path: impl Into<PathBuf>) -> Self {
        Self::start(path.into(), Resume::Start, None)
    }

    /// Like [`Tail::new`] (`new_file` false) or [`Tail::new_file`], with `guard` checked before
    /// every open (the files of a glob, M3-28).
    #[must_use]
    pub fn guarded(
        path: impl Into<PathBuf>,
        resume: Option<Position>,
        new_file: bool,
        guard: Guard,
    ) -> Self {
        let resume = match resume {
            Some(at) => Resume::At(at),
            None if new_file => Resume::Start,
            None => Resume::End,
        };
        Self::start(path.into(), resume, Some(guard))
    }

    fn start(path: PathBuf, resume: Resume, guard: Option<Guard>) -> Self {
        let mut tail = Self {
            path,
            current: None,
            previous: None,
            resume,
            stats: TailStats::default(),
            guard,
        };
        // Now, so that "the end" is the end at the start; an error is tried again by `read`.
        let _ = tail.open();
        tail
    }

    /// The followed path.
    #[must_use]
    pub fn path(&self) -> &Path {
        &self.path
    }

    /// Where the next line of the current file starts; `None` while it is missing.
    #[must_use]
    pub fn position(&self) -> Option<Position> {
        self.current.as_ref().map(|c| c.position(c.offset))
    }

    /// Rotations and truncations seen so far.
    #[must_use]
    pub fn stats(&self) -> TailStats {
        self.stats
    }

    /// Appends the complete lines written since the last call (at most [`MAX_READ_BYTES`]) to
    /// `out`; a missing file is no error, its lines come once it exists.
    ///
    /// # Errors
    ///
    /// Reading the file fails for another reason than its absence.
    pub fn read(&mut self, out: &mut Vec<Line>) -> io::Result<()> {
        let mut budget = MAX_READ_BYTES;
        if self.current.is_none() && !self.open()? {
            return self.drain_previous(&mut budget, out);
        }
        // The old file first: its lines are older.
        self.drain_previous(&mut budget, out)?;
        if let Some(current) = &mut self.current {
            current.read(&mut budget, out)?;
        }
        self.check_replaced(&mut budget, out)
    }

    /// Reads the old file; it is closed once a read reaches its end (the writer may still add a
    /// little after the rotation), not before – lines behind a flood would be lost otherwise.
    fn drain_previous(&mut self, budget: &mut usize, out: &mut Vec<Line>) -> io::Result<()> {
        if let Some(mut previous) = self.previous.take()
            && !previous.read(budget, out)?
        {
            self.previous = Some(previous);
        }
        Ok(())
    }

    /// Opens the file at the start, after a restart or when it appears; `false` if it is missing.
    fn open(&mut self) -> io::Result<bool> {
        let file = match open_checked(&self.path, self.guard.as_ref()) {
            Ok(file) => file,
            Err(error) if error.kind() == io::ErrorKind::NotFound => {
                // Created later, everything in it is new.
                if matches!(self.resume, Resume::End) {
                    self.resume = Resume::Start;
                }
                return Ok(false);
            }
            Err(error) => return Err(error),
        };
        let meta = file.metadata()?;
        let resume = std::mem::replace(&mut self.resume, Resume::Start);
        self.current = Some(match resume {
            Resume::End => Open::at(file, meta.len())?,
            Resume::Start => Open::at(file, 0)?,
            Resume::At(at) if at.dev == meta.dev() && at.inode == meta.ino() => {
                if at.offset > meta.len() {
                    self.stats.truncations += 1;
                    Open::at(file, 0)?
                } else {
                    Open::at(file, at.offset)?
                }
            }
            Resume::At(at) => {
                // Rotated while the agent was stopped: the rest of the old file first.
                self.previous = self.rotated(at)?;
                self.stats.rotations += 1;
                Open::at(file, 0)?
            }
        });
        Ok(true)
    }

    /// The old file of a stored position among the usual names of rotated files.
    fn rotated(&self, at: Position) -> io::Result<Option<Open>> {
        for suffix in [".0", ".1"] {
            let mut name = self.path.clone().into_os_string();
            name.push(suffix);
            let Ok(file) = open_checked(Path::new(&name), self.guard.as_ref()) else {
                continue;
            };
            let meta = file.metadata()?;
            if meta.dev() == at.dev && meta.ino() == at.inode && at.offset <= meta.len() {
                return Open::at(file, at.offset).map(Some);
            }
        }
        Ok(None)
    }

    /// After reading: a new file at the path (rotation) or a shorter one (truncation).
    fn check_replaced(&mut self, budget: &mut usize, out: &mut Vec<Line>) -> io::Result<()> {
        let Some(current) = &mut self.current else {
            return Ok(());
        };
        match fs::metadata(&self.path) {
            Ok(meta) if current.same(&meta) => {
                if meta.len() < current.offset {
                    self.stats.truncations += 1;
                    current.file.seek(SeekFrom::Start(0))?;
                    current.offset = 0;
                    current.partial.clear();
                    current.cut = false;
                }
                Ok(())
            }
            Ok(_) => {
                self.stats.rotations += 1;
                let old = self.current.take();
                self.resume = Resume::Start;
                if self.open()?
                    && let Some(current) = &mut self.current
                {
                    current.read(budget, out)?;
                }
                // A second rotation before the old file was read to its end: its rest is lost.
                if let Some(abandoned) = std::mem::replace(&mut self.previous, old) {
                    self.stats.skipped_bytes += abandoned.rest();
                }
                Ok(())
            }
            // Renamed, the new one not created yet: keep reading the old one.
            Err(error) if error.kind() == io::ErrorKind::NotFound => Ok(()),
            Err(error) => Err(error),
        }
    }
}

#[cfg(test)]
mod tests {
    use std::io::Write;

    use super::*;

    fn append(path: &Path, text: &str) {
        let mut file = fs::OpenOptions::new()
            .create(true)
            .append(true)
            .open(path)
            .unwrap();
        file.write_all(text.as_bytes()).unwrap();
    }

    fn lines(tail: &mut Tail) -> Vec<String> {
        let mut out = Vec::new();
        tail.read(&mut out).unwrap();
        out.into_iter()
            .map(|l| String::from_utf8(l.text).unwrap())
            .collect()
    }

    fn last_end(tail: &mut Tail) -> (Vec<String>, Option<Position>) {
        let mut out = Vec::new();
        tail.read(&mut out).unwrap();
        let end = out.last().map(|l| l.end);
        (
            out.into_iter()
                .map(|l| String::from_utf8(l.text).unwrap())
                .collect(),
            end,
        )
    }

    #[test]
    fn starts_at_the_end_and_returns_complete_lines_only() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("messages");
        append(&path, "old 1\nold 2\n");
        let mut tail = Tail::new(&path, None);
        assert_eq!(
            lines(&mut tail),
            Vec::<String>::new(),
            "old lines are not read"
        );

        append(&path, "a\nb\nhalf");
        assert_eq!(lines(&mut tail), ["a", "b"]);
        append(&path, " line\n");
        assert_eq!(lines(&mut tail), ["half line"]);
        assert_eq!(lines(&mut tail), Vec::<String>::new());
    }

    // A file found after the start (a glob, M3-05) is new: read from its start.
    #[test]
    fn a_new_file_is_read_from_its_start() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("app.log");
        append(&path, "first\nsecond\n");
        let mut tail = Tail::new_file(&path);
        assert_eq!(lines(&mut tail), ["first", "second"]);
        append(&path, "third\n");
        assert_eq!(lines(&mut tail), ["third"]);
    }

    #[test]
    fn waits_for_a_missing_file_and_reads_it_from_the_start() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("messages");
        let mut tail = Tail::new(&path, None);
        assert_eq!(lines(&mut tail), Vec::<String>::new());
        append(&path, "first\n");
        assert_eq!(lines(&mut tail), ["first"]);
    }

    #[test]
    fn follows_rotation_by_renaming() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("messages");
        append(&path, "");
        let mut tail = Tail::new(&path, None);
        append(&path, "before\n");
        assert_eq!(lines(&mut tail), ["before"]);

        append(&path, "last of old\n");
        fs::rename(&path, dir.path().join("messages.0")).unwrap();
        append(&path, "first of new\n");
        let mut got = lines(&mut tail);
        // The writer adds to the old file until it reopens the path.
        append(&dir.path().join("messages.0"), "late in old\n");
        append(&path, "second of new\n");
        got.extend(lines(&mut tail));
        assert_eq!(
            got,
            [
                "last of old",
                "first of new",
                "late in old",
                "second of new"
            ]
        );
        assert_eq!(tail.stats().rotations, 1);
        assert_eq!(
            lines(&mut tail),
            Vec::<String>::new(),
            "the old file is read once more only"
        );
    }

    // Behind by more than one read when the file rotates (a flood): the old file is read to its
    // end over the next reads, not once and dropped (M2-39); only a second rotation before that
    // abandons the rest, counted.
    #[test]
    fn reads_a_rotated_file_to_its_end_even_when_behind() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("messages");
        append(&path, "");
        let mut tail = Tail::new(&path, None);
        let line = format!("{}\n", "x".repeat(1023));
        let old = MAX_READ_BYTES / 1024 * 2 + 5; // two reads and a bit
        append(&path, &line.repeat(old));
        fs::rename(&path, dir.path().join("messages.0")).unwrap();
        append(&path, "first of new\n");
        let mut got = Vec::new();
        for _ in 0..6 {
            got.extend(lines(&mut tail));
        }
        assert_eq!(got.len(), old + 1, "every old line and the new one");
        assert_eq!(got.last().map(String::as_str), Some("first of new"));
        assert_eq!(tail.stats().skipped_bytes, 0);

        // Two rotations within one read: the rest of the first old file is abandoned, counted.
        append(&path, &line.repeat(old));
        fs::rename(&path, dir.path().join("messages.1")).unwrap();
        append(&path, "x\n");
        let _ = lines(&mut tail); // reads part of messages.1, notices the rotation
        fs::rename(&path, dir.path().join("messages.0")).unwrap();
        append(&path, "y\n");
        let _ = lines(&mut tail);
        assert!(tail.stats().skipped_bytes > 0, "{:?}", tail.stats());
    }

    #[test]
    fn rereads_a_truncated_file_from_the_start() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("auth.log");
        append(&path, "");
        let mut tail = Tail::new(&path, None);
        append(&path, "one\ntwo\n");
        assert_eq!(lines(&mut tail), ["one", "two"]);
        // copytruncate: copied away, then emptied in place.
        fs::OpenOptions::new()
            .write(true)
            .open(&path)
            .unwrap()
            .set_len(0)
            .unwrap();
        assert_eq!(lines(&mut tail), Vec::<String>::new());
        append(&path, "three\n");
        assert_eq!(lines(&mut tail), ["three"]);
        assert_eq!(tail.stats().truncations, 1);
    }

    #[test]
    fn continues_after_a_restart_at_the_committed_line() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("messages");
        append(&path, "");
        let mut tail = Tail::new(&path, None);
        append(&path, "a\nb\n");
        let (got, end) = last_end(&mut tail);
        assert_eq!(got, ["a", "b"]);
        let stored = Position::decode(&end.unwrap().encode()).unwrap();

        append(&path, "c\n");
        let mut tail = Tail::new(&path, Some(stored));
        assert_eq!(lines(&mut tail), ["c"]);
    }

    #[test]
    fn reads_the_rest_of_a_file_rotated_while_stopped() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("messages");
        append(&path, "");
        let mut tail = Tail::new(&path, None);
        append(&path, "a\n");
        let (_, end) = last_end(&mut tail);

        append(&path, "b\n");
        fs::rename(&path, dir.path().join("messages.1")).unwrap();
        append(&path, "c\n");
        let mut tail = Tail::new(&path, end);
        assert_eq!(lines(&mut tail), ["b", "c"]);
    }

    #[test]
    fn rereads_from_the_start_when_the_stored_offset_is_beyond_the_end() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("messages");
        append(&path, "a\nb\n");
        let meta = fs::metadata(&path).unwrap();
        let beyond = Position {
            dev: meta.dev(),
            inode: meta.ino(),
            offset: 1000,
        };
        let mut tail = Tail::new(&path, Some(beyond));
        assert_eq!(lines(&mut tail), ["a", "b"]);
    }

    #[test]
    fn cuts_long_lines_and_bounds_a_read() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("messages");
        append(&path, "");
        let mut tail = Tail::new(&path, None);
        append(
            &path,
            &format!("{}\nshort\n", "x".repeat(MAX_LINE_BYTES + 10)),
        );
        let mut out = Vec::new();
        tail.read(&mut out).unwrap();
        assert_eq!(out.len(), 2);
        assert_eq!(out[0].text.len(), MAX_LINE_BYTES);
        assert!(out[0].truncated && !out[1].truncated);
        assert_eq!(out[1].end.offset, fs::metadata(&path).unwrap().len());

        let line = "y".repeat(1023) + "\n";
        append(&path, &line.repeat(MAX_READ_BYTES / 1024 + 10));
        let mut out = Vec::new();
        tail.read(&mut out).unwrap();
        assert_eq!(out.len(), MAX_READ_BYTES / 1024, "one read is bounded");
        let mut rest = Vec::new();
        tail.read(&mut rest).unwrap();
        assert_eq!(rest.len(), 10, "the rest comes with the next read");
    }

    #[test]
    fn decodes_only_well_formed_positions() {
        let p = Position {
            dev: 1,
            inode: 2,
            offset: 3,
        };
        assert_eq!(Position::decode(&p.encode()), Some(p));
        for bad in ["", "1:2", "1:2:3:4", "a:2:3", "1:2:-3"] {
            assert_eq!(Position::decode(bad), None, "{bad:?}");
        }
    }

    // M3-28: a FIFO swapped in at a rotation must neither block the agent nor be read.
    #[test]
    fn a_fifo_swapped_in_is_not_read_and_does_not_block() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("app.log");
        append(&path, "");
        let mut tail = Tail::new_file(&path);
        append(&path, "before\n");
        assert_eq!(lines(&mut tail), ["before"]);
        fs::rename(&path, dir.path().join("app.log.1")).unwrap();
        let made = std::process::Command::new("mkfifo")
            .arg(&path)
            .status()
            .unwrap();
        assert!(made.success());
        let mut out = Vec::new();
        let err = tail.read(&mut out).unwrap_err();
        assert_eq!(err.kind(), io::ErrorKind::InvalidInput, "{err}");
        assert!(tail.read(&mut out).is_err(), "still refused");
    }

    // M3-28: with a guard, a symlink swapped in after the check reads nothing outside.
    #[test]
    fn a_guard_refuses_a_symlink_swapped_in_at_a_rotation() {
        let dir = tempfile::tempdir().unwrap();
        let logs = dir.path().join("logs");
        fs::create_dir(&logs).unwrap();
        let secret = dir.path().join("secret");
        append(&secret, "key material\n");
        let path = logs.join("app.log");
        append(&path, "");
        let allowed = fs::canonicalize(&logs).unwrap();
        let guard = Guard::new(move |p: &Path| {
            let real = fs::canonicalize(p)?;
            if !real.starts_with(&allowed) {
                return Err(io::Error::new(io::ErrorKind::PermissionDenied, "outside"));
            }
            let meta = fs::metadata(real)?;
            Ok((meta.dev(), meta.ino()))
        });
        let mut tail = Tail::guarded(&path, None, true, guard);
        append(&path, "mine\n");
        assert_eq!(lines(&mut tail), ["mine"]);
        fs::rename(&path, logs.join("app.log.1")).unwrap();
        std::os::unix::fs::symlink(&secret, &path).unwrap();
        let mut out = Vec::new();
        let err = tail.read(&mut out).unwrap_err();
        assert_eq!(err.kind(), io::ErrorKind::PermissionDenied, "{err}");
        assert!(out.iter().all(|l| l.text != b"key material"), "{out:?}");
        // Back to a real file: followed again from its start.
        fs::remove_file(&path).unwrap();
        append(&path, "again\n");
        assert_eq!(lines(&mut tail), ["again"]);
    }

    // M3-28: a flood of empty lines comes in bounded reads, not a million lines at once.
    #[test]
    fn one_read_holds_a_bounded_number_of_lines() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("app.log");
        append(&path, "");
        let mut tail = Tail::new_file(&path);
        append(&path, &"\n".repeat(MAX_READ_BYTES));
        let mut out = Vec::new();
        tail.read(&mut out).unwrap();
        assert!(
            out.len() >= MAX_READ_LINES && out.len() <= MAX_READ_LINES + CHUNK_BYTES,
            "{} lines",
            out.len()
        );
        let mut total = out.len();
        while total < MAX_READ_BYTES {
            let mut more = Vec::new();
            tail.read(&mut more).unwrap();
            assert!(!more.is_empty(), "stalled at {total}");
            total += more.len();
        }
        assert_eq!(total, MAX_READ_BYTES, "no line lost");
    }

    // The file opened must be the one the guard checked: swapped in between, it is refused.
    #[test]
    fn a_file_other_than_the_checked_one_is_refused() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("app.log");
        append(&path, "x\n");
        let checked = fs::metadata(dir.path()).unwrap();
        let guard = Guard::new(move |_: &Path| Ok((checked.dev(), checked.ino())));
        let mut tail = Tail::guarded(&path, None, true, guard);
        let mut out = Vec::new();
        let err = tail.read(&mut out).unwrap_err();
        assert_eq!(err.kind(), io::ErrorKind::PermissionDenied, "{err}");
        assert_eq!(out.len(), 0);
    }
}
