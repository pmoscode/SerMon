//! Conversions between domain types (`sermon-core`) and protocol messages.

use sermon_core::{Architecture, HostFacts};

use crate::proto::agent::v1 as pb;

impl From<&HostFacts> for pb::HostFacts {
    fn from(facts: &HostFacts) -> Self {
        Self {
            hostname: facts.hostname.clone(),
            machine_id: facts.machine_id.as_str().to_owned(),
            os_id: facts.os_id.clone(),
            os_version: facts.os_version.clone(),
            kernel_release: facts.kernel_release.clone(),
            architecture: pb::Architecture::from(facts.architecture).into(),
            cpu_count: facts.cpu_count,
            memory_bytes: facts.memory_bytes,
        }
    }
}

impl From<Architecture> for pb::Architecture {
    fn from(architecture: Architecture) -> Self {
        match architecture {
            Architecture::Amd64 => Self::Amd64,
            Architecture::Arm64 => Self::Arm64,
        }
    }
}

#[cfg(test)]
mod tests {
    use sermon_core::MachineId;

    use super::*;

    #[test]
    fn host_facts_are_copied_field_by_field() {
        let facts = HostFacts {
            hostname: "web-01".into(),
            machine_id: MachineId::parse("0123456789abcdef0123456789abcdef").unwrap(),
            os_id: "debian".into(),
            os_version: "13".into(),
            kernel_release: "6.12.0-1-amd64".into(),
            architecture: Architecture::Arm64,
            cpu_count: 4,
            memory_bytes: 8 << 30,
        };

        let message = pb::HostFacts::from(&facts);

        assert_eq!(
            message,
            pb::HostFacts {
                hostname: "web-01".into(),
                machine_id: "0123456789abcdef0123456789abcdef".into(),
                os_id: "debian".into(),
                os_version: "13".into(),
                kernel_release: "6.12.0-1-amd64".into(),
                architecture: pb::Architecture::Arm64.into(),
                cpu_count: 4,
                memory_bytes: 8 << 30,
            }
        );
    }
}
