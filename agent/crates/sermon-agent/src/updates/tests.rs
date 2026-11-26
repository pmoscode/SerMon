use std::io::{Read, Write};
use std::os::unix::net::UnixListener;
use std::sync::Mutex;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::thread;

use prost::Message;
use sermon_helper_proto::v1::RunState;

use super::*;

macro_rules! fixture {
    ($path:literal) => {
        include_bytes!(concat!("../../../../fixtures/updates/", $path)).to_vec()
    };
}

fn command(name: &str, exit_code: i32, output: Vec<u8>) -> Command {
    Command {
        name: name.into(),
        exit_code,
        output,
        truncated: false,
    }
}

fn info(distribution: Distribution) -> StatusInfo {
    StatusInfo {
        distribution: distribution.into(),
        allow_remote_updates: true,
        allow_self_update: true,
        ..StatusInfo::default()
    }
}

fn now() -> SystemTime {
    SystemTime::UNIX_EPOCH + Duration::from_secs(1_795_000_000)
}

fn apt(refresh_exit: Option<i32>) -> Updates {
    let mut commands = Vec::new();
    if let Some(exit) = refresh_exit {
        commands.push(command(
            "/usr/bin/apt-get update -q",
            exit,
            b"E: Failed to fetch\n\x1b[31mhttp://mirror\n".to_vec(),
        ));
    }
    commands.push(command(
        "/usr/bin/apt list --upgradable",
        0,
        fixture!("ubuntu-24.04/apt-list.txt"),
    ));
    Updates {
        distribution: Distribution::Apt.into(),
        refreshed: refresh_exit.is_some(),
        commands,
        reboot_required_pkgs: b"linux-image-6.8.0-90-generic\n".to_vec(),
        reboot_required: true,
    }
}

#[test]
fn reports_what_apt_lists_with_the_reboot_and_the_permissions() {
    let host = HostState {
        last_change: Some(now() - Duration::from_hours(30)),
        ..HostState::default()
    };
    let r = from_updates(&apt(Some(0)), &info(Distribution::Apt), &host, now());
    assert_eq!(r.availability(), UpdateAvailability::Listed);
    assert_eq!(r.package_manager(), PackageManager::Apt);
    assert_eq!(
        (r.total_count, r.security_count, r.packages.len()),
        (4, 2, 4)
    );
    assert!(
        r.packages[0].security && r.packages[1].security && !r.packages[3].security,
        "security first: {:?}",
        r.packages
    );
    assert!(r.reboot_required);
    assert_eq!(r.reboot_reason, "linux-image-6.8.0-90-generic");
    assert!(r.refreshed && r.reason.is_empty(), "{r:?}");
    assert_eq!(r.collected_at, Some(now().into()));
    assert_eq!(
        r.last_upgrade_at,
        Some((now() - Duration::from_hours(30)).into())
    );
    assert_eq!(
        r.permissions,
        Some(HelperPermissions {
            remote_updates: true,
            remote_reboot: false,
            self_update: true
        })
    );
}

// The refresh failed (a mirror down): the old lists still count, the report says so.
#[test]
fn a_failed_refresh_lists_from_the_old_lists() {
    let r = from_updates(
        &apt(Some(100)),
        &info(Distribution::Apt),
        &HostState::default(),
        now(),
    );
    assert_eq!(r.availability(), UpdateAvailability::Listed);
    assert_eq!(r.total_count, 4);
    assert!(!r.refreshed);
    assert!(
        r.reason
            .starts_with("refreshing the sources failed with exit code 100"),
        "{}",
        r.reason
    );
    assert!(
        !r.reason.contains('\u{1b}') && !r.reason.contains('\n'),
        "control characters in {:?}",
        r.reason
    );
    // Without a refresh the list is the first output.
    let r = from_updates(
        &apt(None),
        &info(Distribution::Apt),
        &HostState::default(),
        now(),
    );
    assert_eq!((r.total_count, r.refreshed), (4, false));
}

fn dnf(check_exit: i32, security_exit: i32) -> Updates {
    Updates {
        distribution: Distribution::Dnf.into(),
        refreshed: false,
        commands: vec![
            command(
                "/usr/bin/dnf -q check-update",
                check_exit,
                fixture!("rockylinux-9/dnf-check-update.txt"),
            ),
            command(
                "/usr/bin/dnf -q updateinfo list --security",
                security_exit,
                fixture!("rockylinux-9/dnf-updateinfo-security.txt"),
            ),
            command(
                "/usr/bin/dnf needs-restarting -r",
                1,
                fixture!("synthetic/dnf-needs-restarting-required.txt"),
            ),
        ],
        ..Updates::default()
    }
}

#[test]
fn reports_what_dnf_lists() {
    let expected = sermon_updates::dnf(
        &String::from_utf8(fixture!("rockylinux-9/dnf-check-update.txt")).unwrap(),
        &String::from_utf8(fixture!("rockylinux-9/dnf-updateinfo-security.txt")).unwrap(),
    );
    let security = expected.iter().filter(|p| p.security).count();
    assert!(
        security > 0 && expected.len() > security,
        "the fixture has both kinds"
    );
    let r = from_updates(
        &dnf(100, 0),
        &info(Distribution::Dnf),
        &HostState::default(),
        now(),
    );
    assert_eq!(r.availability(), UpdateAvailability::Listed);
    assert_eq!(r.package_manager(), PackageManager::Dnf);
    assert_eq!(usize::try_from(r.total_count).unwrap(), expected.len());
    assert_eq!(usize::try_from(r.security_count).unwrap(), security);
    assert!(r.reboot_required);
    assert_eq!(r.reboot_reason, "glibc, kernel");
    assert!(r.reason.is_empty(), "{}", r.reason);
    // Exit 0: nothing to update is no failure.
    assert_eq!(
        from_updates(
            &dnf(0, 0),
            &info(Distribution::Dnf),
            &HostState::default(),
            now()
        )
        .availability(),
        UpdateAvailability::Listed
    );
}

#[test]
fn dnf_without_security_list_or_needs_restarting_says_what_is_unknown() {
    let mut updates = dnf(100, 1);
    updates.commands[2] = command("/usr/bin/dnf needs-restarting -r", 1, Vec::new());
    let r = from_updates(
        &updates,
        &info(Distribution::Dnf),
        &HostState::default(),
        now(),
    );
    assert_eq!(r.availability(), UpdateAvailability::Listed);
    assert_eq!(r.security_count, 0);
    assert!(!r.reboot_required);
    assert!(
        r.reason.contains("security updates are unknown")
            && r.reason.contains("reboot is needed is unknown"),
        "{}",
        r.reason
    );
}

#[test]
fn a_failed_list_is_a_failed_report() {
    let r = from_updates(
        &dnf(1, 0),
        &info(Distribution::Dnf),
        &HostState::default(),
        now(),
    );
    assert_eq!(r.availability(), UpdateAvailability::Failed);
    assert!(
        r.reason
            .starts_with("/usr/bin/dnf -q check-update failed with exit code 1"),
        "{}",
        r.reason
    );
    assert!(r.packages.is_empty() && r.total_count == 0);
    let mut short = dnf(100, 0);
    short.commands.pop();
    let r = from_updates(
        &short,
        &info(Distribution::Dnf),
        &HostState::default(),
        now(),
    );
    assert_eq!(r.availability(), UpdateAvailability::Failed);
    assert!(r.reason.contains("2 lists, 3 expected"), "{}", r.reason);
    let mut more = apt(Some(0));
    more.commands
        .push(command("/usr/bin/apt list --upgradable", 0, Vec::new()));
    let r = from_updates(
        &more,
        &info(Distribution::Apt),
        &HostState::default(),
        now(),
    );
    assert!(r.reason.contains("2 lists, 1 expected"), "{}", r.reason);
    let mut none = apt(Some(0));
    none.commands.clear();
    assert_eq!(
        from_updates(
            &none,
            &info(Distribution::Apt),
            &HostState::default(),
            now()
        )
        .availability(),
        UpdateAvailability::Failed
    );
    let unknown = Updates::default();
    assert_eq!(
        from_updates(
            &unknown,
            &info(Distribution::Apt),
            &HostState::default(),
            now()
        )
        .availability(),
        UpdateAvailability::Unsupported
    );
}

#[test]
fn reports_what_apk_lists_with_the_kernel_of_the_host() {
    let updates = Updates {
        distribution: Distribution::Apk.into(),
        commands: vec![command(
            "/sbin/apk version -l <",
            0,
            fixture!("alpine-3.22/apk-version.txt"),
        )],
        ..Updates::default()
    };
    let host = HostState {
        kernel: "6.12.1-0-lts".into(),
        modules: vec!["6.12.9-0-lts".into()],
        last_change: None,
    };
    let r = from_updates(&updates, &info(Distribution::Apk), &host, now());
    assert_eq!(r.package_manager(), PackageManager::Apk);
    assert_eq!((r.total_count, r.security_count), (3, 0));
    assert!(
        r.reboot_required && r.reboot_reason.contains("6.12.1-0-lts"),
        "{}",
        r.reboot_reason
    );
    assert_eq!(r.last_upgrade_at, None);
    let mut cut = updates;
    cut.commands[0].truncated = true;
    let r = from_updates(&cut, &info(Distribution::Apk), &HostState::default(), now());
    assert!(r.reason.contains("cut"), "{}", r.reason);
    assert!(!r.reboot_required, "a container without modules can't tell");
}

// More than the protocol takes: 5000 packages at most, 2 MiB at most, the counts of all.
#[test]
fn a_huge_list_is_bounded_and_keeps_its_counts() {
    use std::fmt::Write as _;
    let mut list = String::from("Listing...\n");
    for i in 0..6000 {
        let long = "v".repeat(240);
        let _ = writeln!(
            list,
            "pkg{i:05}/stable {long}{i} amd64 [upgradable from: {long}]"
        );
    }
    list.push_str("zzz/stable-security 2 amd64 [upgradable from: 1]\n");
    let updates = Updates {
        distribution: Distribution::Apt.into(),
        commands: vec![command(
            "/usr/bin/apt list --upgradable",
            0,
            list.into_bytes(),
        )],
        ..Updates::default()
    };
    let r = from_updates(
        &updates,
        &info(Distribution::Apt),
        &HostState::default(),
        now(),
    );
    assert_eq!((r.total_count, r.security_count), (6001, 1));
    assert!(
        r.packages.len() < MAX_PACKAGES,
        "{} packages",
        r.packages.len()
    );
    assert!(r.encoded_len() <= MAX_REPORT_BYTES);
    assert_eq!(r.packages[0].name, "zzz", "the security update stays");
}

#[test]
fn texts_are_cut_at_character_boundaries() {
    let r = report_of(UpdateAvailability::Failed, &"ä".repeat(600), now());
    assert_eq!(r.reason.len(), MAX_REASON);
    let r = report_of(
        UpdateAvailability::Failed,
        &format!("x{}", "ä".repeat(600)),
        now(),
    );
    assert_eq!(r.reason.len(), MAX_REASON - 1);
    let long = command("x", 1, format!("{}ä", "a".repeat(300)).into_bytes());
    assert!(tail(&long).len() <= 200 && tail(&long).ends_with('ä'));
    // Byte 101 of 301 is inside an ä: the tail starts after it.
    let odd = command("x", 1, format!("{}x", "ä".repeat(150)).into_bytes());
    assert_eq!(
        tail(&odd),
        format!("{}x", "ä".repeat(99)),
        "the cut moves past the character"
    );
}

/// A helper that answers each connection with the next of `answers` (`None`: closes at once).
fn fake_helper(
    answers: Vec<Option<HelperResponse>>,
) -> (tempfile::TempDir, PathBuf, Arc<Mutex<Vec<HelperRequest>>>) {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("helper.sock");
    let listener = UnixListener::bind(&path).unwrap();
    let seen = Arc::new(Mutex::new(Vec::new()));
    let requests = seen.clone();
    thread::spawn(move || {
        for answer in answers {
            let (mut s, _) = listener.accept().unwrap();
            let mut len = [0u8; 4];
            s.read_exact(&mut len).unwrap();
            let mut body = vec![0; usize::try_from(u32::from_be_bytes(len)).unwrap()];
            s.read_exact(&mut body).unwrap();
            requests
                .lock()
                .unwrap()
                .push(HelperRequest::decode(body.as_slice()).unwrap());
            if let Some(answer) = answer {
                let body = answer.encode_to_vec();
                let _ = s.write_all(&u32::try_from(body.len()).unwrap().to_be_bytes());
                let _ = s.write_all(&body);
            }
        }
    });
    (dir, path, seen)
}

#[expect(clippy::unnecessary_wraps, reason = "an answer of fake_helper")]
fn ok(result: Answer) -> Option<HelperResponse> {
    Some(HelperResponse {
        status: Status::Ok.into(),
        result: Some(result),
        ..HelperResponse::default()
    })
}

#[expect(clippy::unnecessary_wraps, reason = "an answer of fake_helper")]
fn status(s: Status, reason: &str) -> Option<HelperResponse> {
    Some(HelperResponse {
        status: s.into(),
        reason: reason.into(),
        ..HelperResponse::default()
    })
}

fn report(listing: Listing) -> UpdateReport {
    match listing {
        Listing::Report(r) => r,
        Listing::Busy => panic!("busy"),
    }
}

#[test]
fn asks_the_helper_for_its_status_and_the_list() {
    let (dir, socket, seen) = fake_helper(vec![
        ok(Answer::StatusInfo(info(Distribution::Apt))),
        ok(Answer::Updates(apt(Some(0)))),
    ]);
    let root = dir.path().join("root");
    std::fs::create_dir_all(root.join("var/lib/dpkg")).unwrap();
    std::fs::write(root.join("var/lib/dpkg/status"), "").unwrap();
    let r = report(HelperSource::new(&socket, &root).list_blocking(true));
    assert_eq!(
        (r.availability(), r.total_count),
        (UpdateAvailability::Listed, 4)
    );
    assert!(r.last_upgrade_at.is_some(), "the dpkg database's time");
    let requests = seen.lock().unwrap();
    assert!(matches!(
        requests[0].operation,
        Some(Operation::GetStatus(_))
    ));
    assert!(matches!(
        requests[1].operation,
        Some(Operation::ListUpdates(ListUpdates { refresh: true }))
    ));
}

#[test]
fn without_a_helper_it_reports_why_instead_of_failing() {
    let dir = tempfile::tempdir().unwrap();
    let r =
        report(HelperSource::new(dir.path().join("none.sock"), dir.path()).list_blocking(false));
    assert_eq!(r.availability(), UpdateAvailability::NoHelper);
    assert!(r.reason.contains("sermon-helper"), "{}", r.reason);
    // The helper closes on this user.
    let (_dir, socket, _) = fake_helper(vec![None]);
    let r = report(HelperSource::new(&socket, dir.path()).list_blocking(false));
    assert_eq!(r.availability(), UpdateAvailability::NoHelper);
    assert!(r.permissions.is_none());
}

#[test]
fn the_helpers_answers_become_availabilities() {
    let dir = tempfile::tempdir().unwrap();
    let list = |answers| HelperSource::new(fake_helper(answers).1, dir.path()).list_blocking(false);
    let apt = || ok(Answer::StatusInfo(info(Distribution::Apt)));
    assert_eq!(
        list(vec![apt(), status(Status::Busy, "run")]),
        Listing::Busy
    );
    let r = report(list(vec![
        apt(),
        status(Status::Unsupported, "unknown operation"),
    ]));
    assert_eq!(r.availability(), UpdateAvailability::HelperTooOld);
    let r = report(list(vec![apt(), status(Status::Failed, "apt crashed")]));
    assert_eq!(r.availability(), UpdateAvailability::Failed);
    assert!(
        r.reason.contains("STATUS_FAILED: apt crashed"),
        "{}",
        r.reason
    );
    let r = report(list(vec![apt(), ok(Answer::Run(RunState::default()))]));
    assert_eq!(r.availability(), UpdateAvailability::Failed);
    let r = report(list(vec![ok(Answer::StatusInfo(info(
        Distribution::Unspecified,
    )))]));
    assert_eq!(r.availability(), UpdateAvailability::Unsupported);
    assert!(r.permissions.is_some(), "the helper's switches are known");
    let r = report(list(vec![status(Status::Refused, "no")]));
    assert_eq!(r.availability(), UpdateAvailability::Failed);
}

/// A source that records when it was asked and how; Busy for the first `busy` calls.
#[derive(Debug, Default)]
struct Fake {
    calls: Mutex<Vec<(Duration, bool)>>,
    busy: AtomicUsize,
    start: Mutex<Option<Instant>>,
}

impl Source for Fake {
    fn list(&self, refresh: bool) -> impl Future<Output = Listing> + Send {
        let start = self.start.lock().unwrap().unwrap();
        self.calls.lock().unwrap().push((start.elapsed(), refresh));
        let listing = if self.busy.load(Ordering::SeqCst) > 0 {
            self.busy.fetch_sub(1, Ordering::SeqCst);
            Listing::Busy
        } else {
            Listing::Report(report_of(
                UpdateAvailability::Listed,
                "",
                SystemTime::UNIX_EPOCH,
            ))
        };
        std::future::ready(listing)
    }
}

struct Running {
    fake: Arc<Fake>,
    refresh: Arc<Notify>,
    ran: Arc<Notify>,
    reports: Arc<AtomicUsize>,
    shutdown: CancellationToken,
    task: tokio::task::JoinHandle<()>,
    root: tempfile::TempDir,
}

fn start(busy: usize) -> Running {
    let fake = Arc::new(Fake {
        busy: AtomicUsize::new(busy),
        start: Mutex::new(Some(Instant::now())),
        ..Fake::default()
    });
    let root = tempfile::tempdir().unwrap();
    std::fs::create_dir_all(root.path().join("var/lib/dpkg")).unwrap();
    std::fs::write(root.path().join("var/lib/dpkg/status"), "").unwrap();
    let refresh = Arc::new(Notify::new());
    let ran = Arc::new(Notify::new());
    let reports = Arc::new(AtomicUsize::new(0));
    let shutdown = CancellationToken::new();
    let counted = reports.clone();
    let task = tokio::spawn(run(
        fake.clone(),
        root.path().to_owned(),
        move |_| {
            counted.fetch_add(1, Ordering::SeqCst);
        },
        refresh.clone(),
        ran.clone(),
        Schedule::default(),
        Spread::seeded(42),
        shutdown.clone(),
    ));
    Running {
        fake,
        refresh,
        ran,
        reports,
        shutdown,
        task,
        root,
    }
}

impl Running {
    fn calls(&self) -> Vec<(Duration, bool)> {
        self.fake.calls.lock().unwrap().clone()
    }

    fn touch(&self) {
        let file = std::fs::File::options()
            .write(true)
            .open(self.root.path().join("var/lib/dpkg/status"))
            .unwrap();
        let at = file.metadata().unwrap().modified().unwrap() + Duration::from_secs(1);
        file.set_modified(at).unwrap();
    }

    async fn stop(self) {
        self.shutdown.cancel();
        self.task.await.unwrap();
    }
}

async fn advance(by: Duration) {
    tokio::time::sleep(by).await;
}

// The first report is spread over 10 to 60 seconds after the start.
#[test]
fn the_first_report_is_spread_over_the_first_minute() {
    let schedule = Schedule::default();
    let delays: Vec<Duration> = (0..200)
        .map(|seed| first_report(&schedule, &mut Spread::seeded(seed * 7919)))
        .collect();
    let (low, high) = (*delays.iter().min().unwrap(), *delays.iter().max().unwrap());
    assert!(
        low >= Duration::from_secs(10) && low < Duration::from_secs(13),
        "{low:?}"
    );
    assert!(
        high <= Duration::from_mins(1) && high > Duration::from_secs(57),
        "{high:?}"
    );
}

// Shortly after the start with fresh sources, then every 6 hours give or take 30 minutes.
#[tokio::test(start_paused = true)]
async fn reports_after_the_start_and_every_six_hours() {
    let r = start(0);
    advance(Duration::from_mins(1) + Duration::from_secs(1)).await;
    let calls = r.calls();
    assert_eq!(calls.len(), 1, "{calls:?}");
    assert!(
        calls[0].0 >= Duration::from_secs(10) && calls[0].0 <= Duration::from_mins(1),
        "{calls:?}"
    );
    assert!(calls[0].1, "the first report refreshes the sources");
    advance(Duration::from_hours(5)).await;
    assert_eq!(r.calls().len(), 1, "too early");
    advance(Duration::from_hours(2)).await;
    let calls = r.calls();
    assert_eq!(calls.len(), 2, "{calls:?}");
    let gap = calls[1].0.checked_sub(calls[0].0).unwrap();
    assert!(
        gap >= Duration::from_mins(330) && gap <= Duration::from_mins(390),
        "{gap:?}"
    );
    assert!(calls[1].1);
    assert_eq!(r.reports.load(Ordering::SeqCst), 2);
    r.stop().await;
}

// Each run of the package manager changes its database: one report from the local lists, two
// minutes after the last change.
#[tokio::test(start_paused = true)]
async fn reports_two_minutes_after_the_package_manager_ran() {
    let r = start(0);
    advance(Duration::from_mins(10)).await;
    assert_eq!(r.calls().len(), 1);
    r.touch();
    advance(Duration::from_mins(1)).await;
    r.touch();
    advance(Duration::from_secs(90)).await;
    assert_eq!(
        r.calls().len(),
        1,
        "debounced: the second change restarts the wait"
    );
    advance(Duration::from_mins(2)).await;
    let calls = r.calls();
    assert_eq!(calls.len(), 2, "{calls:?}");
    let after_last = calls[1].0.checked_sub(Duration::from_mins(11)).unwrap();
    assert!(
        after_last >= Duration::from_mins(2) && after_last <= Duration::from_secs(150),
        "{after_last:?}"
    );
    assert!(!calls[1].1, "no refresh of the sources");
    advance(Duration::from_mins(10)).await;
    assert_eq!(r.calls().len(), 2, "no change, no report");
    r.stop().await;
}

// A run of the helper ended (M4-28): the agent knows the package manager is done and reports from
// the local lists a few seconds later, without the debounce and only once – a rollout waits for
// this report before it decides on the reboot.
#[tokio::test(start_paused = true)]
async fn reports_right_after_a_run_of_the_helper() {
    let r = start(0);
    advance(Duration::from_mins(10)).await;
    assert_eq!(r.calls().len(), 1);
    // The run changed the database just before it ended – the poll hasn't seen it yet.
    advance(Duration::from_secs(1)).await;
    r.touch();
    r.ran.notify_one();
    advance(Duration::from_secs(10)).await;
    let calls = r.calls();
    assert_eq!(calls.len(), 2, "{calls:?}");
    let after = calls[1].0.checked_sub(Duration::from_secs(601)).unwrap();
    assert!(after <= Schedule::default().after_run, "{after:?}");
    assert!(!calls[1].1, "no refresh of the sources");
    advance(Duration::from_mins(5)).await;
    assert_eq!(r.calls().len(), 2, "the change of the run is reported once");
    r.stop().await;
}

// RefreshUpdateInfo: at once with fresh sources, but at most every 5 minutes.
#[tokio::test(start_paused = true)]
async fn a_refresh_on_request_comes_at_most_every_five_minutes() {
    let r = start(0);
    advance(Duration::from_mins(10)).await;
    r.refresh.notify_one();
    advance(Duration::from_secs(1)).await;
    let calls = r.calls();
    assert_eq!(calls.len(), 2, "{calls:?}");
    assert!(
        calls[1].1
            && calls[1].0 >= Duration::from_mins(10)
            && calls[1].0 <= Duration::from_mins(10) + Duration::from_secs(1)
    );
    // Two more requests a minute later: one report, five minutes after the last.
    advance(Duration::from_mins(1)).await;
    r.refresh.notify_one();
    advance(Duration::from_secs(1)).await;
    r.refresh.notify_one();
    advance(Duration::from_mins(3)).await;
    assert_eq!(r.calls().len(), 2, "too early");
    advance(Duration::from_mins(2)).await;
    let calls = r.calls();
    assert_eq!(calls.len(), 3, "{calls:?}");
    let gap = calls[2].0.checked_sub(calls[1].0).unwrap();
    assert!(
        gap >= Duration::from_mins(5) && gap <= Duration::from_mins(5) + Duration::from_secs(1),
        "{gap:?}"
    );
    assert!(calls[2].1);
    r.stop().await;
}

// A run holds the helper's lock: asked again 5 minutes later; nothing is reported meanwhile.
#[tokio::test(start_paused = true)]
async fn a_busy_helper_is_asked_again_later() {
    let r = start(1);
    advance(Duration::from_mins(1) + Duration::from_secs(1)).await;
    assert_eq!(r.calls().len(), 1);
    assert_eq!(r.reports.load(Ordering::SeqCst), 0);
    advance(Duration::from_mins(5)).await;
    let calls = r.calls();
    assert_eq!(calls.len(), 2, "{calls:?}");
    assert!(calls[1].1, "still with fresh sources");
    assert_eq!(
        calls[1].0.checked_sub(calls[0].0).unwrap(),
        Duration::from_mins(5)
    );
    assert_eq!(r.reports.load(Ordering::SeqCst), 1);
    r.stop().await;
}

#[tokio::test(start_paused = true)]
async fn ends_with_the_agent() {
    let r = start(0);
    r.shutdown.cancel();
    tokio::time::timeout(Duration::from_secs(1), r.task)
        .await
        .unwrap()
        .unwrap();
}
