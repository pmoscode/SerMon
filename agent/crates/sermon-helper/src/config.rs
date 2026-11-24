//! `/etc/sermon/helper.toml`: what the host allows (ADR 0035). Only root may change it; the server
//! can't. A file that others could write is refused, and the helper doesn't start.

use std::fs;
use std::io;
use std::os::unix::fs::MetadataExt;
use std::path::Path;

use serde::Deserialize;

/// Where the configuration lives.
pub const PATH: &str = "/etc/sermon/helper.toml";

/// The local permissions; without a file the defaults of ADR 0035.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields, default)]
#[allow(
    clippy::struct_excessive_bools,
    reason = "three independent switches of helper.toml"
)]
pub struct Config {
    /// `ApplyUpdates`: packages only from the distribution's signed sources.
    pub allow_remote_updates: bool,
    /// `Reboot`: off, a reboot of the whole fleet would be the worst a compromised server can do.
    pub allow_remote_reboot: bool,
    /// `SelfUpdate`: only binaries signed with the release key (ADR 0036).
    pub allow_self_update: bool,
}

impl Default for Config {
    fn default() -> Self {
        Self {
            allow_remote_updates: true,
            allow_remote_reboot: false,
            allow_self_update: true,
        }
    }
}

/// Why the configuration can't be used.
#[derive(Debug)]
pub enum ConfigError {
    /// It (or its directory) is not owned by root, or writable by group or others (a symlink).
    Unsafe(String),
    /// It can't be read.
    Io(io::Error),
    /// It is not valid (unknown key, wrong type).
    Invalid(String),
}

impl std::fmt::Display for ConfigError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Unsafe(why) => write!(f, "unsafe configuration: {why}"),
            Self::Io(e) => write!(f, "configuration not readable: {e}"),
            Self::Invalid(why) => write!(f, "invalid configuration: {why}"),
        }
    }
}

/// Whether root alone can change a file or directory with this owner and mode.
#[must_use]
pub fn only_root_writes(uid: u32, mode: u32) -> bool {
    uid == 0 && mode & 0o022 == 0
}

/// Reads the configuration at `path`; a missing file gives the defaults, but its directory must
/// be safe either way.
///
/// # Errors
///
/// [`ConfigError`] if file or directory are unsafe, unreadable or the file is invalid.
pub fn load(path: &Path) -> Result<Config, ConfigError> {
    let dir = path.parent().unwrap_or(Path::new("/"));
    for (what, p) in [("directory", dir), ("file", path)] {
        let meta = match fs::symlink_metadata(p) {
            Ok(m) => m,
            Err(e) if e.kind() == io::ErrorKind::NotFound && what == "file" => {
                return Ok(Config::default());
            }
            Err(e) => return Err(ConfigError::Io(e)),
        };
        // A symlink has the mode 0777 on Linux: refused like any file others could write.
        if !only_root_writes(meta.uid(), meta.mode()) {
            return Err(ConfigError::Unsafe(format!(
                "{} must belong to root and not be writable by group or others",
                p.display()
            )));
        }
    }
    let text = fs::read_to_string(path).map_err(ConfigError::Io)?;
    toml::from_str(&text).map_err(|e| ConfigError::Invalid(e.message().to_owned()))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn defaults_and_values() {
        assert_eq!(
            Config::default(),
            Config {
                allow_remote_updates: true,
                allow_remote_reboot: false,
                allow_self_update: true
            }
        );
        let c: Config =
            toml::from_str("allow_remote_reboot = true\nallow_self_update = false").unwrap();
        assert_eq!(
            c,
            Config {
                allow_remote_updates: true,
                allow_remote_reboot: true,
                allow_self_update: false
            }
        );
        // A typo must not silently leave the default in place.
        assert!(toml::from_str::<Config>("allow_remote_reboots = true").is_err());
        assert!(toml::from_str::<Config>("allow_remote_reboot = \"yes\"").is_err());
    }

    #[test]
    fn only_root_may_write() {
        assert!(only_root_writes(0, 0o100_644));
        assert!(only_root_writes(0, 0o040_750));
        assert!(!only_root_writes(0, 0o100_664));
        assert!(!only_root_writes(0, 0o100_646));
        assert!(!only_root_writes(1000, 0o100_644));
    }

    // In a temporary directory the owner is the test user: refused unless the tests run as root.
    #[test]
    fn refuses_files_others_can_change() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("helper.toml");
        fs::write(&path, "allow_remote_reboot = true").unwrap();
        let root = rustix::process::geteuid().is_root();
        assert_eq!(load(&path).is_ok(), root);
        let link = dir.path().join("link.toml");
        std::os::unix::fs::symlink(&path, &link).unwrap();
        assert!(matches!(load(&link), Err(ConfigError::Unsafe(_))));
        if !root {
            assert!(matches!(load(&path), Err(ConfigError::Unsafe(why)) if why.contains("root")));
            // Missing: the defaults, but only below a safe directory.
            assert!(matches!(
                load(&dir.path().join("none.toml")),
                Err(ConfigError::Unsafe(_))
            ));
        }
        assert_eq!(
            load(Path::new("/sermon-helper-test-none.toml")).unwrap(),
            Config::default()
        );
        // Without its directory the installation is broken: no start.
        assert!(matches!(
            load(Path::new("/sermon-helper-test-none/helper.toml")),
            Err(ConfigError::Io(_))
        ));
    }
}
