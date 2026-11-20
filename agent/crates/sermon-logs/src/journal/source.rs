//! `journalctl` as a child process (ADR 0013).
//!
//! ```text
//! journalctl -o json --all --follow --quiet --no-pager --output-fields=<OUTPUT_FIELDS>
//!            (--after-cursor=<cursor> | --lines=0)
//! ```
//!
//! - **Start:** after the committed cursor of the cursor file; without one at the end of the
//!   journal (`--lines=0`, no backlog of past days).
//! - **Restart:** if `journalctl` ends, it is started again with backoff, after the last entry the
//!   source *read* – those entries are already with the consumer, nothing comes twice.
//! - **Cursor file:** holds what the consumer *committed* (results stored), written at most every
//!   `flush_interval` and at shutdown. After a crash of the agent only uncommitted entries come again.
//! - **Lines** are read one by one with a length limit; a broken line costs only itself.
//! - **Lost entries:** a jump in the sequence number (vacuum, rotation while stopped) is counted.

use std::io;
use std::path::PathBuf;
use std::process::Stdio;
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::Duration;

use tokio::io::{AsyncBufReadExt, AsyncRead, BufReader};
use tokio::process::{Child, Command};
use tokio::sync::mpsc;
use tokio::time::{Interval, MissedTickBehavior};
use tokio_util::sync::CancellationToken;

use super::cursor::{Checkpoint, CursorFile};
use super::entry::{Entry, OUTPUT_FIELDS, Seqnum, parse_line};

/// Settings of the journal source.
#[derive(Debug, Clone)]
pub struct JournalConfig {
    /// The program to run, `journalctl` from the `PATH` by default.
    pub program: PathBuf,
    /// Where the committed cursor is kept.
    pub cursor_file: PathBuf,
    /// First wait before a restart; doubles up to `max_backoff`, back to this after a run that
    /// delivered entries.
    pub backoff: Duration,
    /// Longest wait before a restart.
    pub max_backoff: Duration,
    /// How often a changed cursor is written.
    pub flush_interval: Duration,
    /// Longer lines are skipped (and counted as invalid).
    pub max_line_bytes: usize,
}

impl JournalConfig {
    /// The defaults with the cursor kept at `cursor_file`.
    #[must_use]
    pub fn new(cursor_file: impl Into<PathBuf>) -> Self {
        Self {
            program: PathBuf::from("journalctl"),
            cursor_file: cursor_file.into(),
            backoff: Duration::from_secs(1),
            max_backoff: Duration::from_secs(60),
            flush_interval: Duration::from_secs(1),
            max_line_bytes: 1024 * 1024,
        }
    }
}

/// Counters of the source since the start of the agent.
#[derive(Debug, Default)]
pub struct JournalStats {
    /// Entries delivered.
    pub entries: AtomicU64,
    /// Lines that were no entry (no JSON, too long, no cursor or time).
    pub invalid_lines: AtomicU64,
    /// Entries missing according to the sequence numbers.
    pub lost_entries: AtomicU64,
    /// Starts of `journalctl` after the first (including failed ones).
    pub restarts: AtomicU64,
}

/// The arguments of `journalctl`: after `cursor`, or from the end without one.
#[must_use]
pub fn arguments(after: Option<&str>) -> Vec<String> {
    let mut args: Vec<String> = ["-o", "json", "--all", "--follow", "--quiet", "--no-pager"]
        .into_iter()
        .map(str::to_owned)
        .collect();
    args.push(format!("--output-fields={}", OUTPUT_FIELDS.join(",")));
    args.push(after.map_or_else(|| "--lines=0".to_owned(), |c| format!("--after-cursor={c}")));
    args
}

/// Lines of stderr per run taken over into the agent log, and their length.
const STDERR_LINES: usize = 10;
const STDERR_LINE_BYTES: usize = 512;

/// The systemd journal as a stream of entries.
#[derive(Debug)]
pub struct JournalSource {
    config: JournalConfig,
    file: CursorFile,
    checkpoint: Checkpoint,
    stats: Arc<JournalStats>,
}

/// How one run of `journalctl` ended.
enum Ended {
    /// It exited or its output broke; it is started again.
    Exited { delivered: u64 },
    /// Shutdown, or the consumer is gone.
    Stop,
}

/// Where the source is: the last entry read.
struct Position {
    cursor: Option<String>,
    seqnum: Option<Seqnum>,
}

impl JournalSource {
    /// A source resuming after the cursor stored in `config.cursor_file`.
    #[must_use]
    pub fn new(config: JournalConfig) -> Self {
        let file = CursorFile::new(&config.cursor_file);
        let committed = file.load().unwrap_or_else(|error| {
            tracing::warn!(
                path = %file.path().display(),
                "cannot read the journal cursor, starting at the end of the journal: {error}"
            );
            None
        });
        Self {
            config,
            file,
            checkpoint: Checkpoint::new(committed),
            stats: Arc::default(),
        }
    }

    /// The checkpoint the consumer commits stored entries to.
    #[must_use]
    pub fn checkpoint(&self) -> Checkpoint {
        self.checkpoint.clone()
    }

    /// The source's counters.
    #[must_use]
    pub fn stats(&self) -> Arc<JournalStats> {
        self.stats.clone()
    }

    /// Delivers entries to `sender` until `shutdown` or until the receiver is dropped, then
    /// writes the committed cursor a last time.
    pub async fn run(self, sender: mpsc::Sender<Entry>, shutdown: CancellationToken) {
        let committed = self.checkpoint.committed();
        let mut position = Position {
            seqnum: committed.as_deref().and_then(Seqnum::from_cursor),
            cursor: committed,
        };
        let mut flush = tokio::time::interval(self.config.flush_interval);
        flush.set_missed_tick_behavior(MissedTickBehavior::Delay);
        let mut backoff = self.config.backoff;
        let mut failing = false;
        loop {
            match self
                .follow(&mut position, &sender, &shutdown, &mut flush, &mut failing)
                .await
            {
                Ended::Stop => break,
                Ended::Exited { delivered } if delivered > 0 => backoff = self.config.backoff,
                Ended::Exited { .. } => {}
            }
            self.stats.restarts.fetch_add(1, Ordering::Relaxed);
            let wait = tokio::time::sleep(backoff);
            tokio::pin!(wait);
            loop {
                tokio::select! {
                    () = shutdown.cancelled() => {
                        self.persist().await;
                        return;
                    }
                    () = &mut wait => break,
                    _ = flush.tick() => self.persist().await,
                }
            }
            backoff = (backoff * 2).min(self.config.max_backoff);
        }
        self.persist().await;
    }

    /// One run of `journalctl`.
    async fn follow(
        &self,
        position: &mut Position,
        sender: &mpsc::Sender<Entry>,
        shutdown: &CancellationToken,
        flush: &mut Interval,
        failing: &mut bool,
    ) -> Ended {
        if shutdown.is_cancelled() {
            return Ended::Stop;
        }
        let mut child = match Command::new(&self.config.program)
            .args(arguments(position.cursor.as_deref()))
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .kill_on_drop(true)
            .spawn()
        {
            Ok(child) => child,
            Err(error) => {
                if !*failing {
                    *failing = true;
                    if error.kind() == io::ErrorKind::NotFound {
                        tracing::warn!(
                            program = %self.config.program.display(),
                            "journalctl not found – no journal on this host, retrying"
                        );
                    } else {
                        tracing::warn!("cannot start journalctl, retrying: {error}");
                    }
                }
                return Ended::Exited { delivered: 0 };
            }
        };
        if std::mem::take(failing) {
            tracing::info!("journalctl started");
        }
        let stderr = child
            .stderr
            .take()
            .map(|stderr| tokio::spawn(log_stderr(stderr)));
        let Some(stdout) = child.stdout.take() else {
            return Ended::Exited { delivered: 0 };
        };
        let mut lines = LineReader::new(stdout, self.config.max_line_bytes);
        let mut delivered = 0;
        let ended = loop {
            tokio::select! {
                () = shutdown.cancelled() => break Ended::Stop,
                _ = flush.tick() => self.persist().await,
                line = lines.next() => match line {
                    Ok(Some(Line::Text(bytes))) => {
                        let Some(entry) = self.accept(&bytes, position) else { continue };
                        tokio::select! {
                            () = shutdown.cancelled() => break Ended::Stop,
                            sent = sender.send(entry) => if sent.is_err() { break Ended::Stop },
                        }
                        delivered += 1;
                    }
                    Ok(Some(Line::TooLong)) => {
                        self.stats.invalid_lines.fetch_add(1, Ordering::Relaxed);
                    }
                    Ok(None) => break Ended::Exited { delivered },
                    Err(error) => {
                        tracing::warn!("cannot read from journalctl: {error}");
                        break Ended::Exited { delivered };
                    }
                },
            }
        };
        stop(&mut child, matches!(ended, Ended::Stop)).await;
        if let Some(task) = stderr {
            let _ = task.await;
        }
        ended
    }

    /// Parses a line and advances the position; `None` for lines that are no entry.
    fn accept(&self, bytes: &[u8], position: &mut Position) -> Option<Entry> {
        if bytes.iter().all(u8::is_ascii_whitespace) {
            return None;
        }
        let entry = match parse_line(bytes) {
            Ok(entry) => entry,
            Err(error) => {
                self.stats.invalid_lines.fetch_add(1, Ordering::Relaxed);
                tracing::debug!("skipping a journal line: {error}");
                return None;
            }
        };
        if let Some(seqnum) = &entry.seqnum {
            let lost = position
                .seqnum
                .as_ref()
                .map_or(0, |last| last.lost_before(seqnum));
            if lost > 0 {
                self.stats.lost_entries.fetch_add(lost, Ordering::Relaxed);
                tracing::warn!(lost, "journal entries were lost (vacuum or rotation)");
            }
            position.seqnum = Some(seqnum.clone());
        }
        position.cursor = Some(entry.cursor.clone());
        self.stats.entries.fetch_add(1, Ordering::Relaxed);
        Some(entry)
    }

    /// Writes the committed cursor if it changed; a failed write is tried again later.
    async fn persist(&self) {
        let Some(cursor) = self.checkpoint.take_changed() else {
            return;
        };
        let file = self.file.clone();
        let stored = tokio::task::spawn_blocking(move || file.store(&cursor)).await;
        let error = match stored {
            Ok(Ok(())) => return,
            Ok(Err(error)) => error.to_string(),
            Err(error) => error.to_string(),
        };
        self.checkpoint.mark_changed();
        tracing::warn!(path = %self.file.path().display(), "cannot store the journal cursor: {error}");
    }
}

/// Ends the child (killing it on shutdown) and logs how it ended.
async fn stop(child: &mut Child, kill: bool) {
    if kill {
        let _ = child.start_kill();
    }
    match child.wait().await {
        Ok(status) if kill || status.success() => {}
        Ok(status) => tracing::warn!(%status, "journalctl ended, restarting"),
        Err(error) => tracing::warn!("journalctl ended, restarting: {error}"),
    }
}

/// Takes the first lines of `journalctl`'s stderr into the agent log.
async fn log_stderr(stderr: impl AsyncRead + Unpin) {
    let mut lines = LineReader::new(stderr, STDERR_LINE_BYTES);
    let mut logged = 0;
    while let Ok(Some(line)) = lines.next().await {
        if let Line::Text(bytes) = line
            && logged < STDERR_LINES
        {
            logged += 1;
            tracing::warn!(target: "journalctl", "{}", String::from_utf8_lossy(&bytes).trim_end());
        }
    }
}

enum Line {
    Text(Vec<u8>),
    TooLong,
}

/// Reads lines up to a length; longer ones are skipped up to their end. Cancel-safe: a partly
/// read line stays in the reader when `next` is dropped.
struct LineReader<R> {
    inner: BufReader<R>,
    line: Vec<u8>,
    skipping: bool,
    max: usize,
}

impl<R: AsyncRead + Unpin> LineReader<R> {
    fn new(inner: R, max: usize) -> Self {
        Self {
            inner: BufReader::new(inner),
            line: Vec::new(),
            skipping: false,
            max,
        }
    }

    /// The next line without its newline; `None` at the end.
    async fn next(&mut self) -> io::Result<Option<Line>> {
        loop {
            // The only await: nothing is consumed before it returns.
            let available = self.inner.fill_buf().await?;
            if available.is_empty() {
                if std::mem::take(&mut self.skipping) {
                    return Ok(Some(Line::TooLong));
                }
                if self.line.is_empty() {
                    return Ok(None);
                }
                return Ok(Some(Line::Text(std::mem::take(&mut self.line))));
            }
            let newline = available.iter().position(|b| *b == b'\n');
            let chunk = &available[..newline.unwrap_or(available.len())];
            if !self.skipping {
                if self.line.len() + chunk.len() > self.max {
                    self.skipping = true;
                    self.line = Vec::new();
                } else {
                    self.line.extend_from_slice(chunk);
                }
            }
            let used = newline.map_or(available.len(), |at| at + 1);
            self.inner.consume(used);
            if newline.is_some() {
                if std::mem::take(&mut self.skipping) {
                    return Ok(Some(Line::TooLong));
                }
                return Ok(Some(Line::Text(std::mem::take(&mut self.line))));
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    async fn read_all(input: &[u8], max: usize) -> Vec<Option<String>> {
        let mut reader = LineReader::new(input, max);
        let mut out = Vec::new();
        while let Some(line) = reader.next().await.unwrap() {
            out.push(match line {
                Line::Text(bytes) => Some(String::from_utf8(bytes).unwrap()),
                Line::TooLong => None,
            });
        }
        out
    }

    #[tokio::test]
    async fn line_reader_limits_and_resynchronizes() {
        let input = format!("short\n{}\nafter\nlast without newline", "x".repeat(100));
        assert_eq!(
            read_all(input.as_bytes(), 25).await,
            [
                Some("short".into()),
                None,
                Some("after".into()),
                Some("last without newline".into())
            ]
        );
        assert_eq!(read_all(b"", 10).await, Vec::<Option<String>>::new());
        assert_eq!(
            read_all(&[b'y'; 50], 10).await,
            [None],
            "too long up to the end"
        );
    }

    #[test]
    fn arguments_without_and_with_a_cursor() {
        let first = arguments(None);
        assert_eq!(first.last().map(String::as_str), Some("--lines=0"));
        assert!(
            !first
                .iter()
                .any(|a| a.contains("_CMDLINE") || a.starts_with("--cursor-file"))
        );
        let resumed = arguments(Some("s=a;i=1"));
        assert_eq!(
            resumed.last().map(String::as_str),
            Some("--after-cursor=s=a;i=1")
        );
        assert_eq!(first.len(), resumed.len());
    }
}
