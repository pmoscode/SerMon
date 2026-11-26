//! What the host could update (KONZEPT §14.1, M4-07): the root helper runs the package manager,
//! this module parses its raw lists (`sermon-updates`) and buffers an `UpdateReport`.
//!
//! A report comes shortly after the start and every 6 hours with spread (with fresh sources),
//! 2 minutes after the package databases of dpkg, rpm or apk last changed (an admin installed
//! something; from the local lists), a few seconds after a run of the root helper ended (the agent
//! knows the package manager is done: no debounce, M4-28) and on the server's `RefreshUpdateInfo` (fresh
//! sources, at most every 5 minutes). Without a helper the report says so instead of failing; a
//! busy helper (a run holds its lock) is asked again 5 minutes later.
//!
//! The databases are polled for their modification time every 30 seconds instead of watched
//! with inotify: one `stat` per file is less than a watch costs to keep, and the 2 minutes of
//! debounce make the delay irrelevant.

use std::collections::hash_map::RandomState;
use std::future::Future;
use std::hash::{BuildHasher, Hasher};
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::{Duration, SystemTime};

use sermon_helper_proto::v1::{
    Command, Distribution, GetStatus, HelperRequest, HelperResponse, ListUpdates, Status,
    StatusInfo, Updates, helper_request::Operation, helper_response::Result as Answer,
};
use sermon_transport::proto::agent::v1::{
    HelperPermissions, PackageManager, UpdateAvailability, UpdatePackage, UpdateReport,
};
use sermon_updates::{Package, Reboot};
use tokio::sync::Notify;
use tokio::time::Instant;
use tokio_util::sync::CancellationToken;

use crate::helper::{self, HelperError};

/// The package databases whose change means the package manager ran (relative to the host root).
pub const DATABASES: [&str; 5] = [
    "var/lib/dpkg/status",
    "var/lib/rpm/rpmdb.sqlite",
    "var/lib/rpm/Packages",
    "usr/lib/sysimage/rpm/rpmdb.sqlite",
    "lib/apk/db/installed",
];
/// At most this many packages per report, security updates first (the protocol's limit).
pub const MAX_PACKAGES: usize = 5000;
/// At most this many bytes per report: the packages beyond are left out, the counts stay.
pub const MAX_REPORT_BYTES: usize = 2 << 20;
/// Longest reason (the protocol's limit).
pub const MAX_REASON: usize = 1024;
/// How long a listing may take: the helper allows each command 10 minutes, dnf runs four.
const LIST_TIMEOUT: Duration = Duration::from_mins(45);
const STATUS_TIMEOUT: Duration = Duration::from_secs(10);

/// When reports are due.
#[derive(Debug, Clone, Copy)]
pub struct Schedule {
    /// Between two periodic reports, give or take `spread`.
    pub every: Duration,
    pub spread: Duration,
    /// The first report comes between `first / 6` and `first` after the start.
    pub first: Duration,
    /// After the last change of a package database.
    pub debounce: Duration,
    /// Between two reports with fresh sources on request.
    pub refresh_every: Duration,
    /// After a busy helper.
    pub retry: Duration,
    /// How often the package databases are looked at.
    pub poll: Duration,
    /// After a run of the root helper ended (it releases its lock just after the result).
    pub after_run: Duration,
}

impl Default for Schedule {
    fn default() -> Self {
        Self {
            every: Duration::from_hours(6),
            spread: Duration::from_mins(30),
            first: Duration::from_mins(1),
            debounce: Duration::from_mins(2),
            refresh_every: Duration::from_mins(5),
            retry: Duration::from_mins(5),
            poll: Duration::from_secs(30),
            after_run: Duration::from_secs(5),
        }
    }
}

/// What a listing gave.
#[derive(Debug, Clone, PartialEq)]
pub enum Listing {
    Report(UpdateReport),
    /// The helper is busy with a run; ask again later.
    Busy,
}

/// Where the reports come from: the helper, or a fake in tests.
pub trait Source: Send + Sync + 'static {
    /// Lists the updates, `refresh` with fresh sources.
    fn list(&self, refresh: bool) -> impl Future<Output = Listing> + Send;
}

/// The root helper at its socket.
#[derive(Debug, Clone)]
pub struct HelperSource {
    socket: PathBuf,
    root: PathBuf,
}

impl HelperSource {
    /// The helper at `socket`, the host's files below `root`.
    #[must_use]
    pub fn new(socket: impl Into<PathBuf>, root: impl Into<PathBuf>) -> Self {
        Self {
            socket: socket.into(),
            root: root.into(),
        }
    }

    /// Asks the helper (blocking: up to [`LIST_TIMEOUT`]); the report names its version.
    #[must_use]
    pub fn list_blocking(&self, refresh: bool) -> Listing {
        let mut version = String::new();
        let mut listing = self.list_naming(refresh, &mut version);
        if let Listing::Report(report) = &mut listing {
            report.helper_version = version;
        }
        listing
    }

    fn list_naming(&self, refresh: bool, version: &mut String) -> Listing {
        let now = SystemTime::now();
        let request = |operation| HelperRequest {
            operation: Some(operation),
        };
        let status = helper::call(
            &self.socket,
            &request(Operation::GetStatus(GetStatus {})),
            STATUS_TIMEOUT,
        );
        if let Ok(response) = &status {
            version.clone_from(&release_version(&response.helper_version));
        }
        let info = match answer(status) {
            Ok(Answer::StatusInfo(info)) => info,
            Ok(_) => return failed("the root helper sent no status", now),
            Err(unavailable) => return Listing::Report(unavailable.report(now)),
        };
        if info.distribution() == Distribution::Unspecified {
            let mut report = report_of(
                UpdateAvailability::Unsupported,
                "no apt, dnf or apk on this host",
                now,
            );
            report.permissions = Some(permissions(&info));
            return Listing::Report(report);
        }
        let list = helper::call(
            &self.socket,
            &request(Operation::ListUpdates(ListUpdates { refresh })),
            LIST_TIMEOUT,
        );
        let updates = match answer(list) {
            Ok(Answer::Updates(updates)) => updates,
            Ok(_) => return failed("the root helper sent no updates", now),
            Err(Unavailable::Busy) => return Listing::Busy,
            // It knows the host's package manager, so it is the operation it doesn't know.
            Err(Unavailable::Unsupported(why)) => {
                return Listing::Report(report_of(UpdateAvailability::HelperTooOld, &why, now));
            }
            Err(unavailable) => return Listing::Report(unavailable.report(now)),
        };
        let host = HostState {
            kernel: std::fs::read_to_string(self.root.join("proc/sys/kernel/osrelease"))
                .unwrap_or_default()
                .trim()
                .to_owned(),
            modules: modules(&self.root.join("lib/modules")),
            last_change: last_change(&self.root),
        };
        Listing::Report(from_updates(&updates, &info, &host, now))
    }
}

impl Source for HelperSource {
    async fn list(&self, refresh: bool) -> Listing {
        let source = self.clone();
        tokio::task::spawn_blocking(move || source.list_blocking(refresh))
            .await
            .unwrap_or_else(|e| failed(&format!("listing ended: {e}"), SystemTime::now()))
    }
}

/// Why the helper gave no result.
#[derive(Debug)]
enum Unavailable {
    NoHelper(String),
    Busy,
    Unsupported(String),
    Failed(String),
}

impl Unavailable {
    fn report(self, now: SystemTime) -> UpdateReport {
        match self {
            Self::NoHelper(why) => report_of(UpdateAvailability::NoHelper, &why, now),
            Self::Unsupported(why) => report_of(UpdateAvailability::Unsupported, &why, now),
            Self::Busy => report_of(UpdateAvailability::Failed, "the root helper is busy", now),
            Self::Failed(why) => report_of(UpdateAvailability::Failed, &why, now),
        }
    }
}

fn answer(response: Result<HelperResponse, HelperError>) -> Result<Answer, Unavailable> {
    let response = match response {
        Ok(r) => r,
        Err(e @ (HelperError::Unreachable(_) | HelperError::NoAnswer)) => {
            return Err(Unavailable::NoHelper(e.to_string()));
        }
        Err(e @ HelperError::Malformed(_)) => return Err(Unavailable::Failed(e.to_string())),
    };
    let reason = format!(
        "the root helper answered {}: {}",
        response.status().as_str_name(),
        response.reason
    );
    match response.status() {
        Status::Ok => response
            .result
            .ok_or_else(|| Unavailable::Failed("the root helper sent nothing".into())),
        Status::Busy => Err(Unavailable::Busy),
        Status::Unsupported => Err(Unavailable::Unsupported(reason)),
        Status::Refused | Status::Failed | Status::Unspecified => Err(Unavailable::Failed(reason)),
    }
}

fn failed(why: &str, now: SystemTime) -> Listing {
    Listing::Report(report_of(UpdateAvailability::Failed, why, now))
}

/// `v` if it reads like a release version (the server refuses a report with anything else),
/// else empty (M4-31).
fn release_version(v: &str) -> String {
    let (core, pre) = v.split_once('-').unwrap_or((v, ""));
    let parts: Vec<&str> = core.split('.').collect();
    let numbers = parts.len() == 3
        && parts
            .iter()
            .all(|p| !p.is_empty() && p.bytes().all(|b| b.is_ascii_digit()));
    let suffix = !v.contains('-')
        || !pre.is_empty()
            && pre
                .bytes()
                .all(|b| b.is_ascii_alphanumeric() || b == b'.' || b == b'-');
    if numbers && suffix && v.len() <= 64 {
        v.to_owned()
    } else {
        String::new()
    }
}

/// A report without packages.
#[must_use]
pub fn report_of(availability: UpdateAvailability, reason: &str, now: SystemTime) -> UpdateReport {
    UpdateReport {
        collected_at: Some(now.into()),
        availability: availability.into(),
        reason: bounded_text(reason, MAX_REASON),
        ..UpdateReport::default()
    }
}

fn permissions(info: &StatusInfo) -> HelperPermissions {
    HelperPermissions {
        remote_updates: info.allow_remote_updates,
        remote_reboot: info.allow_remote_reboot,
        self_update: info.allow_self_update,
    }
}

/// What the agent reads itself: the running kernel and its modules (Alpine) and the last change
/// of the package databases (the last upgrade, as far as the host tells).
#[derive(Debug, Clone, Default)]
pub struct HostState {
    pub kernel: String,
    pub modules: Vec<String>,
    pub last_change: Option<SystemTime>,
}

/// The directories of `/lib/modules`, sorted.
fn modules(dir: &Path) -> Vec<String> {
    let mut out: Vec<String> = std::fs::read_dir(dir)
        .into_iter()
        .flatten()
        .flatten()
        .filter(|e| e.file_type().is_ok_and(|t| t.is_dir()))
        .filter_map(|e| e.file_name().into_string().ok())
        .collect();
    out.sort();
    out
}

/// The newest modification time of the package databases below `root`.
#[must_use]
pub fn last_change(root: &Path) -> Option<SystemTime> {
    DATABASES
        .iter()
        .filter_map(|db| {
            std::fs::metadata(root.join(db))
                .and_then(|m| m.modified())
                .ok()
        })
        .max()
}

/// The text of a command, lossy: the package sources are not trusted to send UTF-8.
fn text(c: &Command) -> String {
    String::from_utf8_lossy(&c.output).into_owned()
}

/// The end of an output for a reason: printable, at most 200 bytes.
fn tail(c: &Command) -> String {
    let printable: String = text(c)
        .chars()
        .map(|ch| if ch.is_control() { ' ' } else { ch })
        .collect();
    let trimmed = printable.trim();
    let mut start = trimmed.len().saturating_sub(200);
    while !trimmed.is_char_boundary(start) {
        start += 1;
    }
    trimmed[start..].to_owned()
}

/// `s` cut to `max` bytes at a character boundary.
fn bounded_text(s: &str, max: usize) -> String {
    let mut end = s.len().min(max);
    while !s.is_char_boundary(end) {
        end -= 1;
    }
    s[..end].to_owned()
}

/// The report of the helper's raw lists.
#[must_use]
pub fn from_updates(
    updates: &Updates,
    info: &StatusInfo,
    host: &HostState,
    now: SystemTime,
) -> UpdateReport {
    let mut notes = Vec::new();
    let mut commands = updates.commands.as_slice();
    let mut refreshed = updates.refreshed;
    if updates.refreshed {
        let Some((refresh, rest)) = commands.split_first() else {
            return report_of(
                UpdateAvailability::Failed,
                "the root helper sent no outputs",
                now,
            );
        };
        if refresh.exit_code != 0 {
            refreshed = false;
            notes.push(format!(
                "refreshing the sources failed with exit code {}, the lists may be old: {}",
                refresh.exit_code,
                tail(refresh)
            ));
        }
        commands = rest;
    }
    let expected = match updates.distribution() {
        Distribution::Apt | Distribution::Apk => 1,
        Distribution::Dnf => 3,
        Distribution::Unspecified => {
            return report_of(
                UpdateAvailability::Unsupported,
                "no apt, dnf or apk on this host",
                now,
            );
        }
    };
    if commands.len() != expected {
        return report_of(
            UpdateAvailability::Failed,
            &format!(
                "the root helper sent {} lists, {expected} expected",
                commands.len()
            ),
            now,
        );
    }
    if commands.iter().any(|c| c.truncated) {
        notes.push("an output was cut, the list may be incomplete".into());
    }
    let (manager, packages, reboot) = match parse(updates, commands, host, &mut notes) {
        Ok(parsed) => parsed,
        Err(why) => return report_of(UpdateAvailability::Failed, &why, now),
    };
    let (packages, total, security) = sermon_updates::bounded(packages, MAX_PACKAGES);
    let (reboot_required, reboot_reason) = match reboot {
        Reboot::Required(why) => (true, bounded_text(&why, MAX_REASON)),
        Reboot::NotRequired | Reboot::Unknown => (false, String::new()),
    };
    let mut report = UpdateReport {
        collected_at: Some(now.into()),
        availability: UpdateAvailability::Listed.into(),
        reason: bounded_text(&notes.join("; "), MAX_REASON),
        package_manager: manager.into(),
        packages: packages.into_iter().map(to_proto).collect(),
        total_count: u32::try_from(total).unwrap_or(u32::MAX).min(1_000_000),
        security_count: u32::try_from(security).unwrap_or(u32::MAX).min(1_000_000),
        reboot_required,
        reboot_reason,
        last_upgrade_at: host.last_change.map(Into::into),
        permissions: Some(permissions(info)),
        refreshed,
        helper_version: String::new(),
    };
    fit(&mut report);
    report
}

/// The packages and the reboot of the lists (`commands` without the refresh), or why they
/// failed; what is unknown goes to `notes`.
fn parse(
    updates: &Updates,
    commands: &[Command],
    host: &HostState,
    notes: &mut Vec<String>,
) -> Result<(PackageManager, Vec<Package>, Reboot), String> {
    let list = &commands[0];
    let ok = match updates.distribution() {
        Distribution::Dnf => [0, 100].contains(&list.exit_code), // 100: updates available
        _ => list.exit_code == 0,
    };
    if !ok {
        return Err(format!(
            "{} failed with exit code {}: {}",
            list.name,
            list.exit_code,
            tail(list)
        ));
    }
    Ok(match updates.distribution() {
        Distribution::Apt => (
            PackageManager::Apt,
            sermon_updates::apt(&text(list)),
            sermon_updates::apt_reboot(
                updates.reboot_required,
                &String::from_utf8_lossy(&updates.reboot_required_pkgs),
            ),
        ),
        Distribution::Dnf => {
            let security = &commands[1];
            if security.exit_code != 0 {
                notes.push(format!(
                    "the security updates are unknown: {} failed with exit code {}",
                    security.name, security.exit_code
                ));
            }
            let marked = if security.exit_code == 0 {
                text(security)
            } else {
                String::new()
            };
            let reboot = sermon_updates::dnf_reboot(&text(&commands[2]));
            if reboot == Reboot::Unknown {
                notes
                    .push("whether a reboot is needed is unknown (no dnf needs-restarting)".into());
            }
            (
                PackageManager::Dnf,
                sermon_updates::dnf(&text(list), &marked),
                reboot,
            )
        }
        _ => (
            PackageManager::Apk,
            sermon_updates::apk(&text(list)),
            sermon_updates::apk_reboot(&host.kernel, &host.modules),
        ),
    })
}

fn to_proto(p: Package) -> UpdatePackage {
    UpdatePackage {
        name: p.name,
        installed_version: p.installed,
        candidate_version: p.candidate,
        source: p.source,
        security: p.security,
    }
}

/// Leaves out packages from the end (the ordinary updates first) until the report has at most
/// [`MAX_REPORT_BYTES`]; the counts stay.
fn fit(report: &mut UpdateReport) {
    use prost::Message;
    while report.encoded_len() > MAX_REPORT_BYTES && !report.packages.is_empty() {
        let keep = report.packages.len() * 3 / 4;
        report.packages.truncate(keep);
    }
}

/// A pseudo-random spread from a seed (no generator in the agent for this).
#[derive(Debug, Clone, Copy)]
pub struct Spread(u64);

impl Spread {
    /// Seeded from the process's hash keys.
    #[must_use]
    pub fn random() -> Self {
        Self(RandomState::new().build_hasher().finish() | 1)
    }

    /// Seeded with `seed` (tests).
    #[must_use]
    pub const fn seeded(seed: u64) -> Self {
        Self(seed | 1)
    }

    /// A duration in `[0, max]`.
    fn next(&mut self, max: Duration) -> Duration {
        // xorshift64
        self.0 ^= self.0 << 13;
        self.0 ^= self.0 >> 7;
        self.0 ^= self.0 << 17;
        let millis = u64::try_from(max.as_millis()).unwrap_or(u64::MAX);
        Duration::from_millis(self.0 % millis.saturating_add(1))
    }
}

/// When the first report comes after the start: between `first / 6` and `first`.
fn first_report(schedule: &Schedule, spread: &mut Spread) -> Duration {
    schedule.first / 6 + spread.next(schedule.first.saturating_sub(schedule.first / 6))
}

/// Reports what `source` lists into `sink` on the [`Schedule`] until `shutdown`; `refresh` is
/// woken by the server's `RefreshUpdateInfo`, `ran` when a run of the root helper ended.
#[expect(
    clippy::too_many_arguments,
    reason = "the ports of the task, wired once"
)]
pub async fn run<S: Source>(
    source: Arc<S>,
    root: PathBuf,
    mut sink: impl FnMut(UpdateReport) + Send,
    refresh: Arc<Notify>,
    ran: Arc<Notify>,
    schedule: Schedule,
    mut spread: Spread,
    shutdown: CancellationToken,
) {
    let start = Instant::now();
    let mut periodic = start + first_report(&schedule, &mut spread);
    let mut changed: Option<Instant> = None;
    let mut requested: Option<Instant> = None;
    let mut last_refresh: Option<Instant> = None;
    let mut seen = last_change(&root);
    let mut poll = tokio::time::interval_at(start + schedule.poll, schedule.poll);
    poll.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Delay);
    loop {
        let next = [Some(periodic), changed, requested]
            .into_iter()
            .flatten()
            .min()
            .unwrap_or(periodic);
        tokio::select! {
            () = shutdown.cancelled() => return,
            () = refresh.notified() => {
                let now = Instant::now();
                let due = last_refresh.map_or(now, |t| (t + schedule.refresh_every).max(now));
                // Always 5 minutes after the last fresh sources, also if those came meanwhile.
                requested = Some(due);
                continue;
            }
            () = ran.notified() => {
                // The run's own change is reported now, not again by the poll.
                seen = last_change(&root);
                changed = Some(Instant::now() + schedule.after_run);
                continue;
            }
            _ = poll.tick() => {
                let current = last_change(&root);
                if current != seen {
                    seen = current;
                    changed = Some(Instant::now() + schedule.debounce);
                }
                continue;
            }
            () = tokio::time::sleep_until(next) => {}
        }
        let now = Instant::now();
        let due = |at: Option<Instant>| at.is_some_and(|t| t <= now);
        let (periodic_due, change_due, request_due) =
            (periodic <= now, due(changed), due(requested));
        let with_refresh = periodic_due || request_due;
        let listing = tokio::select! {
            () = shutdown.cancelled() => return,
            listing = source.list(with_refresh) => listing,
        };
        match listing {
            Listing::Report(report) => {
                tracing::debug!(
                    availability = report.availability,
                    total = report.total_count,
                    refresh = with_refresh,
                    "update report"
                );
                sink(report);
                let done = Instant::now();
                if with_refresh {
                    last_refresh = Some(done);
                }
                if periodic_due {
                    periodic =
                        done + schedule.every - schedule.spread + spread.next(schedule.spread * 2);
                }
                if change_due {
                    changed = None;
                }
                if request_due {
                    requested = None;
                }
            }
            Listing::Busy => {
                tracing::info!("the root helper is busy, listing the updates later");
                let later = Instant::now() + schedule.retry;
                if periodic_due {
                    periodic = later;
                }
                if change_due {
                    changed = Some(later);
                }
                if request_due {
                    requested = Some(later);
                }
            }
        }
    }
}

#[cfg(test)]
mod tests;
