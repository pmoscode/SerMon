//! Static facts about the monitored host, sent at enrollment and at the start of every session.

use std::fmt;

/// CPU architecture of the agent binary.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Architecture {
    /// x86-64.
    Amd64,
    /// `AArch64`.
    Arm64,
}

impl Architecture {
    /// The architecture this binary was built for; `None` if the agent doesn't support it.
    #[must_use]
    pub const fn current() -> Option<Self> {
        if cfg!(target_arch = "x86_64") {
            Some(Self::Amd64)
        } else if cfg!(target_arch = "aarch64") {
            Some(Self::Arm64)
        } else {
            None
        }
    }
}

/// The content of `/etc/machine-id`: 32 lowercase hex characters.
#[derive(Clone, PartialEq, Eq, Hash)]
pub struct MachineId(String);

/// The value is not a machine ID.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
#[error("not a machine-id (expected 32 hex characters)")]
pub struct InvalidMachineId;

impl MachineId {
    /// Parses a machine ID; surrounding whitespace is ignored, upper case is normalized.
    ///
    /// # Errors
    ///
    /// [`InvalidMachineId`] unless it is exactly 32 hex characters.
    pub fn parse(text: &str) -> Result<Self, InvalidMachineId> {
        let trimmed = text.trim();
        if trimmed.len() == 32 && trimmed.bytes().all(|byte| byte.is_ascii_hexdigit()) {
            Ok(Self(trimmed.to_ascii_lowercase()))
        } else {
            Err(InvalidMachineId)
        }
    }

    /// The ID as text.
    #[must_use]
    pub fn as_str(&self) -> &str {
        &self.0
    }
}

impl fmt::Debug for MachineId {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "MachineId({})", self.0)
    }
}

/// Where the agent finds the log lines for security events (ADR 0025, M2-31).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub enum SecuritySource {
    /// Not determined (e.g. at enrollment).
    #[default]
    Unknown,
    /// The systemd journal: every line carries its sender.
    Journal,
    /// The file of the SerMon rsyslog template, with the sender's user ID on every line.
    FileVerified,
    /// A classic log file: events are marked unverified.
    FileUnverified,
    /// No journald and no readable log file: no security events.
    None,
}

/// Static facts about the host.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct HostFacts {
    /// Host name as reported by the kernel.
    pub hostname: String,
    /// Stable ID of the installation (`/etc/machine-id`).
    pub machine_id: MachineId,
    /// `ID` from `/etc/os-release`, e.g. `debian`.
    pub os_id: String,
    /// `VERSION_ID` from `/etc/os-release`, e.g. `13`; empty for rolling releases.
    pub os_version: String,
    /// Kernel release (`uname -r`).
    pub kernel_release: String,
    /// CPU architecture of the agent binary.
    pub architecture: Architecture,
    /// Number of logical CPUs.
    pub cpu_count: u32,
    /// Total physical memory in bytes.
    pub memory_bytes: u64,
    /// Where security events come from; set by the agent, not by the facts collector.
    pub security_source: SecuritySource,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn machine_id_is_trimmed_and_lowercased() {
        let id = MachineId::parse(" 0123456789ABCDEF0123456789abcdef\n").unwrap();

        assert_eq!(id.as_str(), "0123456789abcdef0123456789abcdef");
    }

    #[test]
    fn rejects_malformed_machine_ids() {
        for text in [
            "",
            "uninitialized",
            "0123456789abcdef",
            "0123456789abcdef0123456789abcdefaa",
            "0123456789abcdef0123456789abcdeg",
        ] {
            assert_eq!(MachineId::parse(text), Err(InvalidMachineId), "{text}");
        }
    }

    #[test]
    fn current_architecture_is_known_on_supported_targets() {
        if cfg!(any(target_arch = "x86_64", target_arch = "aarch64")) {
            assert!(Architecture::current().is_some());
        }
    }
}
