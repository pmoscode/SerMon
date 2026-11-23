//! Snapshots of the processes for the live view ("top", KONZEPT §9, M3-13). Not a metric
//! collector: the agent takes a snapshot only while a live view runs and sends it as is (never
//! into the WAL).
//!
//! - **Per process** from `/proc/<pid>/{stat,status,cmdline,cgroup}`: PID, PPID, user (from
//!   `/etc/passwd`), state, CPU share over the interval (of one core, like `top`), RSS and its
//!   share of the memory, threads, run time, name, command line, unit and container from the
//!   cgroup. What can't be read is `None` ("n/a"); a process that ends while being read is
//!   skipped.
//! - **Header:** CPU, memory, swap, load, uptime and the processes per state.
//! - **Bounds:** at most [`MAX_PROCESSES`] (the busiest), the rest is counted; command lines are
//!   read up to [`CMDLINE_READ_BYTES`]. Redaction and cutting are the caller's (they need the
//!   organization's rules).

use std::collections::HashMap;
use std::fs::File;
use std::io::Read;
use std::time::Duration;

use sermon_core::{Monotonic, ProcError, ProcRoot};

use crate::CollectError;
use crate::cpu::{CpuTimes, LoadAverage, parse_loadavg, parse_stat as parse_cpu_stat};
use crate::memory::parse_meminfo;
use crate::rate::{round_to_i64, to_f64};
use crate::system::parse_uptime;

/// Processes per snapshot at most.
pub const MAX_PROCESSES: usize = 4000;
/// Bytes of a command line read at most (the caller cuts further).
pub const CMDLINE_READ_BYTES: u64 = 8 * 1024;
/// Clock ticks per second of `stat` (`USER_HZ`, 100 on every Linux architecture).
const TICKS_PER_SECOND: f64 = 100.0;
/// `PF_KTHREAD` in the flags of `stat`.
const KERNEL_THREAD: u64 = 0x0020_0000;
/// How long the user names of `/etc/passwd` are kept.
const USERS_FOR: Duration = Duration::from_secs(30);

/// The fields of `/proc/<pid>/stat` the snapshot uses.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ProcStat {
    pub pid: u32,
    /// The name (`comm`), at most 15 bytes from the kernel; may hold `)` and spaces.
    pub comm: String,
    pub state: char,
    pub ppid: u32,
    pub flags: u64,
    /// Ticks in user and kernel mode.
    pub utime: u64,
    pub stime: u64,
    pub threads: u32,
    /// Start in ticks after boot.
    pub starttime: u64,
}

/// Parses `/proc/<pid>/stat`. The name is between the first `(` and the **last** `)`: it may
/// contain both.
///
/// # Errors
///
/// A description if a field is missing or not a number.
pub fn parse_stat(text: &str) -> Result<ProcStat, String> {
    let open = text.find('(').ok_or("no (")?;
    let close = text.rfind(')').ok_or("no )")?;
    if close < open {
        return Err(") before (".into());
    }
    let pid = text[..open].trim().parse().map_err(|_| "pid")?;
    let comm = text[open + 1..close].to_owned();
    // Fields after the name, numbered as in proc(5) from 3 (state).
    let rest: Vec<&str> = text[close + 1..].split_whitespace().collect();
    let field = |n: usize| rest.get(n - 3).copied().ok_or(format!("field {n}"));
    let number =
        |n: usize| -> Result<u64, String> { field(n)?.parse().map_err(|_| format!("field {n}")) };
    let state = field(3)?.chars().next().ok_or("state")?;
    Ok(ProcStat {
        pid,
        comm,
        state,
        ppid: u32::try_from(number(4)?).map_err(|_| "ppid")?,
        flags: number(9)?,
        utime: number(14)?,
        stime: number(15)?,
        threads: u32::try_from(number(20)?).unwrap_or(u32::MAX),
        starttime: number(22)?,
    })
}

/// The fields of `/proc/<pid>/status` the snapshot uses.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct ProcStatus {
    /// The real user ID.
    pub uid: Option<u32>,
    /// Resident memory; none for kernel threads and zombies.
    pub rss_bytes: Option<u64>,
}

/// Parses `/proc/<pid>/status`.
#[must_use]
pub fn parse_status(text: &str) -> ProcStatus {
    let mut status = ProcStatus::default();
    for line in text.lines() {
        let Some((key, value)) = line.split_once(':') else {
            continue;
        };
        let first = value.split_whitespace().next();
        match key {
            "Uid" => status.uid = first.and_then(|v| v.parse().ok()),
            "VmRSS" => {
                status.rss_bytes = first
                    .and_then(|v| v.parse::<u64>().ok())
                    .map(|kib| kib.saturating_mul(1024));
            }
            _ => {}
        }
    }
    status
}

/// The command line: arguments separated by NUL become separated by spaces; empty for kernel
/// threads and zombies.
#[must_use]
pub fn parse_cmdline(bytes: &[u8]) -> String {
    let trimmed = bytes.strip_suffix(&[0]).unwrap_or(bytes);
    String::from_utf8_lossy(trimmed).replace('\0', " ")
}

/// Where a process runs, from its cgroup.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Place {
    /// The systemd unit (service, scope, socket …).
    pub unit: Option<String>,
    /// The first 12 characters of a container ID (Docker, Podman, containerd, CRI-O).
    pub container: Option<String>,
}

const UNIT_SUFFIXES: [&str; 6] = [".service", ".scope", ".socket", ".mount", ".swap", ".timer"];
const CONTAINER_PREFIXES: [&str; 5] = ["docker-", "libpod-", "cri-containerd-", "crio-", ""];

/// Parses `/proc/<pid>/cgroup`: the unified hierarchy (`0::/…`), else systemd's (`name=systemd`),
/// else the first line.
#[must_use]
pub fn parse_cgroup(text: &str) -> Place {
    let lines: Vec<&str> = text.lines().collect();
    let path = lines
        .iter()
        .find(|l| l.starts_with("0::"))
        .or_else(|| lines.iter().find(|l| l.contains(":name=systemd:")))
        .or_else(|| lines.first())
        .and_then(|l| l.splitn(3, ':').nth(2))
        .unwrap_or("");
    let mut place = Place::default();
    for segment in path.split('/').filter(|s| !s.is_empty()) {
        if UNIT_SUFFIXES.iter().any(|s| segment.ends_with(s)) {
            place.unit = Some(segment.to_owned());
        }
        let bare = segment.strip_suffix(".scope").unwrap_or(segment);
        for prefix in CONTAINER_PREFIXES {
            if let Some(id) = bare.strip_prefix(prefix)
                && id.len() == 64
                && id.bytes().all(|b| b.is_ascii_hexdigit())
            {
                place.container = Some(id[..12].to_ascii_lowercase());
            }
        }
    }
    place
}

/// User names by ID from `/etc/passwd`.
#[must_use]
pub fn parse_passwd(text: &str) -> HashMap<u32, String> {
    text.lines()
        .filter_map(|line| {
            let mut fields = line.split(':');
            let name = fields.next()?;
            let uid = fields.nth(1)?.parse().ok()?;
            (!name.is_empty()).then(|| (uid, name.to_owned()))
        })
        .collect()
}

/// One process of a snapshot; `None` is "not readable".
#[derive(Debug, Clone, PartialEq)]
pub struct Process {
    pub pid: u32,
    pub ppid: u32,
    pub uid: Option<u32>,
    /// The name of the user; `None` for IDs without a name (shown as the number).
    pub user: Option<String>,
    /// `R`, `S`, `D`, `Z`, `T`, `I`, …
    pub state: char,
    /// CPU share of one core over the interval; `None` in the first snapshot of the process.
    pub cpu_ratio: Option<f64>,
    pub rss_bytes: Option<u64>,
    /// RSS of the physical memory.
    pub memory_ratio: Option<f64>,
    pub threads: u32,
    /// Since the start of the process.
    pub runtime_seconds: Option<u64>,
    pub name: String,
    /// Unredacted, at most [`CMDLINE_READ_BYTES`]; `None` if not readable, empty for kernel
    /// threads and zombies.
    pub cmdline: Option<String>,
    pub kernel_thread: bool,
    pub place: Place,
}

/// Processes per state.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct Tasks {
    pub total: u32,
    pub running: u32,
    pub sleeping: u32,
    pub stopped: u32,
    pub zombie: u32,
}

/// The header like `top`'s.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Header {
    /// Busy share of all CPUs over the interval; `None` in the first snapshot.
    pub cpu_busy_ratio: Option<f64>,
    pub cpu_count: u32,
    pub memory_total_bytes: u64,
    pub memory_available_bytes: u64,
    pub swap_total_bytes: u64,
    pub swap_free_bytes: u64,
    pub load: Option<LoadAverage>,
    pub uptime_seconds: Option<u64>,
    pub tasks: Tasks,
}

/// A snapshot.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Snapshot {
    pub header: Header,
    /// The busiest first, at most [`MAX_PROCESSES`].
    pub processes: Vec<Process>,
    /// Processes left out over the bound.
    pub omitted: u32,
}

/// Takes snapshots; keeps the previous one for the CPU shares.
#[derive(Debug, Default)]
pub struct ProcessSampler {
    /// Ticks per process (with its start, so a reused PID is a new process).
    previous: HashMap<u32, (u64, u64)>,
    previous_cpu: Option<(Monotonic, CpuTimes)>,
    previous_at: Option<Monotonic>,
    users: HashMap<u32, String>,
    users_at: Option<Monotonic>,
}

fn read_prefix(root: &ProcRoot, relative: &str, limit: u64) -> Result<Vec<u8>, ProcError> {
    let path = root.proc(relative)?;
    let io = |source| ProcError::Io {
        path: path.clone(),
        source,
    };
    let mut bytes = Vec::new();
    File::open(&path)
        .map_err(io)?
        .take(limit)
        .read_to_end(&mut bytes)
        .map_err(io)?;
    Ok(bytes)
}

fn ratio(part: f64, whole: f64) -> Option<f64> {
    (whole > 0.0 && part.is_finite()).then(|| (part / whole).max(0.0))
}

impl ProcessSampler {
    /// A sampler without history: the first snapshot has no CPU shares.
    #[must_use]
    pub fn new() -> Self {
        Self::default()
    }

    /// Forgets the history (the live view ended).
    pub fn reset(&mut self) {
        *self = Self::default();
    }

    /// Takes a snapshot at `now`.
    ///
    /// # Errors
    ///
    /// If `/proc` can't be listed or `/proc/stat` or `/proc/meminfo` can't be read.
    pub fn snapshot(&mut self, root: &ProcRoot, now: Monotonic) -> Result<Snapshot, CollectError> {
        let cpu =
            parse_cpu_stat(&root.read_proc("stat")?).map_err(|reason| CollectError::Parse {
                file: "/proc/stat",
                reason,
            })?;
        let memory =
            parse_meminfo(&root.read_proc("meminfo")?).map_err(|reason| CollectError::Parse {
                file: "/proc/meminfo",
                reason,
            })?;
        let uptime = root
            .read_proc("uptime")
            .ok()
            .and_then(|t| parse_uptime(&t))
            .and_then(|s| u64::try_from(s).ok());
        if self
            .users_at
            .is_none_or(|at| now.saturating_duration_since(at) >= USERS_FOR)
        {
            self.users = root
                .read_etc("passwd")
                .map_or_else(|_| HashMap::new(), |t| parse_passwd(&t));
            self.users_at = Some(now);
        }
        let elapsed = self
            .previous_at
            .map(|at| now.saturating_duration_since(at).as_secs_f64());
        let mut processes = Vec::new();
        let mut ticks = HashMap::new();
        let mut tasks = Tasks::default();
        for name in root.list_proc("")? {
            let Ok(pid) = name.parse::<u32>() else {
                continue;
            };
            let Some((process, used)) = self.process(root, pid, elapsed, memory.total, uptime)
            else {
                continue;
            };
            tasks.count(process.state);
            ticks.insert(pid, used);
            processes.push(process);
        }
        let cpu_busy_ratio = self.previous_cpu.and_then(|(_, before)| {
            let total = cpu.total.total().saturating_sub(before.total());
            let busy = cpu.total.busy().saturating_sub(before.busy());
            ratio(to_f64(busy), to_f64(total))
        });
        self.previous = ticks;
        self.previous_cpu = Some((now, cpu.total));
        self.previous_at = Some(now);
        // The busiest first: by CPU, then memory, then PID.
        processes.sort_by(|a, b| {
            b.cpu_ratio
                .unwrap_or(0.0)
                .total_cmp(&a.cpu_ratio.unwrap_or(0.0))
                .then(b.rss_bytes.cmp(&a.rss_bytes))
                .then(a.pid.cmp(&b.pid))
        });
        let omitted = processes.len().saturating_sub(MAX_PROCESSES);
        processes.truncate(MAX_PROCESSES);
        Ok(Snapshot {
            header: Header {
                cpu_busy_ratio,
                cpu_count: u32::try_from(cpu.cores.len()).unwrap_or(u32::MAX),
                memory_total_bytes: memory.total,
                memory_available_bytes: memory.available,
                swap_total_bytes: memory.swap_total,
                swap_free_bytes: memory.swap_free,
                load: root
                    .read_proc("loadavg")
                    .ok()
                    .and_then(|t| parse_loadavg(&t).ok()),
                uptime_seconds: uptime,
                tasks,
            },
            processes,
            omitted: u32::try_from(omitted).unwrap_or(u32::MAX),
        })
    }

    /// One process with its start and ticks; `None` if it ended while being read.
    fn process(
        &self,
        root: &ProcRoot,
        pid: u32,
        elapsed: Option<f64>,
        memory_total: u64,
        uptime: Option<u64>,
    ) -> Option<(Process, (u64, u64))> {
        let stat = parse_stat(&root.read_proc(&format!("{pid}/stat")).ok()?).ok()?;
        let status = root
            .read_proc(&format!("{pid}/status"))
            .map(|t| parse_status(&t))
            .ok();
        let cmdline = read_prefix(root, &format!("{pid}/cmdline"), CMDLINE_READ_BYTES)
            .ok()
            .map(|b| parse_cmdline(&b));
        let place = root
            .read_proc(&format!("{pid}/cgroup"))
            .map_or_else(|_| Place::default(), |t| parse_cgroup(&t));
        let used = stat.utime.saturating_add(stat.stime);
        let cpu_ratio = match (self.previous.get(&pid), elapsed) {
            (Some(&(started, before)), Some(seconds)) if started == stat.starttime => ratio(
                to_f64(used.saturating_sub(before)) / TICKS_PER_SECOND,
                seconds,
            ),
            _ => None,
        };
        let uid = status.and_then(|s| s.uid);
        let rss = status.and_then(|s| s.rss_bytes);
        let started = to_f64(stat.starttime) / TICKS_PER_SECOND;
        let process = Process {
            pid,
            ppid: stat.ppid,
            uid,
            user: uid.and_then(|u| self.users.get(&u).cloned()),
            state: stat.state,
            cpu_ratio,
            rss_bytes: rss,
            memory_ratio: rss.and_then(|r| ratio(to_f64(r), to_f64(memory_total))),
            threads: stat.threads,
            runtime_seconds: uptime.map(|up| whole_seconds(to_f64(up) - started)),
            name: stat.comm,
            cmdline,
            kernel_thread: stat.flags & KERNEL_THREAD != 0,
            place,
        };
        Some((process, (stat.starttime, used)))
    }
}

impl Tasks {
    fn count(&mut self, state: char) {
        self.total += 1;
        match state {
            'R' => self.running += 1,
            'S' | 'D' | 'I' => self.sleeping += 1,
            'T' | 't' => self.stopped += 1,
            'Z' => self.zombie += 1,
            _ => {}
        }
    }
}

/// Non-negative whole seconds.
fn whole_seconds(seconds: f64) -> u64 {
    u64::try_from(round_to_i64(seconds.max(0.0).floor())).unwrap_or(0)
}

#[cfg(test)]
mod tests {
    use std::path::{Path, PathBuf};

    use proptest::prelude::*;

    use super::*;

    fn fixture() -> PathBuf {
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../fixtures/processes/debian-13")
    }

    fn copy(from: &Path, to: &Path) {
        std::fs::create_dir_all(to).unwrap();
        for entry in std::fs::read_dir(from).unwrap() {
            let entry = entry.unwrap();
            let target = to.join(entry.file_name());
            if entry.file_type().unwrap().is_dir() {
                copy(&entry.path(), &target);
            } else {
                std::fs::copy(entry.path(), target).unwrap();
            }
        }
    }

    fn by_pid(s: &Snapshot, pid: u32) -> &Process {
        s.processes.iter().find(|p| p.pid == pid).unwrap()
    }

    #[test]
    fn parses_stat_with_odd_names() {
        let stat =
            parse_stat(&std::fs::read_to_string(fixture().join("proc/10/stat")).unwrap()).unwrap();
        assert_eq!(stat.comm, "evil) name (x");
        assert_eq!(
            (stat.pid, stat.state, stat.ppid, stat.threads),
            (10, 'S', 1, 1)
        );
        let kernel =
            parse_stat(&std::fs::read_to_string(fixture().join("proc/9001/stat")).unwrap())
                .unwrap();
        assert_eq!(kernel.comm, "kthreadd");
        assert!(kernel.flags & KERNEL_THREAD != 0);
        assert!(parse_stat("1 (x").is_err());
        assert!(parse_stat("x (a) S").is_err());
        assert!(parse_stat("1 (a) S 0").is_err());
        assert!(parse_stat(") 1 (").is_err());
    }

    #[test]
    fn parses_status_cmdline_cgroup_and_passwd() {
        let status =
            parse_status(&std::fs::read_to_string(fixture().join("proc/8/status")).unwrap());
        assert_eq!(status.uid, Some(65534));
        assert!(status.rss_bytes.is_some_and(|r| r > 0 && r % 1024 == 0));
        assert_eq!(
            parse_status("Name:\tkthreadd\nUid:\t0\t0\t0\t0\n").rss_bytes,
            None
        );
        assert_eq!(
            parse_cmdline(b"/tmp/evil) name (x\0-c\0sleep 300; echo 'a b'\0"),
            "/tmp/evil) name (x -c sleep 300; echo 'a b'"
        );
        assert_eq!(parse_cmdline(b""), "");
        assert_eq!(parse_cmdline(b"a\xffb\0"), "a\u{fffd}b");
        let users = parse_passwd(&std::fs::read_to_string(fixture().join("etc/passwd")).unwrap());
        assert_eq!(users.get(&0).map(String::as_str), Some("root"));
        assert_eq!(users.get(&65534).map(String::as_str), Some("nobody"));
        assert!(parse_passwd("broken\n:x:1:\nok:x:7:7::/:/bin/sh").contains_key(&7));
    }

    #[test]
    fn finds_units_and_containers_in_the_cgroup() {
        let id = "4f1c0a5e8b2d4c6e9a1b3c5d7e9f0a2b4c6d8e0f1a3b5c7d9e1f3a5b7c9d1e3f";
        let docker = format!("docker-{id}.scope");
        let cri = format!("cri-containerd-{id}.scope");
        let cases: Vec<(String, Option<&str>, Option<&str>)> = vec![
            (
                "0::/system.slice/nginx.service\n".into(),
                Some("nginx.service"),
                None,
            ),
            (
                "0::/user.slice/user-1000.slice/session-3.scope\n".into(),
                Some("session-3.scope"),
                None,
            ),
            (
                format!("0::/system.slice/{docker}\n"),
                Some(&docker),
                Some("4f1c0a5e8b2d"),
            ),
            (
                format!("0::/kubepods.slice/kubepods-pod1.slice/{cri}\n"),
                Some(&cri),
                Some("4f1c0a5e8b2d"),
            ),
            (
                format!("12:pids:/docker/{id}\n1:name=systemd:/docker/{id}\n"),
                None,
                Some("4f1c0a5e8b2d"),
            ),
            (
                format!("0::/docker/{}\n", id.to_ascii_uppercase()),
                None,
                Some("4f1c0a5e8b2d"),
            ),
            (
                "12:pids:/system.slice/cron.service\n1:name=systemd:/system.slice/sshd.service\n"
                    .into(),
                Some("sshd.service"),
                None,
            ),
            ("0::/\n".into(), None, None),
            ("0::/../..\n".into(), None, None),
            (String::new(), None, None),
        ];
        for (text, unit, container) in &cases {
            let place = parse_cgroup(text);
            assert_eq!(place.unit.as_deref(), *unit, "{text}");
            assert_eq!(place.container.as_deref(), *container, "{text}");
        }
    }

    #[test]
    fn a_snapshot_of_the_fixture() {
        let root = ProcRoot::new(fixture());
        let mut sampler = ProcessSampler::new();
        let s = sampler
            .snapshot(&root, Monotonic::from_origin(Duration::ZERO))
            .unwrap();
        assert_eq!(s.processes.len(), 9);
        assert_eq!(s.omitted, 0);
        let odd = by_pid(&s, 10);
        assert_eq!(odd.name, "evil) name (x");
        assert_eq!(
            odd.cmdline.as_deref(),
            Some("/tmp/evil) name (x -c sleep 300; echo 'a b'")
        );
        assert_eq!(by_pid(&s, 8).user.as_deref(), Some("nobody"));
        let zombie = by_pid(&s, 14);
        assert_eq!(
            (zombie.state, zombie.rss_bytes, zombie.cmdline.as_deref()),
            ('Z', None, Some(""))
        );
        let kernel = by_pid(&s, 9001);
        assert!(kernel.kernel_thread && kernel.cmdline.as_deref() == Some(""));
        assert_eq!(by_pid(&s, 9003).threads, 37);
        assert!(
            by_pid(&s, 7).cpu_ratio.is_none(),
            "no CPU share in the first snapshot"
        );
        assert!(
            by_pid(&s, 7)
                .memory_ratio
                .is_some_and(|r| r > 0.0 && r < 0.01)
        );
        assert!(by_pid(&s, 7).runtime_seconds.is_some());
        assert_eq!(s.header.tasks.total, 9);
        assert_eq!(s.header.tasks.zombie, 1);
        assert!(
            s.header.memory_total_bytes > 0
                && s.header.load.is_some()
                && s.header.cpu_busy_ratio.is_none()
        );
    }

    #[test]
    fn cpu_shares_from_two_snapshots() {
        let dir = tempfile::tempdir().unwrap();
        copy(&fixture(), dir.path());
        let root = ProcRoot::new(dir.path());
        let mut sampler = ProcessSampler::new();
        sampler
            .snapshot(&root, Monotonic::from_origin(Duration::ZERO))
            .unwrap();
        // sleep (pid 7) used 50 ticks (0.5 s) in 2 s; the machine was busy 25 % of it.
        let stat = std::fs::read_to_string(dir.path().join("proc/7/stat")).unwrap();
        let mut fields: Vec<String> = stat.split(' ').map(str::to_owned).collect();
        fields[13] = (fields[13].parse::<u64>().unwrap() + 30).to_string();
        fields[14] = (fields[14].parse::<u64>().unwrap() + 20).to_string();
        std::fs::write(dir.path().join("proc/7/stat"), fields.join(" ")).unwrap();
        let cpu = std::fs::read_to_string(dir.path().join("proc/stat")).unwrap();
        let line = cpu.lines().next().unwrap();
        let mut parts: Vec<u64> = line
            .split_whitespace()
            .skip(1)
            .map(|v| v.parse().unwrap())
            .collect();
        parts[0] += 100; // user
        parts[3] += 300; // idle
        let new_line = format!(
            "cpu  {}",
            parts
                .iter()
                .map(u64::to_string)
                .collect::<Vec<_>>()
                .join(" ")
        );
        std::fs::write(
            dir.path().join("proc/stat"),
            cpu.replacen(line, &new_line, 1),
        )
        .unwrap();
        // A reused PID is a new process.
        let stat8 = std::fs::read_to_string(dir.path().join("proc/8/stat")).unwrap();
        let mut f8: Vec<String> = stat8.split(' ').map(str::to_owned).collect();
        f8[21] = (f8[21].parse::<u64>().unwrap() + 1).to_string();
        std::fs::write(dir.path().join("proc/8/stat"), f8.join(" ")).unwrap();
        // One process ended.
        std::fs::remove_dir_all(dir.path().join("proc/11")).unwrap();

        let s = sampler
            .snapshot(&root, Monotonic::from_origin(Duration::from_secs(2)))
            .unwrap();
        let busy = by_pid(&s, 7).cpu_ratio.unwrap();
        assert!((busy - 0.25).abs() < 1e-9, "{busy}");
        assert_eq!(s.processes[0].pid, 7, "the busiest first");
        assert!(
            by_pid(&s, 8).cpu_ratio.is_none(),
            "a new process with the old PID"
        );
        assert!(s.processes.iter().all(|p| p.pid != 11));
        let cpu = s.header.cpu_busy_ratio.unwrap();
        assert!((cpu - 0.25).abs() < 1e-9, "{cpu}");
    }

    #[test]
    fn stays_within_its_bounds() {
        let dir = tempfile::tempdir().unwrap();
        copy(&fixture(), dir.path());
        let template = std::fs::read_to_string(dir.path().join("proc/7/stat")).unwrap();
        for pid in 100..100 + u32::try_from(MAX_PROCESSES).unwrap() {
            let d = dir.path().join(format!("proc/{pid}"));
            std::fs::create_dir_all(&d).unwrap();
            std::fs::write(
                d.join("stat"),
                template.replacen("7 (", &format!("{pid} ("), 1),
            )
            .unwrap();
        }
        // A long command line is read only up to the bound.
        std::fs::write(dir.path().join("proc/7/cmdline"), vec![b'a'; 100_000]).unwrap();
        // Unreadable details are n/a, not an error.
        std::fs::remove_file(dir.path().join("proc/8/status")).unwrap();
        let s = ProcessSampler::new()
            .snapshot(
                &ProcRoot::new(dir.path()),
                Monotonic::from_origin(Duration::ZERO),
            )
            .unwrap();
        assert_eq!(s.processes.len(), MAX_PROCESSES);
        assert_eq!(s.omitted, 9);
        assert_eq!(
            s.header.tasks.total,
            u32::try_from(MAX_PROCESSES).unwrap() + 9
        );
        if let Some(p) = s.processes.iter().find(|p| p.pid == 7) {
            assert_eq!(
                p.cmdline.as_ref().map(String::len),
                Some(usize::try_from(CMDLINE_READ_BYTES).unwrap())
            );
        }
        if let Some(p) = s.processes.iter().find(|p| p.pid == 8) {
            assert_eq!((p.uid, p.user.as_deref(), p.rss_bytes), (None, None, None));
        }
    }

    proptest! {
        #[test]
        fn parsers_never_panic(text in "\\PC{0,400}", bytes in proptest::collection::vec(any::<u8>(), 0..400)) {
            let _ = parse_stat(&text);
            let _ = parse_status(&text);
            let _ = parse_cgroup(&text);
            let _ = parse_passwd(&text);
            let _ = parse_cmdline(&bytes);
        }
    }
}
