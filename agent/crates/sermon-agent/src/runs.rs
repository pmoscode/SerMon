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
    ApplyUpdates, ConfirmSelfUpdate, HelperRequest, Reboot, RunPhase, RunResult, Scope, SelfUpdate,
    Status, helper_request::Operation, helper_response::Result as Answer,
};
use sermon_transport::proto::agent::v1::{CommandPhase, CommandResult, UpdateScope};
use sermon_transport::session::{CommandSink, HelperCommand};
use tokio::sync::{Notify, mpsc};
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
    /// Fetches the release `version` and puts it where the helper takes it (ADR 0036).
    fn stage(&self, _version: &str) -> impl Future<Output = Result<(), String>> + Send {
        std::future::ready(Ok(()))
    }
    /// Tells the helper that this new agent connected after its self-update `run_id`.
    fn confirm(&self, _run_id: &str) -> impl Future<Output = Result<(), String>> + Send {
        std::future::ready(Ok(()))
    }
}

/// The helper at its socket; releases for a self-update come through `fetcher` into `staging`.
#[derive(Debug, Clone)]
pub struct SocketHelper {
    pub socket: PathBuf,
    pub fetcher: Option<sermon_transport::release::ReleaseFetcher>,
    pub staging: PathBuf,
}

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
            version: s.version.clone(),
        }),
    };
    HelperRequest {
        operation: Some(operation),
    }
}

impl SocketHelper {
    fn start_blocking(&self, command: &HelperCommand) -> Start {
        match helper::call(&self.socket, &request(command), TIMEOUT) {
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
        match helper::call(&self.socket, &request, TIMEOUT) {
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

    async fn stage(&self, version: &str) -> Result<(), String> {
        let fetcher = self
            .fetcher
            .as_ref()
            .ok_or("no server to fetch the release from")?;
        let release = fetcher.fetch(version).await?;
        let dir = self.staging.clone();
        tokio::task::spawn_blocking(move || stage_files(&dir, &release))
            .await
            .map_err(|e| e.to_string())?
            .map_err(|e| format!("cannot stage the release: {e}"))
    }

    async fn confirm(&self, run_id: &str) -> Result<(), String> {
        let request = HelperRequest {
            operation: Some(Operation::ConfirmSelfUpdate(ConfirmSelfUpdate {
                run_id: run_id.to_owned(),
            })),
        };
        let socket = self.socket.clone();
        let answer = tokio::task::spawn_blocking(move || helper::call(&socket, &request, TIMEOUT))
            .await
            .map_err(|e| e.to_string())?
            .map_err(|e| e.to_string())?;
        if answer.status() == Status::Ok {
            Ok(())
        } else {
            Err(format!(
                "the root helper answered {}: {}",
                answer.status().as_str_name(),
                answer.reason
            ))
        }
    }
}

/// Writes the release where the helper reads it: the state directory's `self-update/`, only for
/// the agent's user (the helper copies and checks it as root).
fn stage_files(dir: &Path, release: &sermon_transport::release::Release) -> std::io::Result<()> {
    use std::os::unix::fs::{DirBuilderExt, OpenOptionsExt};
    std::fs::DirBuilder::new()
        .recursive(true)
        .mode(0o700)
        .create(dir)?;
    for (name, bytes) in [
        ("sermon-agent", release.binary.as_slice()),
        ("sermon-agent.minisig", release.signature.as_bytes()),
    ] {
        let tmp = dir.join(format!(".{name}.tmp"));
        let mut file = std::fs::OpenOptions::new()
            .write(true)
            .create(true)
            .truncate(true)
            .mode(0o600)
            .open(&tmp)?;
        std::io::Write::write_all(&mut file, bytes)?;
        file.sync_all()?;
        std::fs::rename(&tmp, dir.join(name))?;
    }
    Ok(())
}

/// The commands from the session, into the runner (never blocks the session), and the news
/// that a session stands.
#[derive(Debug, Clone)]
pub struct Inbox(mpsc::Sender<Item>, Arc<Notify>);

/// What the server sends the runner: a command, or a run it withdraws (M4-31).
#[derive(Debug)]
enum Item {
    Command(HelperCommand),
    Cancel(String),
}

/// The receiving end of an [`Inbox`], for [`run`].
#[derive(Debug)]
pub struct Commands {
    commands: mpsc::Receiver<Item>,
    connected: Arc<Notify>,
}

impl Inbox {
    /// An inbox and its receiving end for [`run`].
    #[must_use]
    pub fn new() -> (Self, Commands) {
        let (tx, rx) = mpsc::channel(64);
        let connected = Arc::new(Notify::new());
        (
            Self(tx, connected.clone()),
            Commands {
                commands: rx,
                connected,
            },
        )
    }
}

impl CommandSink for Inbox {
    fn connected(&self) {
        self.1.notify_one();
    }

    fn command(&self, command: HelperCommand) {
        if self.0.try_send(Item::Command(command)).is_err() {
            tracing::warn!(
                "too many commands of the root helper at once, one dropped (the server sends it again)"
            );
        }
    }

    fn cancel(&self, run_id: String) {
        if self.0.try_send(Item::Cancel(run_id)).is_err() {
            tracing::warn!(
                "too many commands of the root helper at once, a cancel dropped (the server sends it again)"
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

/// The runs taken, with the time of their last change, persisted as lines `run_id state unix`;
/// a self-update adds `self-update:<instance>`, the agent process that took it (ADR 0036).
#[derive(Debug)]
pub struct Book {
    path: PathBuf,
    runs: BTreeMap<String, (Entry, u64)>,
    /// The self-updates and the instance that took each.
    self_updates: BTreeMap<String, String>,
    /// This agent process.
    instance: String,
}

fn unix(t: SystemTime) -> u64 {
    t.duration_since(SystemTime::UNIX_EPOCH)
        .map_or(0, |d| d.as_secs())
}

impl Book {
    /// Reads the book at `path`; a missing or broken file is an empty book (lines that don't
    /// parse are dropped).
    #[must_use]
    pub fn open(path: &Path, instance: &str) -> Self {
        let mut self_updates = BTreeMap::new();
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
                if !uuid_like(id) {
                    return None;
                }
                if let Some(owner) = parts.next().and_then(|p| p.strip_prefix("self-update:")) {
                    self_updates.insert(id.to_owned(), owner.to_owned());
                }
                Some((id.to_owned(), (entry, at)))
            })
            .collect();
        Self {
            path: path.to_owned(),
            runs,
            self_updates,
            instance: instance
                .chars()
                .filter(char::is_ascii_alphanumeric)
                .collect(),
        }
    }

    /// The self-updates still going that another agent process took: this one is the new agent.
    fn to_confirm(&self) -> Vec<String> {
        self.running()
            .into_iter()
            .filter(|id| {
                self.self_updates
                    .get(id)
                    .is_some_and(|o| *o != self.instance)
            })
            .collect()
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

    /// Forgets a run that was taken too early (a busy helper starts it later).
    fn forget(&mut self, id: &str) {
        self.runs.remove(id);
        self.self_updates.remove(id);
        self.save();
    }

    /// Records the run and writes the book (atomically), forgetting old ended runs.
    fn set(&mut self, id: &str, entry: Entry, now: SystemTime) {
        self.runs.insert(id.to_owned(), (entry, unix(now)));
        let horizon = unix(now).saturating_sub(KEEP.as_secs());
        self.runs
            .retain(|_, (e, at)| *e == Entry::Running || *at >= horizon);
        self.self_updates.retain(|id, _| self.runs.contains_key(id));
        while self.runs.len() > MAX_RUNS {
            let oldest = self
                .runs
                .iter()
                .filter(|(_, (e, _))| *e == Entry::Ended)
                .min_by_key(|(_, (_, at))| *at)
                .map(|(id, _)| id.clone());
            match oldest {
                Some(id) => {
                    self.runs.remove(&id);
                    self.self_updates.remove(&id);
                }
                None => break,
            }
        }
        self.save();
    }

    /// Writes the book atomically.
    fn save(&self) {
        let mut text = String::new();
        for (id, (e, at)) in &self.runs {
            let state = if *e == Entry::Running {
                "running"
            } else {
                "ended"
            };
            match self.self_updates.get(id) {
                Some(owner) => writeln!(text, "{id} {state} {at} self-update:{owner}"),
                None => writeln!(text, "{id} {state} {at}"),
            }
            .ok();
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

/// How long from its arrival the server lets the run start (M4-31); `None` from an older server.
fn valid_for(command: &HelperCommand) -> Option<Duration> {
    let seconds = match command {
        HelperCommand::Update(c) => c.valid_for_seconds,
        HelperCommand::Reboot(c) => c.valid_for_seconds,
        HelperCommand::SelfUpdate(c) => c.valid_for_seconds,
    };
    (seconds > 0).then(|| Duration::from_secs(seconds.into()))
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
    /// The release of a self-update is staged already: a retry doesn't fetch it again.
    staged: bool,
}

/// Runs the commands from `inbox` through `helper` until `shutdown`, the results into `sink`.
#[expect(
    clippy::too_many_arguments,
    reason = "the ports of the runner, wired once"
)]
pub async fn run<H: Helper>(
    helper: Arc<H>,
    mut book: Book,
    inbox: Commands,
    mut sink: impl FnMut(CommandResult) + Send,
    redact: impl Fn(&str) -> String + Send,
    now: impl Fn() -> SystemTime + Send,
    ended: Arc<Notify>,
    schedule: Schedule,
    shutdown: CancellationToken,
) {
    let Commands {
        commands: mut inbox,
        connected,
    } = inbox;
    let mut waiting: VecDeque<Waiting> = VecDeque::new();
    let mut poll = tokio::time::interval(schedule.poll);
    poll.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Delay);
    loop {
        let retry_at = waiting.iter().map(|w| w.next).min();
        tokio::select! {
            () = shutdown.cancelled() => return,
            () = connected.notified() => {
                for id in book.to_confirm() {
                    match helper.confirm(&id).await {
                        Ok(()) => tracing::info!(run_id = %id, "the new agent confirmed its self-update"),
                        Err(why) => tracing::warn!(run_id = %id, "cannot confirm the self-update: {why}"),
                    }
                }
            }
            item = inbox.recv() => {
                let command = match item {
                    None => return,
                    Some(Item::Cancel(id)) => {
                        withdraw(&id, &mut book, &mut waiting, &mut sink, &now);
                        continue;
                    }
                    Some(Item::Command(command)) => command,
                };
                let id = run_id(&command).to_owned();
                if book.knows(&id) || waiting.iter().any(|w| run_id(&w.command) == id) {
                    tracing::debug!(run_id = %id, "run taken before, not started again");
                    continue;
                }
                let first = Instant::now();
                let until = first + valid_for(&command).map_or(schedule.busy_for, |v| v.min(schedule.busy_for));
                let job = Waiting { command, next: first, until, staged: false };
                start(&*helper, &mut book, &mut sink, &now, job, &mut waiting, schedule).await;
            }
            () = async { tokio::time::sleep_until(retry_at.unwrap_or_else(Instant::now)).await }, if retry_at.is_some() => {
                let due: Vec<Waiting> = {
                    let at = Instant::now();
                    let (due, later): (Vec<_>, Vec<_>) = waiting.drain(..).partition(|w| w.next <= at);
                    waiting.extend(later);
                    due
                };
                for w in due {
                    start(&*helper, &mut book, &mut sink, &now, w, &mut waiting, schedule).await;
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
                        // The update information follows at once (M4-28): the package manager is done.
                        ended.notify_one();
                    }
                }
            }
        }
    }
}

/// Withdraws a run (M4-31): one waiting for the helper is dropped, one not taken yet (lost by a
/// restart, the server sends the cancel again) is booked as ended so it never starts; both are
/// reported refused. One taken before goes on.
fn withdraw(
    id: &str,
    book: &mut Book,
    waiting: &mut VecDeque<Waiting>,
    sink: &mut (impl FnMut(CommandResult) + Send),
    now: &(impl Fn() -> SystemTime + Send),
) {
    let before = waiting.len();
    waiting.retain(|w| run_id(&w.command) != id);
    if waiting.len() == before && book.knows(id) {
        tracing::info!(run_id = %id, "a run withdrawn by the server goes on: it started already");
        return;
    }
    tracing::info!(run_id = %id, "run withdrawn by the server, not started");
    sink(result(id, CommandPhase::Refused, "cancelled on the server"));
    book.set(id, Entry::Ended, now());
}

/// Starts a command; a busy helper puts it back on `waiting` until its `until`.
async fn start<H: Helper>(
    helper: &H,
    book: &mut Book,
    sink: &mut (impl FnMut(CommandResult) + Send),
    now: &(impl Fn() -> SystemTime + Send),
    job: Waiting,
    waiting: &mut VecDeque<Waiting>,
    schedule: Schedule,
) {
    let Waiting {
        command,
        until,
        staged,
        ..
    } = job;
    let id = run_id(&command).to_owned();
    if let HelperCommand::SelfUpdate(s) = &command
        && !staged
        && let Err(why) = helper.stage(&s.version).await
    {
        tracing::warn!(run_id = %id, "cannot fetch the release for the self-update: {why}");
        sink(result(
            &id,
            CommandPhase::Failed,
            &format!("cannot fetch the release: {why}"),
        ));
        book.set(&id, Entry::Ended, now());
        return;
    }
    // Nothing starts after the server's deadline (M4-31): a slow download of the release.
    if Instant::now() >= until {
        let reason = "the command's time ran out before the run could start";
        tracing::warn!(run_id = %id, "run of the root helper not started: {reason}");
        sink(result(&id, CommandPhase::Failed, reason));
        book.set(&id, Entry::Ended, now());
        return;
    }
    if let HelperCommand::SelfUpdate(_) = &command {
        // Before the helper replaces this process: the new agent finds the run and confirms it.
        book.self_updates.insert(id.clone(), book.instance.clone());
        book.set(&id, Entry::Running, now());
    }
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
            book.forget(&id);
            waiting.push_back(Waiting {
                command,
                next: Instant::now() + schedule.retry,
                until,
                staged: true,
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
