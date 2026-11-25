//! The server's commands for the root helper (KONZEPT §14.2, M4-12): install updates, reboot,
//! replace the agent. The helper runs them as runs of their own; this module starts them, asks
//! for their results and buffers a `CommandResult` per phase.
//!
//! A run ID is taken once: the run book (`runs` in the state directory) remembers the runs and
//! whether they ended. A command the server sends again after a reconnect starts nothing; after a
//! restart of the agent (for instance by its own package update) the runs still going are asked
//! for again. Before it is sent, a run's output passes the organization's redaction.

use std::collections::{BTreeMap, VecDeque};
use std::fmt::Write as _;
use std::future::Future;
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::{Duration, SystemTime};

use prost_types::Timestamp;
use sermon_helper_proto::v1::{
    ApplyUpdates, HelperRequest, Reboot, RunPhase, RunResult, Scope, SelfUpdate, Status,
    helper_request::Operation, helper_response::Result as Answer,
};
use sermon_transport::proto::agent::v1::{CommandPhase, CommandResult, UpdateScope};
use sermon_transport::session::{CommandSink, HelperCommand};
use tokio::sync::mpsc;
use tokio::time::Instant;
use tokio_util::sync::CancellationToken;

use crate::helper::{self, HelperError};

/// At most this much output goes to the server (the protocol's limit).
pub const MAX_OUTPUT: usize = 256 << 10;
/// Runs the book keeps at most.
pub const MAX_RUNS: usize = 500;
/// Ended runs are forgotten after this (the helper keeps results 7 days).
pub const KEEP: Duration = Duration::from_hours(8 * 24);
const MAX_REASON: usize = 1024;
const TIMEOUT: Duration = Duration::from_secs(30);

/// When the runner asks.
#[derive(Debug, Clone, Copy)]
pub struct Schedule {
    /// How often runs still going are asked for their result.
    pub poll: Duration,
    /// After a busy helper (another run holds its lock).
    pub retry: Duration,
    /// How long a busy helper is asked again before the command fails.
    pub busy_for: Duration,
}

impl Default for Schedule {
    fn default() -> Self {
        Self {
            poll: Duration::from_secs(10),
            retry: Duration::from_secs(30),
            busy_for: Duration::from_mins(15),
        }
    }
}

/// How the helper took a start.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Start {
    Started,
    Refused(String),
    Unsupported(String),
    Busy(String),
    Failed(String),
}

/// What the helper says of a run.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Poll {
    Running,
    Done {
        exit_code: i32,
        output: Vec<u8>,
        truncated: bool,
        started: i64,
        ended: i64,
    },
    /// The helper knows the run no longer.
    Unknown,
    /// No answer now (the helper restarts); asked again later.
    Unreachable(String),
}

/// The root helper, or a fake in tests.
pub trait Helper: Send + Sync + 'static {
    fn start(&self, command: &HelperCommand) -> impl Future<Output = Start> + Send;
    fn result(&self, run_id: &str) -> impl Future<Output = Poll> + Send;
}

/// The helper at its socket.
#[derive(Debug, Clone)]
pub struct SocketHelper(pub PathBuf);

fn request(command: &HelperCommand) -> HelperRequest {
    let operation = match command {
        HelperCommand::Update(u) => Operation::ApplyUpdates(ApplyUpdates {
            run_id: u.run_id.clone(),
            scope: match u.scope() {
                UpdateScope::Security => Scope::Security,
                UpdateScope::All => Scope::All,
                UpdateScope::Unspecified => Scope::Unspecified,
            }
            .into(),
        }),
        HelperCommand::Reboot(r) => Operation::Reboot(Reboot {
            run_id: r.run_id.clone(),
            reason: r.reason.clone(),
        }),
        HelperCommand::SelfUpdate(s) => Operation::SelfUpdate(SelfUpdate {
            run_id: s.run_id.clone(),
        }),
    };
    HelperRequest {
        operation: Some(operation),
    }
}

impl SocketHelper {
    fn start_blocking(&self, command: &HelperCommand) -> Start {
        match helper::call(&self.0, &request(command), TIMEOUT) {
            Err(e @ (HelperError::Unreachable(_) | HelperError::NoAnswer)) => {
                Start::Unsupported(e.to_string())
            }
            Err(e @ HelperError::Malformed(_)) => Start::Failed(e.to_string()),
            Ok(r) => {
                let reason = format!(
                    "the root helper answered {}: {}",
                    r.status().as_str_name(),
                    r.reason
                );
                match r.status() {
                    Status::Ok => Start::Started,
                    Status::Refused => Start::Refused(reason),
                    Status::Unsupported => Start::Unsupported(reason),
                    Status::Busy => Start::Busy(reason),
                    Status::Failed | Status::Unspecified => Start::Failed(reason),
                }
            }
        }
    }

    fn result_blocking(&self, run_id: &str) -> Poll {
        let request = HelperRequest {
            operation: Some(Operation::RunResult(RunResult {
                run_id: run_id.to_owned(),
            })),
        };
        match helper::call(&self.0, &request, TIMEOUT) {
            Err(e) => Poll::Unreachable(e.to_string()),
            Ok(r) if r.status() != Status::Ok => Poll::Unreachable(format!(
                "the root helper answered {}: {}",
                r.status().as_str_name(),
                r.reason
            )),
            Ok(r) => match r.result {
                Some(Answer::Run(run)) => match run.phase() {
                    RunPhase::Running => Poll::Running,
                    RunPhase::Unspecified => Poll::Unknown,
                    RunPhase::Done => {
                        let result = run.result.unwrap_or_default();
                        Poll::Done {
                            exit_code: result.exit_code,
                            output: result.output,
                            truncated: result.truncated,
                            started: run.started_unix,
                            ended: run.ended_unix,
                        }
                    }
                },
                _ => Poll::Unreachable("the root helper sent no run".into()),
            },
        }
    }
}

impl Helper for SocketHelper {
    async fn start(&self, command: &HelperCommand) -> Start {
        let (helper, command) = (self.clone(), command.clone());
        tokio::task::spawn_blocking(move || helper.start_blocking(&command))
            .await
            .unwrap_or_else(|e| Start::Failed(e.to_string()))
    }

    async fn result(&self, run_id: &str) -> Poll {
        let (helper, run_id) = (self.clone(), run_id.to_owned());
        tokio::task::spawn_blocking(move || helper.result_blocking(&run_id))
            .await
            .unwrap_or_else(|e| Poll::Unreachable(e.to_string()))
    }
}

/// The commands from the session, into the runner (never blocks the session).
#[derive(Debug, Clone)]
pub struct Inbox(mpsc::Sender<HelperCommand>);

impl Inbox {
    /// An inbox and its receiving end for [`run`].
    #[must_use]
    pub fn new() -> (Self, mpsc::Receiver<HelperCommand>) {
        let (tx, rx) = mpsc::channel(64);
        (Self(tx), rx)
    }
}

impl CommandSink for Inbox {
    fn command(&self, command: HelperCommand) {
        if self.0.try_send(command).is_err() {
            tracing::warn!(
                "too many commands of the root helper at once, one dropped (the server sends it again)"
            );
        }
    }
}

/// Whether a run of the book still goes.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Entry {
    Running,
    Ended,
}

/// The runs taken, with the time of their last change, persisted as lines `run_id state unix`.
#[derive(Debug)]
pub struct Book {
    path: PathBuf,
    runs: BTreeMap<String, (Entry, u64)>,
}

fn unix(t: SystemTime) -> u64 {
    t.duration_since(SystemTime::UNIX_EPOCH)
        .map_or(0, |d| d.as_secs())
}

impl Book {
    /// Reads the book at `path`; a missing or broken file is an empty book (lines that don't
    /// parse are dropped).
    #[must_use]
    pub fn open(path: &Path) -> Self {
        let runs = std::fs::read_to_string(path)
            .unwrap_or_default()
            .lines()
            .filter_map(|line| {
                let mut parts = line.split(' ');
                let (id, state, at) = (parts.next()?, parts.next()?, parts.next()?.parse().ok()?);
                let entry = match state {
                    "running" => Entry::Running,
                    "ended" => Entry::Ended,
                    _ => return None,
                };
                uuid_like(id).then(|| (id.to_owned(), (entry, at)))
            })
            .collect();
        Self {
            path: path.to_owned(),
            runs,
        }
    }

    fn running(&self) -> Vec<String> {
        self.runs
            .iter()
            .filter(|(_, (e, _))| *e == Entry::Running)
            .map(|(id, _)| id.clone())
            .collect()
    }

    fn knows(&self, id: &str) -> bool {
        self.runs.contains_key(id)
    }

    /// Records the run and writes the book (atomically), forgetting old ended runs.
    fn set(&mut self, id: &str, entry: Entry, now: SystemTime) {
        self.runs.insert(id.to_owned(), (entry, unix(now)));
        let horizon = unix(now).saturating_sub(KEEP.as_secs());
        self.runs
            .retain(|_, (e, at)| *e == Entry::Running || *at >= horizon);
        while self.runs.len() > MAX_RUNS {
            let oldest = self
                .runs
                .iter()
                .filter(|(_, (e, _))| *e == Entry::Ended)
                .min_by_key(|(_, (_, at))| *at)
                .map(|(id, _)| id.clone());
            match oldest {
                Some(id) => self.runs.remove(&id),
                None => break,
            };
        }
        let mut text = String::new();
        for (id, (e, at)) in &self.runs {
            let state = if *e == Entry::Running {
                "running"
            } else {
                "ended"
            };
            let _ = writeln!(text, "{id} {state} {at}");
        }
        let tmp = self.path.with_extension("tmp");
        if let Err(error) =
            std::fs::write(&tmp, text).and_then(|()| std::fs::rename(&tmp, &self.path))
        {
            tracing::warn!("cannot write the run book: {error}");
        }
    }
}

/// A lowercase UUID as the protocol has it.
fn uuid_like(s: &str) -> bool {
    s.len() == 36
        && s.bytes().enumerate().all(|(i, b)| {
            if [8, 13, 18, 23].contains(&i) {
                b == b'-'
            } else {
                b.is_ascii_digit() || (b'a'..=b'f').contains(&b)
            }
        })
}

fn run_id(command: &HelperCommand) -> &str {
    match command {
        HelperCommand::Update(u) => &u.run_id,
        HelperCommand::Reboot(r) => &r.run_id,
        HelperCommand::SelfUpdate(s) => &s.run_id,
    }
}

fn timestamp(unix: i64) -> Option<Timestamp> {
    (unix > 0).then_some(Timestamp {
        seconds: unix,
        nanos: 0,
    })
}

fn bounded(s: &str, max: usize) -> String {
    let mut end = s.len().min(max);
    while !s.is_char_boundary(end) {
        end -= 1;
    }
    s[..end].to_owned()
}

/// The end of a redacted output, at most [`MAX_OUTPUT`]; true if it was cut.
fn tail(redacted: &str) -> (String, bool) {
    if redacted.len() <= MAX_OUTPUT {
        return (redacted.to_owned(), false);
    }
    let mut start = redacted.len() - MAX_OUTPUT;
    while !redacted.is_char_boundary(start) {
        start += 1;
    }
    (redacted[start..].to_owned(), true)
}

fn result(run_id: &str, phase: CommandPhase, reason: &str) -> CommandResult {
    CommandResult {
        run_id: run_id.to_owned(),
        phase: phase.into(),
        reason: bounded(reason, MAX_REASON),
        ..CommandResult::default()
    }
}

/// A command the helper was busy for, asked again from `next` until `until`.
#[derive(Debug)]
struct Waiting {
    command: HelperCommand,
    next: Instant,
    until: Instant,
}

/// Runs the commands from `inbox` through `helper` until `shutdown`, the results into `sink`.
#[expect(
    clippy::too_many_arguments,
    reason = "the ports of the runner, wired once"
)]
pub async fn run<H: Helper>(
    helper: Arc<H>,
    mut book: Book,
    mut inbox: mpsc::Receiver<HelperCommand>,
    mut sink: impl FnMut(CommandResult) + Send,
    redact: impl Fn(&str) -> String + Send,
    now: impl Fn() -> SystemTime + Send,
    schedule: Schedule,
    shutdown: CancellationToken,
) {
    let mut waiting: VecDeque<Waiting> = VecDeque::new();
    let mut poll = tokio::time::interval(schedule.poll);
    poll.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Delay);
    loop {
        let retry_at = waiting.iter().map(|w| w.next).min();
        tokio::select! {
            () = shutdown.cancelled() => return,
            command = inbox.recv() => {
                let Some(command) = command else { return };
                let id = run_id(&command).to_owned();
                if book.knows(&id) || waiting.iter().any(|w| run_id(&w.command) == id) {
                    tracing::debug!(run_id = %id, "run taken before, not started again");
                    continue;
                }
                let first = Instant::now();
                start(&*helper, &mut book, &mut sink, &now, command, first + schedule.busy_for, &mut waiting, schedule).await;
            }
            () = async { tokio::time::sleep_until(retry_at.unwrap_or_else(Instant::now)).await }, if retry_at.is_some() => {
                let due: Vec<Waiting> = {
                    let at = Instant::now();
                    let (due, later): (Vec<_>, Vec<_>) = waiting.drain(..).partition(|w| w.next <= at);
                    waiting.extend(later);
                    due
                };
                for w in due {
                    start(&*helper, &mut book, &mut sink, &now, w.command, w.until, &mut waiting, schedule).await;
                }
            }
            _ = poll.tick() => {
                for id in book.running() {
                    let finished = match helper.result(&id).await {
                        Poll::Running | Poll::Unreachable(_) => None,
                        Poll::Unknown => Some(result(&id, CommandPhase::Lost, "the root helper knows the run no longer")),
                        Poll::Done { exit_code, output, truncated, started, ended } => {
                            let (output, cut) = tail(&redact(&String::from_utf8_lossy(&output)));
                            let phase = if exit_code == 0 { CommandPhase::Succeeded } else { CommandPhase::Failed };
                            let mut r = result(&id, phase, "");
                            r.exit_code = exit_code;
                            r.output = output;
                            r.output_truncated = truncated || cut;
                            r.started_at = timestamp(started);
                            r.ended_at = timestamp(ended);
                            Some(r)
                        }
                    };
                    if let Some(r) = finished {
                        tracing::info!(run_id = %id, phase = r.phase, exit_code = r.exit_code, "run of the root helper ended");
                        sink(r);
                        book.set(&id, Entry::Ended, now());
                    }
                }
            }
        }
    }
}

/// Starts a command; a busy helper puts it back on `waiting` until `until`.
#[expect(
    clippy::too_many_arguments,
    reason = "the runner's state, passed down once"
)]
async fn start<H: Helper>(
    helper: &H,
    book: &mut Book,
    sink: &mut (impl FnMut(CommandResult) + Send),
    now: &(impl Fn() -> SystemTime + Send),
    command: HelperCommand,
    until: Instant,
    waiting: &mut VecDeque<Waiting>,
    schedule: Schedule,
) {
    let id = run_id(&command).to_owned();
    let (phase, reason) = match helper.start(&command).await {
        Start::Started => {
            book.set(&id, Entry::Running, now());
            let mut r = result(&id, CommandPhase::Started, "");
            r.started_at = Some(now().into());
            tracing::info!(run_id = %id, "run of the root helper started");
            sink(r);
            return;
        }
        Start::Busy(why) if Instant::now() + schedule.retry < until => {
            tracing::info!(run_id = %id, "the root helper is busy, starting the run later: {why}");
            waiting.push_back(Waiting {
                command,
                next: Instant::now() + schedule.retry,
                until,
            });
            return;
        }
        Start::Busy(why) => (
            CommandPhase::Failed,
            format!("the root helper stayed busy: {why}"),
        ),
        Start::Refused(why) => (CommandPhase::Refused, why),
        Start::Unsupported(why) => (CommandPhase::Unsupported, why),
        Start::Failed(why) => (CommandPhase::Failed, why),
    };
    tracing::warn!(run_id = %id, "run of the root helper not started: {reason}");
    sink(result(&id, phase, &reason));
    book.set(&id, Entry::Ended, now());
}

#[cfg(test)]
mod tests;
