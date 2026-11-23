//! Log entries in the agent (M3-01, KONZEPT §10): normalized records as protocol messages. Sending
//! them through the WAL follows with M3-04.

use sermon_logs::record::{LogRecord, Origin, Severity};
use sermon_transport::proto::agent::v1 as proto;

fn timestamp(us: u64) -> prost_types::Timestamp {
    prost_types::Timestamp {
        seconds: i64::try_from(us / 1_000_000).unwrap_or(i64::MAX),
        // < 1 000 000 000, fits.
        nanos: i32::try_from(us % 1_000_000 * 1000).unwrap_or(0),
    }
}

fn severity(severity: Option<Severity>) -> proto::LogSeverity {
    match severity {
        None => proto::LogSeverity::Unspecified,
        Some(Severity::Emergency) => proto::LogSeverity::Emergency,
        Some(Severity::Alert) => proto::LogSeverity::Alert,
        Some(Severity::Critical) => proto::LogSeverity::Critical,
        Some(Severity::Error) => proto::LogSeverity::Error,
        Some(Severity::Warning) => proto::LogSeverity::Warning,
        Some(Severity::Notice) => proto::LogSeverity::Notice,
        Some(Severity::Info) => proto::LogSeverity::Info,
        Some(Severity::Debug) => proto::LogSeverity::Debug,
    }
}

/// The record as a protocol message.
#[must_use]
pub fn to_proto(record: &LogRecord) -> proto::LogEntry {
    let (source, file) = match &record.origin {
        Origin::Journal => (proto::LogSource::Journal, String::new()),
        Origin::File(path) => (proto::LogSource::File, path.clone()),
    };
    let text = |value: &Option<String>| value.clone().unwrap_or_default();
    proto::LogEntry {
        occurred_at: Some(timestamp(record.at_us)),
        source: source.into(),
        unit: text(&record.unit),
        file,
        severity: severity(record.severity).into(),
        message: record.message.clone(),
        truncated: record.truncated,
        identifier: text(&record.identifier),
        pid: record.pid.unwrap_or(0),
        process: text(&record.process),
        uid: record.uid,
        boot_id: text(&record.boot_id),
        transport: text(&record.transport),
        facility: record.facility.map(u32::from),
    }
}

#[cfg(test)]
mod tests {
    use std::path::{Path, PathBuf};

    use prost::Message;
    use sermon_logs::file::{Format, LocalTime, parse};
    use sermon_logs::journal::{MAX_MESSAGE_BYTES, parse_line};
    use sermon_logs::record::MAX_BATCH_RECORDS;

    use super::*;

    fn fixtures() -> PathBuf {
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../fixtures/logs")
    }

    fn lines(data: &[u8]) -> impl Iterator<Item = &[u8]> {
        data.split(|b| *b == b'\n').filter(|l| !l.is_empty())
    }

    /// Records of the fixtures of every distro: journals, the two file formats of Alpine and an
    /// entry longer than a message may be.
    fn fixture_records() -> Vec<LogRecord> {
        let mut records = Vec::new();
        for distro in [
            "debian-13",
            "ubuntu-24.04",
            "rockylinux-9",
            "rockylinux-8",
            "almalinux-10",
            "fedora-44",
        ] {
            for file in [
                "journal-security.jsonl",
                "journal-security-variants.jsonl",
                "journal-oom.jsonl",
                "journal-format.jsonl",
            ] {
                let Ok(data) = std::fs::read(fixtures().join(distro).join(file)) else {
                    continue; // not captured for every distro
                };
                records.extend(
                    lines(&data).map(|line| LogRecord::from_journal(&parse_line(line).unwrap())),
                );
            }
        }
        let time = LocalTime::new(None, 1_795_291_200_000_000);
        for (name, format) in [
            ("messages", Format::Syslog),
            ("security.log", Format::Trusted),
        ] {
            let data = std::fs::read(fixtures().join("alpine-3.22").join(name)).unwrap();
            let origin = Origin::file(&Path::new("/var/log").join(name)).unwrap();
            for line in lines(&data) {
                let text = String::from_utf8_lossy(line);
                let record = parse(format, &text, &time).unwrap();
                records.push(LogRecord::from_file(origin.clone(), &record, false));
            }
        }
        let long = format!(
            r#"{{"__CURSOR":"s=a;i=1","__REALTIME_TIMESTAMP":"1795291200000000","MESSAGE":"{}","PRIORITY":"7"}}"#,
            "ü".repeat(MAX_MESSAGE_BYTES)
        );
        records.push(LogRecord::from_journal(
            &parse_line(long.as_bytes()).unwrap(),
        ));
        records
    }

    fn batch() -> proto::LogBatch {
        let records = fixture_records();
        assert!(records.len() <= MAX_BATCH_RECORDS, "{}", records.len());
        proto::LogBatch {
            entries: records.iter().map(to_proto).collect(),
        }
    }

    #[test]
    fn records_convert_field_by_field() {
        let record = LogRecord {
            at_us: 1_795_163_620_776_631,
            origin: Origin::File("/var/log/nginx/error.log".into()),
            unit: Some("nginx".into()),
            identifier: Some("nginx".into()),
            severity: Some(Severity::Error),
            message: "upstream timed out".into(),
            truncated: true,
            pid: Some(812),
            process: Some("nginx".into()),
            uid: Some(0),
            boot_id: Some("cc8f81a486f84386b00bc31030b4e1bb".into()),
            transport: Some("stdout".into()),
            facility: Some(3),
        };
        let entry = to_proto(&record);
        assert_eq!(
            entry.occurred_at,
            Some(prost_types::Timestamp {
                seconds: 1_795_163_620,
                nanos: 776_631_000
            })
        );
        assert_eq!(entry.source(), proto::LogSource::File);
        assert_eq!(entry.file, "/var/log/nginx/error.log");
        assert_eq!(entry.severity(), proto::LogSeverity::Error);
        assert_eq!(
            (
                entry.unit.as_str(),
                entry.identifier.as_str(),
                entry.process.as_str()
            ),
            ("nginx", "nginx", "nginx")
        );
        assert_eq!(entry.message, "upstream timed out");
        assert!(entry.truncated);
        assert_eq!(
            (entry.pid, entry.uid, entry.facility),
            (812, Some(0), Some(3))
        );
        assert_eq!(entry.boot_id, "cc8f81a486f84386b00bc31030b4e1bb");
        assert_eq!(entry.transport, "stdout");

        let journal = to_proto(&LogRecord {
            origin: Origin::Journal,
            severity: None,
            pid: None,
            uid: None,
            facility: None,
            ..record
        });
        assert_eq!(journal.source(), proto::LogSource::Journal);
        assert_eq!(journal.file, "", "no file for the journal");
        assert_eq!(journal.severity(), proto::LogSeverity::Unspecified);
        assert_eq!(
            (journal.pid, journal.uid, journal.facility),
            (0, None, None)
        );
    }

    #[test]
    fn every_severity_has_its_own_value() {
        let all = [
            Severity::Emergency,
            Severity::Alert,
            Severity::Critical,
            Severity::Error,
            Severity::Warning,
            Severity::Notice,
            Severity::Info,
            Severity::Debug,
        ];
        let values: Vec<i32> = all.iter().map(|s| severity(Some(*s)).into()).collect();
        assert_eq!(values, (1..=8).collect::<Vec<_>>(), "priority + 1");
        assert_eq!(i32::from(severity(None)), 0);
    }

    #[test]
    fn the_fixture_batch_roundtrips() {
        let batch = batch();
        let decoded = proto::LogBatch::decode(batch.encode_to_vec().as_slice()).unwrap();
        assert_eq!(decoded, batch);
        assert!(batch.entries.iter().any(|e| e.truncated));
        assert!(
            batch
                .entries
                .iter()
                .any(|e| e.source() == proto::LogSource::File)
        );
    }

    // The server validates what the agent sends (protovalidate, Go): the encoded batch of the
    // fixtures is checked in server/internal/transport/agentapi/schema_test.go.
    // SERMON_UPDATE_GOLDEN=1 rewrites the file after a deliberate change.
    #[test]
    fn golden_batch_for_the_server_schema_test() {
        let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
            .join("../../../proto/testdata/log_batch.binpb");
        let encoded = batch().encode_to_vec();
        if std::env::var_os("SERMON_UPDATE_GOLDEN").is_some() {
            std::fs::create_dir_all(path.parent().unwrap()).unwrap();
            std::fs::write(&path, &encoded).unwrap();
        }
        let golden = std::fs::read(&path).unwrap_or_default();
        assert!(
            golden == encoded,
            "{} is stale – run with SERMON_UPDATE_GOLDEN=1",
            path.display()
        );
    }
}
