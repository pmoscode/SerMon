use std::sync::Mutex;
use std::sync::atomic::{AtomicUsize, Ordering};

use sermon_transport::proto::agent::v1::{RebootNode, RunUpdate};

use super::*;

const ID: &str = "0193f5a0-0000-7000-8000-000000000001";
const OTHER: &str = "0193f5a0-0000-7000-8000-000000000002";

fn update(id: &str) -> HelperCommand {
    HelperCommand::Update(RunUpdate {
        run_id: id.into(),
        scope: UpdateScope::All.into(),
    })
}

/// A helper with a script: what a start gives, and the polls of each run in turn.
#[derive(Debug, Default)]
struct Fake {
    starts: Mutex<Vec<String>>,
    start: Mutex<VecDeque<Start>>,
    polls: Mutex<BTreeMap<String, VecDeque<Poll>>>,
    asked: AtomicUsize,
    /// Versions staged for a self-update; `stage_fails` makes the next one fail.
    staged: Mutex<Vec<String>>,
    stage_fails: Mutex<Option<String>>,
    confirmed: Mutex<Vec<String>>,
}

impl Fake {
    fn with(start: Vec<Start>, polls: Vec<(&str, Vec<Poll>)>) -> Arc<Self> {
        Arc::new(Self {
            start: Mutex::new(start.into()),
            polls: Mutex::new(
                polls
                    .into_iter()
                    .map(|(id, p)| (id.to_owned(), p.into()))
                    .collect(),
            ),
            ..Self::default()
        })
    }
}

impl Helper for Fake {
    fn start(&self, command: &HelperCommand) -> impl Future<Output = Start> + Send {
        self.starts.lock().unwrap().push(run_id(command).to_owned());
        let s = self
            .start
            .lock()
            .unwrap()
            .pop_front()
            .unwrap_or(Start::Started);
        std::future::ready(s)
    }

    fn result(&self, run_id: &str) -> impl Future<Output = Poll> + Send {
        self.asked.fetch_add(1, Ordering::SeqCst);
        let mut polls = self.polls.lock().unwrap();
        let queue = polls.entry(run_id.to_owned()).or_default();
        let p = if queue.len() > 1 {
            queue.pop_front().unwrap()
        } else {
            queue.front().cloned().unwrap_or(Poll::Running)
        };
        std::future::ready(p)
    }

    fn stage(&self, version: &str) -> impl Future<Output = Result<(), String>> + Send {
        self.staged.lock().unwrap().push(version.to_owned());
        std::future::ready(self.stage_fails.lock().unwrap().take().map_or(Ok(()), Err))
    }

    fn confirm(&self, run_id: &str) -> impl Future<Output = Result<(), String>> + Send {
        self.confirmed.lock().unwrap().push(run_id.to_owned());
        std::future::ready(Ok(()))
    }
}

struct Running {
    inbox: Inbox,
    results: Arc<Mutex<Vec<CommandResult>>>,
    ended: Arc<Notify>,
    shutdown: CancellationToken,
    task: tokio::task::JoinHandle<()>,
}

fn start_runner(fake: Arc<Fake>, book: Book) -> Running {
    let (inbox, rx) = Inbox::new();
    let results = Arc::new(Mutex::new(Vec::new()));
    let shutdown = CancellationToken::new();
    let sink = results.clone();
    let ended = Arc::new(Notify::new());
    let task = tokio::spawn(run(
        fake,
        book,
        rx,
        move |r| sink.lock().unwrap().push(r),
        |text: &str| text.replace("hunter2", "[REDACTED]"),
        || SystemTime::UNIX_EPOCH + Duration::from_secs(1_795_000_000),
        ended.clone(),
        Schedule::default(),
        shutdown.clone(),
    ));
    Running {
        inbox,
        results,
        ended,
        shutdown,
        task,
    }
}

impl Running {
    fn phases(&self) -> Vec<(String, CommandPhase)> {
        self.results
            .lock()
            .unwrap()
            .iter()
            .map(|r| (r.run_id.clone(), r.phase()))
            .collect()
    }

    async fn stop(self) {
        self.shutdown.cancel();
        self.task.await.unwrap();
    }
}

fn book(dir: &tempfile::TempDir) -> Book {
    Book::open(&dir.path().join("runs"), "first")
}

/// The book of the agent process after a restart.
fn book_after_restart(dir: &tempfile::TempDir) -> Book {
    Book::open(&dir.path().join("runs"), "second")
}

// A run starts once, reports its start and, polled, its end with the redacted output; a
// command sent again (a reconnect) starts nothing.
#[tokio::test(start_paused = true)]
async fn runs_a_command_once_and_reports_its_phases() {
    let dir = tempfile::tempdir().unwrap();
    let done = Poll::Done {
        exit_code: 0,
        output: b"Setting up openssl\npassword=hunter2\n".to_vec(),
        truncated: false,
        started: 1_795_000_000,
        ended: 1_795_000_060,
    };
    let fake = Fake::with(vec![], vec![(ID, vec![Poll::Running, done])]);
    let r = start_runner(fake.clone(), book(&dir));
    r.inbox.command(update(ID));
    tokio::time::sleep(Duration::from_secs(1)).await;
    r.inbox.command(update(ID));
    // M4-28: the end wakes the update information.
    let woken = tokio::time::timeout(Duration::from_secs(25), r.ended.notified()).await;
    assert!(woken.is_ok(), "the end of the run woke nothing");
    tokio::time::sleep(Duration::from_secs(25)).await;
    assert_eq!(*fake.starts.lock().unwrap(), [ID], "started once");
    assert_eq!(
        r.phases(),
        [
            (ID.into(), CommandPhase::Started),
            (ID.into(), CommandPhase::Succeeded)
        ]
    );
    let end = r.results.lock().unwrap()[1].clone();
    assert_eq!(end.output, "Setting up openssl\npassword=[REDACTED]\n");
    assert_eq!((end.exit_code, end.output_truncated), (0, false));
    assert_eq!(end.started_at.unwrap().seconds, 1_795_000_000);
    assert_eq!(end.ended_at.unwrap().seconds, 1_795_000_060);
    // Ended: the book knows it, a later resend starts nothing and nothing is polled.
    let asked = fake.asked.load(Ordering::SeqCst);
    r.inbox.command(update(ID));
    tokio::time::sleep(Duration::from_secs(60)).await;
    assert_eq!(fake.starts.lock().unwrap().len(), 1);
    assert_eq!(fake.asked.load(Ordering::SeqCst), asked);
    r.stop().await;
    let text = std::fs::read_to_string(dir.path().join("runs")).unwrap();
    assert!(text.starts_with(&format!("{ID} ended ")), "{text}");
}

// The agent restarts mid-run (its own package update): the book still knows the run, the new
// runner asks the helper for it and reports its end; a run the helper forgot is lost.
#[tokio::test(start_paused = true)]
async fn after_a_restart_it_asks_for_the_runs_still_going() {
    let dir = tempfile::tempdir().unwrap();
    let first = start_runner(Fake::with(vec![], vec![]), book(&dir));
    first.inbox.command(update(ID));
    first.inbox.command(update(OTHER));
    tokio::time::sleep(Duration::from_secs(1)).await;
    first.stop().await;
    let failed = Poll::Done {
        exit_code: 100,
        output: b"E: dpkg was interrupted".to_vec(),
        truncated: true,
        started: 1,
        ended: 2,
    };
    let fake = Fake::with(
        vec![],
        vec![(ID, vec![failed]), (OTHER, vec![Poll::Unknown])],
    );
    let second = start_runner(fake.clone(), book_after_restart(&dir));
    tokio::time::sleep(Duration::from_secs(1)).await;
    let mut phases = second.phases();
    phases.sort();
    assert_eq!(
        phases,
        [
            (ID.into(), CommandPhase::Failed),
            (OTHER.into(), CommandPhase::Lost)
        ]
    );
    assert!(
        fake.starts.lock().unwrap().is_empty(),
        "nothing started again"
    );
    let failed = second
        .results
        .lock()
        .unwrap()
        .iter()
        .find(|r| r.run_id == ID)
        .cloned()
        .unwrap();
    assert!(failed.exit_code == 100 && failed.output_truncated);
    second.stop().await;
}

// The host's veto, no helper and a failed start end the run with their reason; a busy helper is
// asked again every 30 s and, after 15 minutes, the run fails.
#[tokio::test(start_paused = true)]
async fn a_veto_or_a_busy_helper_ends_the_run_with_a_reason() {
    let dir = tempfile::tempdir().unwrap();
    let mut busy = vec![Start::Busy("a run".into()); 3];
    busy.splice(
        0..0,
        [
            Start::Refused("allow_remote_reboot is false".into()),
            Start::Unsupported("no root helper".into()),
        ],
    );
    let fake = Fake::with(busy, vec![]);
    let r = start_runner(fake.clone(), book(&dir));
    r.inbox.command(HelperCommand::Reboot(RebootNode {
        run_id: ID.into(),
        reason: "updates".into(),
    }));
    r.inbox.command(update(OTHER));
    let third = "0193f5a0-0000-7000-8000-000000000003";
    r.inbox.command(update(third));
    tokio::time::sleep(Duration::from_secs(1)).await;
    assert_eq!(
        r.phases(),
        [
            (ID.into(), CommandPhase::Refused),
            (OTHER.into(), CommandPhase::Unsupported)
        ]
    );
    assert!(
        r.results.lock().unwrap()[0]
            .reason
            .contains("allow_remote_reboot")
    );
    // The third: busy, busy, busy, then the fake starts it.
    tokio::time::sleep(Duration::from_secs(61)).await;
    assert_eq!(
        fake.starts
            .lock()
            .unwrap()
            .iter()
            .filter(|s| *s == third)
            .count(),
        3
    );
    tokio::time::sleep(Duration::from_secs(30)).await;
    assert_eq!(
        r.phases().last().unwrap(),
        &(third.to_owned(), CommandPhase::Started)
    );
    r.stop().await;

    let dir = tempfile::tempdir().unwrap();
    let always = Fake::with(vec![Start::Busy("a run".into()); 40], vec![]);
    let r = start_runner(always.clone(), book(&dir));
    r.inbox.command(update(ID));
    tokio::time::sleep(Duration::from_mins(16)).await;
    assert_eq!(r.phases(), [(ID.into(), CommandPhase::Failed)]);
    assert!(r.results.lock().unwrap()[0].reason.contains("stayed busy"));
    assert!(always.starts.lock().unwrap().len() <= 31);
    r.stop().await;
}

#[test]
fn the_output_is_cut_from_the_front() {
    let long = format!("ä{}", "x".repeat(MAX_OUTPUT));
    let (out, cut) = tail(&long);
    assert!(cut && out.len() == MAX_OUTPUT && out.chars().all(|c| c == 'x'));
    // Byte 1 is inside the ä: the tail starts after it.
    let odd = format!("ä{}", "x".repeat(MAX_OUTPUT - 1));
    let (out, cut) = tail(&odd);
    assert!(
        cut && out.len() == MAX_OUTPUT - 1 && !out.contains('ä'),
        "{}",
        out.len()
    );
    assert_eq!(tail("short"), ("short".into(), false));
}

#[test]
fn the_book_survives_broken_lines_and_stays_bounded() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("runs");
    std::fs::write(
        &path,
        format!("{ID} running 5\nnot a line\n../../etc running 1\n{OTHER} done 3\n"),
    )
    .unwrap();
    let mut b = Book::open(&path, "first");
    assert_eq!(b.running(), [ID]);
    assert!(!b.knows(OTHER) && !b.knows("../../etc"));
    let now = SystemTime::UNIX_EPOCH + Duration::from_secs(1_795_000_000);
    for i in 0..MAX_RUNS + 10 {
        b.set(
            &format!("0193f5a0-0000-7000-9000-{i:012}"),
            Entry::Ended,
            now,
        );
    }
    assert_eq!(b.runs.len(), MAX_RUNS);
    assert!(
        b.knows(ID) && b.running() == [ID],
        "a run still going is never dropped"
    );
    // Ended runs older than KEEP go.
    let later = now + KEEP + Duration::from_secs(1);
    b.set(OTHER, Entry::Ended, later);
    assert_eq!(b.runs.len(), 2);
    let reread = Book::open(&path, "first");
    assert!(reread.knows(ID) && reread.knows(OTHER));
}

fn self_update(id: &str) -> HelperCommand {
    HelperCommand::SelfUpdate(sermon_transport::proto::agent::v1::SelfUpdate {
        run_id: id.into(),
        version: "1.1.0".into(),
    })
}

// ADR 0036: the release is staged before the helper starts the run, and the run is in the book
// before the helper replaces this process. The new agent (another process) confirms it once its
// session stands; the old one never does.
#[tokio::test(start_paused = true)]
async fn a_self_update_is_staged_and_confirmed_by_the_new_agent() {
    let dir = tempfile::tempdir().unwrap();
    let fake = Fake::with(vec![], vec![]);
    let old = start_runner(fake.clone(), book(&dir));
    old.inbox.command(self_update(ID));
    tokio::time::sleep(Duration::from_secs(1)).await;
    assert_eq!(*fake.staged.lock().unwrap(), ["1.1.0"]);
    assert_eq!(old.phases(), [(ID.into(), CommandPhase::Started)]);
    let text = std::fs::read_to_string(dir.path().join("runs")).unwrap();
    assert!(
        text.contains(&format!("{ID} running ")) && text.contains("self-update:first"),
        "{text}"
    );
    old.inbox.connected();
    tokio::time::sleep(Duration::from_secs(1)).await;
    assert!(
        fake.confirmed.lock().unwrap().is_empty(),
        "the old agent confirmed"
    );
    old.stop().await;

    let done = Poll::Done {
        exit_code: 0,
        output: Vec::new(),
        truncated: false,
        started: 1,
        ended: 2,
    };
    let helper = Fake::with(vec![], vec![(ID, vec![Poll::Running, done])]);
    let new = start_runner(helper.clone(), book_after_restart(&dir));
    new.inbox.connected();
    tokio::time::sleep(Duration::from_secs(1)).await;
    assert_eq!(*helper.confirmed.lock().unwrap(), [ID]);
    tokio::time::sleep(Duration::from_secs(20)).await;
    assert_eq!(new.phases(), [(ID.into(), CommandPhase::Succeeded)]);
    // Ended: a later session confirms nothing.
    new.inbox.connected();
    tokio::time::sleep(Duration::from_secs(1)).await;
    assert_eq!(helper.confirmed.lock().unwrap().len(), 1);
    new.stop().await;
}

// A release that can't be fetched fails the run without the helper; a busy helper starts it
// later, and until then the run is not in the book (no lost run while it waits).
#[tokio::test(start_paused = true)]
async fn a_self_update_without_its_release_fails_and_a_busy_one_waits() {
    let dir = tempfile::tempdir().unwrap();
    let fake = Fake::with(vec![Start::Busy("a run".into())], vec![]);
    *fake.stage_fails.lock().unwrap() = Some("NOT_FOUND".into());
    let r = start_runner(fake.clone(), book(&dir));
    r.inbox.command(self_update(ID));
    tokio::time::sleep(Duration::from_secs(1)).await;
    assert_eq!(r.phases(), [(ID.into(), CommandPhase::Failed)]);
    assert!(
        r.results.lock().unwrap()[0]
            .reason
            .contains("cannot fetch the release: NOT_FOUND")
    );
    assert!(fake.starts.lock().unwrap().is_empty());
    r.inbox.command(self_update(OTHER));
    tokio::time::sleep(Duration::from_secs(1)).await;
    assert_eq!(r.phases().len(), 1, "busy: nothing reported yet");
    let asked = fake.asked.load(Ordering::SeqCst);
    tokio::time::sleep(Duration::from_secs(15)).await;
    assert_eq!(
        fake.asked.load(Ordering::SeqCst),
        asked,
        "a waiting run was polled"
    );
    tokio::time::sleep(Duration::from_secs(20)).await;
    assert_eq!(
        r.phases().last().unwrap(),
        &(OTHER.to_owned(), CommandPhase::Started)
    );
    // Staged once: a busy helper doesn't make the agent fetch the release again (the server
    // allows 10 an hour).
    assert_eq!(
        fake.staged.lock().unwrap().len(),
        2,
        "staged again for the retry"
    );
    r.stop().await;
}
