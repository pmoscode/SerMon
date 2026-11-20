//! Layout of the agent's state directory (`/var/lib/sermon-agent`).
//!
//! The agent only ever removes the entries listed here, never the directory itself: it may be a
//! mount point or contain files of the administrator.

use std::io;
use std::path::{Path, PathBuf};

/// File name of the private key in the identity directory.
pub const KEY_FILE: &str = "agent.key";
/// File name of the certificate chain in the identity directory.
pub const CERTIFICATE_FILE: &str = "agent.crt";
/// File name of the trust bundle in the identity directory.
pub const TRUST_BUNDLE_FILE: &str = "trust.pem";
/// File name of the identity metadata in the identity directory.
pub const IDENTITY_FILE: &str = "identity.toml";

/// Paths of the files and directories the agent keeps in its state directory.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct StateLayout {
    root: PathBuf,
}

impl StateLayout {
    /// The layout below `root`.
    pub fn new(root: impl Into<PathBuf>) -> Self {
        Self { root: root.into() }
    }

    /// The state directory itself.
    #[must_use]
    pub fn root(&self) -> &Path {
        &self.root
    }

    /// Directory with the agent's identity: key, certificates, trust bundle.
    #[must_use]
    pub fn identity_dir(&self) -> PathBuf {
        self.root.join("identity")
    }

    /// Private key of the agent (PKCS#8 PEM, mode 0600).
    #[must_use]
    pub fn key_file(&self) -> PathBuf {
        self.identity_dir().join(KEY_FILE)
    }

    /// Client certificate followed by the issuing CA certificates (PEM).
    #[must_use]
    pub fn certificate_file(&self) -> PathBuf {
        self.identity_dir().join(CERTIFICATE_FILE)
    }

    /// Roots the server certificate of the agent API must chain to (PEM).
    #[must_use]
    pub fn trust_bundle_file(&self) -> PathBuf {
        self.identity_dir().join(TRUST_BUNDLE_FILE)
    }

    /// Node, organization and certificate expiry (TOML).
    #[must_use]
    pub fn identity_file(&self) -> PathBuf {
        self.identity_dir().join(IDENTITY_FILE)
    }

    /// Write-ahead log with data not yet acknowledged by the server.
    #[must_use]
    pub fn wal_dir(&self) -> PathBuf {
        self.root.join("wal")
    }

    /// Cursor of the journal source: the last entry whose results are stored (ADR 0013).
    #[must_use]
    pub fn journal_cursor_file(&self) -> PathBuf {
        self.root.join("journal.cursor")
    }

    /// Removes everything the agent created (identity, buffer, journal cursor) and returns what
    /// existed.
    ///
    /// Symbolic links are removed themselves, never followed.
    ///
    /// # Errors
    ///
    /// The first error while removing; entries removed before stay removed.
    pub fn remove_agent_data(&self) -> io::Result<Vec<PathBuf>> {
        let mut removed = Vec::new();
        for entry in [
            self.identity_dir(),
            self.wal_dir(),
            self.journal_cursor_file(),
        ] {
            let metadata = match std::fs::symlink_metadata(&entry) {
                Ok(metadata) => metadata,
                Err(error) if error.kind() == io::ErrorKind::NotFound => continue,
                Err(error) => return Err(error),
            };
            if metadata.is_dir() {
                std::fs::remove_dir_all(&entry)?;
            } else {
                std::fs::remove_file(&entry)?;
            }
            removed.push(entry);
        }
        Ok(removed)
    }
}

#[cfg(test)]
mod tests {
    use std::fs;

    use super::*;

    #[test]
    fn removes_identity_and_buffer_but_nothing_else() {
        let dir = tempfile::tempdir().unwrap();
        let layout = StateLayout::new(dir.path());
        fs::create_dir_all(layout.identity_dir()).unwrap();
        fs::write(layout.identity_dir().join("agent.key"), "key").unwrap();
        fs::create_dir_all(layout.wal_dir().join("segments")).unwrap();
        fs::write(layout.journal_cursor_file(), "s=a;i=1").unwrap();
        fs::write(dir.path().join("notes.txt"), "admin's file").unwrap();

        let removed = layout.remove_agent_data().unwrap();

        assert_eq!(
            removed,
            vec![
                layout.identity_dir(),
                layout.wal_dir(),
                layout.journal_cursor_file()
            ]
        );
        assert!(!layout.journal_cursor_file().exists());
        assert!(!layout.identity_dir().exists());
        assert!(!layout.wal_dir().exists());
        assert!(dir.path().join("notes.txt").exists());
    }

    #[test]
    fn missing_entries_are_not_an_error() {
        let dir = tempfile::tempdir().unwrap();
        let layout = StateLayout::new(dir.path());

        assert_eq!(layout.remove_agent_data().unwrap(), Vec::<PathBuf>::new());
    }

    #[cfg(unix)]
    #[test]
    fn symlinks_are_removed_without_following_them() {
        let dir = tempfile::tempdir().unwrap();
        let elsewhere = tempfile::tempdir().unwrap();
        fs::write(elsewhere.path().join("precious"), "keep").unwrap();
        let layout = StateLayout::new(dir.path());
        std::os::unix::fs::symlink(elsewhere.path(), layout.identity_dir()).unwrap();

        let removed = layout.remove_agent_data().unwrap();

        assert_eq!(removed, vec![layout.identity_dir()]);
        assert!(elsewhere.path().join("precious").exists());
    }
}
