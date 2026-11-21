//! File systems: `/proc/1/mountinfo` and `statvfs` (KONZEPT §8.1).
//!
//! | Metric | Labels | Unit |
//! |---|---|---|
//! | `sermon_filesystem_{size,avail,used}_bytes` | `mountpoint` | bytes |
//! | `sermon_filesystem_utilization_ratio` | `mountpoint` | ppm, used / (used + avail) like `df` |
//! | `sermon_filesystem_inodes_{total,used}` | `mountpoint` | count |
//! | `sermon_filesystem_readonly` | `mountpoint` | 1 = mounted read-only |
//!
//! - Mounts come from PID 1, not from the agent: systemd's `ProtectSystem=strict` gives the agent
//!   its own namespace in which everything is read-only.
//! - Only local disk file systems are reported (allowlist). Pseudo file systems, tmpfs, overlay,
//!   squashfs (snaps) and loop mounts are left out; network file systems as well, because a hung
//!   server would block `statvfs`.
//! - A device mounted several times (bind mounts) is reported once, at its shortest path.

use std::collections::BTreeMap;
use std::io;
use std::path::{Path, PathBuf};

use sermon_core::Sample;

use crate::rate::ppm;
use crate::{CollectError, Collector, Context};

/// File system types that are reported.
const LOCAL_TYPES: [&str; 13] = [
    "ext2", "ext3", "ext4", "xfs", "btrfs", "zfs", "f2fs", "vfat", "exfat", "ntfs", "ntfs3", "jfs",
    "bcachefs",
];
/// Most mount points reported (bounded cardinality).
const MAX_MOUNTS: usize = 64;

/// One line of `mountinfo`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Mount {
    /// `major:minor` of the device.
    pub device: String,
    /// Path inside the file system that is mounted (`/` for the whole file system).
    pub root: String,
    /// Where it is mounted.
    pub mountpoint: String,
    /// File system type.
    pub fstype: String,
    /// Mounted read-only.
    pub readonly: bool,
}

/// Parses `mountinfo` (see `proc(5)`); malformed lines are skipped.
#[must_use]
pub fn parse_mountinfo(text: &str) -> Vec<Mount> {
    text.lines()
        .filter_map(|line| {
            let (left, right) = line.split_once(" - ")?;
            let left: Vec<&str> = left.split(' ').collect();
            let mut right = right.split(' ');
            let fstype = right.next()?;
            let (device, root, mountpoint, options) =
                (left.get(2)?, left.get(3)?, left.get(4)?, left.get(5)?);
            Some(Mount {
                device: (*device).to_owned(),
                root: unescape(root),
                mountpoint: unescape(mountpoint),
                fstype: fstype.to_owned(),
                readonly: options.split(',').any(|option| option == "ro"),
            })
        })
        .collect()
}

/// Decodes the octal escapes of `mountinfo` (`\040` = space, `\011`, `\012`, `\134`).
fn unescape(field: &str) -> String {
    let bytes = field.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut index = 0;
    while index < bytes.len() {
        let octal = bytes.get(index + 1..index + 4).filter(|digits| {
            bytes[index] == b'\\' && digits.iter().all(|digit| (b'0'..=b'7').contains(digit))
        });
        if let Some(byte) =
            octal.and_then(|digits| u8::from_str_radix(std::str::from_utf8(digits).ok()?, 8).ok())
        {
            out.push(byte);
            index += 4;
        } else {
            out.push(bytes[index]);
            index += 1;
        }
    }
    String::from_utf8_lossy(&out).into_owned()
}

/// The mounts to report: local types, each device once (whole file system preferred, then the
/// shortest path), sorted by mount point, at most [`MAX_MOUNTS`].
#[must_use]
pub fn reported_mounts(mounts: Vec<Mount>) -> Vec<Mount> {
    let mut by_device: BTreeMap<String, Mount> = BTreeMap::new();
    for mount in mounts
        .into_iter()
        .filter(|mount| LOCAL_TYPES.contains(&mount.fstype.as_str()))
    {
        let better = |existing: &Mount| {
            (mount.root != "/", mount.mountpoint.len())
                < (existing.root != "/", existing.mountpoint.len())
        };
        if by_device.get(&mount.device).is_none_or(better) {
            by_device.insert(mount.device.clone(), mount);
        }
    }
    let mut mounts: Vec<Mount> = by_device.into_values().collect();
    mounts.sort_by(|a, b| a.mountpoint.cmp(&b.mountpoint));
    mounts.truncate(MAX_MOUNTS);
    mounts
}

/// Size and inode figures of a mounted file system.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct FsUsage {
    /// Size in bytes.
    pub total_bytes: u64,
    /// Free bytes (including the space reserved for root).
    pub free_bytes: u64,
    /// Bytes available to unprivileged users.
    pub avail_bytes: u64,
    /// Inodes.
    pub inodes_total: u64,
    /// Free inodes.
    pub inodes_free: u64,
}

/// Port for `statvfs`, so that tests don't depend on the machine.
pub trait UsageSource: Send + std::fmt::Debug {
    /// Usage of the file system mounted at `path`.
    ///
    /// # Errors
    ///
    /// If `statvfs` fails.
    fn usage(&self, path: &Path) -> io::Result<FsUsage>;
}

/// The real `statvfs`.
#[derive(Debug, Clone, Copy)]
pub struct StatvfsUsage;

fn to_u64(value: impl TryInto<u64>) -> u64 {
    value.try_into().unwrap_or_default()
}

impl UsageSource for StatvfsUsage {
    fn usage(&self, path: &Path) -> io::Result<FsUsage> {
        let stat = rustix::fs::statvfs(path)?;
        let block = to_u64(stat.f_frsize);
        Ok(FsUsage {
            total_bytes: to_u64(stat.f_blocks).saturating_mul(block),
            free_bytes: to_u64(stat.f_bfree).saturating_mul(block),
            avail_bytes: to_u64(stat.f_bavail).saturating_mul(block),
            inodes_total: to_u64(stat.f_files),
            inodes_free: to_u64(stat.f_ffree),
        })
    }
}

fn bytes(metric: &'static str, value: u64) -> Sample {
    Sample::int(metric, i64::try_from(value).unwrap_or(i64::MAX))
}

/// Samples of one mount point.
#[must_use]
pub fn mount_samples(mount: &Mount, usage: &FsUsage) -> Vec<Sample> {
    let used = usage.total_bytes.saturating_sub(usage.free_bytes);
    let mut samples = vec![
        bytes("sermon_filesystem_size_bytes", usage.total_bytes),
        bytes("sermon_filesystem_avail_bytes", usage.avail_bytes),
        bytes("sermon_filesystem_used_bytes", used),
        bytes("sermon_filesystem_inodes_total", usage.inodes_total),
        bytes(
            "sermon_filesystem_inodes_used",
            usage.inodes_total.saturating_sub(usage.inodes_free),
        ),
        Sample::int("sermon_filesystem_readonly", i64::from(mount.readonly)),
    ];
    if let Some(value) = ppm(used, used.saturating_add(usage.avail_bytes)) {
        samples.push(Sample::int("sermon_filesystem_utilization_ratio", value));
    }
    samples
        .into_iter()
        .map(|sample| sample.label("mountpoint", mount.mountpoint.clone()))
        .collect()
}

/// File system collector (every 10 s: fill levels change slowly).
#[derive(Debug)]
pub struct FilesystemCollector {
    usage: Box<dyn UsageSource>,
}

impl FilesystemCollector {
    /// A collector using `usage` for `statvfs`.
    #[must_use]
    pub fn new(usage: Box<dyn UsageSource>) -> Self {
        Self { usage }
    }
}

impl Collector for FilesystemCollector {
    fn name(&self) -> &'static str {
        "filesystem"
    }

    fn interval_ticks(&self) -> u32 {
        10
    }

    fn collect(
        &mut self,
        context: &Context<'_>,
        out: &mut Vec<Sample>,
    ) -> Result<(), CollectError> {
        let text = match context.root.read_proc("1/mountinfo") {
            Ok(text) => text,
            // PID 1 not readable (hidepid): the agent's own view is better than nothing.
            Err(_) => context.root.read_proc("self/mountinfo")?,
        };
        for mount in reported_mounts(parse_mountinfo(&text)) {
            let path = host_path(context.root.root(), &mount.mountpoint);
            // A file system that can't be queried (permissions, just unmounted) is skipped.
            if let Ok(usage) = self.usage.usage(&path) {
                out.extend(mount_samples(&mount, &usage));
            }
        }
        Ok(())
    }
}

/// `mountpoint` below the host root (the root itself on a host, e.g. `/host` in a container).
fn host_path(root: &Path, mountpoint: &str) -> PathBuf {
    root.join(mountpoint.trim_start_matches('/'))
}

#[cfg(test)]
mod tests {
    use std::collections::HashMap;

    use proptest::prelude::*;

    use super::*;
    use crate::testing::{at, fixture, int, root_with};

    const HOST: &str = "\
22 1 259:2 / / rw,relatime shared:1 - ext4 /dev/nvme0n1p2 rw,errors=remount-ro
23 22 0:21 / /proc rw,nosuid,nodev,noexec,relatime shared:12 - proc proc rw
24 22 0:5 / /dev rw,nosuid shared:2 - devtmpfs udev rw,size=8000000k
25 22 259:1 / /boot/efi rw,relatime shared:30 - vfat /dev/nvme0n1p1 rw,fmask=0077
26 22 259:3 / /srv/my\\040data ro,relatime shared:31 - xfs /dev/nvme0n1p3 rw
27 22 7:0 / /snap/core22/1234 ro,nodev,relatime shared:40 - squashfs /dev/loop0 ro
28 22 0:30 / /run rw,nosuid,nodev,noexec,relatime shared:5 - tmpfs tmpfs rw,size=1600000k
29 22 259:2 /var/lib/docker /var/lib/docker rw,relatime shared:1 - ext4 /dev/nvme0n1p2 rw
30 22 0:45 / /mnt/share rw,relatime shared:50 - nfs4 server:/export rw
31 22 0:60 / /var/lib/docker/overlay2/abc/merged rw,relatime - overlay overlay rw
";

    #[test]
    fn keeps_local_file_systems_once_each() {
        let mounts = reported_mounts(parse_mountinfo(HOST));

        let points: Vec<&str> = mounts
            .iter()
            .map(|mount| mount.mountpoint.as_str())
            .collect();
        assert_eq!(points, vec!["/", "/boot/efi", "/srv/my data"]);
        assert!(mounts[2].readonly);
        assert!(!mounts[0].readonly);
    }

    #[test]
    fn container_fixtures_have_no_local_file_systems_but_parse() {
        for distro in [
            "debian-13",
            "ubuntu-24.04",
            "rockylinux-9",
            "almalinux-10",
            "fedora-44",
        ] {
            let mounts = parse_mountinfo(&fixture(distro).read_proc("self/mountinfo").unwrap());
            assert!(mounts.len() > 10, "{distro}");
            assert!(
                mounts.iter().any(|mount| mount.fstype == "overlay"),
                "{distro}"
            );
            // The bind-mounted /etc/hostname is ext4 but not the whole file system.
            let reported = reported_mounts(mounts);
            assert!(
                reported.iter().all(|mount| mount.fstype == "ext4"),
                "{distro}"
            );
        }
    }

    #[test]
    fn unescapes_octal_sequences_only() {
        assert_eq!(unescape("/a\\040b\\011c\\134d"), "/a b\tc\\d");
        assert_eq!(unescape("/a\\9xx\\04"), "/a\\9xx\\04");
    }

    #[derive(Debug)]
    struct FakeUsage(HashMap<PathBuf, FsUsage>);

    impl UsageSource for FakeUsage {
        fn usage(&self, path: &Path) -> io::Result<FsUsage> {
            self.0
                .get(path)
                .copied()
                .ok_or_else(|| io::Error::from(io::ErrorKind::PermissionDenied))
        }
    }

    #[test]
    fn reports_usage_from_pid_1_and_skips_unqueryable_mounts() {
        let (dir, root) = root_with(&[("proc/1/mountinfo", HOST), ("proc/self/mountinfo", "")]);
        let usage = FsUsage {
            total_bytes: 100 << 30,
            free_bytes: 30 << 30,
            avail_bytes: 25 << 30,
            inodes_total: 1000,
            inodes_free: 900,
        };
        let fake = FakeUsage(HashMap::from([
            (dir.path().join(""), usage),
            (dir.path().join("srv/my data"), usage),
        ]));
        let mut collector = FilesystemCollector::new(Box::new(fake));

        let mut samples = Vec::new();
        collector
            .collect(
                &Context {
                    root: &root,
                    now: at(0),
                },
                &mut samples,
            )
            .unwrap();

        let root_mount = [("mountpoint", "/")];
        assert_eq!(
            int(&samples, "sermon_filesystem_size_bytes", &root_mount),
            Some(100 << 30)
        );
        assert_eq!(
            int(&samples, "sermon_filesystem_used_bytes", &root_mount),
            Some(70 << 30)
        );
        assert_eq!(
            int(&samples, "sermon_filesystem_avail_bytes", &root_mount),
            Some(25 << 30)
        );
        // 70 used of 95 usable (the reserved blocks don't count, like df).
        assert_eq!(
            int(&samples, "sermon_filesystem_utilization_ratio", &root_mount),
            Some(736_842)
        );
        assert_eq!(
            int(&samples, "sermon_filesystem_inodes_used", &root_mount),
            Some(100)
        );
        assert_eq!(
            int(
                &samples,
                "sermon_filesystem_readonly",
                &[("mountpoint", "/srv/my data")]
            ),
            Some(1)
        );
        assert_eq!(
            int(
                &samples,
                "sermon_filesystem_size_bytes",
                &[("mountpoint", "/boot/efi")]
            ),
            None
        );
    }

    #[test]
    fn statvfs_works_on_this_machine() {
        let usage = StatvfsUsage.usage(Path::new("/")).unwrap();

        assert!(usage.total_bytes > 0 && usage.avail_bytes <= usage.total_bytes);
    }

    proptest! {
        #[test]
        fn parser_never_panics(text in "\\PC*") {
            let _ = reported_mounts(parse_mountinfo(&text));
        }
    }
}
