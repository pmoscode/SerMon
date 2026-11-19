//! Port `ProcRoot`: the file system root that collectors read `/proc`, `/sys` and `/etc` from.
//!
//! On a host the root is `/`. In tests it is a fixture directory (`agent/fixtures/<distro>/`), in a
//! container the mounted host file system (e.g. `/host`).

use std::fs::File;
use std::io::{self, Read};
use std::path::{Component, Path, PathBuf};

/// Files larger than this are refused, so that a strange file can't exhaust the agent's memory.
/// Generous: `/proc/self/mountinfo` on a container host with thousands of mounts stays below 2 MiB.
pub const MAX_FILE_SIZE: u64 = 8 * 1024 * 1024;

/// Errors when reading below the root.
#[derive(Debug, thiserror::Error)]
pub enum ProcError {
    /// The relative path is empty, absolute or leaves its area (`..`).
    #[error("invalid path {0:?}: must be relative and stay below its directory")]
    InvalidPath(String),
    /// The file could not be read; `NotFound` means the kernel or distro doesn't provide it.
    #[error("cannot read {path}: {source}")]
    Io {
        /// Full path of the file.
        path: PathBuf,
        /// Cause.
        #[source]
        source: io::Error,
    },
    /// The file is larger than [`MAX_FILE_SIZE`].
    #[error("{path} is larger than {limit} bytes")]
    TooLarge {
        /// Full path of the file.
        path: PathBuf,
        /// The limit that was exceeded.
        limit: u64,
    },
}

impl ProcError {
    /// Whether the file doesn't exist – e.g. PSI on an older kernel. Callers skip such metrics.
    #[must_use]
    pub fn is_not_found(&self) -> bool {
        matches!(self, Self::Io { source, .. } if source.kind() == io::ErrorKind::NotFound)
    }
}

/// The directory that contains `proc/`, `sys/` and `etc/`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ProcRoot {
    root: PathBuf,
}

impl ProcRoot {
    /// The root of the host the agent runs on (`/`).
    #[must_use]
    pub fn host() -> Self {
        Self::new("/")
    }

    /// A root at `root`, e.g. a fixture directory.
    pub fn new(root: impl Into<PathBuf>) -> Self {
        Self { root: root.into() }
    }

    /// The root directory itself.
    #[must_use]
    pub fn root(&self) -> &Path {
        &self.root
    }

    /// Path of `relative` below `proc/`, e.g. `proc("stat")` → `/proc/stat`.
    ///
    /// # Errors
    ///
    /// [`ProcError::InvalidPath`] if `relative` is empty, absolute or contains `..`.
    pub fn proc(&self, relative: &str) -> Result<PathBuf, ProcError> {
        self.below("proc", relative)
    }

    /// Path of `relative` below `sys/`, e.g. `sys("class/net/eth0/speed")`.
    ///
    /// # Errors
    ///
    /// [`ProcError::InvalidPath`] if `relative` is empty, absolute or contains `..`.
    pub fn sys(&self, relative: &str) -> Result<PathBuf, ProcError> {
        self.below("sys", relative)
    }

    /// Path of `relative` below `etc/`, e.g. `etc("machine-id")`.
    ///
    /// # Errors
    ///
    /// [`ProcError::InvalidPath`] if `relative` is empty, absolute or contains `..`.
    pub fn etc(&self, relative: &str) -> Result<PathBuf, ProcError> {
        self.below("etc", relative)
    }

    /// Reads `proc/<relative>` as text.
    ///
    /// # Errors
    ///
    /// See [`ProcRoot::proc`] and [`read_bounded`].
    pub fn read_proc(&self, relative: &str) -> Result<String, ProcError> {
        read_bounded(&self.proc(relative)?)
    }

    /// Reads `sys/<relative>` as text.
    ///
    /// # Errors
    ///
    /// See [`ProcRoot::sys`] and [`read_bounded`].
    pub fn read_sys(&self, relative: &str) -> Result<String, ProcError> {
        read_bounded(&self.sys(relative)?)
    }

    /// Reads `etc/<relative>` as text.
    ///
    /// # Errors
    ///
    /// See [`ProcRoot::etc`] and [`read_bounded`].
    pub fn read_etc(&self, relative: &str) -> Result<String, ProcError> {
        read_bounded(&self.etc(relative)?)
    }

    fn below(&self, area: &str, relative: &str) -> Result<PathBuf, ProcError> {
        let path = Path::new(relative);
        let only_names = path
            .components()
            .all(|component| matches!(component, Component::Normal(_)));
        if relative.is_empty() || !only_names {
            return Err(ProcError::InvalidPath(relative.to_owned()));
        }
        Ok(self.root.join(area).join(path))
    }
}

/// Reads a text file of at most [`MAX_FILE_SIZE`] bytes; invalid UTF-8 is replaced.
///
/// `/proc` files report a size of 0, so the limit is enforced while reading, not via metadata.
///
/// # Errors
///
/// [`ProcError::Io`] if the file can't be read, [`ProcError::TooLarge`] above the limit.
pub fn read_bounded(path: &Path) -> Result<String, ProcError> {
    let io_error = |source| ProcError::Io {
        path: path.to_owned(),
        source,
    };
    let file = File::open(path).map_err(io_error)?;
    let mut bytes = Vec::new();
    file.take(MAX_FILE_SIZE + 1)
        .read_to_end(&mut bytes)
        .map_err(io_error)?;
    if u64::try_from(bytes.len()).map_or(true, |len| len > MAX_FILE_SIZE) {
        return Err(ProcError::TooLarge {
            path: path.to_owned(),
            limit: MAX_FILE_SIZE,
        });
    }
    Ok(match String::from_utf8(bytes) {
        Ok(text) => text,
        Err(error) => String::from_utf8_lossy(error.as_bytes()).into_owned(),
    })
}

#[cfg(test)]
mod tests {
    use std::fs;

    use super::*;

    fn fixture() -> (tempfile::TempDir, ProcRoot) {
        let dir = tempfile::tempdir().unwrap();
        fs::create_dir_all(dir.path().join("proc/net")).unwrap();
        fs::create_dir_all(dir.path().join("sys/class/net/eth0")).unwrap();
        fs::create_dir_all(dir.path().join("etc")).unwrap();
        fs::write(
            dir.path().join("proc/loadavg"),
            "0.10 0.20 0.30 1/123 4567\n",
        )
        .unwrap();
        fs::write(dir.path().join("sys/class/net/eth0/speed"), "1000\n").unwrap();
        fs::write(dir.path().join("etc/machine-id"), "0123456789abcdef\n").unwrap();
        let root = ProcRoot::new(dir.path());
        (dir, root)
    }

    #[test]
    fn host_root_maps_to_the_real_file_system() {
        let root = ProcRoot::host();

        assert_eq!(root.proc("stat").unwrap(), PathBuf::from("/proc/stat"));
        assert_eq!(
            root.sys("class/net/eth0/speed").unwrap(),
            PathBuf::from("/sys/class/net/eth0/speed")
        );
        assert_eq!(
            root.etc("os-release").unwrap(),
            PathBuf::from("/etc/os-release")
        );
    }

    #[test]
    fn reads_files_below_each_area() {
        let (_dir, root) = fixture();

        assert_eq!(
            root.read_proc("loadavg").unwrap(),
            "0.10 0.20 0.30 1/123 4567\n"
        );
        assert_eq!(root.read_sys("class/net/eth0/speed").unwrap(), "1000\n");
        assert_eq!(root.read_etc("machine-id").unwrap(), "0123456789abcdef\n");
    }

    #[test]
    fn rejects_paths_that_leave_their_area() {
        let root = ProcRoot::new("/fixtures/debian");

        for relative in [
            "",
            "/etc/shadow",
            "../etc/shadow",
            "net/../../etc/shadow",
            "./stat",
        ] {
            let result = root.proc(relative);
            assert!(
                matches!(result, Err(ProcError::InvalidPath(_))),
                "{relative:?} was accepted: {result:?}"
            );
        }
    }

    #[test]
    fn missing_files_are_reported_as_not_found() {
        let (_dir, root) = fixture();

        let error = root.read_proc("pressure/cpu").unwrap_err();

        assert!(error.is_not_found(), "{error}");
        assert!(!ProcError::InvalidPath(String::new()).is_not_found());
    }

    #[test]
    fn refuses_files_above_the_size_limit() {
        let (dir, root) = fixture();
        let limit = usize::try_from(MAX_FILE_SIZE).unwrap();
        fs::write(dir.path().join("proc/huge"), vec![b'x'; limit + 1]).unwrap();
        fs::write(dir.path().join("proc/exact"), vec![b'x'; limit]).unwrap();

        assert!(matches!(
            root.read_proc("huge"),
            Err(ProcError::TooLarge { .. })
        ));
        assert_eq!(root.read_proc("exact").unwrap().len(), limit);
    }

    #[test]
    fn replaces_invalid_utf8_instead_of_failing() {
        let (dir, root) = fixture();
        fs::write(dir.path().join("proc/comm"), b"bad\xffname\n").unwrap();

        assert_eq!(root.read_proc("comm").unwrap(), "bad\u{fffd}name\n");
    }
}
