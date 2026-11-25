//! The work of the operations on this host (ADR 0035, KONZEPT §14): which package manager, the
//! fixed commands, the lock and the runs. A run is a process of its own (a transient unit under
//! systemd, a process group of its own under OpenRC): it outlives the agent and the helper, whose
//! packages it may renew. Its request, start and result lie root-owned in the state directory, the
//! result for [`KEEP`].

use std::fs::{self, File, OpenOptions};
use std::io::{self, Read, Write};
use std::os::unix::fs::{DirBuilderExt, OpenOptionsExt};
use std::path::{Path, PathBuf};
use std::process::Stdio;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use prost::Message;
use sermon_helper_proto::v1::{
    Command as Output, Distribution, RunPhase, RunState, Scope, StatusInfo, Updates,
};

use crate::command;
use crate::minisign::{self, PublicKey};
use crate::request::{Executor, Failure, Run};

/// How long the result of a run is kept.
pub const KEEP: Duration = Duration::from_hours(7 * 24);
/// The sources are refreshed at most this often (T89).
pub const REFRESH_EVERY: Duration = Duration::from_mins(5);
const LIST_OUTPUT: usize = 2 << 20;
const RUN_OUTPUT: usize = 256 << 10;
/// A run that was launched but never started within this time is lost.
const LAUNCH_TIME: Duration = Duration::from_mins(5);
/// Runs kept at most, the newest (review M4-04: no disk filled with runs).
const MAX_RUNS: usize = 500;
/// Every apt-get that changes packages keeps changed configuration files.
const APT_GET: [&str; 6] = [
    "-y",
    "-q",
    "-o",
    "Dpkg::Options::=--force-confdef",
    "-o",
    "Dpkg::Options::=--force-confold",
];
const UNSUPPORTED: &str = "no apt, dnf or apk distribution in /etc/os-release";
/// The agent of a script installation, the only one the self-update replaces (ADR 0036).
const AGENT: &str = "usr/local/bin/sermon-agent";
/// Where the agent puts the new binary and its signature, below the host's root.
const STAGED: [&str; 4] = ["var", "lib", "sermon-agent", "self-update"];
const MAX_BINARY: u64 = 64 << 20;
const MAX_SIGNATURE: u64 = 4 << 10;

/// A fixed command: the program below the host's root and its arguments.
type Fixed = (&'static str, &'static [&'static str]);

/// The package manager's program and its commands to refresh the sources and to list.
fn commands(d: Distribution) -> Option<(&'static str, Fixed, &'static [Fixed])> {
    Some(match d {
        Distribution::Apt => (
            "usr/bin/apt-get",
            ("usr/bin/apt-get", &["update", "-q"]),
            &[("usr/bin/apt", &["list", "--upgradable"])],
        ),
        Distribution::Dnf => (
            "usr/bin/dnf",
            ("usr/bin/dnf", &["-q", "makecache"]),
            &[
                ("usr/bin/dnf", &["-q", "check-update"]),
                ("usr/bin/dnf", &["-q", "updateinfo", "list", "--security"]),
                ("usr/bin/dnf", &["needs-restarting", "-r"]),
            ],
        ),
        Distribution::Apk => (
            "sbin/apk",
            ("sbin/apk", &["update", "-q"]),
            &[("sbin/apk", &["version", "-l", "<"])],
        ),
        Distribution::Unspecified => return None,
    })
}

/// How a run is started.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Launch {
    /// `systemd-run`: a transient unit of its own.
    Systemd,
    /// A process group of its own (OpenRC).
    Detached,
    /// In a thread of this process (tests only).
    #[cfg(test)]
    Inline,
}

/// The host: `root` holds `/etc/os-release` and the programs, `state` the runs. Only tests choose
/// other paths than `/` and `/var/lib/sermon-helper` – no environment, no configuration.
#[derive(Debug, Clone)]
pub struct Host {
    root: PathBuf,
    state: PathBuf,
    launch: Launch,
    list_limit: Duration,
    run_limit: Duration,
    /// The release keys a new agent must be signed with.
    keys: Vec<PublicKey>,
    /// How long a self-update waits for the new agent to connect before it rolls back.
    confirm_within: Duration,
}

impl Host {
    /// This host.
    #[must_use]
    pub fn system() -> Self {
        let systemd = Path::new("/run/systemd/system").is_dir();
        let launch = if systemd {
            Launch::Systemd
        } else {
            Launch::Detached
        };
        let (list_limit, run_limit) = (Duration::from_mins(10), Duration::from_hours(1));
        Self {
            root: "/".into(),
            state: "/var/lib/sermon-helper".into(),
            launch,
            list_limit,
            run_limit,
            keys: minisign::release_keys(),
            confirm_within: Duration::from_mins(2),
        }
    }

    fn distribution(&self) -> Distribution {
        fs::read_to_string(self.root.join("etc/os-release"))
            .map_or(Distribution::Unspecified, |t| distribution(&t))
    }

    fn run(
        &self,
        (program, args): (&str, &[&str]),
        limit: Duration,
        keep: usize,
        stderr: bool,
    ) -> Output {
        command::run(&self.root.join(program), args, limit, keep, stderr)
    }

    fn run_dir(&self, id: &str) -> PathBuf {
        self.state.join("runs").join(id)
    }

    /// The lock of runs and refreshes, taken without waiting; `None` if held.
    fn lock(&self) -> io::Result<Option<File>> {
        fs::DirBuilder::new()
            .recursive(true)
            .mode(0o700)
            .create(&self.state)?;
        let mut open = OpenOptions::new();
        let file = open
            .read(true)
            .write(true)
            .create(true)
            .truncate(false)
            .mode(0o600)
            .open(self.state.join("lock"))?;
        match rustix::fs::flock(&file, rustix::fs::FlockOperation::NonBlockingLockExclusive) {
            Ok(()) => Ok(Some(file)),
            Err(rustix::io::Errno::WOULDBLOCK) => Ok(None),
            Err(e) => Err(e.into()),
        }
    }

    /// The lock for a run or a list: a `GetStatus` may hold it for a moment.
    fn lock_waiting(&self) -> io::Result<Option<File>> {
        for _ in 0..25 {
            if let Some(lock) = self.lock()? {
                return Ok(Some(lock));
            }
            std::thread::sleep(Duration::from_millis(200));
        }
        Ok(None)
    }

    /// The run holding the lock, if any.
    fn running(&self) -> Option<String> {
        if self.lock().ok()?.is_some() {
            return None;
        }
        let id = fs::read_to_string(self.state.join("lock")).ok()?;
        Some(id.trim().to_owned()).filter(|id| !id.is_empty())
    }

    fn lists(&self, lists: &[Fixed]) -> Vec<Output> {
        lists
            .iter()
            .map(|&c| self.run(c, self.list_limit, LIST_OUTPUT, false))
            .collect()
    }

    /// Starts `sermon-helper run <id>` apart from this process.
    fn spawn(&self, id: &str) -> Result<(), Failure> {
        let exe = std::env::current_exe().map_err(fail)?;
        let exe = exe.to_string_lossy();
        match self.launch {
            Launch::Systemd => {
                let unit = format!("--unit=sermon-helper-run-{id}");
                let args = [
                    unit.as_str(),
                    "--collect",
                    "--quiet",
                    "--no-block",
                    &exe,
                    "run",
                    id,
                ];
                let o = self.run(
                    ("usr/bin/systemd-run", &args),
                    Duration::from_secs(30),
                    1024,
                    true,
                );
                if o.exit_code != 0 {
                    return Err(Failure::Failed(format!(
                        "systemd-run: {}",
                        String::from_utf8_lossy(&o.output).trim()
                    )));
                }
            }
            Launch::Detached => {
                let mut c = std::process::Command::new(exe.as_ref());
                c.args(["run", id])
                    .env_clear()
                    .current_dir("/")
                    .stdin(Stdio::null())
                    .stdout(Stdio::null())
                    .stderr(Stdio::inherit());
                let mut child = std::os::unix::process::CommandExt::process_group(&mut c, 0)
                    .spawn()
                    .map_err(fail)?;
                std::thread::spawn(move || child.wait());
            }
            #[cfg(test)]
            Launch::Inline => {
                let (host, id) = (self.clone(), id.to_owned());
                std::thread::spawn(move || host.execute(&id));
            }
        }
        Ok(())
    }

    /// Runs the run `id` (`sermon-helper run <id>`) and stores its result.
    ///
    /// # Errors
    ///
    /// If the run's directory can't be read or written.
    pub fn execute(&self, id: &str) -> io::Result<()> {
        let dir = self.run_dir(id);
        let request = fs::read_to_string(dir.join("request"))?;
        let started = now();
        eprintln!(
            "sermon-helper: run {id} started: {}",
            request.lines().next().unwrap_or_default()
        );
        let Some(mut lock) = self.lock_waiting()? else {
            return store(
                &dir,
                &done(id, started, failed("another run holds the lock")),
            );
        };
        lock.set_len(0)?;
        lock.write_all(id.as_bytes())?;
        write_atomically(&dir.join("started"), started.to_string().as_bytes())?;
        let d = self.distribution();
        let result = match request.split_once('\n').unwrap_or((&request, "")) {
            ("updates security", _) => self.updates(d, true),
            ("updates all", _) => self.updates(d, false),
            (first, _) if first.starts_with("self-update ") => self.self_update(&dir),
            ("reboot", _) if self.launch == Launch::Detached => {
                // busybox reboots after the delay and never returns: the result comes first.
                store(
                    &dir,
                    &done(
                        id,
                        started,
                        Output {
                            name: "reboot -d 60".into(),
                            ..Output::default()
                        },
                    ),
                )?;
                let _ = self.run(
                    ("sbin/reboot", &["-d", "60"]),
                    Duration::from_mins(2),
                    1024,
                    true,
                );
                return Ok(());
            }
            // "--" ends the options and the prefix keeps a reason from being one (review M4-04).
            ("reboot", reason) => self.run(
                (
                    "usr/sbin/shutdown",
                    &["-r", "--", "+1", &format!("SerMon: {reason}")],
                ),
                Duration::from_mins(1),
                4096,
                true,
            ),
            _ => failed("unknown request"),
        };
        eprintln!(
            "sermon-helper: run {id} ended with exit code {}",
            result.exit_code
        );
        store(&dir, &done(id, started, result))
    }

    /// Refreshes the sources and installs the updates; for security only on apt the packages of a
    /// security suite.
    fn updates(&self, d: Distribution, security: bool) -> Output {
        let Some((program, refresh, lists)) = commands(d) else {
            return failed(UNSUPPORTED);
        };
        let refresh = self.run(refresh, self.run_limit, RUN_OUTPUT, true);
        if refresh.exit_code != 0 {
            return refresh;
        }
        let names = match (d, security) {
            (Distribution::Apt, true) => security_packages(&String::from_utf8_lossy(
                &self.lists(lists).remove(0).output,
            )),
            _ => Vec::new(),
        };
        let mut args: Vec<&str> = match (d, security) {
            (Distribution::Apt, false) => [&APT_GET[..], &["--with-new-pkgs", "upgrade"]].concat(),
            (Distribution::Apt, true) if names.is_empty() => {
                return Output {
                    output: b"no security updates\n".into(),
                    ..refresh
                };
            }
            (Distribution::Apt, true) => {
                [&APT_GET[..], &["--no-remove", "install", "--only-upgrade"]].concat()
            }
            (Distribution::Dnf, false) => vec!["-y", "upgrade"],
            (Distribution::Dnf, true) => vec!["-y", "upgrade", "--security"],
            (Distribution::Apk, false) => vec!["upgrade", "-q"],
            _ => return failed("unsupported scope for this distribution"),
        };
        args.extend(names.iter().map(String::as_str));
        let mut out = self.run((program, &args), self.run_limit, RUN_OUTPUT, true);
        let mut joined = [refresh.output, out.output].concat();
        out.truncated |= refresh.truncated || joined.len() > RUN_OUTPUT;
        out.output = joined.split_off(joined.len().saturating_sub(RUN_OUTPUT));
        out
    }

    /// Reads a file the agent staged for its self-update without following a link anywhere below
    /// the host's root, at most `max` bytes of a regular file.
    fn staged(&self, name: &str, max: u64) -> Result<Vec<u8>, String> {
        use rustix::fs::{FileType, Mode, OFlags, fstat, open, openat};
        let flags = OFlags::RDONLY | OFlags::CLOEXEC | OFlags::NOFOLLOW;
        let why = |e: rustix::io::Errno| format!("cannot read the staged {name}: {e}");
        let mut dir = open(
            &self.root,
            OFlags::RDONLY | OFlags::DIRECTORY | OFlags::CLOEXEC,
            Mode::empty(),
        )
        .map_err(why)?;
        for part in STAGED {
            dir = openat(&dir, part, flags | OFlags::DIRECTORY, Mode::empty()).map_err(why)?;
        }
        let fd = openat(&dir, name, flags | OFlags::NONBLOCK, Mode::empty()).map_err(why)?;
        let st = fstat(&fd).map_err(why)?;
        if FileType::from_raw_mode(st.st_mode) != FileType::RegularFile {
            return Err(format!("the staged {name} is no regular file"));
        }
        let mut bytes = Vec::new();
        File::from(fd)
            .take(max + 1)
            .read_to_end(&mut bytes)
            .map_err(|e| format!("cannot read the staged {name}: {e}"))?;
        if u64::try_from(bytes.len()).unwrap_or(u64::MAX) > max {
            return Err(format!("the staged {name} is larger than {max} bytes"));
        }
        Ok(bytes)
    }

    /// Checks the staged agent of a self-update and keeps a copy of exactly the checked bytes in
    /// the run's directory: what the agent changes afterwards has no effect (ADR 0036).
    fn prepare(&self, dir: &Path, version: &str) -> Result<(), Failure> {
        if self.keys.is_empty() {
            return Err(Failure::Unsupported(
                "this helper was built without a release key".into(),
            ));
        }
        let agent = self.root.join(AGENT);
        if !fs::symlink_metadata(&agent).is_ok_and(|m| m.is_file()) {
            return Err(Failure::Unsupported(
                "no /usr/local/bin/sermon-agent: a package installation is updated by the package manager".into(),
            ));
        }
        let file = format!("sermon-agent-linux-{}", arch());
        let installed = fs::read(&agent)
            .ok()
            .zip(fs::read_to_string(self.state.join("agent.minisig")).ok())
            .and_then(|(bytes, sig)| minisign::verify(&self.keys, &sig, &bytes).ok())
            .filter(|s| s.file == file)
            .ok_or_else(|| {
                Failure::Refused("the installed version is unknown: run install.sh again".into())
            })?;
        let binary = self
            .staged("sermon-agent", MAX_BINARY)
            .map_err(Failure::Refused)?;
        let signature = self
            .staged("sermon-agent.minisig", MAX_SIGNATURE)
            .map_err(Failure::Refused)?;
        let signature = String::from_utf8(signature)
            .map_err(|_| Failure::Refused("malformed signature file".into()))?;
        let signed = minisign::verify(&self.keys, &signature, &binary).map_err(Failure::Refused)?;
        if signed.file != file || signed.version != version {
            return Err(Failure::Refused(format!(
                "the signature is for {} {}, not {file} {version}",
                signed.file, signed.version
            )));
        }
        if !minisign::newer(version, &installed.version) {
            return Err(Failure::Refused(format!(
                "{version} is not newer than the installed {}",
                installed.version
            )));
        }
        write_atomically(&dir.join("agent"), &binary).map_err(fail)?;
        write_atomically(&dir.join("agent.minisig"), signature.as_bytes()).map_err(fail)
    }

    /// Puts `binary` in place of the agent (`rename` in its directory) and `signature` as the
    /// record of the installed version.
    fn install(&self, binary: &Path, signature: &Path) -> io::Result<()> {
        let agent = self.root.join(AGENT);
        let next = agent.with_file_name(".sermon-agent.new");
        let mut f = OpenOptions::new()
            .write(true)
            .create(true)
            .truncate(true)
            .mode(0o755)
            .open(&next)?;
        io::copy(&mut File::open(binary)?, &mut f)?;
        f.sync_all()?;
        fs::rename(&next, &agent)?;
        File::open(agent.parent().unwrap_or(&self.root))?.sync_all()?;
        write_atomically(&self.state.join("agent.minisig"), &fs::read(signature)?)
    }

    /// Restarts the agent's service.
    fn restart_agent(&self) -> Output {
        let command: Fixed = match self.launch {
            Launch::Detached => ("sbin/rc-service", &["sermon-agent", "restart"]),
            #[cfg(test)]
            Launch::Inline => ("usr/bin/systemctl", &["restart", "sermon-agent"]),
            Launch::Systemd => ("usr/bin/systemctl", &["restart", "sermon-agent"]),
        };
        self.run(command, Duration::from_mins(1), 4096, true)
    }

    /// Replaces the agent with the checked copy, restarts it and waits for the new one to
    /// connect; without its confirmation the previous binary comes back (ADR 0036).
    fn self_update(&self, dir: &Path) -> Output {
        let keep = self.state.join("self-update");
        let saved = fs::DirBuilder::new()
            .recursive(true)
            .mode(0o700)
            .create(&keep)
            .and_then(|()| fs::copy(self.root.join(AGENT), keep.join("previous")))
            .and_then(|_| {
                fs::copy(
                    self.state.join("agent.minisig"),
                    keep.join("previous.minisig"),
                )
            });
        if let Err(e) =
            saved.and_then(|_| self.install(&dir.join("agent"), &dir.join("agent.minisig")))
        {
            return failed(&format!("cannot replace the agent: {e}"));
        }
        let _ = write_atomically(&dir.join("replaced"), now().to_string().as_bytes());
        let restart = self.restart_agent();
        let deadline = std::time::Instant::now() + self.confirm_within;
        while std::time::Instant::now() < deadline {
            if dir.join("confirmed").exists() {
                return Output {
                    name: "self-update".into(),
                    output: b"the new agent is installed and connected\n".to_vec(),
                    ..Output::default()
                };
            }
            std::thread::sleep(self.confirm_within / 60);
        }
        let back = self.install(&keep.join("previous"), &keep.join("previous.minisig"));
        let again = self.restart_agent();
        let why = match back {
            Ok(()) => "rolled back: the new agent did not connect in time".to_owned(),
            Err(e) => {
                format!("the new agent did not connect in time, and the rollback failed: {e}")
            }
        };
        let log = [restart.output, again.output].concat();
        Output {
            name: "self-update".into(),
            exit_code: 1,
            output: [why.as_bytes(), b"\n", &log].concat(),
            ..Output::default()
        }
    }

    /// The runs with the time of their start and their request, the newest first.
    fn runs(&self) -> Vec<(SystemTime, PathBuf, String)> {
        let mut runs: Vec<_> = fs::read_dir(self.state.join("runs"))
            .into_iter()
            .flatten()
            .flatten()
            .filter_map(|e| {
                let modified = e.metadata().and_then(|m| m.modified()).ok()?;
                let request = fs::read_to_string(e.path().join("request")).unwrap_or_default();
                Some((modified, e.path(), request))
            })
            .collect();
        runs.sort_by_key(|r| std::cmp::Reverse(r.0));
        runs
    }

    /// Deletes the runs older than [`KEEP`] and all but the newest [`MAX_RUNS`].
    pub fn prune(&self) {
        for (i, (modified, path, _)) in self.runs().into_iter().enumerate() {
            if i >= MAX_RUNS || older(modified, KEEP) {
                let _ = fs::remove_dir_all(path);
            }
        }
    }
}

impl Executor for Host {
    fn status(&self) -> StatusInfo {
        let running_run_id = self.running().unwrap_or_default();
        StatusInfo {
            distribution: self.distribution().into(),
            running_run_id,
            ..StatusInfo::default()
        }
    }

    fn list_updates(&mut self, refresh: bool) -> Result<Updates, Failure> {
        let d = self.distribution();
        let (_, refresh_command, lists) =
            commands(d).ok_or_else(|| Failure::Unsupported(UNSUPPORTED.into()))?;
        let Some(_lock) = self.lock_waiting().map_err(fail)? else {
            return Err(Failure::Busy);
        };
        let stamp = self.state.join("refreshed");
        let refreshed = refresh
            && fs::metadata(&stamp)
                .and_then(|m| m.modified())
                .map_or(true, |t| older(t, REFRESH_EVERY));
        let mut commands = Vec::new();
        if refreshed {
            commands.push(self.run(refresh_command, self.list_limit, RUN_OUTPUT, true));
            let _ = fs::write(&stamp, b"");
        }
        commands.extend(self.lists(lists));
        let reboot = self.root.join("var/run/reboot-required");
        let mut pkgs = Vec::new();
        if let Ok(f) = File::open(reboot.with_extension("pkgs")) {
            let _ = f.take(64 << 10).read_to_end(&mut pkgs);
        }
        Ok(Updates {
            distribution: d.into(),
            refreshed,
            commands,
            reboot_required_pkgs: pkgs,
            reboot_required: reboot.exists(),
        })
    }

    fn start(&mut self, run: &Run) -> Result<(), Failure> {
        self.prune();
        let d = self.distribution();
        let (id, request) = match run {
            Run::Updates { .. } if d == Distribution::Unspecified => {
                return Err(Failure::Unsupported(UNSUPPORTED.into()));
            }
            Run::Updates {
                scope: Scope::Security,
                ..
            } if d == Distribution::Apk => {
                return Err(Failure::Unsupported(
                    "Alpine has no security metadata: use the scope all".into(),
                ));
            }
            Run::Updates {
                id,
                scope: Scope::Security,
            } => (id, "updates security".to_owned()),
            Run::Updates { id, .. } => (id, "updates all".to_owned()),
            Run::Reboot { id, reason } => (id, format!("reboot\n{reason}")),
            Run::SelfUpdate { id, version } => (id, format!("self-update {version}")),
        };
        let dir = self.run_dir(id);
        if let Ok(before) = fs::read_to_string(dir.join("request")) {
            // Started before: a run happens once, and its ID names that run only.
            return if before == request {
                Ok(())
            } else {
                Err(fail(format!("run_id {id} names another run")))
            };
        }
        let kind = |r: &str| r.split([' ', '\n']).next().unwrap_or_default().to_owned();
        if self
            .runs()
            .iter()
            .any(|(t, _, r)| kind(r) == kind(&request) && !older(*t, REFRESH_EVERY))
        {
            return Err(Failure::Later(format!(
                "a {} run started less than 5 minutes ago",
                kind(&request)
            )));
        }
        if self.lock().map_err(fail)?.is_none() {
            return Err(Failure::Busy);
        }
        fs::DirBuilder::new()
            .recursive(true)
            .mode(0o700)
            .create(&dir)
            .map_err(fail)?;
        if let Run::SelfUpdate { version, .. } = run
            && let Err(e) = self.prepare(&dir, version)
        {
            let _ = fs::remove_dir_all(&dir);
            return Err(e);
        }
        write_atomically(&dir.join("request"), request.as_bytes()).map_err(fail)?;
        self.spawn(id)
    }

    fn confirm(&mut self, id: &str) -> Result<(), Failure> {
        let dir = self.run_dir(id);
        let request = fs::read_to_string(dir.join("request")).unwrap_or_default();
        if !request.starts_with("self-update ")
            || !dir.join("replaced").exists()
            || dir.join("result").exists()
        {
            return Err(Failure::Refused(
                "no self-update of this run waits for a confirmation".into(),
            ));
        }
        write_atomically(&dir.join("confirmed"), b"").map_err(fail)
    }

    fn result(&self, id: &str) -> RunState {
        let dir = self.run_dir(id);
        if let Ok(bytes) = fs::read(dir.join("result")) {
            return RunState::decode(bytes.as_slice()).unwrap_or_default();
        }
        let Ok(meta) = fs::metadata(dir.join("request")) else {
            return RunState {
                run_id: id.into(),
                ..RunState::default()
            };
        };
        let started = fs::read_to_string(dir.join("started"))
            .ok()
            .and_then(|s| s.trim().parse().ok());
        let launching = started.is_none() && meta.modified().is_ok_and(|t| !older(t, LAUNCH_TIME));
        if launching || self.running().as_deref() == Some(id) {
            let phase = RunPhase::Running.into();
            return RunState {
                run_id: id.into(),
                phase,
                started_unix: started.unwrap_or(0),
                ..RunState::default()
            };
        }
        done(
            id,
            started.unwrap_or(0),
            failed("the run ended without a result (killed, or the host restarted)"),
        )
    }
}

/// The package manager family of `/etc/os-release`.
#[must_use]
pub fn distribution(os_release: &str) -> Distribution {
    let words: Vec<&str> = os_release
        .lines()
        .filter_map(|l| l.strip_prefix("ID=").or_else(|| l.strip_prefix("ID_LIKE=")))
        .flat_map(|v| v.trim_matches(['"', '\'']).split_whitespace())
        .collect();
    let any = |names: &[&str]| words.iter().any(|w| names.contains(w));
    if any(&["debian", "ubuntu"]) {
        Distribution::Apt
    } else if any(&["rhel", "fedora", "centos", "rocky", "almalinux"]) {
        Distribution::Dnf
    } else if any(&["alpine"]) {
        Distribution::Apk
    } else {
        Distribution::Unspecified
    }
}

/// The packages `apt list --upgradable` names from a security suite
/// (`openssl/bookworm-security 3.0.16-1 amd64 [upgradable from: …]`), only with valid Debian
/// names: they become arguments of apt-get.
#[must_use]
pub fn security_packages(list: &str) -> Vec<String> {
    let valid = |n: &str| {
        n.len() >= 2
            && n.as_bytes()[0].is_ascii_alphanumeric()
            && n.bytes()
                .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b"+-.".contains(&b))
    };
    let security = |rest: &str| {
        rest.split(' ')
            .next()
            .is_some_and(|s| s.split(',').any(|s| s.ends_with("-security")))
    };
    list.lines()
        .filter_map(|l| l.split_once('/'))
        .filter(|(name, rest)| valid(name) && security(rest))
        .map(|(name, _)| name.to_owned())
        .collect()
}

/// The architecture as in the names of the release files.
fn arch() -> &'static str {
    match std::env::consts::ARCH {
        "x86_64" => "amd64",
        "aarch64" => "arm64",
        other => other,
    }
}

fn fail(e: impl std::fmt::Display) -> Failure {
    Failure::Failed(e.to_string())
}

fn older(t: SystemTime, age: Duration) -> bool {
    t.elapsed().map_or(true, |a| a >= age)
}

fn now() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |d| i64::try_from(d.as_secs()).unwrap_or(i64::MAX))
}

fn failed(why: &str) -> Output {
    Output {
        exit_code: -1,
        output: why.as_bytes().to_vec(),
        ..Output::default()
    }
}

fn done(id: &str, started: i64, result: Output) -> RunState {
    let phase = RunPhase::Done.into();
    RunState {
        run_id: id.into(),
        phase,
        result: Some(result),
        started_unix: started,
        ended_unix: now(),
    }
}

fn store(dir: &Path, state: &RunState) -> io::Result<()> {
    write_atomically(&dir.join("result"), &state.encode_to_vec())
}

fn write_atomically(path: &Path, bytes: &[u8]) -> io::Result<()> {
    let tmp = path.with_extension("tmp");
    let mut f = OpenOptions::new()
        .write(true)
        .create(true)
        .truncate(true)
        .mode(0o600)
        .open(&tmp)?;
    f.write_all(bytes)?;
    f.sync_all()?;
    fs::rename(tmp, path)
}

#[cfg(test)]
mod tests {
    use std::os::unix::fs::PermissionsExt;
    use std::thread::sleep;

    use super::*;

    const ID: &str = "0193f5a0-0000-7000-8000-000000000001";
    const OTHER_ID: &str = "0193f5a0-0000-7000-8000-000000000002";
    const APT_LIST: &str = "Listing...\n\
        openssl/bookworm-security 3.0.16-1~deb12u1 arm64 [upgradable from: 3.0.15-1~deb12u1]\n\
        libc6/bookworm-updates,bookworm-security 2.36-9+deb12u10 arm64 [upgradable from: 2.36-9+deb12u9]\n\
        tzdata/bookworm-updates 2025b-0+deb12u1 all [upgradable from: 2025a-0+deb12u1]\n";

    /// A host in a temporary root with fake programs that log their arguments to `calls`.
    struct Fake {
        /// Keeps the temporary root alive.
        _dir: tempfile::TempDir,
        host: Host,
    }

    impl Fake {
        fn new(os_release: &str) -> Self {
            let dir = tempfile::tempdir().unwrap();
            let root = dir.path().join("root");
            fs::create_dir_all(root.join("etc")).unwrap();
            fs::write(root.join("etc/os-release"), os_release).unwrap();
            let host = Host {
                root,
                state: dir.path().join("state"),
                launch: Launch::Inline,
                list_limit: Duration::from_secs(5),
                run_limit: Duration::from_secs(5),
                keys: Vec::new(),
                confirm_within: Duration::from_secs(1),
            };
            let fake = Self { _dir: dir, host };
            for p in [
                "usr/bin/apt-get",
                "usr/bin/dnf",
                "sbin/apk",
                "usr/sbin/shutdown",
                "sbin/reboot",
                "usr/bin/systemctl",
            ] {
                fake.program(p, "");
            }
            fake.program(
                "usr/bin/apt",
                &format!("printf '{}'", APT_LIST.replace('\n', "\\n")),
            );
            fake
        }

        /// A fake program: logs its name and arguments, then runs `body`; fails if the file
        /// `fail-<name>` exists.
        fn program(&self, path: &str, body: &str) {
            let file = self.host.root.join(path);
            fs::create_dir_all(file.parent().unwrap()).unwrap();
            let name = Path::new(path)
                .file_name()
                .unwrap()
                .to_string_lossy()
                .into_owned();
            let root = self.host.root.display();
            fs::write(&file, format!("#!/bin/sh\necho \"{name} $*\" >>{root}/calls\nif [ -e {root}/fail-{name} ]; then exit 100; fi\n{body}\n")).unwrap();
            fs::set_permissions(&file, fs::Permissions::from_mode(0o755)).unwrap();
        }

        fn calls(&self) -> String {
            fs::read_to_string(self.host.root.join("calls")).unwrap_or_default()
        }

        fn wait(&self, id: &str) -> RunState {
            for _ in 0..250 {
                let state = self.host.result(id);
                if state.phase() == RunPhase::Done {
                    return state;
                }
                sleep(Duration::from_millis(20));
            }
            panic!("the run did not end: {:?}", self.host.result(id));
        }
    }

    fn updates(scope: Scope) -> Run {
        Run::Updates {
            id: ID.into(),
            scope,
        }
    }

    #[test]
    fn knows_the_package_manager_of_every_target_distribution() {
        for (os_release, want) in [
            ("ID=debian\nVERSION_ID=\"12\"\n", Distribution::Apt),
            ("ID=ubuntu\nID_LIKE=debian\n", Distribution::Apt),
            (
                "ID=\"rocky\"\nID_LIKE=\"rhel centos fedora\"\n",
                Distribution::Dnf,
            ),
            (
                "ID=\"almalinux\"\nID_LIKE=\"rhel centos fedora\"\n",
                Distribution::Dnf,
            ),
            ("ID=fedora\n", Distribution::Dnf),
            ("ID=alpine\n", Distribution::Apk),
            ("ID=arch\n", Distribution::Unspecified),
            ("NAME=\"ID=debian\"\n", Distribution::Unspecified),
            ("", Distribution::Unspecified),
        ] {
            assert_eq!(distribution(os_release), want, "{os_release:?}");
        }
    }

    #[test]
    fn picks_the_packages_of_security_suites_with_valid_names() {
        let mut list = APT_LIST.to_owned();
        list.push_str("evil;reboot/bookworm-security 1 all [upgradable from: 0]\nx/bookworm-security 1 all\nUpper/stable-security 1 all\n");
        assert_eq!(security_packages(&list), ["openssl", "libc6"]);
        assert_eq!(
            security_packages("WARNING: apt does not have a stable CLI interface.\n"),
            Vec::<String>::new()
        );
    }

    #[test]
    fn lists_with_a_refresh_at_most_every_five_minutes() {
        let fake = Fake::new("ID=debian\n");
        let mut host = fake.host.clone();
        fs::create_dir_all(host.root.join("var/run")).unwrap();
        fs::write(host.root.join("var/run/reboot-required"), "").unwrap();
        fs::write(
            host.root.join("var/run/reboot-required.pkgs"),
            "linux-image-6.1.0-28-arm64\n",
        )
        .unwrap();
        let first = host.list_updates(true).unwrap();
        assert!(
            first.refreshed
                && first.reboot_required
                && first.reboot_required_pkgs == b"linux-image-6.1.0-28-arm64\n"
        );
        assert_eq!(first.distribution(), Distribution::Apt);
        let names: Vec<_> = first
            .commands
            .iter()
            .map(|c| c.name.rsplit('/').next().unwrap().to_owned())
            .collect();
        assert_eq!(names, ["apt-get update -q", "apt list --upgradable"]);
        assert_eq!(first.commands[1].output, APT_LIST.as_bytes());
        let second = host.list_updates(true).unwrap();
        assert!(
            !second.refreshed && second.commands.len() == 1,
            "{second:?}"
        );
        assert_eq!(fake.calls().matches("apt-get update").count(), 1);
    }

    #[test]
    fn keeps_the_exit_codes_of_dnf() {
        let fake = Fake::new("ID=fedora\n");
        fake.program("usr/bin/dnf", "[ \"$2\" = check-update ] && { echo 'openssl.x86_64 1:3.2.4-1.fc44 updates'; exit 100; }; exit 0");
        let updates = fake.host.clone().list_updates(false).unwrap();
        let codes: Vec<_> = updates
            .commands
            .iter()
            .map(|c| (c.name.rsplit('/').next().unwrap().to_owned(), c.exit_code))
            .collect();
        assert_eq!(
            codes,
            [
                ("dnf -q check-update".into(), 100),
                ("dnf -q updateinfo list --security".into(), 0),
                ("dnf needs-restarting -r".into(), 0)
            ]
        );
        assert!(!updates.refreshed && !updates.reboot_required);
    }

    #[test]
    fn installs_all_updates_or_the_security_ones_once() {
        let fake = Fake::new("ID=debian\n");
        let mut host = fake.host.clone();
        host.start(&updates(Scope::All)).unwrap();
        let state = fake.wait(ID);
        assert_eq!(state.result.as_ref().unwrap().exit_code, 0);
        assert!(state.started_unix > 0 && state.ended_unix >= state.started_unix);
        // Started again: nothing happens, the result stays.
        host.start(&updates(Scope::All)).unwrap();
        sleep(Duration::from_millis(100));
        let calls = fake.calls();
        assert_eq!(calls.matches("apt-get update").count(), 1, "{calls}");
        assert!(calls.contains("apt-get -y -q -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold --with-new-pkgs upgrade"), "{calls}");

        let fake = Fake::new("ID=ubuntu\nID_LIKE=debian\n");
        fake.host.clone().start(&updates(Scope::Security)).unwrap();
        fake.wait(ID);
        assert!(
            fake.calls()
                .contains("--force-confold --no-remove install --only-upgrade openssl libc6\n"),
            "{}",
            fake.calls()
        );

        let fake = Fake::new("ID=rocky\n");
        fake.host.clone().start(&updates(Scope::Security)).unwrap();
        fake.wait(ID);
        assert!(
            fake.calls()
                .contains("dnf -q makecache\ndnf -y upgrade --security\n"),
            "{}",
            fake.calls()
        );

        let fake = Fake::new("ID=alpine\n");
        fake.host.clone().start(&updates(Scope::All)).unwrap();
        fake.wait(ID);
        assert!(
            fake.calls().contains("apk update -q\napk upgrade -q\n"),
            "{}",
            fake.calls()
        );
    }

    #[test]
    fn a_failed_refresh_ends_the_run_with_its_output() {
        let fake = Fake::new("ID=debian\n");
        fs::write(fake.host.root.join("fail-apt-get"), "").unwrap();
        fake.host.clone().start(&updates(Scope::All)).unwrap();
        let state = fake.wait(ID);
        assert_eq!(state.result.unwrap().exit_code, 100);
        assert!(!fake.calls().contains("upgrade"));
    }

    #[test]
    fn refuses_what_the_host_cannot_do() {
        for (os, run) in [
            ("ID=alpine\n", updates(Scope::Security)),
            ("ID=arch\n", updates(Scope::All)),
            (
                "ID=debian\n",
                Run::SelfUpdate {
                    id: ID.into(),
                    version: "1.0.0".into(),
                },
            ),
        ] {
            let fake = Fake::new(os);
            assert!(
                matches!(fake.host.clone().start(&run), Err(Failure::Unsupported(_))),
                "{os} {run:?}"
            );
            assert!(!fake.host.run_dir(ID).exists());
        }
        let mut arch = Fake::new("ID=arch\n").host;
        assert!(matches!(
            arch.list_updates(false),
            Err(Failure::Unsupported(_))
        ));
    }

    // One run at a time: while the lock is held, neither a run nor a list starts.
    // Review M4-04: a run of a kind starts at most every five minutes (no refresh flood through
    // ApplyUpdates), a reboot after the updates still can; a run ID names one run only.
    #[test]
    fn runs_of_a_kind_start_at_most_every_five_minutes() {
        let fake = Fake::new("ID=debian\n");
        let mut host = fake.host.clone();
        host.start(&updates(Scope::All)).unwrap();
        fake.wait(ID);
        let second = Run::Updates {
            id: "0193f5a0-0000-7000-8000-000000000002".into(),
            scope: Scope::Security,
        };
        assert!(matches!(host.start(&second), Err(Failure::Later(_))));
        let reboot = Run::Reboot {
            id: "0193f5a0-0000-7000-8000-000000000003".into(),
            reason: "updates".into(),
        };
        host.start(&reboot).unwrap();
        // The same ID for another run is refused, not taken as the earlier one.
        let reused = Run::Reboot {
            id: ID.into(),
            reason: "updates".into(),
        };
        assert!(
            matches!(host.start(&reused), Err(Failure::Failed(why)) if why.contains("another run"))
        );
        // Five minutes later the next updates may start (once the reboot run is done).
        fake.wait("0193f5a0-0000-7000-8000-000000000003");
        let old = SystemTime::now() - REFRESH_EVERY - Duration::from_secs(1);
        File::open(fake.host.run_dir(ID))
            .unwrap()
            .set_modified(old)
            .unwrap();
        host.start(&second).unwrap();
    }

    // Review M4-04: a run waits a moment for the lock a GetStatus or a list holds briefly.
    #[test]
    fn a_run_waits_briefly_for_the_lock() {
        let fake = Fake::new("ID=debian\n");
        let host = fake.host.clone();
        let held = host.lock().unwrap().unwrap();
        fs::create_dir_all(host.run_dir(ID)).unwrap();
        fs::write(host.run_dir(ID).join("request"), "updates all").unwrap();
        let runner = std::thread::spawn(move || host.execute(ID));
        sleep(Duration::from_millis(500));
        drop(held);
        runner.join().unwrap().unwrap();
        assert_eq!(fake.host.result(ID).result.unwrap().exit_code, 0);
    }

    #[test]
    fn the_lock_keeps_runs_apart_and_names_the_running_one() {
        let fake = Fake::new("ID=debian\n");
        let mut host = fake.host.clone();
        let mut held = host.lock().unwrap().unwrap();
        held.write_all(ID.as_bytes()).unwrap();
        assert_eq!(host.status().running_run_id, ID);
        assert_eq!(host.start(&updates(Scope::All)), Err(Failure::Busy));
        assert!(
            matches!(host.list_updates(true), Err(Failure::Busy)),
            "the list waited 5 s in vain"
        );
        drop(held);
        // Another test's child may hold a copy of the descriptor until its exec, for a moment.
        for _ in 0..50 {
            if host.status().running_run_id.is_empty() {
                break;
            }
            sleep(Duration::from_millis(20));
        }
        assert_eq!(host.status().running_run_id, "");
        assert_eq!(host.status().distribution(), Distribution::Apt);
    }

    #[test]
    fn reboots_after_a_minute_with_the_reason() {
        let fake = Fake::new("ID=debian\n");
        fake.host
            .clone()
            .start(&Run::Reboot {
                id: ID.into(),
                reason: "updates (SerMon)".into(),
            })
            .unwrap();
        assert_eq!(fake.wait(ID).result.unwrap().exit_code, 0);
        assert!(
            // Review M4-04: "--" ends the options, the prefix keeps a reason from being one.
            fake.calls()
                .contains("shutdown -r -- +1 SerMon: updates (SerMon)\n"),
            "{}",
            fake.calls()
        );
        // OpenRC: busybox waits and never returns, so the result comes first.
        let mut fake = Fake::new("ID=alpine\n");
        fake.host.launch = Launch::Detached;
        fs::create_dir_all(fake.host.run_dir(ID)).unwrap();
        fs::write(fake.host.run_dir(ID).join("request"), "reboot\nupdates").unwrap();
        fake.host.execute(ID).unwrap();
        let state = fake.host.result(ID);
        assert_eq!(
            (state.phase(), state.result.unwrap().exit_code),
            (RunPhase::Done, 0)
        );
        assert!(fake.calls().contains("reboot -d 60\n"), "{}", fake.calls());
    }

    #[test]
    fn results_of_unknown_lost_and_old_runs() {
        let fake = Fake::new("ID=debian\n");
        assert_eq!(fake.host.result(ID).phase(), RunPhase::Unspecified);
        // Started, but nobody holds the lock and no result came: lost (killed, reboot).
        let dir = fake.host.run_dir(ID);
        fs::create_dir_all(&dir).unwrap();
        fs::write(dir.join("request"), "updates all").unwrap();
        assert_eq!(
            fake.host.result(ID).phase(),
            RunPhase::Running,
            "just launched"
        );
        fs::write(dir.join("started"), "1795000000").unwrap();
        let lost = fake.host.result(ID);
        assert_eq!(
            (
                lost.phase(),
                lost.result.unwrap().exit_code,
                lost.started_unix
            ),
            (RunPhase::Done, -1, 1_795_000_000)
        );
        // After seven days a run is forgotten.
        let old = SystemTime::now() - KEEP - Duration::from_secs(60);
        File::open(&dir).unwrap().set_modified(old).unwrap();
        let fresh = fake.host.run_dir("0193f5a0-0000-7000-8000-000000000002");
        fs::create_dir_all(&fresh).unwrap();
        fake.host.prune();
        assert!(!dir.exists());
        assert!(fresh.exists(), "a fresh run was pruned");
        // At most MAX_RUNS stay, the newest.
        for i in 0..MAX_RUNS + 2 {
            fs::create_dir_all(
                fake.host
                    .run_dir(&format!("0193f5a0-0000-7000-8000-{i:012}")),
            )
            .unwrap();
        }
        let oldest = fake.host.run_dir("0193f5a0-0000-7000-8000-000000000000");
        File::open(&oldest)
            .unwrap()
            .set_modified(SystemTime::now() - Duration::from_secs(3600))
            .unwrap();
        fake.host.prune();
        assert_eq!(
            fs::read_dir(fake.host.state.join("runs")).unwrap().count(),
            MAX_RUNS
        );
        assert!(!oldest.exists());
    }

    /// A script installation of agent 1.0.0 and a staged 1.1.0, signed with `signer`.
    fn installed(signer: &minisign::tests::Signer) -> Fake {
        let mut fake = Fake::new("ID=debian\n");
        fake.host.keys = vec![signer.key.clone()];
        let file = format!("sermon-agent-linux-{}", arch());
        fs::create_dir_all(fake.host.root.join("usr/local/bin")).unwrap();
        fs::create_dir_all(&fake.host.state).unwrap();
        fs::write(fake.host.root.join(AGENT), b"agent 1.0.0").unwrap();
        fs::write(
            fake.host.state.join("agent.minisig"),
            signer.sign(b"agent 1.0.0", &file, "1.0.0"),
        )
        .unwrap();
        fake.stage(b"agent 1.1.0", &signer.sign(b"agent 1.1.0", &file, "1.1.0"));
        fake
    }

    impl Fake {
        fn stage(&self, binary: &[u8], signature: &str) {
            let staged = self.host.root.join("var/lib/sermon-agent/self-update");
            fs::create_dir_all(&staged).unwrap();
            fs::write(staged.join("sermon-agent"), binary).unwrap();
            fs::write(staged.join("sermon-agent.minisig"), signature).unwrap();
        }

        fn agent(&self) -> Vec<u8> {
            fs::read(self.host.root.join(AGENT)).unwrap()
        }
    }

    /// A change of the installation that makes a self-update invalid.
    type Change<'a> = Box<dyn Fn(&Fake) + 'a>;

    fn self_update(version: &str) -> Run {
        Run::SelfUpdate {
            id: ID.into(),
            version: version.into(),
        }
    }

    // ADR 0036: the checked copy replaces the agent, the service restarts, the new agent confirms:
    // installed, and its signature is the record of the installed version. A change of the staged
    // file after the check has no effect.
    #[test]
    fn a_self_update_installs_the_checked_copy_once_the_new_agent_confirms() {
        let signer = minisign::tests::Signer::new(1);
        let fake = installed(&signer);
        let mut host = fake.host.clone();
        host.confirm_within = Duration::from_secs(4);
        assert_eq!(
            host.confirm(ID),
            Err(Failure::Refused(
                "no self-update of this run waits for a confirmation".into()
            ))
        );
        host.start(&self_update("1.1.0")).unwrap();
        fake.stage(b"evil", "");
        for _ in 0..100 {
            if fake.calls().contains("systemctl") {
                break;
            }
            sleep(Duration::from_millis(20));
        }
        assert_eq!(fake.agent(), b"agent 1.1.0");
        assert!(
            fake.calls().contains("systemctl restart sermon-agent"),
            "{}",
            fake.calls()
        );
        host.confirm(ID).unwrap();
        let state = fake.wait(ID);
        assert_eq!(state.result.unwrap().exit_code, 0);
        assert_eq!(fake.agent(), b"agent 1.1.0");
        let record = fs::read_to_string(host.state.join("agent.minisig")).unwrap();
        assert!(record.contains("version:1.1.0"));
        // Ended: no confirmation any more, the same run starts nothing again.
        assert!(matches!(host.confirm(ID), Err(Failure::Refused(_))));
        host.start(&self_update("1.1.0")).unwrap();
        assert_eq!(fake.calls().matches("systemctl").count(), 1);
        // A self-update not replaced yet: no confirmation either.
        let early = host.run_dir(OTHER_ID);
        fs::create_dir_all(&early).unwrap();
        fs::write(early.join("request"), "self-update 1.1.0").unwrap();
        assert!(matches!(host.confirm(OTHER_ID), Err(Failure::Refused(_))));
        fs::write(early.join("replaced"), "1").unwrap();
        assert_eq!(host.confirm(OTHER_ID), Ok(()));
    }

    // Without a confirmation in time the previous binary and its record come back.
    #[test]
    fn a_self_update_without_confirmation_rolls_back() {
        let signer = minisign::tests::Signer::new(1);
        let fake = installed(&signer);
        let before = fs::read_to_string(fake.host.state.join("agent.minisig")).unwrap();
        fake.host.clone().start(&self_update("1.1.0")).unwrap();
        let state = fake.wait(ID);
        let result = state.result.unwrap();
        assert_eq!(result.exit_code, 1);
        assert!(String::from_utf8_lossy(&result.output).contains("rolled back"));
        assert_eq!(fake.agent(), b"agent 1.0.0");
        assert_eq!(
            fs::read_to_string(fake.host.state.join("agent.minisig")).unwrap(),
            before
        );
        assert_eq!(
            fake.calls()
                .matches("systemctl restart sermon-agent")
                .count(),
            2
        );
    }

    // Only a newer version of this architecture, signed with the release key, from regular files
    // of the agent's staging directory; nothing is started otherwise.
    #[test]
    #[expect(clippy::too_many_lines, reason = "one case per line of the table")]
    fn a_self_update_refuses_what_is_not_a_newer_signed_agent() {
        let signer = minisign::tests::Signer::new(1);
        let other = minisign::tests::Signer::new(2);
        let file = format!("sermon-agent-linux-{}", arch());
        let new = b"agent 1.1.0";
        let cases: Vec<(&str, Change, &str)> = vec![
            (
                "another key",
                Box::new(|f| f.stage(new, &other.sign(new, &file, "1.1.0"))),
                "another key",
            ),
            (
                "another file",
                Box::new(|f| f.stage(new, &signer.sign(new, "sermon-helper-linux-amd64", "1.1.0"))),
                "not sermon-agent",
            ),
            (
                "another version",
                Box::new(|f| f.stage(new, &signer.sign(new, &file, "1.2.0"))),
                "1.2.0, not",
            ),
            (
                "changed binary",
                Box::new(|f| f.stage(b"agent 1.1.1", &signer.sign(new, &file, "1.1.0"))),
                "does not match",
            ),
            (
                "no record",
                Box::new(|f| fs::remove_file(f.host.state.join("agent.minisig")).unwrap()),
                "run install.sh",
            ),
            (
                "forged record",
                Box::new(|f| {
                    fs::write(
                        f.host.state.join("agent.minisig"),
                        other.sign(b"agent 1.0.0", &file, "0.0.1"),
                    )
                    .unwrap();
                }),
                "run install.sh",
            ),
            (
                "record of another file",
                Box::new(|f| {
                    fs::write(
                        f.host.state.join("agent.minisig"),
                        signer.sign(b"agent 1.0.0", "sermon-helper-linux-amd64", "0.0.1"),
                    )
                    .unwrap();
                }),
                "run install.sh",
            ),
            (
                "not staged",
                Box::new(|f| {
                    fs::remove_dir_all(f.host.root.join("var/lib/sermon-agent/self-update"))
                        .unwrap();
                }),
                "cannot read",
            ),
            (
                "linked file",
                Box::new(|f| {
                    let staged = f
                        .host
                        .root
                        .join("var/lib/sermon-agent/self-update/sermon-agent");
                    fs::remove_file(&staged).unwrap();
                    std::os::unix::fs::symlink(f.host.root.join(AGENT), &staged).unwrap();
                }),
                "cannot read",
            ),
            (
                "linked directory",
                Box::new(|f| {
                    let lib = f.host.root.join("var/lib");
                    fs::rename(lib.join("sermon-agent"), lib.join("elsewhere")).unwrap();
                    std::os::unix::fs::symlink(lib.join("elsewhere"), lib.join("sermon-agent"))
                        .unwrap();
                }),
                "cannot read",
            ),
            (
                "a directory",
                Box::new(|f| {
                    let staged = f
                        .host
                        .root
                        .join("var/lib/sermon-agent/self-update/sermon-agent");
                    fs::remove_file(&staged).unwrap();
                    fs::create_dir(&staged).unwrap();
                }),
                "no regular file",
            ),
        ];
        for (name, change, why) in cases {
            let fake = installed(&signer);
            change(&fake);
            match fake.host.clone().start(&self_update("1.1.0")) {
                Err(Failure::Refused(r)) => assert!(r.contains(why), "{name}: {r}"),
                other => panic!("{name}: {other:?}"),
            }
            assert!(!fake.host.run_dir(ID).exists(), "{name}");
            assert_eq!(fake.agent(), b"agent 1.0.0", "{name}");
        }
        for version in ["1.0.0", "0.9.0", "1.0.0-rc.1"] {
            let fake = installed(&signer);
            fake.stage(new, &signer.sign(new, &file, version));
            let r = fake.host.clone().start(&self_update(version));
            assert!(
                matches!(&r, Err(Failure::Refused(why)) if why.contains("not newer")),
                "{version}: {r:?}"
            );
        }
        let fake = installed(&signer);
        fake.stage(&vec![0; usize::try_from(MAX_BINARY).unwrap() + 1], "");
        assert!(
            matches!(fake.host.clone().start(&self_update("1.1.0")), Err(Failure::Refused(r)) if r.contains("larger"))
        );
        // A package installation, a helper without a key.
        let fake = installed(&signer);
        fs::remove_file(fake.host.root.join(AGENT)).unwrap();
        assert!(matches!(
            fake.host.clone().start(&self_update("1.1.0")),
            Err(Failure::Unsupported(_))
        ));
        let mut fake = installed(&signer);
        fake.host.keys.clear();
        assert!(matches!(
            fake.host.clone().start(&self_update("1.1.0")),
            Err(Failure::Unsupported(_))
        ));
    }
}
