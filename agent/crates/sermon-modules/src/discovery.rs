//! Discovery (ADR 0039, M5-04): what runs on the host, so the UI can suggest a module. Nothing is
//! activated; a hint names the module and why (`process:nginx`, `port:80`, `unit:nginx.service`)
//! and may carry a configuration to start the form with – never raw data of the host.
//!
//! The facts come from files the agent reads anyway: the listening sockets of `/proc/net/tcp`,
//! `tcp6` and `unix`, the process names (`/proc/<pid>/comm`) and the running services (from the
//! agent, which asks systemd). Ports can't be tied to a process without privileges, so a hint
//! lists its reasons one by one.

use std::collections::BTreeSet;
use std::fmt;
use std::net::{IpAddr, Ipv4Addr, Ipv6Addr};
use std::panic::{AssertUnwindSafe, catch_unwind};
use std::sync::Arc;

use sermon_core::ProcRoot;

use crate::Module;

/// Most hints sent (the protocol's bound).
pub const MAX_HINTS: usize = 32;
/// Most reasons of a hint (the protocol's bound).
pub const MAX_REASONS: usize = 8;
/// Longest suggested configuration (JSON, the protocol's bound).
pub const MAX_SUGGESTED: usize = 1024;
/// Most entries of each kind taken from the host: more listeners, processes or units than this
/// are no host to guess on.
pub const MAX_FACTS: usize = 1024;
/// Most processes looked at.
pub const MAX_PIDS: usize = 32_768;
/// Longest value of a reason (the protocol's bound).
const MAX_REASON_VALUE: usize = 128;
/// Longest path of a Unix socket (`sun_path`).
const MAX_SOCKET_PATH: usize = 108;
/// `__SO_ACCEPTCON` in the flags of `/proc/net/unix`: the socket listens.
const ACCEPTCON: u32 = 0x0001_0000;
/// `TCP_LISTEN` in the state column of `/proc/net/tcp`.
const TCP_LISTEN: &str = "0A";

/// A listening TCP socket.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub struct Listener {
    /// The address it listens on (`0.0.0.0` or `::` for all).
    pub ip: IpAddr,
    /// Its port.
    pub port: u16,
}

impl Listener {
    /// The address to reach it: loopback for one on all addresses, else its own.
    #[must_use]
    pub fn reach(&self) -> IpAddr {
        match self.ip {
            IpAddr::V4(v4) if v4.is_unspecified() => IpAddr::V4(Ipv4Addr::LOCALHOST),
            IpAddr::V6(v6) if v6.is_unspecified() => IpAddr::V6(Ipv6Addr::LOCALHOST),
            ip => ip,
        }
    }
}

/// What the agent saw on the host.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct HostFacts {
    /// Listening TCP sockets (IPv4 and IPv6).
    pub listeners: BTreeSet<Listener>,
    /// Paths of listening Unix sockets (abstract ones left out).
    pub sockets: BTreeSet<String>,
    /// Process names (`comm`, at most 15 bytes).
    pub processes: BTreeSet<String>,
    /// Running units, e.g. `nginx.service`.
    pub units: BTreeSet<String>,
}

impl HostFacts {
    /// Reads the facts below `root`; `units` are the running units (none without systemd).
    /// Missing files leave their part empty (no IPv6, a hardened `/proc`).
    #[must_use]
    pub fn read(root: &ProcRoot, units: impl IntoIterator<Item = String>) -> Self {
        let mut listeners = BTreeSet::new();
        for file in ["net/tcp", "net/tcp6"] {
            if let Ok(text) = root.read_proc(file) {
                listeners.extend(parse_tcp(&text));
            }
        }
        let sockets = root
            .read_proc("net/unix")
            .map_or_else(|_| Vec::new(), |text| parse_unix(&text));
        Self {
            listeners: listeners.into_iter().take(MAX_FACTS).collect(),
            sockets: sockets.into_iter().take(MAX_FACTS).collect(),
            processes: processes(root),
            units: units.into_iter().take(MAX_FACTS).collect(),
        }
    }

    /// The first listener on `port`.
    #[must_use]
    pub fn listener(&self, port: u16) -> Option<Listener> {
        self.listeners.iter().copied().find(|l| l.port == port)
    }

    /// Whether a process named `name` runs.
    #[must_use]
    pub fn runs(&self, name: &str) -> bool {
        self.processes.contains(name)
    }

    /// Whether the unit `name` runs.
    #[must_use]
    pub fn has_unit(&self, name: &str) -> bool {
        self.units.contains(name)
    }

    /// Whether a Unix socket listens at `path`.
    #[must_use]
    pub fn has_socket(&self, path: &str) -> bool {
        self.sockets.contains(path)
    }
}

/// The process names of `/proc/<pid>/comm`, at most [`MAX_FACTS`] of the first [`MAX_PIDS`]
/// processes; one that ends while reading is skipped.
fn processes(root: &ProcRoot) -> BTreeSet<String> {
    let mut names = BTreeSet::new();
    let Ok(entries) = root.list_proc("") else {
        return names;
    };
    let pids = entries
        .iter()
        .filter(|e| !e.is_empty() && e.bytes().all(|b| b.is_ascii_digit()))
        .take(MAX_PIDS);
    for pid in pids {
        if names.len() >= MAX_FACTS {
            break;
        }
        if let Ok(comm) = root.read_proc(&format!("{pid}/comm")) {
            let comm = comm.trim_end_matches('\n');
            if !comm.is_empty() && comm.len() <= 64 {
                names.insert(comm.to_owned());
            }
        }
    }
    names
}

/// The listening sockets of `/proc/net/tcp` or `tcp6`; other lines are skipped. The addresses
/// are words in the host's byte order, little-endian on the agent's targets (amd64, arm64).
#[must_use]
pub fn parse_tcp(text: &str) -> Vec<Listener> {
    let mut out = Vec::new();
    for line in text.lines().skip(1) {
        let mut fields = line.split_whitespace();
        let (Some(_), Some(local), Some(_), Some(state)) =
            (fields.next(), fields.next(), fields.next(), fields.next())
        else {
            continue;
        };
        if state != TCP_LISTEN {
            continue;
        }
        if let Some(listener) = parse_endpoint(local)
            && !out.contains(&listener)
        {
            out.push(listener);
            if out.len() >= MAX_FACTS {
                break;
            }
        }
    }
    out
}

/// `0100007F:0050` → 127.0.0.1:80; 32 hex digits for IPv6.
fn parse_endpoint(text: &str) -> Option<Listener> {
    let (addr, port) = text.split_once(':')?;
    if port.len() != 4 {
        return None;
    }
    let port = u16::from_str_radix(port, 16).ok()?;
    let ip = match addr.len() {
        8 => IpAddr::V4(Ipv4Addr::from(word(addr)?)),
        32 => {
            let mut octets = [0u8; 16];
            for (i, chunk) in octets.as_chunks_mut::<4>().0.iter_mut().enumerate() {
                *chunk = word(addr.get(i * 8..i * 8 + 8)?)?;
            }
            IpAddr::V6(Ipv6Addr::from(octets))
        }
        _ => return None,
    };
    Some(Listener { ip, port })
}

/// Eight hex digits of a word printed in little-endian byte order.
fn word(hex: &str) -> Option<[u8; 4]> {
    if hex.len() != 8 || !hex.bytes().all(|b| b.is_ascii_hexdigit()) {
        return None;
    }
    u32::from_str_radix(hex, 16).ok().map(u32::to_le_bytes)
}

/// The paths of the listening sockets of `/proc/net/unix` (flag `__SO_ACCEPTCON`, state
/// unconnected); abstract (`@…`) and nameless ones are skipped.
#[must_use]
pub fn parse_unix(text: &str) -> Vec<String> {
    let mut out = Vec::new();
    for line in text.lines().skip(1) {
        let mut rest = line.trim_start();
        let mut fields = [""; 7];
        for field in &mut fields {
            let end = rest.find(char::is_whitespace).unwrap_or(rest.len());
            *field = &rest[..end];
            rest = rest[end..].trim_start();
        }
        let [_, _, _, flags, _, state, _] = fields;
        let listening = u32::from_str_radix(flags, 16).is_ok_and(|f| f & ACCEPTCON != 0);
        let path = rest.trim_end();
        if listening
            && state == "01"
            && path.starts_with('/')
            && path.len() <= MAX_SOCKET_PATH
            && !path.chars().any(char::is_control)
            && !out.iter().any(|p| p == path)
        {
            out.push(path.to_owned());
            if out.len() >= MAX_FACTS {
                break;
            }
        }
    }
    out
}

/// Why a module is suggested.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Reason {
    /// A process of that name runs.
    Process(&'static str),
    /// Something listens on that port.
    Port(u16),
    /// The unit runs.
    Unit(&'static str),
}

impl fmt::Display for Reason {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Process(name) => write!(f, "process:{name}"),
            Self::Port(port) => write!(f, "port:{port}"),
            Self::Unit(name) => write!(f, "unit:{name}"),
        }
    }
}

/// What a module found on the host ([`Module::discover`]).
#[derive(Debug, Clone, PartialEq)]
pub struct Discovered {
    /// Why, one reason each.
    pub reasons: Vec<Reason>,
    /// A configuration to start the form with (a JSON object), e.g. the port found.
    pub suggested: Option<serde_json::Value>,
}

impl Discovered {
    /// The usual evidence of a service: one of `processes` or `units` runs (a port alone is any
    /// service), with the `ports` that listen as further reasons. `None` without a process or
    /// unit.
    #[must_use]
    pub fn evidence(
        facts: &HostFacts,
        processes: &[&'static str],
        units: &[&'static str],
        ports: &[u16],
    ) -> Option<Self> {
        let mut reasons: Vec<Reason> = processes
            .iter()
            .filter(|p| facts.runs(p))
            .map(|p| Reason::Process(p))
            .chain(
                units
                    .iter()
                    .filter(|u| facts.has_unit(u))
                    .map(|u| Reason::Unit(u)),
            )
            .collect();
        if reasons.is_empty() {
            return None;
        }
        reasons.extend(
            ports
                .iter()
                .filter(|p| facts.listener(**p).is_some())
                .map(|p| Reason::Port(*p)),
        );
        Some(Self {
            reasons,
            suggested: None,
        })
    }
}

/// A hint as it goes to the server.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Hint {
    /// The module.
    pub module: String,
    /// Why, as `kind:value`.
    pub reasons: Vec<String>,
    /// The suggested configuration (JSON object) or empty.
    pub suggested_config_json: String,
}

/// The hints of `modules` for `facts`, sorted by module, in the protocol's bounds: at most
/// [`MAX_HINTS`], [`MAX_REASONS`] reasons each, reasons outside the protocol's character set
/// dropped, a suggestion that is no object or too long left out. A module whose discovery panics
/// gives no hint; the others still do.
#[must_use]
pub fn hints(modules: &[Arc<dyn Module>], facts: &HostFacts) -> Vec<Hint> {
    let mut out: Vec<Hint> = Vec::new();
    for module in modules {
        let Ok(Some(found)) = catch_unwind(AssertUnwindSafe(|| module.discover(facts))) else {
            continue;
        };
        let mut reasons: Vec<String> = Vec::new();
        for reason in found.reasons.iter().map(ToString::to_string) {
            if valid_reason(&reason) && !reasons.contains(&reason) && reasons.len() < MAX_REASONS {
                reasons.push(reason);
            }
        }
        if reasons.is_empty() || out.iter().any(|h| h.module == module.id()) {
            continue;
        }
        let suggested_config_json = found
            .suggested
            .filter(serde_json::Value::is_object)
            .map(|v| v.to_string())
            .filter(|json| json.len() <= MAX_SUGGESTED)
            .unwrap_or_default();
        out.push(Hint {
            module: module.id().to_owned(),
            reasons,
            suggested_config_json,
        });
    }
    out.sort_by(|a, b| a.module.cmp(&b.module));
    out.truncate(MAX_HINTS);
    out
}

/// `kind:value` as the protocol takes it: `process`, `port` or `unit` and 1 to 128 of
/// `[A-Za-z0-9@._:-]`.
fn valid_reason(reason: &str) -> bool {
    let Some((kind, value)) = reason.split_once(':') else {
        return false;
    };
    matches!(kind, "process" | "port" | "unit")
        && (1..=MAX_REASON_VALUE).contains(&value.len())
        && value
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b"@._:-".contains(&b))
}

#[cfg(test)]
mod tests;
