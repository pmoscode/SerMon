//! Failed systemd units (M2-22, KONZEPT §8.1): one series per failed unit, so an alert names it.
//! Runs every 30 s on hosts with systemd (`/run/systemd/system`).
//!
//! | Metric | Labels | Unit |
//! |---|---|---|
//! | `sermon_systemd_unit_failed` | `unit` (at most 50) | boolean |
//!
//! The list comes from `systemctl list-units --state=failed` with fixed arguments (no shell, an
//! empty environment, a timeout); as an unprivileged user systemctl asks systemd over D-Bus.
//!
//! Missing data does not end an alert. So the collector remembers units it saw failed and reports
//! 0 for them for 24 h after they recovered; the memory survives restarts in the state directory
//! (ADR 0026). Units that vanish (a transient unit is collected) count as recovered.

use std::collections::BTreeMap;
use std::io::{self, Read};
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::Arc;
use std::time::{Duration, UNIX_EPOCH};

use sermon_core::{Clock, Sample};

use crate::{CollectError, Collector, Context};

/// At most so many units are reported (failed ones and remembered ones together).
pub const MAX_UNITS: usize = 50;
/// How long a recovered unit is reported as 0.
pub const REMEMBER: Duration = Duration::from_hours(24);
/// The longest unit name taken (systemd allows 255 characters).
const MAX_UNIT_NAME: usize = 255;
/// systemctl gets this long.
const TIMEOUT: Duration = Duration::from_secs(10);
/// More output than this is no list of failed units.
const MAX_OUTPUT: u64 = 256 * 1024;

const SUFFIXES: [&str; 11] = [
    ".service",
    ".socket",
    ".target",
    ".device",
    ".mount",
    ".automount",
    ".swap",
    ".timer",
    ".path",
    ".slice",
    ".scope",
];

/// The unit names in the output of `systemctl list-units --plain --no-legend`, at most
/// [`MAX_UNITS`]; lines that name no valid unit are skipped.
#[must_use]
pub fn parse_failed(text: &str) -> Vec<String> {
    let mut out = Vec::new();
    for line in text.lines() {
        let mut words = line.split_whitespace();
        // Older systemd prints a bullet even with --plain.
        let name = match words.next() {
            Some("●" | "*") => words.next(),
            other => other,
        };
        if let Some(name) = name.filter(|n| valid_unit(n))
            && !out.iter().any(|u: &String| u == name)
            && out.len() < MAX_UNITS
        {
            out.push(name.to_owned());
        }
    }
    out
}

fn valid_unit(name: &str) -> bool {
    name.len() <= MAX_UNIT_NAME
        && SUFFIXES
            .iter()
            .any(|s| name.len() > s.len() && name.ends_with(s))
        && name
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b":_.-@\\".contains(&b))
}

/// Where the failed units come from: systemctl, or a fake in tests.
pub trait FailedUnits: Send {
    /// The output of `systemctl list-units --state=failed --plain --no-legend`.
    ///
    /// # Errors
    ///
    /// systemctl is missing, fails or takes too long.
    fn list(&self) -> io::Result<String>;
}

/// `systemctl` with fixed arguments.
#[derive(Debug, Clone)]
pub struct Systemctl {
    program: PathBuf,
    timeout: Duration,
}

impl Systemctl {
    /// systemctl at its usual places below the host root (`/usr/bin`, `/bin`).
    #[must_use]
    pub fn find(root: &Path) -> Option<Self> {
        ["usr/bin/systemctl", "bin/systemctl"]
            .iter()
            .map(|p| root.join(p))
            .find(|p| p.is_file())
            .map(|program| Self {
                program,
                timeout: TIMEOUT,
            })
    }
}

impl FailedUnits for Systemctl {
    fn list(&self) -> io::Result<String> {
        let mut child = Command::new(&self.program)
            .args([
                "list-units",
                "--state=failed",
                "--plain",
                "--no-legend",
                "--no-pager",
                "--full",
            ])
            .env_clear()
            .env("LC_ALL", "C")
            .env("SYSTEMD_COLORS", "0")
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .spawn()?;
        let stdout = child
            .stdout
            .take()
            .ok_or_else(|| io::Error::other("no stdout"))?;
        // Read in a thread: a hanging systemctl (D-Bus) must not block the collectors.
        let (done, result) = std::sync::mpsc::channel();
        std::thread::spawn(move || {
            let mut text = String::new();
            let read = stdout.take(MAX_OUTPUT).read_to_string(&mut text);
            let _ = done.send(read.map(|_| text));
        });
        let Ok(text) = result.recv_timeout(self.timeout) else {
            let _ = child.kill();
            let _ = child.wait();
            return Err(io::ErrorKind::TimedOut.into());
        };
        let status = child.wait()?;
        if !status.success() {
            return Err(io::Error::other(format!("systemctl: {status}")));
        }
        text
    }
}

/// Units seen failed, with the last time (seconds since the epoch), stored as `unit time` lines.
#[derive(Debug, Default, Clone, PartialEq, Eq)]
struct Memory(BTreeMap<String, u64>);

impl Memory {
    fn load(file: &Path) -> Self {
        let Ok(text) = std::fs::read_to_string(file) else {
            return Self::default();
        };
        Self(
            text.lines()
                .filter_map(|line| {
                    let (unit, at) = line.rsplit_once(' ')?;
                    Some((unit.to_owned(), at.parse().ok()?))
                })
                .filter(|(unit, _)| valid_unit(unit))
                .take(MAX_UNITS)
                .collect(),
        )
    }

    fn store(&self, file: &Path) -> io::Result<()> {
        let mut text = String::new();
        for (unit, at) in &self.0 {
            text.push_str(unit);
            text.push(' ');
            text.push_str(&at.to_string());
            text.push('\n');
        }
        let temporary = file.with_extension("new");
        std::fs::write(&temporary, text)?;
        std::fs::rename(&temporary, file)
    }
}

/// Failed-units collector.
pub struct SystemdCollector {
    source: Option<Box<dyn FailedUnits>>,
    clock: Arc<dyn Clock>,
    file: Option<PathBuf>,
    memory: Memory,
    warned: bool,
}

impl std::fmt::Debug for SystemdCollector {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("SystemdCollector")
            .field("memory", &self.memory)
            .finish_non_exhaustive()
    }
}

impl SystemdCollector {
    /// A collector asking `source`; it remembers recovered units in `file` (none: in memory only).
    #[must_use]
    pub fn new(
        source: Option<Box<dyn FailedUnits>>,
        clock: Arc<dyn Clock>,
        file: Option<PathBuf>,
    ) -> Self {
        let memory = file.as_deref().map_or_else(Memory::default, Memory::load);
        Self {
            source,
            clock,
            file,
            memory,
            warned: false,
        }
    }

    /// For a host: systemctl if systemd runs below `root`, the memory in `file`.
    #[must_use]
    pub fn for_host(root: &Path, clock: Arc<dyn Clock>, file: PathBuf) -> Self {
        let source = root
            .join("run/systemd/system")
            .is_dir()
            .then(|| Systemctl::find(root))
            .flatten()
            .map(|s| -> Box<dyn FailedUnits> { Box::new(s) });
        Self::new(source, clock, Some(file))
    }
}

impl Collector for SystemdCollector {
    fn name(&self) -> &'static str {
        "systemd"
    }

    fn interval_ticks(&self) -> u32 {
        30
    }

    fn collect(&mut self, _: &Context<'_>, out: &mut Vec<Sample>) -> Result<(), CollectError> {
        let Some(source) = &self.source else {
            return Ok(());
        };
        let text = match source.list() {
            Ok(text) => text,
            Err(error) => {
                // No D-Bus, an old unit filter: say it once, report nothing (the memory stays).
                if !std::mem::replace(&mut self.warned, true) {
                    tracing::debug!("cannot list failed systemd units: {error}");
                }
                return Ok(());
            }
        };
        self.warned = false;
        let now = self
            .clock
            .now()
            .duration_since(UNIX_EPOCH)
            .map_or(0, |d| d.as_secs());
        let failed = parse_failed(&text);
        let before = self.memory.clone();
        for unit in &failed {
            self.memory.0.insert(unit.clone(), now);
        }
        self.memory
            .0
            .retain(|_, at| now.saturating_sub(*at) < REMEMBER.as_secs());
        // Bounded: the newest failures stay.
        while self.memory.0.len() > MAX_UNITS {
            let oldest = self
                .memory
                .0
                .iter()
                .min_by_key(|(_, at)| **at)
                .map(|(unit, _)| unit.clone());
            if let Some(unit) = oldest {
                self.memory.0.remove(&unit);
            }
        }
        for unit in self.memory.0.keys() {
            let value = i64::from(failed.contains(unit));
            out.push(Sample::int("sermon_systemd_unit_failed", value).label("unit", unit.clone()));
        }
        if self.memory != before
            && let Some(file) = &self.file
            && let Err(error) = self.memory.store(file)
        {
            tracing::warn!("cannot store the failed systemd units: {error}");
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use std::sync::Mutex;
    use std::time::SystemTime;

    use sermon_core::{Monotonic, ProcRoot, Value};

    use super::*;
    use crate::testing::{at, value};

    #[derive(Debug)]
    struct FakeClock(Mutex<SystemTime>);

    impl FakeClock {
        fn at(seconds: u64) -> Arc<Self> {
            Arc::new(Self(Mutex::new(UNIX_EPOCH + Duration::from_secs(seconds))))
        }
        fn advance(&self, by: Duration) {
            *self.0.lock().unwrap() += by;
        }
    }

    impl Clock for FakeClock {
        fn now(&self) -> SystemTime {
            *self.0.lock().unwrap()
        }
        fn monotonic(&self) -> Monotonic {
            Monotonic::from_origin(Duration::ZERO)
        }
    }

    /// What systemctl answers, changed by the test.
    #[derive(Clone)]
    struct Fake(Arc<Mutex<io::Result<String>>>);

    impl Default for Fake {
        fn default() -> Self {
            Self(Arc::new(Mutex::new(Ok(String::new()))))
        }
    }

    impl Fake {
        fn answer(&self, text: &str) {
            *self.0.lock().unwrap() = Ok(text.to_owned());
        }
        fn fail(&self) {
            *self.0.lock().unwrap() = Err(io::ErrorKind::TimedOut.into());
        }
    }

    impl FailedUnits for Fake {
        fn list(&self) -> io::Result<String> {
            match &*self.0.lock().unwrap() {
                Ok(text) => Ok(text.clone()),
                Err(error) => Err(io::Error::new(error.kind(), "fake")),
            }
        }
    }

    fn collect(collector: &mut SystemdCollector) -> Vec<Sample> {
        let root = ProcRoot::new("/nonexistent");
        let context = Context {
            root: &root,
            now: at(0),
        };
        let mut out = Vec::new();
        collector.collect(&context, &mut out).unwrap();
        out
    }

    fn fixture(name: &str) -> String {
        std::fs::read_to_string(
            PathBuf::from(env!("CARGO_MANIFEST_DIR"))
                .join("../../fixtures/states/systemd")
                .join(name),
        )
        .unwrap()
    }

    #[test]
    fn reads_the_failed_units_of_every_distro() {
        for distro in ["debian-13", "ubuntu-24-04", "rockylinux-9", "fedora-44"] {
            let units = parse_failed(&fixture(&format!("failed-{distro}.txt")));
            assert!(
                units.contains(&"backup-nightly.service".to_owned()),
                "{distro}: {units:?}"
            );
            assert!(
                units.contains(&"app@web\\x2d01.service".to_owned()),
                "{distro}: {units:?}"
            );
        }
        // Older systemd prints a bullet; descriptions with spaces stay out.
        assert_eq!(
            parse_failed("● nginx.service loaded failed failed A high performance web server\n"),
            ["nginx.service"]
        );
    }

    #[test]
    fn skips_what_is_no_unit_name_and_bounds_the_list() {
        for line in [
            "0 loaded units listed.",
            "nginx loaded failed failed no suffix",
            "../etc/passwd.service loaded failed failed",
            "a$(id).service loaded failed failed",
            ".service loaded failed failed",
        ] {
            assert_eq!(parse_failed(line), Vec::<String>::new(), "{line}");
        }
        // Each unit once.
        assert_eq!(
            parse_failed("a.service x\n● a.service x\nb.timer y\n"),
            ["a.service", "b.timer"]
        );
        let long = format!("{}.service", "a".repeat(300));
        assert_eq!(parse_failed(&long), Vec::<String>::new());
        let many: String = (0..80)
            .map(|i| format!("u{i}.service x\n"))
            .collect::<Vec<_>>()
            .concat();
        assert_eq!(parse_failed(&many).len(), MAX_UNITS);
    }

    #[test]
    fn reports_recovered_units_as_zero_for_a_day_also_after_a_restart() {
        let dir = tempfile::tempdir().unwrap();
        let file = dir.path().join("systemd-failed");
        let clock = FakeClock::at(1_800_000_000);
        let fake = Fake::default();
        let make = |fake: &Fake| {
            SystemdCollector::new(
                Some(Box::new(fake.clone())),
                clock.clone(),
                Some(file.clone()),
            )
        };
        let failed = |out: &[Sample], unit: &str| {
            value(out, "sermon_systemd_unit_failed", &[("unit", unit)])
        };

        fake.answer("backup.service loaded failed failed Backup\n");
        let mut collector = make(&fake);
        let out = collect(&mut collector);
        assert_eq!(failed(&out, "backup.service"), Some(Value::Int(1)));

        // Recovered: 0, so the alert resolves.
        fake.answer("");
        clock.advance(Duration::from_secs(60));
        assert_eq!(
            failed(&collect(&mut collector), "backup.service"),
            Some(Value::Int(0))
        );

        // A restart of the agent remembers it.
        let mut restarted = make(&fake);
        clock.advance(Duration::from_secs(3600));
        assert_eq!(
            failed(&collect(&mut restarted), "backup.service"),
            Some(Value::Int(0))
        );

        // After 24 h without failing it is forgotten.
        clock.advance(REMEMBER);
        let out = collect(&mut restarted);
        assert_eq!(failed(&out, "backup.service"), None);
        assert_eq!(std::fs::read_to_string(&file).unwrap(), "");
    }

    #[test]
    fn a_failing_systemctl_reports_nothing_and_keeps_the_memory() {
        let clock = FakeClock::at(1_800_000_000);
        let fake = Fake::default();
        let mut collector =
            SystemdCollector::new(Some(Box::new(fake.clone())), clock.clone(), None);
        fake.answer("backup.service loaded failed failed Backup\n");
        collect(&mut collector);
        fake.fail();
        assert_eq!(collect(&mut collector), Vec::<Sample>::new());
        fake.answer("");
        assert_eq!(
            value(
                &collect(&mut collector),
                "sermon_systemd_unit_failed",
                &[("unit", "backup.service")]
            ),
            Some(Value::Int(0))
        );
        // Without systemd nothing at all.
        let mut none = SystemdCollector::new(None, clock, None);
        assert_eq!(collect(&mut none), Vec::<Sample>::new());
    }

    #[test]
    fn the_memory_is_bounded_and_keeps_the_newest_failures() {
        let clock = FakeClock::at(1_800_000_000);
        let fake = Fake::default();
        let mut collector =
            SystemdCollector::new(Some(Box::new(fake.clone())), clock.clone(), None);
        for round in 0..3 {
            let units: String = (0..30)
                .map(|i| format!("r{round}u{i}.service x\n"))
                .collect::<Vec<_>>()
                .concat();
            fake.answer(&units);
            clock.advance(Duration::from_secs(60));
            let out = collect(&mut collector);
            assert!(out.len() <= MAX_UNITS, "{}", out.len());
        }
        let out = collect(&mut collector);
        assert!(
            value(
                &out,
                "sermon_systemd_unit_failed",
                &[("unit", "r2u0.service")]
            )
            .is_some()
        );
        assert!(
            value(
                &out,
                "sermon_systemd_unit_failed",
                &[("unit", "r0u0.service")]
            )
            .is_none()
        );
    }

    fn script(dir: &Path, body: &str) -> Systemctl {
        use std::os::unix::fs::PermissionsExt;
        let program = dir.join("systemctl");
        std::fs::write(&program, format!("#!/bin/sh\n{body}\n")).unwrap();
        std::fs::set_permissions(&program, std::fs::Permissions::from_mode(0o755)).unwrap();
        Systemctl {
            program,
            timeout: Duration::from_millis(500),
        }
    }

    #[test]
    fn runs_systemctl_with_fixed_arguments_and_an_empty_environment() {
        let dir = tempfile::tempdir().unwrap();
        let echo = script(dir.path(), r#"echo "$*"; env | sort"#);
        let text = echo.list().unwrap();
        let mut lines = text.lines();
        assert_eq!(
            lines.next(),
            Some("list-units --state=failed --plain --no-legend --no-pager --full")
        );
        let env: Vec<&str> = lines
            .filter(|l| !l.starts_with("PWD=") && !l.starts_with("SHLVL=") && !l.starts_with('_'))
            .collect();
        assert_eq!(env, ["LC_ALL=C", "SYSTEMD_COLORS=0"]);

        let failing = script(dir.path(), "echo partial; exit 1");
        assert!(failing.list().is_err());
    }

    #[test]
    fn a_hanging_systemctl_is_killed() {
        let dir = tempfile::tempdir().unwrap();
        let hanging = script(dir.path(), "exec sleep 30");
        let started = std::time::Instant::now();
        let error = hanging.list().unwrap_err();
        assert_eq!(error.kind(), io::ErrorKind::TimedOut);
        assert!(started.elapsed() < Duration::from_secs(5));
    }

    #[test]
    fn only_with_systemd_and_systemctl() {
        let dir = tempfile::tempdir().unwrap();
        let clock: Arc<dyn Clock> = FakeClock::at(0);
        let host = SystemdCollector::for_host(dir.path(), clock.clone(), dir.path().join("m"));
        assert!(host.source.is_none(), "no /run/systemd/system");
        std::fs::create_dir_all(dir.path().join("run/systemd/system")).unwrap();
        std::fs::create_dir_all(dir.path().join("usr/bin")).unwrap();
        std::fs::write(dir.path().join("usr/bin/systemctl"), "").unwrap();
        let host = SystemdCollector::for_host(dir.path(), clock, dir.path().join("m"));
        assert!(host.source.is_some());
    }
}
