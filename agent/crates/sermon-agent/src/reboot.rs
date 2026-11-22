//! Reboots on hosts without journald (M2-31, ADR 0025). The journal source sees a reboot in the
//! boot IDs of its entries; log files have none. So the agent keeps the kernel's boot ID
//! (`/proc/sys/kernel/random/boot_id`) in its state directory and reports `system.reboot` at the
//! start when it changed. The event is verified: the ID comes from the kernel, not from a line
//! anyone could write. Reboots while the agent did not run count as one.

use std::fs;
use std::io::{self, Write};
use std::path::Path;
use std::time::{SystemTime, UNIX_EPOCH};

use sermon_collectors::cpu::parse_stat;
use sermon_core::ProcRoot;
use sermon_security::{EventKind, SecurityEvent};

/// A boot ID: a UUID as the kernel prints it.
fn valid(id: &str) -> bool {
    id.len() == 36
        && id
            .bytes()
            .all(|b| b.is_ascii_hexdigit() && !b.is_ascii_uppercase() || b == b'-')
}

/// The result of comparing the boot with the stored one.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct BootCheck {
    current: String,
    /// The reboot to report; none at the first start or within the same boot.
    pub event: Option<SecurityEvent>,
}

/// Compares the kernel's boot ID with the one stored in `file`. `None` if the kernel's ID can't
/// be read (then nothing is reported or stored).
#[must_use]
pub fn check(root: &ProcRoot, file: &Path) -> Option<BootCheck> {
    let current = root
        .read_proc("sys/kernel/random/boot_id")
        .ok()?
        .trim()
        .to_owned();
    if !valid(&current) {
        return None;
    }
    let previous = fs::read_to_string(file)
        .ok()
        .map(|s| s.trim().to_owned())
        .filter(|s| valid(s));
    let event = previous
        .filter(|previous| *previous != current)
        .map(|previous| SecurityEvent {
            at_us: boot_time_us(root),
            kind: EventKind::SystemReboot {
                previous_boot: previous,
                uptime_before_us: None,
            },
            verified: true,
        });
    Some(BootCheck { current, event })
}

/// When the host booted (`btime` of `/proc/stat`), or now if that can't be read.
fn boot_time_us(root: &ProcRoot) -> u64 {
    root.read_proc("stat")
        .ok()
        .and_then(|text| parse_stat(&text).ok())
        .and_then(|stat| stat.boot_time)
        .map_or_else(
            || {
                SystemTime::now()
                    .duration_since(UNIX_EPOCH)
                    .map_or(0, |d| u64::try_from(d.as_micros()).unwrap_or(u64::MAX))
            },
            |seconds| seconds.saturating_mul(1_000_000),
        )
}

impl BootCheck {
    /// Stores the current boot ID (via a temporary file and rename, owner-only).
    ///
    /// # Errors
    ///
    /// If the file can't be written.
    pub fn commit(&self, file: &Path) -> io::Result<()> {
        use std::os::unix::fs::OpenOptionsExt;
        let tmp = file.with_extension("tmp");
        let mut out = fs::OpenOptions::new()
            .write(true)
            .create(true)
            .truncate(true)
            .mode(0o600)
            .open(&tmp)?;
        out.write_all(self.current.as_bytes())?;
        out.sync_all()?;
        fs::rename(&tmp, file)
    }
}

/// Checks the boot at the start of the agent, hands a reboot to `deliver` (true: buffered) and
/// stores the boot only then, so a reboot is reported again after a failed delivery.
pub fn report(root: &ProcRoot, file: &Path, deliver: impl FnOnce(&SecurityEvent) -> bool) {
    let Some(check) = check(root, file) else {
        tracing::debug!("cannot read the boot ID; reboots without journald are not reported");
        return;
    };
    if let Some(event) = &check.event {
        if !deliver(event) {
            return;
        }
        tracing::info!("reboot since the last start reported");
    }
    if let Err(error) = check.commit(file) {
        tracing::warn!(file = %file.display(), "cannot store the boot ID: {error}");
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const FIRST: &str = "6b1c57d4-3c39-4e5c-9f3a-5a4b8d3c2e11";
    const SECOND: &str = "0f2e4d6c-8b9a-4c1d-a2e3-f4a5b6c7d8e9";

    fn host(boot_id: &str) -> (tempfile::TempDir, ProcRoot) {
        let dir = tempfile::tempdir().unwrap();
        fs::create_dir_all(dir.path().join("proc/sys/kernel/random")).unwrap();
        fs::write(
            dir.path().join("proc/sys/kernel/random/boot_id"),
            format!("{boot_id}\n"),
        )
        .unwrap();
        fs::write(
            dir.path().join("proc/stat"),
            "cpu  1 2 3 4\nbtime 1795000000\n",
        )
        .unwrap();
        let root = ProcRoot::new(dir.path());
        (dir, root)
    }

    fn set_boot(dir: &tempfile::TempDir, boot_id: &str) {
        fs::write(dir.path().join("proc/sys/kernel/random/boot_id"), boot_id).unwrap();
    }

    // A restart of the agent: nothing at the first start or within the same boot, one verified
    // reboot with the previous boot and the boot time after a new boot.
    #[test]
    fn a_new_boot_id_since_the_last_start_is_a_reboot() {
        let (dir, root) = host(FIRST);
        let file = dir.path().join("boot_id");
        let mut seen = Vec::new();
        report(&root, &file, |e| {
            seen.push(e.clone());
            true
        });
        assert!(seen.is_empty(), "first start: {seen:?}");
        assert_eq!(fs::read_to_string(&file).unwrap(), FIRST);
        report(&root, &file, |e| {
            seen.push(e.clone());
            true
        });
        assert!(seen.is_empty(), "same boot: {seen:?}");

        set_boot(&dir, SECOND);
        report(&root, &file, |e| {
            seen.push(e.clone());
            true
        });
        assert_eq!(
            seen,
            [SecurityEvent {
                at_us: 1_795_000_000_000_000,
                kind: EventKind::SystemReboot {
                    previous_boot: FIRST.to_owned(),
                    uptime_before_us: None
                },
                verified: true,
            }]
        );
        assert_eq!(fs::read_to_string(&file).unwrap(), SECOND);
    }

    // The boot is stored only once the reboot is buffered: a failed delivery reports it again.
    #[test]
    fn a_failed_delivery_reports_the_reboot_again() {
        let (dir, root) = host(FIRST);
        let file = dir.path().join("boot_id");
        report(&root, &file, |_| true);
        set_boot(&dir, SECOND);
        report(&root, &file, |_| false);
        assert_eq!(fs::read_to_string(&file).unwrap(), FIRST);
        let mut again = 0;
        report(&root, &file, |_| {
            again += 1;
            true
        });
        assert_eq!(again, 1);
    }

    // Garbage in the stored file or from the kernel reports nothing.
    #[test]
    fn invalid_boot_ids_report_nothing() {
        let (dir, root) = host("not-a-boot-id");
        let file = dir.path().join("boot_id");
        assert_eq!(check(&root, &file), None);
        set_boot(&dir, FIRST);
        fs::write(&file, "x; rm -rf /").unwrap();
        assert_eq!(check(&root, &file).unwrap().event, None);
        set_boot(&dir, &FIRST.to_uppercase());
        assert_eq!(check(&root, &file), None);
        let missing = ProcRoot::new(dir.path().join("nothing"));
        assert_eq!(check(&missing, &file), None);
    }
}
