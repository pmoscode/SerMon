//! The local veto of the modules (ADR 0039): what `agent.toml` lets them reach. The server
//! chooses what a module connects to; the host bounds where – by default loopback only. A
//! server can't make the agent a probe in its host's network.

use std::fmt;
use std::net::{IpAddr, SocketAddr};
use std::path::{Component, Path, PathBuf};

/// Paths no module reads, whatever the prefixes say (as for the log globs, ADR 0030); the
/// directory of the root helper's socket too.
const BLOCKED: [&str; 6] = [
    "/etc/sermon",
    "/proc",
    "/sys",
    "/dev",
    "/var/lib/sermon-agent",
    "/run/sermon",
];

/// Sockets of container runtimes: whoever talks to them is root (O4).
const CONTAINER_SOCKETS: [&str; 3] = ["docker.sock", "podman.sock", "containerd.sock"];

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

/// What the host allows the modules.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Veto {
    /// `allow_modules`: false, and no module runs.
    pub allow: bool,
    /// `module_targets`: networks modules connect to.
    pub targets: Vec<Net>,
    /// `module_sockets`: prefixes of Unix sockets.
    pub sockets: Vec<PathBuf>,
    /// `module_file_prefixes`: prefixes of files modules read.
    pub file_prefixes: Vec<PathBuf>,
    /// `allow_docker_socket`: the Docker socket is root-equivalent (O4).
    pub docker_socket: bool,
}

impl Default for Veto {
    /// The defaults of `agent.toml`: loopback, sockets below `/run`, certificate directories.
    fn default() -> Self {
        Self {
            allow: true,
            targets: vec![
                Net::parse("127.0.0.0/8").unwrap_or(Net {
                    addr: IpAddr::from([127, 0, 0, 0]),
                    bits: 8,
                }),
                Net {
                    addr: IpAddr::from([0u16, 0, 0, 0, 0, 0, 0, 1]),
                    bits: 128,
                },
            ],
            sockets: vec!["/run".into(), "/var/run".into()],
            file_prefixes: vec![
                "/etc/ssl".into(),
                "/etc/pki".into(),
                "/etc/letsencrypt".into(),
            ],
            docker_socket: false,
        }
    }
}

/// Why the veto refuses.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
#[error("{0}")]
pub struct Refused(pub String);

impl Veto {
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

    /// Resolves `host:port` and checks every address: one outside the targets refuses all (no
    /// name that points partly elsewhere). Connect to the addresses returned, not to the name
    /// again – it could resolve differently the second time.
    ///
    /// # Errors
    ///
    /// [`Refused`] if the name doesn't resolve or an address isn't allowed.
    pub async fn resolve(&self, host: &str, port: u16) -> Result<Vec<SocketAddr>, Refused> {
        let found: Vec<SocketAddr> = tokio::net::lookup_host((host, port))
            .await
            .map_err(|e| Refused(format!("{host}: cannot resolve: {e}")))?
            .collect();
        if found.is_empty() {
            return Err(Refused(format!("{host}: no address")));
        }
        for a in &found {
            self.address(a.ip())?;
        }
        Ok(found)
    }

    /// Checks a Unix socket; one of a container runtime needs `allow_docker_socket`.
    ///
    /// # Errors
    ///
    /// [`Refused`] if the socket lies outside `module_sockets`.
    pub fn socket(&self, path: &Path) -> Result<PathBuf, Refused> {
        let resolved = resolve(path)?;
        let container = resolved
            .file_name()
            .and_then(|n| n.to_str())
            .is_some_and(|n| CONTAINER_SOCKETS.contains(&n));
        if container && !self.docker_socket {
            return Err(Refused(
                "a container runtime's socket is root-equivalent: allow_docker_socket in agent.toml"
                    .into(),
            ));
        }
        within(&resolved, &self.sockets, "module_sockets")?;
        Ok(resolved)
    }

    /// Checks a file a module reads; the path is resolved (symlinks) and checked again.
    ///
    /// # Errors
    ///
    /// [`Refused`] if the file lies outside `module_file_prefixes` or in a blocked place.
    pub fn file(&self, path: &Path) -> Result<PathBuf, Refused> {
        let resolved = resolve(path)?;
        within(&resolved, &self.file_prefixes, "module_file_prefixes")?;
        Ok(resolved)
    }
}

/// `path` with its symlinks resolved; one that doesn't exist yet (a socket) with its directory
/// resolved.
fn resolve(path: &Path) -> Result<PathBuf, Refused> {
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
    if let Ok(resolved) = std::fs::canonicalize(path) {
        return Ok(resolved);
    }
    let resolved = match (path.parent(), path.file_name()) {
        (Some(dir), Some(name)) => std::fs::canonicalize(dir).map(|d| d.join(name)).ok(),
        _ => None,
    };
    Ok(resolved.unwrap_or_else(|| path.to_path_buf()))
}

fn within(path: &Path, prefixes: &[PathBuf], key: &str) -> Result<(), Refused> {
    if BLOCKED.iter().any(|b| path.starts_with(b)) {
        return Err(Refused(format!(
            "{}: never read by modules",
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn networks_hold_their_addresses() {
        let lo = Net::parse("127.0.0.0/8").unwrap();
        assert!(lo.contains("127.1.2.3".parse().unwrap()));
        assert!(!lo.contains("128.0.0.1".parse().unwrap()));
        assert!(
            lo.contains("::ffff:127.0.0.1".parse().unwrap()),
            "mapped IPv4"
        );
        let lan = Net::parse("192.0.2.0/25").unwrap();
        assert!(lan.contains("192.0.2.127".parse().unwrap()));
        assert!(!lan.contains("192.0.2.128".parse().unwrap()));
        let v6 = Net::parse("2001:db8::/32").unwrap();
        assert!(v6.contains("2001:db8:1::5".parse().unwrap()));
        assert!(!v6.contains("2001:db9::1".parse().unwrap()));
        assert!(Net::parse("::1").unwrap().contains("::1".parse().unwrap()));
        assert!(
            Net::parse("0.0.0.0/0")
                .unwrap()
                .contains("192.0.2.1".parse().unwrap())
        );
        for bad in ["127.0.0.0/33", "::/129", "host", "10.0.0.0/x"] {
            assert!(Net::parse(bad).is_err(), "{bad}");
        }
    }

    // ADR 0039: by default loopback only – no metadata service, no LAN.
    #[test]
    fn the_default_allows_loopback_only() {
        let v = Veto::default();
        assert!(v.address("127.0.0.1".parse().unwrap()).is_ok());
        assert!(v.address("::1".parse().unwrap()).is_ok());
        for other in [
            "169.254.169.254",
            "192.0.2.10",
            "0.0.0.0",
            "::",
            "2001:db8::1",
        ] {
            assert!(v.address(other.parse().unwrap()).is_err(), "{other}");
        }
    }

    #[tokio::test]
    async fn a_name_is_allowed_only_with_every_address() {
        let v = Veto::default();
        let found = v.resolve("localhost", 80).await.unwrap();
        assert!(found.iter().all(|a| a.ip().is_loopback()));
        assert!(v.resolve("192.0.2.10", 80).await.is_err());
    }

    #[test]
    fn files_and_sockets_only_below_their_prefixes() {
        let dir = tempfile::tempdir().unwrap();
        let allowed = dir.path().join("certs");
        std::fs::create_dir(&allowed).unwrap();
        std::fs::write(allowed.join("a.pem"), "").unwrap();
        std::fs::write(dir.path().join("secret.key"), "").unwrap();
        std::os::unix::fs::symlink(dir.path().join("secret.key"), allowed.join("link.pem"))
            .unwrap();
        let v = Veto {
            file_prefixes: vec![std::fs::canonicalize(&allowed).unwrap()],
            sockets: vec![std::fs::canonicalize(dir.path()).unwrap()],
            ..Veto::default()
        };
        assert!(v.file(&allowed.join("a.pem")).is_ok());
        assert!(v.file(&allowed.join("link.pem")).is_err(), "a link out");
        assert!(v.file(&allowed.join("../secret.key")).is_err());
        assert!(v.file(Path::new("relative.pem")).is_err());
        assert!(v.file(Path::new("/etc/sermon/agent.toml")).is_err());
        let sock = dir.path().join("redis.sock");
        assert!(v.socket(&sock).is_ok());
        assert!(v.socket(&dir.path().join("docker.sock")).is_err());
        assert!(v.socket(&dir.path().join("podman.sock")).is_err());
        std::fs::write(dir.path().join("docker.sock"), "").unwrap();
        std::os::unix::fs::symlink(dir.path().join("docker.sock"), dir.path().join("app.sock"))
            .unwrap();
        assert!(
            v.socket(&dir.path().join("app.sock")).is_err(),
            "a link to it"
        );
        let run = Veto {
            sockets: vec!["/run".into()],
            ..Veto::default()
        };
        assert!(
            run.socket(Path::new("/run/sermon/helper.sock")).is_err(),
            "the root helper"
        );
        let docker = Veto {
            docker_socket: true,
            ..v.clone()
        };
        assert!(docker.socket(&dir.path().join("docker.sock")).is_ok());
    }

    #[test]
    fn blocked_paths_stay_blocked() {
        let v = Veto {
            file_prefixes: vec!["/".into()],
            ..Veto::default()
        };
        for p in [
            "/proc/1/environ",
            "/etc/sermon/agent.toml",
            "/var/lib/sermon-agent/identity/key.pem",
        ] {
            assert!(v.file(Path::new(p)).is_err(), "{p}");
        }
        assert!(v.file(Path::new("/etc/hostname")).is_ok());
    }
}
