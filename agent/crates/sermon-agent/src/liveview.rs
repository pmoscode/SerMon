//! The live view of the agent ("top", KONZEPT §9, M3-13): snapshots of the processes while the
//! server asks for them, sent at once and never buffered.
//!
//! - **Local veto:** `allow_live_view = false` in `agent.toml` refuses every start with a reason.
//! - **Command lines** pass the organization's redaction (built-in patterns and rules, §10.3)
//!   and are cut to [`MAX_CMDLINE_BYTES`]; all of a snapshot together to [`CMDLINE_BUDGET`], the
//!   rest to [`CMDLINE_AFTER_BUDGET`]. With `hide_cmdline` (the organization's switch) none are
//!   sent.

use std::sync::{Arc, Mutex, PoisonError};

use sermon_collectors::process::{Process, ProcessSampler, Snapshot};
use sermon_core::{Clock, ProcRoot};
use sermon_transport::proto::agent::v1::{ProcessInfo, ProcessSnapshot, SystemSummary, TaskCounts};
use sermon_transport::session::LiveSource;
use tokio::sync::watch;

use crate::agent_config::Active;

/// Bytes of one command line at most (the protocol's bound).
pub const MAX_CMDLINE_BYTES: usize = 1024;
/// Bytes of all command lines of a snapshot before the rest is cut shorter.
pub const CMDLINE_BUDGET: usize = 1024 * 1024;
/// Bytes of a command line once the budget is used up.
pub const CMDLINE_AFTER_BUDGET: usize = 128;
const MAX_NAME_BYTES: usize = 64;
const MAX_UNIT_BYTES: usize = 256;

/// The reason sent when agent.toml forbids the live view.
pub const VETO: &str = "agent.toml forbids the live view (allow_live_view = false)";

/// Takes the snapshots for the transport.
#[derive(Debug)]
pub struct LiveView {
    root: ProcRoot,
    allowed: bool,
    config: watch::Receiver<Arc<Active>>,
    clock: Arc<dyn Clock>,
    sampler: Mutex<ProcessSampler>,
}

impl LiveView {
    /// A live view of the host at `root`; `allowed` is `allow_live_view` of agent.toml.
    #[must_use]
    pub fn new(
        root: ProcRoot,
        allowed: bool,
        config: watch::Receiver<Arc<Active>>,
        clock: Arc<dyn Clock>,
    ) -> Self {
        Self {
            root,
            allowed,
            config,
            clock,
            sampler: Mutex::new(ProcessSampler::new()),
        }
    }
}

impl LiveSource for LiveView {
    fn refusal(&self) -> Option<String> {
        (!self.allowed).then(|| VETO.to_owned())
    }

    fn snapshot(&self, hide_cmdline: bool) -> Option<ProcessSnapshot> {
        if !self.allowed {
            return None;
        }
        let snapshot = self
            .sampler
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .snapshot(&self.root, self.clock.monotonic());
        match snapshot {
            Ok(snapshot) => {
                let active = self.config.borrow().clone();
                Some(to_proto(
                    snapshot,
                    hide_cmdline,
                    |text| active.redactor.redact(text).into_owned(),
                    self.clock.now(),
                ))
            }
            Err(error) => {
                tracing::warn!("cannot take a process snapshot: {error}");
                None
            }
        }
    }

    fn stopped(&self) {
        self.sampler
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .reset();
    }
}

/// `text` cut to at most `max` bytes at a character boundary; whether it was cut.
fn cut(text: &str, max: usize) -> (&str, bool) {
    if text.len() <= max {
        return (text, false);
    }
    let mut end = max;
    while !text.is_char_boundary(end) {
        end -= 1;
    }
    (&text[..end], true)
}

/// The snapshot as the protocol sends it.
pub fn to_proto(
    snapshot: Snapshot,
    hide_cmdline: bool,
    redact: impl Fn(&str) -> String,
    now: std::time::SystemTime,
) -> ProcessSnapshot {
    let h = &snapshot.header;
    let summary = SystemSummary {
        cpu_busy_ratio: h.cpu_busy_ratio.map(|r| r.clamp(0.0, 1.0)),
        cpu_count: h.cpu_count.min(4096),
        memory_total_bytes: h.memory_total_bytes,
        memory_available_bytes: h.memory_available_bytes,
        swap_total_bytes: h.swap_total_bytes,
        swap_free_bytes: h.swap_free_bytes,
        load_1: h.load.map(|l| l.one.clamp(0.0, 1e6)),
        load_5: h.load.map(|l| l.five.clamp(0.0, 1e6)),
        load_15: h.load.map(|l| l.fifteen.clamp(0.0, 1e6)),
        uptime_seconds: h.uptime_seconds,
        tasks: Some(TaskCounts {
            total: h.tasks.total,
            running: h.tasks.running,
            sleeping: h.tasks.sleeping,
            stopped: h.tasks.stopped,
            zombie: h.tasks.zombie,
        }),
    };
    let mut budget = CMDLINE_BUDGET;
    let processes = snapshot
        .processes
        .into_iter()
        .map(|p| process(p, hide_cmdline, &redact, &mut budget))
        .collect();
    ProcessSnapshot {
        taken_at: Some(now.into()),
        summary: Some(summary),
        processes,
        omitted: snapshot.omitted,
    }
}

fn process(
    p: Process,
    hide: bool,
    redact: &impl Fn(&str) -> String,
    budget: &mut usize,
) -> ProcessInfo {
    let (cmdline, truncated) = match (&p.cmdline, hide) {
        (_, true) | (None, _) => (None, false),
        (Some(raw), false) => {
            // Per argument, joined with spaces afterwards (M3-29).
            let redacted = sermon_security::redact_args(raw, redact);
            let max = if *budget >= MAX_CMDLINE_BYTES {
                MAX_CMDLINE_BYTES
            } else {
                CMDLINE_AFTER_BUDGET
            };
            let (text, cut_here) = cut(&redacted, max);
            *budget = budget.saturating_sub(text.len());
            // Read up to the collector's bound: longer ones are cut there already.
            let read_cut = raw.len()
                >= usize::try_from(sermon_collectors::process::CMDLINE_READ_BYTES)
                    .unwrap_or(usize::MAX);
            (Some(text.to_owned()), cut_here || read_cut)
        }
    };
    ProcessInfo {
        pid: p.pid,
        ppid: p.ppid,
        uid: p.uid,
        user: p
            .user
            .map_or_else(String::new, |u| cut(&u, 64).0.to_owned()),
        state: p.state.to_string(),
        cpu_ratio: p.cpu_ratio.map(|r| r.clamp(0.0, 4096.0)),
        rss_bytes: p.rss_bytes,
        memory_ratio: p.memory_ratio.map(|r| r.clamp(0.0, 1.0)),
        threads: p.threads,
        runtime_seconds: p.runtime_seconds,
        name: cut(&p.name, MAX_NAME_BYTES).0.to_owned(),
        cmdline,
        cmdline_truncated: truncated,
        cmdline_hidden: hide,
        kernel_thread: p.kernel_thread,
        unit: p
            .place
            .unit
            .map_or_else(String::new, |u| cut(&u, MAX_UNIT_BYTES).0.to_owned()),
        container: p.place.container.unwrap_or_default(),
    }
}

#[cfg(test)]
mod tests {
    use std::path::PathBuf;
    use std::time::SystemTime;

    use prost::Message;
    use sermon_collectors::process::{Header, Place, Tasks};
    use sermon_core::SystemClock;

    use super::*;
    use crate::logship::tests::active;

    fn fixture() -> ProcRoot {
        ProcRoot::new(
            PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../fixtures/processes/debian-13"),
        )
    }

    fn proc_with(cmdline: Option<&str>) -> Process {
        Process {
            pid: 42,
            ppid: 1,
            uid: Some(0),
            user: Some("root".into()),
            state: 'S',
            cpu_ratio: Some(0.5),
            rss_bytes: Some(1024),
            memory_ratio: Some(0.01),
            threads: 1,
            runtime_seconds: Some(5),
            name: "mysql".into(),
            cmdline: cmdline.map(str::to_owned),
            kernel_thread: false,
            place: Place {
                unit: Some("mysql.service".into()),
                container: None,
            },
        }
    }

    fn snapshot(processes: Vec<Process>) -> Snapshot {
        Snapshot {
            header: Header {
                tasks: Tasks {
                    total: 1,
                    ..Tasks::default()
                },
                ..Header::default()
            },
            processes,
            omitted: 3,
        }
    }

    #[test]
    fn redacts_and_cuts_the_command_lines() {
        let redactor = sermon_security::Redactor::builtin();
        let redact = |t: &str| redactor.redact(t).into_owned();
        let secret = "mysql --password=hunter2 -e 'select 1'"; // betterleaks:allow
        let long = format!("java {}", "x".repeat(3000));
        let s = to_proto(
            snapshot(vec![
                proc_with(Some(secret)),
                proc_with(Some(&long)),
                proc_with(None),
                proc_with(Some("")),
            ]),
            false,
            redact,
            SystemTime::now(),
        );
        assert_eq!(
            s.processes[0].cmdline.as_deref(),
            Some("mysql --password=*** -e 'select 1'")
        );
        // M3-29: per argument – the whole argument after a password option, mysql -pSECRET.
        let args = to_proto(
            snapshot(vec![
                proc_with(Some("mysql\0-uroot\0-pS3cret\0db")), // betterleaks:allow
                proc_with(Some("app\0--password\0a b c\0--verbose")),
            ]),
            false,
            redact,
            SystemTime::now(),
        );
        assert_eq!(
            args.processes[0].cmdline.as_deref(),
            Some("mysql -uroot -p*** db")
        );
        assert_eq!(
            args.processes[1].cmdline.as_deref(),
            Some("app --password *** --verbose")
        );
        assert!(!s.processes[0].cmdline_truncated);
        assert_eq!(
            s.processes[1].cmdline.as_ref().map(String::len),
            Some(MAX_CMDLINE_BYTES)
        );
        assert!(s.processes[1].cmdline_truncated);
        assert_eq!(s.processes[2].cmdline, None, "not readable");
        assert_eq!(
            s.processes[3].cmdline.as_deref(),
            Some(""),
            "kernel thread or zombie"
        );
        assert_eq!(
            (
                s.omitted,
                s.processes[0].unit.as_str(),
                s.processes[0].state.as_str()
            ),
            (3, "mysql.service", "S")
        );
        // A multi-byte character at the bound is not split.
        let umlaut = format!("{}ü", "a".repeat(MAX_CMDLINE_BYTES - 1));
        let s = to_proto(
            snapshot(vec![proc_with(Some(&umlaut))]),
            false,
            str::to_owned,
            SystemTime::now(),
        );
        assert_eq!(
            s.processes[0].cmdline.as_ref().map(String::len),
            Some(MAX_CMDLINE_BYTES - 1)
        );
    }

    #[test]
    fn hides_the_command_lines_and_keeps_within_the_budget() {
        let s = to_proto(
            snapshot(vec![proc_with(Some("secret stuff"))]),
            true,
            str::to_owned,
            SystemTime::now(),
        );
        assert_eq!(s.processes[0].cmdline, None);
        assert!(s.processes[0].cmdline_hidden);

        let many: Vec<Process> = (0..2000)
            .map(|_| proc_with(Some(&"y".repeat(2000))))
            .collect();
        let s = to_proto(snapshot(many), false, str::to_owned, SystemTime::now());
        let total: usize = s
            .processes
            .iter()
            .filter_map(|p| p.cmdline.as_ref())
            .map(String::len)
            .sum();
        assert!(
            total <= CMDLINE_BUDGET + 2000 * CMDLINE_AFTER_BUDGET,
            "{total}"
        );
        assert_eq!(
            s.processes
                .last()
                .unwrap()
                .cmdline
                .as_ref()
                .map(String::len),
            Some(CMDLINE_AFTER_BUDGET)
        );
        assert!(
            s.encoded_len() < 4 << 20,
            "within the server's message bound"
        );
    }

    #[test]
    fn takes_snapshots_of_the_host_and_respects_the_veto() {
        let (_sender, config) = watch::channel(active(true));
        let live = LiveView::new(
            fixture(),
            true,
            config.clone(),
            Arc::new(SystemClock::new()),
        );
        assert_eq!(live.refusal(), None);
        let s = live.snapshot(false).unwrap();
        assert_eq!(s.processes.len(), 9);
        assert!(s.processes.iter().any(|p| p.name == "evil) name (x"
            && p.cmdline.as_deref() == Some("/tmp/evil) name (x -c sleep 300; echo 'a b'")));
        assert!(s.summary.as_ref().unwrap().cpu_busy_ratio.is_none());
        assert!(
            s.processes.iter().all(|p| p.cpu_ratio.is_none()),
            "no shares in the first snapshot"
        );
        // The second has shares (here 0: the fixture does not change); after a stop none again.
        assert!(
            live.snapshot(false)
                .unwrap()
                .processes
                .iter()
                .any(|p| p.cpu_ratio == Some(0.0))
        );
        live.stopped();
        assert!(
            live.snapshot(false)
                .unwrap()
                .processes
                .iter()
                .all(|p| p.cpu_ratio.is_none()),
            "history forgotten"
        );

        let vetoed = LiveView::new(fixture(), false, config, Arc::new(SystemClock::new()));
        assert_eq!(vetoed.refusal().as_deref(), Some(VETO));
        assert!(vetoed.snapshot(false).is_none());
    }

    // The server validates what the agent sends (protovalidate, Go): the encoded snapshot of the
    // fixtures is checked in server/internal/transport/agentapi/schema_test.go.
    // SERMON_UPDATE_GOLDEN=1 rewrites the file after a deliberate change.
    #[test]
    fn golden_snapshot_for_the_server_schema_test() {
        let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
            .join("../../../proto/testdata/process_snapshot.binpb");
        let raw = ProcessSampler::new()
            .snapshot(
                &fixture(),
                sermon_core::Monotonic::from_origin(std::time::Duration::ZERO),
            )
            .unwrap();
        let redactor = sermon_security::Redactor::builtin();
        let at = SystemTime::UNIX_EPOCH + std::time::Duration::from_hours(498_732);
        let encoded = to_proto(raw, false, |t| redactor.redact(t).into_owned(), at).encode_to_vec();
        if std::env::var_os("SERMON_UPDATE_GOLDEN").is_some() {
            std::fs::write(&path, &encoded).unwrap();
        }
        let golden = std::fs::read(&path).unwrap_or_default();
        assert!(
            golden == encoded,
            "{} is stale – run with SERMON_UPDATE_GOLDEN=1",
            path.display()
        );
    }
}
