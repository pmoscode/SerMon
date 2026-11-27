//! The local veto of the modules (ADR 0039): what `agent.toml` lets them reach. The server
//! chooses what a module connects to; the host bounds where – by default loopback only, a few
//! ports, no names but `localhost`. A server can't make the agent a probe in its host's network,
//! and what the agent tells it about a refusal says no more than the server asked for: no
//! address a name resolves to, no path a link leads to (M5-33).

use std::fmt;
use std::fs::File;
use std::net::{IpAddr, SocketAddr};
use std::os::unix::fs::{MetadataExt, OpenOptionsExt, PermissionsExt};
use std::path::{Component, Path, PathBuf};

/// Paths no module reads, whatever the prefixes say (as for the log globs, ADR 0030); the
/// directory of the root helper's socket too. The agent adds its state directory and the
/// directory of its configuration file ([`Veto::block`]).
const BLOCKED: [&str; 6] = [
    "/etc/sermon",
    "/proc",
    "/sys",
    "/dev",
    "/var/lib/sermon-agent",
    "/run/sermon",
];

/// Sockets of container runtimes and VM managers by name: whoever talks to them is root (O4).
const RUNTIME_SOCKETS: [&str; 14] = [
    "docker.sock",
    "docker.raw.sock",
    "podman.sock",
    "containerd.sock",
    "containerd.sock.ttrpc",
    "crio.sock",
    "cri-dockerd.sock",
    "dockershim.sock",
    "buildkitd.sock",
    "libvirt-sock",
    "libvirt-admin-sock",
    "virtqemud-sock",
    "virtqemud-admin-sock",
    "virtproxyd-sock",
];

/// … and by a directory on the way (shim sockets of containerd, LXD, Incus, rootful Podman).
const RUNTIME_DIRS: [&str; 8] = [
    "docker",
    "containerd",
    "podman",
    "crio",
    "buildkit",
    "libvirt",
    "lxd",
    "incus",
];

/// The ports of the services the modules are made for: web servers, the databases, mail over
/// TLS, LDAPS, exporters (`module_ports`).
const DEFAULT_PORTS: [Ports; 12] = [
    Ports::one(80),
    Ports::one(443),
    Ports::one(465),
    Ports::one(636),
    Ports::one(993),
    Ports::one(995),
    Ports::one(3306),
    Ports::one(5432),
    Ports::one(6379),
    Ports::one(8080),
    Ports::one(8443),
    Ports::range(9090, 9999),
];

/// A network, e.g. `127.0.0.0/8`.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Net {
    addr: IpAddr,
    bits: u8,
}

impl Net {
    /// Parses `address/bits` (or an address alone, a single host).
    ///
    /// # Errors
    ///
    /// If `s` is no network.
    pub fn parse(s: &str) -> Result<Self, String> {
        let (addr, bits) = s.split_once('/').map_or((s, None), |(a, b)| (a, Some(b)));
        let addr: IpAddr = addr.parse().map_err(|_| format!("{s}: not an address"))?;
        let max = if addr.is_ipv4() { 32 } else { 128 };
        let bits = match bits {
            None => max,
            Some(b) => b
                .parse()
                .ok()
                .filter(|b| *b <= max)
                .ok_or_else(|| format!("{s}: the prefix is 0 to {max}"))?,
        };
        Ok(Self { addr, bits })
    }

    /// Whether `ip` lies in the network; an IPv4 address mapped into IPv6 counts as IPv4.
    #[must_use]
    pub fn contains(&self, ip: IpAddr) -> bool {
        let ip = match ip {
            IpAddr::V6(v6) => v6.to_ipv4_mapped().map_or(ip, IpAddr::V4),
            v4 @ IpAddr::V4(_) => v4,
        };
        match (self.addr, ip) {
            (IpAddr::V4(net), IpAddr::V4(ip)) => prefix_eq(&net.octets(), &ip.octets(), self.bits),
            (IpAddr::V6(net), IpAddr::V6(ip)) => prefix_eq(&net.octets(), &ip.octets(), self.bits),
            _ => false,
        }
    }
}

fn prefix_eq(a: &[u8], b: &[u8], bits: u8) -> bool {
    let bits = usize::from(bits);
    let (whole, rest) = (bits / 8, bits % 8);
    if a[..whole] != b[..whole] {
        return false;
    }
    rest == 0 || {
        let mask = 0xffu8 << (8 - rest);
        a[whole] & mask == b[whole] & mask
    }
}

impl fmt::Display for Net {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}/{}", self.addr, self.bits)
    }
}

/// Ports, e.g. `443` or `9090-9999`.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Ports {
    first: u16,
    last: u16,
}

impl Ports {
    const fn one(port: u16) -> Self {
        Self {
            first: port,
            last: port,
        }
    }

    const fn range(first: u16, last: u16) -> Self {
        Self { first, last }
    }

    /// Every port (`1-65535`).
    #[must_use]
    pub const fn all() -> Self {
        Self::range(1, u16::MAX)
    }

    /// Parses `port` or `first-last`.
    ///
    /// # Errors
    ///
    /// If `s` is no port or range of ports 1 to 65535.
    pub fn parse(s: &str) -> Result<Self, String> {
        let port = |p: &str| {
            p.trim()
                .parse::<u16>()
                .ok()
                .filter(|p| *p > 0)
                .ok_or_else(|| format!("{s}: ports are 1 to 65535"))
        };
        let (first, last) = if let Some((a, b)) = s.split_once('-') {
            (port(a)?, port(b)?)
        } else {
            let p = port(s)?;
            (p, p)
        };
        if first > last {
            return Err(format!("{s}: the first port is the lower"));
        }
        Ok(Self { first, last })
    }

    /// Whether `port` is one of them.
    #[must_use]
    pub fn contains(&self, port: u16) -> bool {
        (self.first..=self.last).contains(&port)
    }
}

/// Checks a pattern of `module_hosts`: a name, `*.` and a name (the names below it), or `*`.
///
/// # Errors
///
/// If `s` is none of these.
pub fn host_pattern(s: &str) -> Result<String, String> {
    let name = s.strip_prefix("*.").unwrap_or(s);
    let ok = s == "*"
        || (!name.is_empty()
            && name.len() <= 253
            && name.split('.').all(|label| {
                !label.is_empty()
                    && label.len() <= 63
                    && label
                        .bytes()
                        .all(|b| b.is_ascii_alphanumeric() || b == b'-')
            }));
    if ok {
        Ok(s.to_ascii_lowercase())
    } else {
        Err(format!("{s}: a host name, *.name or *"))
    }
}

/// The socket checks of the tests: path and thread ([`crate::checked_off_executor`]).
#[cfg(test)]
pub(crate) static SOCKET_CHECKS: std::sync::Mutex<Vec<(PathBuf, std::thread::ThreadId)>> =
    std::sync::Mutex::new(Vec::new());

/// What the host allows the modules.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Veto {
    /// `allow_modules`: false, and no module runs.
    pub allow: bool,
    /// `module_targets`: networks modules connect to.
    pub targets: Vec<Net>,
    /// `module_hosts`: names modules may resolve (patterns of [`host_pattern`]); any other
    /// name is refused before a DNS query goes out.
    pub hosts: Vec<String>,
    /// `module_ports`: ports modules may connect to over TCP.
    pub ports: Vec<Ports>,
    /// `module_sockets`: prefixes of Unix sockets.
    pub sockets: Vec<PathBuf>,
    /// `module_file_prefixes`: prefixes of files modules read.
    pub file_prefixes: Vec<PathBuf>,
    /// `allow_docker_socket`: the sockets of container runtimes and VM managers are
    /// root-equivalent (O4).
    pub docker_socket: bool,
    /// Paths no module reads or connects to, whatever the prefixes say.
    pub blocked: Vec<PathBuf>,
}

impl Default for Veto {
    /// The defaults of `agent.toml`: loopback and `localhost`, the ports of the modules'
    /// services, sockets below `/run`, certificate directories.
    fn default() -> Self {
        Self {
            allow: true,
            targets: vec![
                Net {
                    addr: IpAddr::from([127, 0, 0, 0]),
                    bits: 8,
                },
                Net {
                    addr: IpAddr::from([0u16, 0, 0, 0, 0, 0, 0, 1]),
                    bits: 128,
                },
            ],
            hosts: vec!["localhost".into()],
            ports: DEFAULT_PORTS.to_vec(),
            sockets: vec!["/run".into(), "/var/run".into()],
            file_prefixes: vec![
                "/etc/ssl".into(),
                "/etc/pki".into(),
                "/etc/letsencrypt".into(),
            ],
            docker_socket: false,
            blocked: BLOCKED.iter().map(PathBuf::from).collect(),
        }
    }
}

/// Why the veto refuses.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
#[error("{0}")]
pub struct Refused(pub String);

/// Why [`Veto::open`] gave no file.
#[derive(Debug, thiserror::Error)]
pub enum OpenError {
    /// The veto refuses the path, or the file opened isn't the one it allowed.
    #[error(transparent)]
    Vetoed(#[from] Refused),
    /// The file can't be opened, or it isn't a regular file.
    #[error(transparent)]
    Io(#[from] std::io::Error),
}

impl Veto {
    /// Every port: the tests' servers listen on any.
    #[must_use]
    pub fn any_port(mut self) -> Self {
        self.ports = vec![Ports::all()];
        self
    }

    /// Blocks `path` (and where it leads) for the modules, e.g. the agent's state directory.
    pub fn block(&mut self, path: &Path) {
        if !self.blocked.iter().any(|b| b == path) {
            self.blocked.push(path.to_path_buf());
        }
    }

    /// Checks an address the module connects to.
    ///
    /// # Errors
    ///
    /// [`Refused`] if no network of `module_targets` holds it.
    pub fn address(&self, ip: IpAddr) -> Result<(), Refused> {
        if self.targets.iter().any(|n| n.contains(ip)) {
            Ok(())
        } else {
            Err(Refused(format!(
                "{ip} is not in module_targets of agent.toml"
            )))
        }
    }

    /// Whether `module_hosts` lets modules resolve `name`.
    fn name_allowed(&self, name: &str) -> bool {
        let name = name.strip_suffix('.').unwrap_or(name).to_ascii_lowercase();
        self.hosts.iter().any(|pattern| {
            pattern == "*"
                || *pattern == name
                || pattern
                    .strip_prefix('*')
                    .is_some_and(|suffix| suffix.starts_with('.') && name.ends_with(suffix))
        })
    }

    /// Checks `host:port` and resolves it: a port outside `module_ports` and a name outside
    /// `module_hosts` are refused before any DNS query; every address of a name must lie in
    /// `module_targets` (none that points partly elsewhere). Connect to the addresses returned,
    /// not to the name again – it could resolve differently the second time. A refusal names
    /// no address a name resolved to and doesn't tell a missing name from a refused one: the
    /// server learns nothing about the host's network it didn't send itself.
    ///
    /// # Errors
    ///
    /// [`Refused`] if the port, the name or an address isn't allowed, or the name doesn't
    /// resolve.
    pub async fn resolve(&self, host: &str, port: u16) -> Result<Vec<SocketAddr>, Refused> {
        if !self.ports.iter().any(|p| p.contains(port)) {
            return Err(Refused(format!(
                "port {port} is not in module_ports of agent.toml"
            )));
        }
        if let Ok(ip) = host.parse::<IpAddr>() {
            self.address(ip)?;
            return Ok(vec![SocketAddr::new(ip, port)]);
        }
        let refused = || {
            Refused(format!(
                "{host}: not allowed by module_hosts and module_targets of agent.toml"
            ))
        };
        if !self.name_allowed(host) {
            return Err(refused());
        }
        let name = host.to_owned();
        let found = bounded(&LOOKUPS, move || {
            std::net::ToSocketAddrs::to_socket_addrs(&(name.as_str(), port))
                .map_or_else(|_| Vec::new(), Iterator::collect)
        })
        .await;
        if found.is_empty() || found.iter().any(|a| self.address(a.ip()).is_err()) {
            return Err(refused());
        }
        Ok(found)
    }

    /// Checks a Unix socket; one of a container runtime needs `allow_docker_socket`. The path
    /// as configured lies below `module_sockets`, and so does where it leads; no directory on
    /// the way from the prefix is writable by everyone (`/run/lock`, `/tmp`): there any local
    /// user could put a link in its place. Check again before every connection.
    ///
    /// # Errors
    ///
    /// [`Refused`] if the socket lies outside `module_sockets` or in a blocked place.
    pub fn socket(&self, path: &Path) -> Result<PathBuf, Refused> {
        #[cfg(test)]
        SOCKET_CHECKS
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner)
            .push((path.to_path_buf(), std::thread::current().id()));
        plain(path)?;
        let shown = path.display();
        let runtime = |p: &Path| {
            p.file_name()
                .and_then(|n| n.to_str())
                .is_some_and(|n| RUNTIME_SOCKETS.contains(&n))
                || p.components().any(|c| {
                    c.as_os_str()
                        .to_str()
                        .is_some_and(|c| RUNTIME_DIRS.contains(&c))
                })
        };
        let container = || {
            Refused(format!(
                "{shown}: a container runtime's socket is root-equivalent: allow_docker_socket in agent.toml"
            ))
        };
        if runtime(path) && !self.docker_socket {
            return Err(container());
        }
        self.inside(path, &self.sockets, "module_sockets", "used")?;
        let resolved = resolve(path)?;
        if runtime(&resolved) && !self.docker_socket {
            return Err(container());
        }
        let prefix = self.leads_inside(path, &resolved, &self.sockets, "module_sockets")?;
        let mut dir = resolved.parent();
        while let Some(d) = dir.filter(|d| d.starts_with(&prefix)) {
            if std::fs::metadata(d).is_ok_and(|m| m.permissions().mode() & 0o002 != 0) {
                return Err(Refused(format!(
                    "{shown}: in a directory everyone may write to"
                )));
            }
            dir = d.parent();
        }
        Ok(resolved)
    }

    /// Checks a file a module reads; the path is resolved (symlinks) and checked again. Read it
    /// with [`Veto::open`], which checks the file it opened.
    ///
    /// # Errors
    ///
    /// [`Refused`] if the file lies outside `module_file_prefixes` or in a blocked place.
    pub fn file(&self, path: &Path) -> Result<PathBuf, Refused> {
        plain(path)?;
        self.inside(path, &self.file_prefixes, "module_file_prefixes", "read")?;
        let resolved = resolve(path)?;
        self.leads_inside(path, &resolved, &self.file_prefixes, "module_file_prefixes")?;
        Ok(resolved)
    }

    /// Opens a file the veto allows, without blocking (FIFOs) and only a regular file, and
    /// checks the file opened: a link swapped in after the check is refused (M5-33).
    ///
    /// # Errors
    ///
    /// [`OpenError::Vetoed`] outside the veto, [`OpenError::Io`] if it can't be read.
    pub fn open(&self, path: &Path) -> Result<File, OpenError> {
        self.open_after_check(path, || {})
    }

    /// [`Veto::open`] with `between` run between the check and the open (the tests' race).
    fn open_after_check(&self, path: &Path, between: impl FnOnce()) -> Result<File, OpenError> {
        let resolved = self.file(path)?;
        between();
        let flags = rustix::fs::OFlags::NONBLOCK
            | rustix::fs::OFlags::NOCTTY
            | rustix::fs::OFlags::NOFOLLOW;
        let file = std::fs::OpenOptions::new()
            .read(true)
            .custom_flags(i32::try_from(flags.bits()).unwrap_or(0))
            .open(&resolved)?;
        if !file.metadata()?.is_file() {
            return Err(std::io::Error::new(
                std::io::ErrorKind::InvalidInput,
                format!("{}: not a regular file", path.display()),
            )
            .into());
        }
        self.opened(path, &file, &resolved)?;
        Ok(file)
    }

    /// Whether `file`, opened at `resolved`, is still inside the veto: where the descriptor
    /// leads, or – without a way to ask – the same file as at `resolved` now.
    fn opened(&self, path: &Path, file: &File, resolved: &Path) -> Result<(), Refused> {
        if let Some(real) = fd_path(file) {
            return self
                .leads_inside(path, &real, &self.file_prefixes, "module_file_prefixes")
                .map(|_| ());
        }
        let same = match (file.metadata(), std::fs::metadata(resolved)) {
            (Ok(a), Ok(b)) => a.dev() == b.dev() && a.ino() == b.ino(),
            _ => false,
        };
        if same {
            Ok(())
        } else {
            Err(Refused(format!(
                "{}: changed after its check",
                path.display()
            )))
        }
    }

    /// The blocked paths, as written and where they lead.
    fn is_blocked(&self, path: &Path) -> bool {
        self.blocked.iter().any(|b| {
            path.starts_with(b) || std::fs::canonicalize(b).is_ok_and(|c| path.starts_with(c))
        })
    }

    /// The path as configured: not blocked, below a prefix. Touches nothing on the disk, so a
    /// refusal says nothing about what exists outside the prefixes.
    fn inside(
        &self,
        path: &Path,
        prefixes: &[PathBuf],
        key: &str,
        verb: &str,
    ) -> Result<(), Refused> {
        if self.is_blocked(path) {
            return Err(Refused(format!(
                "{}: never {verb} by modules",
                path.display()
            )));
        }
        if prefixes.iter().any(|p| path.starts_with(p)) {
            Ok(())
        } else {
            Err(Refused(format!(
                "{} is not below {key} of agent.toml",
                path.display()
            )))
        }
    }

    /// Where the path leads (`resolved`): not blocked, below a prefix (as written or where it
    /// leads), which is returned. The refusal names the path as configured, not its target.
    fn leads_inside(
        &self,
        path: &Path,
        resolved: &Path,
        prefixes: &[PathBuf],
        key: &str,
    ) -> Result<PathBuf, Refused> {
        let outside = || {
            Refused(format!(
                "{} leads outside {key} of agent.toml",
                path.display()
            ))
        };
        if self.is_blocked(resolved) {
            return Err(outside());
        }
        prefixes
            .iter()
            .flat_map(|p| [Some(p.clone()), std::fs::canonicalize(p).ok()])
            .flatten()
            .find(|p| resolved.starts_with(p))
            .ok_or_else(outside)
    }
}

/// Most name lookups at once, for all modules (V1-13).
const MAX_LOOKUPS: usize = 8;

/// The places for name lookups.
static LOOKUPS: tokio::sync::Semaphore = tokio::sync::Semaphore::const_new(MAX_LOOKUPS);

/// Runs the lookup `f` on a blocking thread once a place of `places` is free; the place stays
/// taken until `f` returns. A lookup (`getaddrinfo`) can't be cancelled: a run that times out
/// leaves it running, and a server could otherwise tie up ever more threads with names that take
/// long to resolve. Nothing found if the lookup failed.
async fn bounded<F>(places: &'static tokio::sync::Semaphore, f: F) -> Vec<SocketAddr>
where
    F: FnOnce() -> Vec<SocketAddr> + Send + 'static,
{
    let Ok(place) = places.acquire().await else {
        return Vec::new();
    };
    tokio::task::spawn_blocking(move || {
        let _place = place;
        f()
    })
    .await
    .unwrap_or_default()
}

/// An absolute path without `.` and `..`.
fn plain(path: &Path) -> Result<(), Refused> {
    if !path.is_absolute()
        || path
            .components()
            .any(|c| matches!(c, Component::ParentDir | Component::CurDir))
    {
        return Err(Refused(format!(
            "{}: an absolute path without . and ..",
            path.display()
        )));
    }
    Ok(())
}

/// `path` with its symlinks resolved; of one that doesn't exist yet (a socket, its directory)
/// the part that exists resolved and the rest appended.
///
/// # Errors
///
/// [`Refused`] for a link that leads nowhere: where it leads can't be checked.
fn resolve(path: &Path) -> Result<PathBuf, Refused> {
    let mut rest = Vec::new();
    let mut existing = path;
    loop {
        if let Ok(resolved) = std::fs::canonicalize(existing) {
            return Ok(rest.iter().rev().fold(resolved, |p, name| p.join(name)));
        }
        if std::fs::symlink_metadata(existing).is_ok() {
            return Err(Refused(format!(
                "{}: a link that leads nowhere",
                path.display()
            )));
        }
        match (existing.parent(), existing.file_name()) {
            (Some(parent), Some(name)) => {
                rest.push(name.to_owned());
                existing = parent;
            }
            _ => return Ok(path.to_path_buf()),
        }
    }
}

/// Where an open file is, as the kernel says.
#[cfg(target_os = "linux")]
fn fd_path(file: &File) -> Option<PathBuf> {
    use std::os::fd::AsRawFd;
    std::fs::read_link(format!("/proc/self/fd/{}", file.as_raw_fd())).ok()
}

/// Where an open file is, as the kernel says.
#[cfg(target_vendor = "apple")]
fn fd_path(file: &File) -> Option<PathBuf> {
    use std::os::unix::ffi::OsStrExt;
    rustix::fs::getpath(file)
        .ok()
        .map(|p| PathBuf::from(std::ffi::OsStr::from_bytes(p.as_bytes())))
}

#[cfg(not(any(target_os = "linux", target_vendor = "apple")))]
fn fd_path(_: &File) -> Option<PathBuf> {
    None
}

#[cfg(test)]
mod tests;
