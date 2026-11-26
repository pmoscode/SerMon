//! Containers in cgroup v2 (M5-16): where Docker and Podman put them, and what their files say.
//! Only through [`ProcRoot`], read-only, without the runtimes' sockets. Bounded: at most
//! [`MAX_SCANNED`] containers, [`MAX_USERS`] users of rootless runtimes.

use std::collections::BTreeSet;

use sermon_collectors::network::parse_net_dev;
use sermon_core::ProcRoot;

/// Most containers read in one run (the samples are cut further, `MAX_CONTAINERS`).
pub const MAX_SCANNED: usize = 512;
/// Most users whose rootless containers are looked for.
pub const MAX_USERS: usize = 64;
const MAX_NAME: usize = 64;
const MAX_DEVICES: usize = 64;

/// The container runtime of a cgroup.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
pub enum Runtime {
    Docker,
    Podman,
}

impl Runtime {
    /// The label value.
    #[must_use]
    pub fn name(self) -> &'static str {
        match self {
            Self::Docker => "docker",
            Self::Podman => "podman",
        }
    }
}

/// A container's cgroup.
#[derive(Debug, Clone, PartialEq, Eq, PartialOrd, Ord)]
pub struct Found {
    /// Its ID, 64 hex digits.
    pub id: String,
    pub runtime: Runtime,
    /// Its directory below `sys/fs/cgroup`.
    pub dir: String,
}

/// The ID in a cgroup's name: `<id>` (Docker, cgroupfs), `docker-<id>.scope` (Docker, systemd),
/// `libpod-<id>.scope` (Podman, systemd), `libpod-<id>` (Podman, cgroupfs). `conmon`'s cgroups
/// (`libpod-conmon-<id>.scope`) are the runtime's, not the container's.
#[must_use]
pub fn id_of(name: &str) -> Option<(Runtime, &str)> {
    let hex = |s: &str| {
        s.len() == 64
            && s.bytes()
                .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
    };
    let (runtime, rest) = if let Some(rest) = name.strip_prefix("docker-") {
        (Runtime::Docker, rest.strip_suffix(".scope")?)
    } else if let Some(rest) = name.strip_prefix("libpod-") {
        (Runtime::Podman, rest.strip_suffix(".scope").unwrap_or(rest))
    } else {
        (Runtime::Docker, name)
    };
    hex(rest).then_some((runtime, rest))
}

/// The containers below `root`, sorted, at most [`MAX_SCANNED`]; `None` without cgroup v2.
#[must_use]
pub fn scan(root: &ProcRoot) -> Option<Vec<Found>> {
    root.read_sys("fs/cgroup/cgroup.controllers").ok()?;
    let mut parents = vec![
        ("docker".to_owned(), false),
        ("system.slice".to_owned(), true),
        ("machine.slice".to_owned(), true),
        ("libpod_parent".to_owned(), false),
    ];
    // Rootless: user.slice/user-<uid>.slice/user@<uid>.service/{user,app}.slice.
    let users = root.list_sys("fs/cgroup/user.slice").unwrap_or_default();
    for user in users
        .iter()
        .filter_map(|u| u.strip_prefix("user-")?.strip_suffix(".slice"))
        .take(MAX_USERS)
    {
        if user.bytes().all(|b| b.is_ascii_digit()) {
            for slice in ["user.slice", "app.slice"] {
                parents.push((
                    format!("user.slice/user-{user}.slice/user@{user}.service/{slice}"),
                    true,
                ));
            }
        }
    }
    let mut found = BTreeSet::new();
    for (parent, scopes) in parents {
        let Ok(children) = root.list_sys(&format!("fs/cgroup/{parent}")) else {
            continue;
        };
        for child in children {
            // Directly below docker/ the bare ID; below a slice only scopes.
            #[allow(
                clippy::case_sensitive_file_extension_comparisons,
                reason = "systemd names its scopes in lower case"
            )]
            let scope = child.ends_with(".scope");
            if scopes != scope {
                continue;
            }
            if let Some((runtime, id)) = id_of(&child) {
                found.insert(Found {
                    id: id.to_owned(),
                    runtime,
                    dir: format!("{parent}/{child}"),
                });
            }
            if found.len() >= MAX_SCANNED {
                return Some(found.into_iter().collect());
            }
        }
    }
    Some(found.into_iter().collect())
}

/// What a container's cgroup and its first process say; counters as they are.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Reading {
    /// The name of its first process (`comm`): without the socket the best name there is.
    pub name: String,
    /// CPU time, µs.
    pub cpu_usec: Option<u64>,
    /// Memory in use without the inactive page cache (as `docker stats`).
    pub memory: Option<u64>,
    /// Its memory limit; `None` without one.
    pub limit: Option<u64>,
    pub oom_kills: Option<u64>,
    /// Bytes read and written, all devices.
    pub io: Option<(u64, u64)>,
    pub processes: Option<u64>,
    /// Bytes received and sent of its own network namespace; `None` in the host's.
    pub net: Option<(u64, u64)>,
}

/// Reads the container of cgroup `dir`; `host_interfaces` are those of the agent's network
/// namespace (a container with the same ones uses the host's network: its traffic is the host's).
#[must_use]
pub fn read(root: &ProcRoot, dir: &str, host_interfaces: &BTreeSet<String>) -> Reading {
    let file = |name: &str| root.read_sys(&format!("fs/cgroup/{dir}/{name}")).ok();
    let mut r = Reading {
        cpu_usec: file("cpu.stat").and_then(|t| field(&t, "usage_usec")),
        limit: file("memory.max").and_then(|t| t.trim().parse().ok()),
        oom_kills: file("memory.events").and_then(|t| field(&t, "oom_kill")),
        io: file("io.stat").map(|t| io(&t)),
        processes: file("pids.current").and_then(|t| t.trim().parse().ok()),
        ..Reading::default()
    };
    r.memory = file("memory.current")
        .and_then(|t| t.trim().parse::<u64>().ok())
        .map(|current| {
            let inactive = file("memory.stat").and_then(|t| field(&t, "inactive_file"));
            current.saturating_sub(inactive.unwrap_or(0))
        });
    // Podman with systemd runs the processes in a child cgroup `container`.
    let pid = file("cgroup.procs")
        .and_then(|t| first_pid(&t))
        .or_else(|| file("container/cgroup.procs").and_then(|t| first_pid(&t)));
    if let Some(pid) = pid {
        r.name = root
            .read_proc(&format!("{pid}/comm"))
            .map_or_else(|_| String::new(), |c| name(&c));
        r.net = root
            .read_proc(&format!("{pid}/net/dev"))
            .ok()
            .and_then(|t| own_traffic(&t, host_interfaces));
    }
    r
}

/// The interfaces of `/proc/net/dev`.
#[must_use]
pub fn interfaces(net_dev: &str) -> BTreeSet<String> {
    parse_net_dev(net_dev).into_keys().take(256).collect()
}

/// Received and sent bytes without `lo`; `None` if the interfaces are the host's.
fn own_traffic(net_dev: &str, host: &BTreeSet<String>) -> Option<(u64, u64)> {
    let all = parse_net_dev(net_dev);
    if all.is_empty() || (!host.is_empty() && all.keys().eq(host.iter())) {
        return None;
    }
    Some(
        all.iter()
            .filter(|(n, _)| n.as_str() != "lo")
            .fold((0u64, 0u64), |(rx, tx), (_, c)| {
                (
                    rx.saturating_add(c.receive[0]),
                    tx.saturating_add(c.transmit[0]),
                )
            }),
    )
}

/// The value of `key` in a flat keyed file (`cpu.stat`, `memory.stat`, `memory.events`).
#[must_use]
pub fn field(text: &str, key: &str) -> Option<u64> {
    text.lines()
        .find_map(|l| l.strip_prefix(key)?.strip_prefix(' '))
        .and_then(|v| v.trim().parse().ok())
}

/// Bytes read and written of `io.stat` (`8:0 rbytes=… wbytes=… …` per device).
#[must_use]
pub fn io(text: &str) -> (u64, u64) {
    let mut sum = (0u64, 0u64);
    for line in text.lines().take(MAX_DEVICES) {
        for pair in line.split_whitespace().skip(1) {
            match pair.split_once('=') {
                Some(("rbytes", v)) => sum.0 = sum.0.saturating_add(v.parse().unwrap_or(0)),
                Some(("wbytes", v)) => sum.1 = sum.1.saturating_add(v.parse().unwrap_or(0)),
                _ => {}
            }
        }
    }
    sum
}

fn first_pid(procs: &str) -> Option<u32> {
    procs.lines().next()?.trim().parse().ok().filter(|p| *p > 0)
}

/// A process name as a label: printable, at most [`MAX_NAME`] bytes.
#[must_use]
pub fn name(comm: &str) -> String {
    comm.trim_end_matches('\n')
        .chars()
        .filter(|c| !c.is_control())
        .scan(0usize, |len, c| {
            *len += c.len_utf8();
            (*len <= MAX_NAME).then_some(c)
        })
        .collect()
}
