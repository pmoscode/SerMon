//! The request rules of the helper (ADR 0035): what is valid, what the host allows, and which
//! answer an operation gets. Pure: the work itself is behind [`Executor`].

use sermon_helper_proto::v1::{
    ApplyUpdates, HelperRequest, HelperResponse, Reboot, RunState, Scope, SelfUpdate, Smart,
    Status, StatusInfo, Updates, helper_request::Operation, helper_response::Result as Answer,
};

use crate::config::Config;

/// Longest reason of a reboot, in bytes.
pub const MAX_REASON: usize = 200;

/// A run the agent asks for, valid and allowed.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Run {
    /// Install updates.
    Updates { id: String, scope: Scope },
    /// Reboot the host.
    Reboot { id: String, reason: String },
    /// Replace the agent's binary with `version` (ADR 0036).
    SelfUpdate { id: String, version: String },
}

/// Why the work could not be done.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Failure {
    /// Another run or refresh holds the lock.
    Busy,
    /// Not yet: a run of this kind started a short while ago.
    Later(String),
    /// Not on this host (distribution, version of the helper).
    Unsupported(String),
    /// Not allowed: the request doesn't hold (signature, version, state of the run).
    Refused(String),
    /// It failed.
    Failed(String),
}

/// Does the work of the operations (M4-03).
pub trait Executor {
    /// Distribution and the run in progress, for `GetStatus`.
    fn status(&self) -> StatusInfo;
    /// The raw lists of the package manager.
    ///
    /// # Errors
    ///
    /// [`Failure`] if they can't be listed.
    fn list_updates(&mut self, refresh: bool) -> Result<Updates, Failure>;
    /// Starts a run and returns at once.
    ///
    /// # Errors
    ///
    /// [`Failure`] if it can't be started.
    fn start(&mut self, run: &Run) -> Result<(), Failure>;
    /// The state of a run.
    fn result(&self, id: &str) -> RunState;
    /// The new agent of the self-update `id` connected (ADR 0036).
    ///
    /// # Errors
    ///
    /// [`Failure`] if no self-update of this run waits for it.
    fn confirm(&mut self, id: &str) -> Result<(), Failure>;
    /// The raw SMART data of the host's disks (M5-18).
    ///
    /// # Errors
    ///
    /// [`Failure`] without smartctl, or if read a moment ago.
    fn read_smart(&mut self) -> Result<Smart, Failure>;
}

/// One line for the journal: what was asked and how it ended.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Audit {
    pub operation: &'static str,
    /// The run's ID if valid, else empty: nothing unchecked reaches the journal.
    pub run_id: String,
    pub status: Status,
    pub reason: String,
    /// What the run was asked to do (scope, version, reboot reason), escaped: the journal says
    /// who triggered what (T41, review M4-30).
    pub params: String,
}

/// Answers one request.
pub fn handle(
    request: &HelperRequest,
    config: Config,
    exec: &mut dyn Executor,
) -> (HelperResponse, Audit) {
    let (operation, run_id, outcome) = match &request.operation {
        None => (
            "unknown",
            "",
            Err(unsupported("unknown operation: update the helper")),
        ),
        Some(Operation::GetStatus(_)) => {
            let mut info = exec.status();
            info.allow_remote_updates = config.allow_remote_updates;
            info.allow_remote_reboot = config.allow_remote_reboot;
            info.allow_self_update = config.allow_self_update;
            info.allow_smart = config.allow_smart;
            ("get_status", "", Ok(Some(Answer::StatusInfo(info))))
        }
        Some(Operation::ListUpdates(l)) => (
            "list_updates",
            "",
            exec.list_updates(l.refresh)
                .map(|u| Some(Answer::Updates(u)))
                .map_err(answer),
        ),
        Some(Operation::ApplyUpdates(a)) => (
            "apply_updates",
            a.run_id.as_str(),
            start(updates(a), config, exec),
        ),
        Some(Operation::Reboot(r)) => ("reboot", r.run_id.as_str(), start(reboot(r), config, exec)),
        Some(Operation::SelfUpdate(s)) => (
            "self_update",
            s.run_id.as_str(),
            start(self_update(s), config, exec),
        ),
        Some(Operation::ConfirmSelfUpdate(c)) => (
            "confirm_self_update",
            c.run_id.as_str(),
            run_id(&c.run_id).and_then(|id| exec.confirm(&id).map(|()| None).map_err(answer)),
        ),
        Some(Operation::RunResult(r)) => (
            "run_result",
            r.run_id.as_str(),
            run_id(&r.run_id).map(|id| Some(Answer::Run(exec.result(&id)))),
        ),
        Some(Operation::ReadSmart(_)) if !config.allow_smart => (
            "read_smart",
            "",
            Err(refused(&format!(
                "allow_smart = false in {}",
                crate::config::PATH
            ))),
        ),
        Some(Operation::ReadSmart(_)) => (
            "read_smart",
            "",
            exec.read_smart()
                .map(|s| Some(Answer::Smart(s)))
                .map_err(answer),
        ),
    };
    let params = match &request.operation {
        Some(Operation::ApplyUpdates(a)) => format!(
            "scope={}",
            Scope::try_from(a.scope).map_or("invalid".into(), |s| s
                .as_str_name()
                .trim_start_matches("SCOPE_")
                .to_lowercase())
        ),
        Some(Operation::Reboot(r)) => format!("reason={:?}", truncated(&r.reason)),
        Some(Operation::SelfUpdate(s)) => format!("version={:?}", truncated(&s.version)),
        Some(Operation::ListUpdates(l)) => format!("refresh={}", l.refresh),
        _ => String::new(),
    };
    let (status, reason, result) = match outcome {
        Ok(result) => (Status::Ok, String::new(), result),
        Err((status, reason)) => (status, reason, None),
    };
    let audit = Audit {
        operation,
        run_id: if valid_run_id(run_id) {
            run_id.to_owned()
        } else {
            String::new()
        },
        status,
        reason: reason.clone(),
        params,
    };
    let response = HelperResponse {
        status: status.into(),
        reason,
        helper_version: crate::VERSION.to_owned(),
        result,
    };
    (response, audit)
}

type Outcome = Result<Option<Answer>, (Status, String)>;

fn refused(reason: &str) -> (Status, String) {
    (Status::Refused, reason.to_owned())
}

fn unsupported(reason: &str) -> (Status, String) {
    (Status::Unsupported, reason.to_owned())
}

fn answer(f: Failure) -> (Status, String) {
    match f {
        Failure::Busy => (Status::Busy, "another run is in progress".to_owned()),
        Failure::Later(r) => (Status::Busy, r),
        Failure::Unsupported(r) => (Status::Unsupported, r),
        Failure::Refused(r) => (Status::Refused, r),
        Failure::Failed(r) => (Status::Failed, r),
    }
}

/// Checks the run and the host's veto, then starts it.
fn start(run: Result<Run, (Status, String)>, config: Config, exec: &mut dyn Executor) -> Outcome {
    let run = run?;
    let (allowed, key) = match run {
        Run::Updates { .. } => (config.allow_remote_updates, "allow_remote_updates"),
        Run::Reboot { .. } => (config.allow_remote_reboot, "allow_remote_reboot"),
        Run::SelfUpdate { .. } => (config.allow_self_update, "allow_self_update"),
    };
    if !allowed {
        return Err(refused(&format!(
            "{key} = false in {}",
            crate::config::PATH
        )));
    }
    exec.start(&run).map(|()| None).map_err(answer)
}

/// At most [`MAX_REASON`] bytes of what a caller sent, for the journal.
fn truncated(s: &str) -> &str {
    let mut end = s.len().min(MAX_REASON);
    while !s.is_char_boundary(end) {
        end -= 1;
    }
    &s[..end]
}

fn updates(a: &ApplyUpdates) -> Result<Run, (Status, String)> {
    let Ok(scope @ (Scope::Security | Scope::All)) = Scope::try_from(a.scope) else {
        return Err(refused("scope must be security or all"));
    };
    Ok(Run::Updates {
        id: run_id(&a.run_id)?,
        scope,
    })
}

fn reboot(r: &Reboot) -> Result<Run, (Status, String)> {
    let id = run_id(&r.run_id)?;
    if !valid_reason(&r.reason) {
        return Err(refused("reason must be 1 to 200 bytes of printable text"));
    }
    Ok(Run::Reboot {
        id,
        reason: r.reason.clone(),
    })
}

fn self_update(s: &SelfUpdate) -> Result<Run, (Status, String)> {
    let id = run_id(&s.run_id)?;
    if !crate::minisign::valid_version(&s.version) {
        return Err(refused(
            "version must be a semantic version of at most 64 bytes",
        ));
    }
    Ok(Run::SelfUpdate {
        id,
        version: s.version.clone(),
    })
}

fn run_id(id: &str) -> Result<String, (Status, String)> {
    if valid_run_id(id) {
        Ok(id.to_owned())
    } else {
        Err(refused("run_id must be a lowercase UUID"))
    }
}

/// A canonical, lowercase UUID: it ends up in file and unit names.
#[must_use]
pub fn valid_run_id(id: &str) -> bool {
    id.len() == 36
        && id.bytes().enumerate().all(|(i, b)| match i {
            8 | 13 | 18 | 23 => b == b'-',
            _ => b.is_ascii_digit() || (b'a'..=b'f').contains(&b),
        })
}

/// Printable text for the journal and the users' terminals: no control or format characters
/// (Cf: bidi overrides, zero width, soft hyphen, tags), no line or paragraph separators and no
/// Hangul fillers – the same as the server's textsafe (review M4-30).
#[must_use]
pub fn valid_reason(reason: &str) -> bool {
    !reason.is_empty()
        && reason.len() <= MAX_REASON
        && !reason.chars().any(|c| {
            c.is_control()
                || matches!(c, '\u{00ad}' | '\u{0600}'..='\u{0605}' | '\u{061c}' | '\u{06dd}' | '\u{070f}'
                    | '\u{08e2}' | '\u{115f}' | '\u{1160}' | '\u{180e}' | '\u{200b}'..='\u{200f}'
                    | '\u{2028}'..='\u{202e}' | '\u{2060}'..='\u{206f}' | '\u{3164}' | '\u{feff}'
                    | '\u{ffa0}' | '\u{fff9}'..='\u{fffb}' | '\u{110bd}' | '\u{110cd}'
                    | '\u{13430}'..='\u{1343f}' | '\u{1bca0}'..='\u{1bca3}' | '\u{1d173}'..='\u{1d17a}'
                    | '\u{e0001}' | '\u{e0020}'..='\u{e007f}')
        })
}

#[cfg(test)]
mod tests {
    use sermon_helper_proto::v1::{
        Command, ConfirmSelfUpdate, GetStatus, ListUpdates, ReadSmart, RunPhase, RunResult,
    };

    use super::*;

    const ID: &str = "0193f5a0-0000-7000-8000-000000000001";

    /// Records the runs it was asked to start.
    #[derive(Default)]
    struct Recorder {
        started: Vec<Run>,
        busy: bool,
        confirmed: Vec<String>,
        smart_reads: usize,
    }

    impl Executor for Recorder {
        fn status(&self) -> StatusInfo {
            StatusInfo {
                running_run_id: ID.to_owned(),
                allow_remote_reboot: true,
                ..StatusInfo::default()
            }
        }
        fn list_updates(&mut self, refresh: bool) -> Result<Updates, Failure> {
            Ok(Updates {
                refreshed: refresh,
                ..Updates::default()
            })
        }
        fn start(&mut self, run: &Run) -> Result<(), Failure> {
            if self.busy {
                return Err(Failure::Busy);
            }
            self.started.push(run.clone());
            Ok(())
        }
        fn result(&self, id: &str) -> RunState {
            RunState {
                run_id: id.to_owned(),
                phase: RunPhase::Running.into(),
                ..RunState::default()
            }
        }
        fn confirm(&mut self, id: &str) -> Result<(), Failure> {
            if self.busy {
                return Err(Failure::Refused("no self-update waits".into()));
            }
            self.confirmed.push(id.to_owned());
            Ok(())
        }
        fn read_smart(&mut self) -> Result<Smart, Failure> {
            self.smart_reads += 1;
            if self.busy {
                return Err(Failure::Later("read a minute ago".into()));
            }
            Ok(Smart {
                disks: vec![Command::default()],
                truncated: false,
            })
        }
    }

    fn ask(op: Operation, config: Config, exec: &mut Recorder) -> (HelperResponse, Audit) {
        handle(
            &HelperRequest {
                operation: Some(op),
            },
            config,
            exec,
        )
    }

    fn all_allowed() -> Config {
        Config {
            allow_remote_updates: true,
            allow_remote_reboot: true,
            allow_self_update: true,
            allow_prerelease: true,
            allow_smart: true,
        }
    }

    fn apply(id: &str, scope: Scope) -> Operation {
        Operation::ApplyUpdates(ApplyUpdates {
            run_id: id.to_owned(),
            scope: scope.into(),
        })
    }

    fn reboot_op(reason: &str) -> Operation {
        Operation::Reboot(Reboot {
            run_id: ID.to_owned(),
            reason: reason.to_owned(),
        })
    }

    #[test]
    fn starts_valid_allowed_runs() {
        let mut exec = Recorder::default();
        let config = all_allowed();
        for op in [
            apply(ID, Scope::Security),
            apply(ID, Scope::All),
            reboot_op("kernel 6.1.0-28"),
            Operation::SelfUpdate(SelfUpdate {
                run_id: ID.to_owned(),
                version: "1.2.3-rc.1".to_owned(),
            }),
        ] {
            let (r, audit) = ask(op, config, &mut exec);
            assert_eq!(r.status(), Status::Ok, "{r:?}");
            assert_eq!((audit.run_id.as_str(), audit.status), (ID, Status::Ok));
            assert_eq!(r.helper_version, crate::VERSION);
        }
        assert_eq!(
            exec.started,
            [
                Run::Updates {
                    id: ID.into(),
                    scope: Scope::Security
                },
                Run::Updates {
                    id: ID.into(),
                    scope: Scope::All
                },
                Run::Reboot {
                    id: ID.into(),
                    reason: "kernel 6.1.0-28".into()
                },
                Run::SelfUpdate {
                    id: ID.into(),
                    version: "1.2.3-rc.1".into()
                },
            ]
        );
    }

    // The host's veto wins over everything the agent or the server ask for.
    #[test]
    fn the_hosts_veto_refuses_and_starts_nothing() {
        let mut exec = Recorder::default();
        let none = Config {
            allow_remote_updates: false,
            allow_remote_reboot: false,
            allow_self_update: false,
            allow_prerelease: false,
            allow_smart: false,
        };
        for (op, key) in [
            (apply(ID, Scope::Security), "allow_remote_updates"),
            (reboot_op("x"), "allow_remote_reboot"),
            (
                Operation::SelfUpdate(SelfUpdate {
                    run_id: ID.to_owned(),
                    version: "1.2.3".to_owned(),
                }),
                "allow_self_update",
            ),
        ] {
            let (r, audit) = ask(op, none, &mut exec);
            assert_eq!(r.status(), Status::Refused);
            assert!(r.reason.contains(key) && audit.reason == r.reason, "{r:?}");
        }
        // Each switch on its own.
        let only_reboot = Config {
            allow_remote_reboot: true,
            ..none
        };
        assert_eq!(
            ask(apply(ID, Scope::All), only_reboot, &mut exec)
                .0
                .status(),
            Status::Refused
        );
        assert_eq!(
            ask(reboot_op("x"), only_reboot, &mut exec).0.status(),
            Status::Ok
        );
        let only_updates = Config {
            allow_remote_updates: true,
            ..none
        };
        assert_eq!(
            ask(reboot_op("x"), only_updates, &mut exec).0.status(),
            Status::Refused
        );
        let only_self = Config {
            allow_self_update: true,
            ..none
        };
        assert_eq!(
            ask(apply(ID, Scope::All), only_self, &mut exec).0.status(),
            Status::Refused
        );
        assert_eq!(exec.started.len(), 1);
    }

    #[test]
    fn refuses_invalid_requests_and_logs_no_unchecked_id() {
        let mut exec = Recorder::default();
        let config = all_allowed();
        for op in [
            apply(ID, Scope::Unspecified),
            Operation::ApplyUpdates(ApplyUpdates {
                run_id: ID.to_owned(),
                scope: 7,
            }),
            apply("../../etc/passwd", Scope::All),
            apply(&ID.to_uppercase(), Scope::All),
            apply(&format!("{ID}0"), Scope::All),
            apply("0193f5a0x0000-7000-8000-000000000001", Scope::All),
            apply("", Scope::All),
            reboot_op(""),
            reboot_op(&"x".repeat(MAX_REASON + 1)),
            reboot_op("line\nbreak"),
            reboot_op("\u{1b}[2J"),
            reboot_op("evil\u{202e}txt"),
            reboot_op("zero\u{200b}width"),
            // Review M4-30: all format characters, separators and blank fillers.
            reboot_op("soft\u{ad}hyphen"),
            reboot_op("line\u{2028}separator"),
            reboot_op("tag\u{e0041}"),
            reboot_op("blank\u{3164}"),
            Operation::RunResult(RunResult {
                run_id: "x; reboot".to_owned(),
            }),
            Operation::SelfUpdate(SelfUpdate {
                run_id: ID.to_owned(),
                version: "../../bin/sh".to_owned(),
            }),
            Operation::SelfUpdate(SelfUpdate {
                run_id: ID.to_owned(),
                version: "e2e".to_owned(),
            }),
            Operation::ConfirmSelfUpdate(ConfirmSelfUpdate {
                run_id: "../x".to_owned(),
            }),
        ] {
            let (r, audit) = ask(op.clone(), config, &mut exec);
            assert_eq!(r.status(), Status::Refused, "{op:?}");
            assert!(audit.run_id.is_empty() || audit.run_id == ID, "{audit:?}");
        }
        assert_eq!(exec.started, []);
        assert!(valid_reason(&"ä".repeat(MAX_REASON / 2)));
    }

    // T41 (review M4-30): the journal names what a run was asked to do, escaped and bounded.
    #[test]
    fn the_journal_line_names_the_parameters() {
        let mut exec = Recorder::default();
        let config = Config {
            allow_remote_reboot: true,
            ..Config::default()
        };
        for (op, want) in [
            (
                reboot_op("kernel update"),
                r#"reason="kernel update""#.to_owned(),
            ),
            (
                reboot_op("line\nbreak"),
                r#"reason="line\nbreak""#.to_owned(),
            ),
            (
                reboot_op(&"x".repeat(MAX_REASON + 50)),
                format!("reason={:?}", "x".repeat(MAX_REASON)),
            ),
            (apply(ID, Scope::Security), "scope=security".to_owned()),
            (
                Operation::SelfUpdate(SelfUpdate {
                    run_id: ID.to_owned(),
                    version: "1.2.3".to_owned(),
                }),
                r#"version="1.2.3""#.to_owned(),
            ),
        ] {
            let (_, audit) = ask(op, config, &mut exec);
            assert_eq!(audit.params, want);
        }
    }

    // A newer agent asks for an operation this helper doesn't know: prost leaves it out.
    #[test]
    fn an_unknown_operation_is_unsupported() {
        let (r, audit) = handle(
            &HelperRequest { operation: None },
            all_allowed(),
            &mut Recorder::default(),
        );
        assert_eq!(
            (r.status(), audit.operation),
            (Status::Unsupported, "unknown")
        );
    }

    #[test]
    fn status_lists_and_results() {
        let mut exec = Recorder::default();
        let config = Config {
            allow_remote_reboot: false,
            ..all_allowed()
        };
        let (r, _) = ask(Operation::GetStatus(GetStatus {}), config, &mut exec);
        let Some(Answer::StatusInfo(info)) = r.result else {
            panic!("{r:?}")
        };
        // The permissions come from the configuration, not from the executor.
        assert!(info.allow_remote_updates && !info.allow_remote_reboot && info.allow_self_update);
        assert_eq!(info.running_run_id, ID);
        let (r, _) = ask(
            Operation::ListUpdates(ListUpdates { refresh: true }),
            config,
            &mut exec,
        );
        assert!(matches!(
            r.result,
            Some(Answer::Updates(Updates {
                refreshed: true,
                ..
            }))
        ));
        let (r, audit) = ask(
            Operation::RunResult(RunResult {
                run_id: ID.to_owned(),
            }),
            config,
            &mut exec,
        );
        assert!(
            matches!(&r.result, Some(Answer::Run(s)) if s.run_id == ID && s.phase() == RunPhase::Running)
        );
        assert_eq!(audit.run_id, ID);
        let confirm = || {
            Operation::ConfirmSelfUpdate(ConfirmSelfUpdate {
                run_id: ID.to_owned(),
            })
        };
        let (r, audit) = ask(confirm(), config, &mut exec);
        assert_eq!(
            (r.status(), audit.operation),
            (Status::Ok, "confirm_self_update")
        );
        assert_eq!(exec.confirmed, [ID]);
        exec.busy = true;
        assert_eq!(
            ask(apply(ID, Scope::All), config, &mut exec).0.status(),
            Status::Busy
        );
        assert_eq!(
            ask(confirm(), config, &mut exec).0.status(),
            Status::Refused
        );
    }

    // M5-18: SMART is read only if helper.toml allows it; the helper asks the executor, which
    // bounds how often.
    #[test]
    fn reads_smart_only_if_the_host_allows_it() {
        let mut exec = Recorder::default();
        let read = || Operation::ReadSmart(ReadSmart {});
        let (r, audit) = ask(read(), all_allowed(), &mut exec);
        assert_eq!((r.status(), audit.operation), (Status::Ok, "read_smart"));
        assert!(matches!(r.result, Some(Answer::Smart(s)) if s.disks.len() == 1));
        let vetoed = Config {
            allow_smart: false,
            ..all_allowed()
        };
        let (r, audit) = ask(read(), vetoed, &mut exec);
        assert_eq!(r.status(), Status::Refused);
        assert!(r.reason.contains("allow_smart = false") && audit.reason == r.reason);
        assert_eq!(exec.smart_reads, 1, "asked although vetoed");
        exec.busy = true;
        assert_eq!(
            ask(read(), all_allowed(), &mut exec).0.status(),
            Status::Busy
        );
        let (r, _) = ask(Operation::GetStatus(GetStatus {}), vetoed, &mut exec);
        assert!(
            matches!(r.result, Some(Answer::StatusInfo(i)) if !i.allow_smart && i.allow_remote_updates)
        );
        let (r, _) = ask(Operation::GetStatus(GetStatus {}), all_allowed(), &mut exec);
        assert!(matches!(r.result, Some(Answer::StatusInfo(i)) if i.allow_smart));
    }
}
