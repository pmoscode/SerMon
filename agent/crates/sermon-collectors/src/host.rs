//! Host facts from `/proc` and `/etc` (hostname, OS, kernel, CPUs, memory, machine-id).

use sermon_core::{Architecture, HostFacts, MachineId, ProcError, ProcRoot};

/// Longest host name the server accepts (RFC 1123).
const MAX_HOSTNAME_LEN: usize = 253;
/// Longest kernel release the server accepts.
const MAX_KERNEL_RELEASE_LEN: usize = 128;
/// Longest OS ID and version the server accepts; longer values are cut.
const MAX_OS_FIELD_LEN: usize = 64;
/// Most logical CPUs the server accepts.
const MAX_CPU_COUNT: u32 = 4096;
/// OS ID if `/etc/os-release` is missing or has no `ID`.
const UNKNOWN_OS_ID: &str = "linux";

/// Errors when reading the host facts.
#[derive(Debug, thiserror::Error)]
pub enum HostFactsError {
    /// A required file could not be read.
    #[error(transparent)]
    Read(#[from] ProcError),
    /// A file has unexpected content.
    #[error("{file}: {reason}")]
    Invalid {
        /// The file, e.g. `/etc/machine-id`.
        file: &'static str,
        /// What is wrong.
        reason: String,
    },
    /// The agent was built for an architecture the server doesn't know.
    #[error("unsupported CPU architecture {0}")]
    UnsupportedArchitecture(&'static str),
}

/// Reads the facts of the host below `root`.
///
/// `/etc/os-release` is optional; everything else is required.
///
/// # Errors
///
/// [`HostFactsError`] if a required file is missing or malformed.
pub fn read_host_facts(root: &ProcRoot) -> Result<HostFacts, HostFactsError> {
    let architecture = Architecture::current().ok_or(HostFactsError::UnsupportedArchitecture(
        std::env::consts::ARCH,
    ))?;
    let hostname = bounded_line(
        &root.read_proc("sys/kernel/hostname")?,
        "/proc/sys/kernel/hostname",
        MAX_HOSTNAME_LEN,
    )?;
    let kernel_release = bounded_line(
        &root.read_proc("sys/kernel/osrelease")?,
        "/proc/sys/kernel/osrelease",
        MAX_KERNEL_RELEASE_LEN,
    )?;
    // Without systemd (Alpine) dbus may keep it; install.sh creates /etc/machine-id otherwise.
    let (text, file) = match root.read_etc("machine-id") {
        Err(error) if error.is_not_found() => match root.read_var("lib/dbus/machine-id") {
            Ok(text) => (text, "/var/lib/dbus/machine-id"),
            Err(_) => return Err(error.into()),
        },
        other => (other?, "/etc/machine-id"),
    };
    let machine_id = MachineId::parse(&text).map_err(|error| HostFactsError::Invalid {
        file,
        reason: error.to_string(),
    })?;
    let os = match root.read_etc("os-release") {
        Ok(text) => parse_os_release(&text),
        Err(error) if error.is_not_found() => OsRelease::default(),
        Err(error) => return Err(error.into()),
    };
    let cpu_count = count_cpus(&root.read_proc("stat")?).ok_or(HostFactsError::Invalid {
        file: "/proc/stat",
        reason: format!("expected 1 to {MAX_CPU_COUNT} CPUs"),
    })?;
    let memory_bytes =
        parse_mem_total(&root.read_proc("meminfo")?).ok_or(HostFactsError::Invalid {
            file: "/proc/meminfo",
            reason: "no valid MemTotal".to_owned(),
        })?;

    Ok(HostFacts {
        hostname,
        machine_id,
        os_id: os.id,
        os_version: os.version_id,
        kernel_release,
        architecture,
        cpu_count,
        memory_bytes,
        security_source: sermon_core::SecuritySource::Unknown,
    })
}

/// `ID` and `VERSION_ID` from `/etc/os-release`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct OsRelease {
    /// `ID`, e.g. `debian`; `linux` if missing.
    pub id: String,
    /// `VERSION_ID`, e.g. `24.04`; empty if missing.
    pub version_id: String,
}

impl Default for OsRelease {
    fn default() -> Self {
        Self {
            id: UNKNOWN_OS_ID.to_owned(),
            version_id: String::new(),
        }
    }
}

/// Parses `/etc/os-release` (shell-style `KEY=value`, values optionally quoted).
#[must_use]
pub fn parse_os_release(text: &str) -> OsRelease {
    let mut release = OsRelease::default();
    for line in text.lines() {
        let Some((key, raw)) = line.trim().split_once('=') else {
            continue;
        };
        let value = truncate(&unquote(raw.trim()), MAX_OS_FIELD_LEN);
        match key {
            "ID" if !value.is_empty() => release.id = value,
            "VERSION_ID" => release.version_id = value,
            _ => {}
        }
    }
    release
}

fn unquote(raw: &str) -> String {
    let quoted = |quote: char| {
        raw.strip_prefix(quote)
            .and_then(|inner| inner.strip_suffix(quote))
    };
    if let Some(inner) = quoted('\'') {
        return inner.to_owned();
    }
    let Some(inner) = quoted('"') else {
        return raw.to_owned();
    };
    let mut value = String::with_capacity(inner.len());
    let mut chars = inner.chars();
    while let Some(c) = chars.next() {
        if c == '\\' {
            value.extend(chars.next());
        } else {
            value.push(c);
        }
    }
    value
}

fn truncate(value: &str, max_chars: usize) -> String {
    value
        .chars()
        .filter(|c| !c.is_control())
        .take(max_chars)
        .collect()
}

/// Number of logical CPUs: the `cpuN` lines in `/proc/stat` (online CPUs).
#[must_use]
pub fn count_cpus(stat: &str) -> Option<u32> {
    let count = stat
        .lines()
        .filter(|line| {
            line.strip_prefix("cpu")
                .is_some_and(|rest| rest.starts_with(|c: char| c.is_ascii_digit()))
        })
        .count();
    u32::try_from(count)
        .ok()
        .filter(|count| (1..=MAX_CPU_COUNT).contains(count))
}

/// `MemTotal` from `/proc/meminfo` in bytes.
#[must_use]
pub fn parse_mem_total(meminfo: &str) -> Option<u64> {
    let line = meminfo
        .lines()
        .find_map(|line| line.strip_prefix("MemTotal:"))?;
    let mut parts = line.split_whitespace();
    let kib: u64 = parts.next()?.parse().ok()?;
    if parts.next() != Some("kB") {
        return None;
    }
    kib.checked_mul(1024).filter(|bytes| *bytes > 0)
}

fn bounded_line(text: &str, file: &'static str, max_len: usize) -> Result<String, HostFactsError> {
    let value = text.trim();
    if value.is_empty() || value.len() > max_len || value.chars().any(char::is_control) {
        return Err(HostFactsError::Invalid {
            file,
            reason: format!("expected 1 to {max_len} printable characters"),
        });
    }
    Ok(value.to_owned())
}

#[cfg(test)]
mod tests {
    use std::path::PathBuf;

    use proptest::prelude::*;

    use super::*;

    fn fixture(distro: &str) -> ProcRoot {
        ProcRoot::new(
            PathBuf::from(env!("CARGO_MANIFEST_DIR"))
                .join("../../fixtures")
                .join(distro),
        )
    }

    #[test]
    fn reads_debian_13() {
        let facts = read_host_facts(&fixture("debian-13")).unwrap();

        assert_eq!(facts.hostname, "web-01");
        assert_eq!(
            facts.machine_id.as_str(),
            "0123456789abcdef0123456789abcdef"
        );
        assert_eq!(facts.os_id, "debian");
        assert_eq!(facts.os_version, "13");
        assert_eq!(facts.kernel_release, "7.0.12-linuxkit");
        assert_eq!(facts.cpu_count, 16);
        assert_eq!(facts.memory_bytes, 8_123_740 * 1024);
        assert_eq!(Some(facts.architecture), Architecture::current());
    }

    #[test]
    fn reads_ubuntu_24_04() {
        let facts = read_host_facts(&fixture("ubuntu-24.04")).unwrap();

        assert_eq!(facts.hostname, "db-01");
        assert_eq!(facts.os_id, "ubuntu");
        assert_eq!(facts.os_version, "24.04");
    }

    // The RHEL family quotes its values and names itself by ID (rocky, almalinux), with ID_LIKE
    // naming rhel; the version carries the minor release (M2-19).
    #[test]
    fn reads_the_rhel_family_and_fedora() {
        for (distro, id, version) in [
            ("rockylinux-9", "rocky", "9.9"),
            ("almalinux-10", "almalinux", "10.3"),
            ("fedora-44", "fedora", "44"),
        ] {
            let facts = read_host_facts(&fixture(distro)).unwrap();
            assert_eq!(
                (facts.os_id.as_str(), facts.os_version.as_str()),
                (id, version),
                "{distro}"
            );
            assert_eq!(facts.hostname, "web-01", "{distro}");
            assert_eq!(facts.cpu_count, 16, "{distro}");
        }
    }

    #[test]
    fn missing_os_release_falls_back_to_linux() {
        let dir = copy_fixture("debian-13");
        std::fs::remove_file(dir.path().join("etc/os-release")).unwrap();

        let facts = read_host_facts(&ProcRoot::new(dir.path())).unwrap();

        assert_eq!(facts.os_id, "linux");
        assert_eq!(facts.os_version, "");
    }

    #[test]
    fn missing_machine_id_is_an_error() {
        let dir = copy_fixture("debian-13");
        std::fs::remove_file(dir.path().join("etc/machine-id")).unwrap();

        let error = read_host_facts(&ProcRoot::new(dir.path())).unwrap_err();

        assert!(
            matches!(&error, HostFactsError::Read(read) if read.is_not_found()),
            "{error}"
        );
    }

    // Alpine without systemd: dbus keeps the machine-id in /var/lib/dbus (M2-20).
    #[test]
    fn falls_back_to_the_machine_id_of_dbus() {
        let dir = copy_fixture("debian-13");
        std::fs::remove_file(dir.path().join("etc/machine-id")).unwrap();
        std::fs::create_dir_all(dir.path().join("var/lib/dbus")).unwrap();
        std::fs::write(
            dir.path().join("var/lib/dbus/machine-id"),
            "fedcba9876543210fedcba9876543210\n",
        )
        .unwrap();

        let facts = read_host_facts(&ProcRoot::new(dir.path())).unwrap();

        assert_eq!(
            facts.machine_id.as_str(),
            "fedcba9876543210fedcba9876543210"
        );
    }

    #[test]
    fn rejects_an_uninitialized_machine_id() {
        let dir = copy_fixture("debian-13");
        std::fs::write(dir.path().join("etc/machine-id"), "uninitialized\n").unwrap();

        let error = read_host_facts(&ProcRoot::new(dir.path())).unwrap_err();

        assert_eq!(
            error.to_string(),
            "/etc/machine-id: not a machine-id (expected 32 hex characters)"
        );
    }

    #[test]
    fn rejects_empty_or_overlong_hostnames() {
        for hostname in [
            String::new(),
            "a".repeat(254),
            "bad\u{1b}[31mname".to_owned(),
        ] {
            let dir = copy_fixture("debian-13");
            std::fs::write(dir.path().join("proc/sys/kernel/hostname"), &hostname).unwrap();

            let error = read_host_facts(&ProcRoot::new(dir.path())).unwrap_err();

            assert!(
                error.to_string().starts_with("/proc/sys/kernel/hostname"),
                "{error}"
            );
        }
    }

    #[test]
    fn parses_quoted_and_unquoted_os_release_values() {
        let release = parse_os_release(
            "NAME=\"Rocky Linux\"\nID=\"rocky\"\nID_LIKE=\"rhel centos fedora\"\nVERSION_ID='9.5'\n",
        );
        assert_eq!(
            release,
            OsRelease {
                id: "rocky".into(),
                version_id: "9.5".into()
            }
        );

        let alpine = parse_os_release("ID=alpine\nVERSION_ID=3.22.2\n");
        assert_eq!(alpine.id, "alpine");
        assert_eq!(alpine.version_id, "3.22.2");
    }

    #[test]
    fn os_release_handles_escapes_comments_and_overlong_values() {
        let release = parse_os_release(&format!(
            "# comment\n\nID=\"my\\\"os\"\nVERSION_ID=\"{}\"\n",
            "9".repeat(100)
        ));

        assert_eq!(release.id, "my\"os");
        assert_eq!(release.version_id.len(), MAX_OS_FIELD_LEN);
    }

    #[test]
    fn os_release_without_id_falls_back_to_linux() {
        assert_eq!(parse_os_release("ID=\nNAME=x\n").id, "linux");
    }

    #[test]
    fn counts_only_per_cpu_lines() {
        assert_eq!(
            count_cpus("cpu  1 2 3\ncpu0 1 2 3\ncpu1 1 2 3\ncpufreq x\nintr 1\n"),
            Some(2)
        );
        assert_eq!(count_cpus("cpu  1 2 3\n"), None);
    }

    #[test]
    fn mem_total_needs_a_kib_value() {
        assert_eq!(
            parse_mem_total("MemTotal:  1024 kB\nMemFree: 1 kB\n"),
            Some(1024 * 1024)
        );
        assert_eq!(parse_mem_total("MemTotal: 1024\n"), None);
        assert_eq!(parse_mem_total("MemTotal: 0 kB\n"), None);
        assert_eq!(parse_mem_total("MemTotal: x kB\n"), None);
        assert_eq!(
            parse_mem_total(&format!("MemTotal: {} kB\n", u64::MAX)),
            None
        );
        assert_eq!(parse_mem_total("MemFree: 1 kB\n"), None);
    }

    proptest! {
        #[test]
        fn parsers_never_panic(text in "\\PC*") {
            let release = parse_os_release(&text);
            prop_assert!(!release.id.is_empty());
            prop_assert!(release.id.chars().count() <= MAX_OS_FIELD_LEN);
            let _ = count_cpus(&text);
            let _ = parse_mem_total(&text);
        }
    }

    fn copy_fixture(distro: &str) -> tempfile::TempDir {
        let dir = tempfile::tempdir().unwrap();
        copy_dir(fixture(distro).root(), dir.path());
        dir
    }

    fn copy_dir(from: &std::path::Path, to: &std::path::Path) {
        std::fs::create_dir_all(to).unwrap();
        for entry in std::fs::read_dir(from).unwrap() {
            let entry = entry.unwrap();
            let target = to.join(entry.file_name());
            if entry.file_type().unwrap().is_dir() {
                copy_dir(&entry.path(), &target);
            } else {
                std::fs::copy(entry.path(), target).unwrap();
            }
        }
    }
}
