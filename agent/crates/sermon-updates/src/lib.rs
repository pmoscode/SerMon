//! Parsers for what apt, dnf and apk print, as the root helper returns it (ADR 0035): parsed in the
//! agent, without privileges. The text comes from the package sources – third parties – so a line
//! counts only if each of its parts is 1 to [`MAX_FIELD`] printable ASCII characters without
//! spaces; anything else is skipped, never repaired.
//!
//! Formats (fixtures of every target distribution in `agent/fixtures/updates/`):
//!
//! - apt: `apt list --upgradable` – `name/suite[,suite] candidate arch [upgradable from: installed]`;
//!   security if a suite ends in `-security`.
//! - dnf 4 and 5: `check-update` – `name.arch candidate repository` (no installed version), and
//!   `updateinfo list --security` – the NEVRA in each line marks a package as security update.
//!   `needs-restarting -r` says in words whether a reboot is needed; its exit code 1 means either
//!   "reboot" or "no such command" (Rocky without dnf-utils).
//! - apk: `apk version -l '<'` – `name-installed < candidate`; no security metadata (KONZEPT O6).

use std::collections::HashSet;

/// Longest name, version or source taken.
pub const MAX_FIELD: usize = 256;

/// One available update.
#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub struct Package {
    pub name: String,
    /// Empty where the package manager doesn't say (dnf).
    pub installed: String,
    pub candidate: String,
    /// The suite (apt) or repository (dnf); empty for apk.
    pub source: String,
    pub security: bool,
}

/// Whether the host needs a reboot to use what is installed.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Reboot {
    Required(String),
    NotRequired,
    /// The host can't tell (no `needs-restarting`).
    Unknown,
}

fn field(s: &str) -> Option<String> {
    (!s.is_empty() && s.len() <= MAX_FIELD && s.bytes().all(|b| b.is_ascii_graphic()))
        .then(|| s.to_owned())
}

/// The packages of `apt list --upgradable`.
#[must_use]
pub fn apt(list: &str) -> Vec<Package> {
    list.lines().filter_map(apt_line).collect()
}

fn apt_line(line: &str) -> Option<Package> {
    let (name, rest) = line.split_once('/')?;
    let mut parts = rest.split(' ');
    let (suites, candidate) = (parts.next()?, parts.next()?);
    let _arch = parts.next()?;
    let installed = rest.split_once("[upgradable from: ")?.1.strip_suffix(']')?;
    Some(Package {
        name: field(name)?,
        installed: field(installed)?,
        candidate: field(candidate)?,
        source: field(suites)?,
        security: suites.split(',').any(|s| s.ends_with("-security")),
    })
}

/// The packages of `dnf check-update`, marked by `dnf updateinfo list --security`.
#[must_use]
pub fn dnf(check_update: &str, security: &str) -> Vec<Package> {
    let marked: HashSet<(String, String)> = security
        .lines()
        .flat_map(str::split_whitespace)
        .filter_map(nevra)
        .collect();
    let mut out = Vec::new();
    for line in check_update.lines() {
        // dnf 4 lists replaced packages after this; they are no updates of their own.
        if line.starts_with("Obsoleting") {
            break;
        }
        let mut parts = line.split_whitespace();
        let (Some(name_arch), Some(candidate), Some(repo), None) =
            (parts.next(), parts.next(), parts.next(), parts.next())
        else {
            continue;
        };
        let Some((name, arch)) = name_arch.rsplit_once('.') else {
            continue;
        };
        let (Some(name), Some(candidate), Some(source)) =
            (field(name), field(candidate), field(repo))
        else {
            continue;
        };
        let security = marked.contains(&(name.clone(), arch.to_owned()));
        out.push(Package {
            name,
            installed: String::new(),
            candidate,
            source,
            security,
        });
    }
    out
}

/// Name and architecture of a NEVRA (`vim-data-2:9.1.083-26.el10_3.noarch`).
fn nevra(token: &str) -> Option<(String, String)> {
    const ARCHES: [&str; 8] = [
        "x86_64", "aarch64", "noarch", "i686", "ppc64le", "s390x", "armv7hl", "riscv64",
    ];
    let (rest, arch) = token.rsplit_once('.')?;
    if !ARCHES.contains(&arch) {
        return None;
    }
    let (rest, _release) = rest.rsplit_once('-')?;
    let (name, _version) = rest.rsplit_once('-')?;
    Some((field(name)?, arch.to_owned()))
}

/// Whether `dnf needs-restarting -r` asks for a reboot, by its words.
#[must_use]
pub fn dnf_reboot(output: &str) -> Reboot {
    if output.contains("Reboot is required") {
        let reasons: Vec<&str> = output
            .lines()
            .filter_map(|l| l.trim().strip_prefix("* "))
            .filter_map(|r| field(r).map(|_| r))
            .collect();
        Reboot::Required(if reasons.is_empty() {
            "core libraries or services were updated".into()
        } else {
            reasons.join(", ")
        })
    } else if output.contains("Reboot should not be necessary") {
        Reboot::NotRequired
    } else {
        Reboot::Unknown
    }
}

/// The reboot of apt: `/var/run/reboot-required` and the packages in `.pkgs`.
#[must_use]
pub fn apt_reboot(required: bool, pkgs: &str) -> Reboot {
    if !required {
        return Reboot::NotRequired;
    }
    let names: Vec<&str> = pkgs.lines().filter(|l| field(l).is_some()).collect();
    Reboot::Required(if names.is_empty() {
        "an update needs a reboot".into()
    } else {
        names.join(", ")
    })
}

/// The packages of `apk version -l '<'`.
#[must_use]
pub fn apk(version: &str) -> Vec<Package> {
    version.lines().filter_map(apk_line).collect()
}

fn apk_line(line: &str) -> Option<Package> {
    let mut parts = line.split_whitespace();
    let (Some(name_version), Some("<"), Some(candidate), None) =
        (parts.next(), parts.next(), parts.next(), parts.next())
    else {
        return None;
    };
    // name-1.2.3-r0: the version is the last two parts.
    let (rest, revision) = name_version.rsplit_once('-')?;
    let (name, version) = rest.rsplit_once('-')?;
    Some(Package {
        name: field(name)?,
        installed: field(&format!("{version}-{revision}"))?,
        candidate: field(candidate)?,
        source: String::new(),
        security: false,
    })
}

/// The reboot of Alpine: the running kernel's modules are gone once a new kernel replaced it.
/// `modules` are the directories of `/lib/modules`; without any (a container) it can't tell.
#[must_use]
pub fn apk_reboot(running: &str, modules: &[String]) -> Reboot {
    match modules {
        [] => Reboot::Unknown,
        m if m.iter().any(|d| d == running) => Reboot::NotRequired,
        m => Reboot::Required(format!(
            "kernel {running} is running, {} is installed",
            m.join(", ")
        )),
    }
}

/// At most `max` packages, security updates first, and the counts of all.
#[must_use]
pub fn bounded(mut packages: Vec<Package>, max: usize) -> (Vec<Package>, usize, usize) {
    let total = packages.len();
    let security = packages.iter().filter(|p| p.security).count();
    packages.sort_by(|a, b| {
        b.security
            .cmp(&a.security)
            .then_with(|| a.name.cmp(&b.name))
    });
    packages.truncate(max);
    (packages, total, security)
}

#[cfg(test)]
mod tests {
    use super::*;

    macro_rules! fixture {
        ($path:literal) => {
            include_str!(concat!("../../../fixtures/updates/", $path))
        };
    }

    fn names(packages: &[Package], security: bool) -> Vec<&str> {
        packages
            .iter()
            .filter(|p| p.security == security)
            .map(|p| p.name.as_str())
            .collect()
    }

    #[test]
    fn apt_on_debian_and_ubuntu() {
        assert_eq!(apt(fixture!("debian-12/apt-list.txt")), []);
        let debian = apt(fixture!("debian-13/apt-list.txt"));
        assert_eq!(debian.len(), 6);
        assert_eq!(names(&debian, true), ["zlib1g"]);
        assert_eq!(
            debian[5],
            Package {
                name: "zlib1g".into(),
                installed: "1:1.3.dfsg+really1.3.1-1+b1".into(),
                candidate: "1:1.3.dfsg+really1.3.1-1+deb13u1".into(),
                source: "stable,stable-security".into(),
                security: true
            }
        );
        let ubuntu = apt(fixture!("ubuntu-24.04/apt-list.txt"));
        assert_eq!(
            (names(&ubuntu, true), names(&ubuntu, false)),
            (vec!["libc-bin", "libc6"], vec!["libsystemd0", "libudev1"])
        );
    }

    // Lines from a hostile source: control characters, overlong names and broken lines are
    // skipped, the rest stays.
    #[test]
    fn apt_skips_what_is_not_a_plain_line() {
        let parsed = apt(fixture!("synthetic/apt-list-hostile.txt"));
        assert_eq!(parsed, []);
        let mut ok = fixture!("synthetic/apt-list-hostile.txt").to_owned();
        ok.push_str("libfoo/stable 2.0 amd64 [upgradable from: 1.0]\n");
        assert_eq!(apt(&ok).len(), 1);
    }

    #[test]
    fn dnf_4_and_5_with_their_security_lists() {
        let rocky = dnf(
            fixture!("rockylinux-9/dnf-check-update.txt"),
            fixture!("rockylinux-9/dnf-updateinfo-security.txt"),
        );
        assert_eq!(names(&rocky, false), ["systemd-libs"]);
        assert_eq!(
            names(&rocky, true),
            ["curl-minimal", "expat", "libcurl-minimal", "vim-minimal"]
        );
        assert_eq!(
            rocky[4],
            Package {
                name: "vim-minimal".into(),
                installed: String::new(),
                candidate: "2:8.2.2637-44.el9_9".into(),
                source: "baseos".into(),
                security: true
            }
        );
        let alma = dnf(
            fixture!("almalinux-10/dnf-check-update.txt"),
            fixture!("almalinux-10/dnf-updateinfo-security.txt"),
        );
        assert_eq!((alma.len(), names(&alma, false).len()), (6, 0));
        let fedora = dnf(
            fixture!("fedora-44/dnf-check-update.txt"),
            fixture!("fedora-44/dnf-updateinfo-security.txt"),
        );
        assert_eq!(fedora.len(), 18, "the header Upgrades is no package");
        assert_eq!(
            names(&fedora, true),
            [
                "dnf5",
                "dnf5-plugins",
                "libdnf5",
                "libdnf5-cli",
                "libmodulemd",
                "pcre2",
                "pcre2-syntax",
                "tpm2-tss"
            ]
        );
        // Without a security list nothing is marked.
        assert_eq!(
            names(&dnf(fixture!("fedora-44/dnf-check-update.txt"), ""), true).len(),
            0
        );
    }

    #[test]
    fn dnf_matches_name_and_architecture_and_stops_at_obsoletes() {
        let check = "openssl.x86_64 1:3.2.4-1 updates\nopenssl.i686 1:3.2.4-1 updates\nObsoleting Packages\ngrub2.x86_64 1:2.06-1 updates\n";
        let parsed = dnf(
            check,
            "RHSA-2026:1 Important/Sec. openssl-1:3.2.4-1.x86_64\n",
        );
        assert_eq!(
            parsed
                .iter()
                .map(|p| (p.name.as_str(), p.security))
                .collect::<Vec<_>>(),
            [("openssl", true), ("openssl", false)]
        );
    }

    #[test]
    fn reboots() {
        assert_eq!(
            dnf_reboot(fixture!("fedora-44/dnf-needs-restarting.txt")),
            Reboot::NotRequired
        );
        assert_eq!(
            dnf_reboot(fixture!("rockylinux-9/dnf-needs-restarting.txt")),
            Reboot::Unknown,
            "no needs-restarting: exit 1"
        );
        assert_eq!(
            dnf_reboot(fixture!("synthetic/dnf-needs-restarting-required.txt")),
            Reboot::Required("glibc, kernel".into())
        );
        assert_eq!(apt_reboot(false, "linux-image\n"), Reboot::NotRequired);
        assert_eq!(
            apt_reboot(true, "linux-image-6.1.0-28-arm64\nsermon-e2e-demo\n"),
            Reboot::Required("linux-image-6.1.0-28-arm64, sermon-e2e-demo".into())
        );
        assert_eq!(
            apt_reboot(true, ""),
            Reboot::Required("an update needs a reboot".into())
        );
        // Package scripts write the file: a line with control characters doesn't count.
        assert_eq!(
            apt_reboot(true, "\u{1b}[2Jevil\nlinux-image\n"),
            Reboot::Required("linux-image".into())
        );
        assert_eq!(
            dnf_reboot("Reboot is required\n  * \u{1b}]8;;x\n  * glibc\n"),
            Reboot::Required("glibc".into())
        );
        let modules = |m: &[&str]| m.iter().map(|s| (*s).to_owned()).collect::<Vec<_>>();
        assert_eq!(
            apk_reboot("6.12.13-0-lts", &modules(&["6.12.13-0-lts"])),
            Reboot::NotRequired
        );
        assert_eq!(
            apk_reboot("6.12.13-0-lts", &modules(&["6.12.20-0-lts"])),
            Reboot::Required("kernel 6.12.13-0-lts is running, 6.12.20-0-lts is installed".into())
        );
        assert_eq!(apk_reboot("7.0.12-linuxkit", &[]), Reboot::Unknown);
    }

    #[test]
    fn apk_on_alpine() {
        let alpine = apk(fixture!("alpine-3.22/apk-version.txt"));
        assert_eq!(alpine.len(), 3, "the header is no package");
        assert_eq!(
            alpine[2],
            Package {
                name: "zlib".into(),
                installed: "1.3.2-r1".into(),
                candidate: "1.4-r0".into(),
                source: String::new(),
                security: false
            }
        );
        assert_eq!(
            apk("sermon-e2e-demo-1.0.0-r1  < 2.0.0-r1\nnot-a-version < 1\n")[0].name,
            "sermon-e2e-demo"
        );
    }

    #[test]
    fn bounds_with_security_updates_first() {
        let fedora = dnf(
            fixture!("fedora-44/dnf-check-update.txt"),
            fixture!("fedora-44/dnf-updateinfo-security.txt"),
        );
        let (kept, total, security) = bounded(fedora, 10);
        assert_eq!((kept.len(), total, security), (10, 18, 8));
        assert!(kept[..8].iter().all(|p| p.security) && !kept[8].security);
    }
}
