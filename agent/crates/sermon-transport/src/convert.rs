//! Conversions between domain types (`sermon-core`) and protocol messages.

use sermon_core::{Architecture, HostFacts, Sample, Value};

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

impl From<&Sample> for pb::Sample {
    fn from(sample: &Sample) -> Self {
        Self {
            metric: sample.metric.to_owned(),
            labels: sample
                .labels
                .iter()
                .map(|(name, value)| ((*name).to_owned(), value.clone()))
                .collect(),
            value: Some(match sample.value {
                Value::Int(value) => pb::sample::Value::IntValue(value),
                Value::Float(value) => pb::sample::Value::DoubleValue(value),
            }),
        }
    }
}

#[cfg(test)]
mod tests {
    use sermon_core::MachineId;

    use super::*;

    #[test]
    fn samples_keep_name_labels_and_value_type() {
        let int = pb::Sample::from(&Sample::int("sermon_x", 7).label("cpu", "0"));
        let float = pb::Sample::from(&Sample::float("sermon_y", 0.5));

        assert_eq!(int.metric, "sermon_x");
        assert_eq!(int.labels.get("cpu").map(String::as_str), Some("0"));
        assert_eq!(int.value, Some(pb::sample::Value::IntValue(7)));
        assert_eq!(float.value, Some(pb::sample::Value::DoubleValue(0.5)));
    }

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
