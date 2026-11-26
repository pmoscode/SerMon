use std::collections::BTreeMap;
use std::path::Path;
use std::sync::Mutex;

use sermon_core::Value as V;
use sermon_helper_proto::v1::{Command, HelperResponse, Smart as Answered};

use super::*;
use crate::{Host, Schema, Secrets, Veto};

fn fixture(version: &str, name: &str) -> Vec<u8> {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../fixtures/modules/smart")
        .join(version)
        .join(format!("{name}.json"));
    std::fs::read(&path).unwrap_or_else(|e| panic!("{}: {e}", path.display()))
}

fn read(version: &str, name: &str, device: &str) -> Disk {
    parse(
        &format!("/usr/sbin/smartctl --json=c -i -H -A -n standby /dev/{device}"),
        &fixture(version, name),
    )
    .unwrap()
}

#[test]
fn its_schema_is_in_the_subset() {
    let schema = Schema::parse(SCHEMA).unwrap();
    assert_eq!(schema.secrets().count(), 0);
    let config = schema
        .validate(r#"{"exclude":["sdb","nvme1n1"]}"#, &[])
        .unwrap();
    assert_eq!(config["exclude"][1], "nvme1n1");
    for bad in [
        r#"{"exclude":["../sda"]}"#,
        r#"{"exclude":["sda1"]}"#,
        r#"{"x":1}"#,
    ] {
        assert!(schema.validate(bad, &[]).is_err(), "{bad}");
    }
}

// Every supported version of smartmontools: a SATA SSD and an NVMe SSD.
#[test]
fn reads_sata_and_nvme_of_every_version() {
    for version in ["7.2", "7.4", "7.5"] {
        let sata = read(version, "sata-ssd", "sda");
        assert_eq!(
            sata,
            Disk {
                device: "sda".into(),
                protocol: "ata",
                model: "Samsung SSD 870 EVO 1TB".into(),
                read: Read::Data,
                passed: Some(true),
                temperature: Some(34),
                power_on_hours: Some(21874),
                power_cycles: Some(412),
                reallocated: Some(0),
                pending: None,
                uncorrectable: None,
                crc_errors: Some(2),
                ..Disk::default()
            },
            "{version}"
        );
        let nvme = read(version, "nvme", "nvme0n1");
        assert_eq!(
            nvme,
            Disk {
                device: "nvme0n1".into(),
                protocol: "nvme",
                model: "Samsung SSD 980 PRO 1TB".into(),
                read: Read::Data,
                passed: Some(true),
                temperature: Some(41),
                power_on_hours: Some(9213),
                power_cycles: Some(603),
                media_errors: Some(0),
                critical_warning: Some(0),
                spare_percent: Some(100),
                used_percent: Some(3),
                ..Disk::default()
            },
            "{version}"
        );
        let asleep = read(version, "standby", "sdc");
        assert_eq!(
            (asleep.read, asleep.passed),
            (Read::Standby, None),
            "{version}"
        );
        let failed = read(version, "open-failed", "sda");
        assert_eq!(failed.read, Read::Unreadable, "{version}");
    }
}

#[test]
fn reads_failing_disks() {
    let hdd = read("7.4", "hdd", "sdb");
    assert_eq!(
        (hdd.passed, hdd.reallocated, hdd.pending, hdd.uncorrectable),
        (Some(true), Some(0), Some(0), Some(0))
    );
    let failing = read("7.4", "hdd-failing", "sdb");
    assert_eq!(
        (
            failing.passed,
            failing.reallocated,
            failing.pending,
            failing.uncorrectable
        ),
        (Some(false), Some(1912), Some(24), Some(8))
    );
    for version in ["7.4", "7.5"] {
        let worn = read(version, "nvme-worn", "nvme1n1");
        assert_eq!(
            (
                worn.passed,
                worn.critical_warning,
                worn.spare_percent,
                worn.used_percent,
                worn.media_errors
            ),
            (Some(false), Some(4), Some(6), Some(104), Some(17)),
            "{version}"
        );
    }
    for version in ["7.2", "7.4"] {
        let sas = read(version, "sas", "sdd");
        assert_eq!(
            (
                sas.protocol,
                sas.passed,
                sas.temperature,
                sas.reallocated,
                sas.power_on_hours
            ),
            ("scsi", Some(true), Some(31), Some(3), Some(58210)),
            "{version}"
        );
    }
}

// Foreign data: nothing panics, nothing out of range or of another protocol is taken.
#[test]
fn takes_only_what_holds() {
    let name = "/usr/sbin/smartctl /dev/sda";
    for broken in [
        &b""[..],
        b"{",
        b"[]",
        b"null",
        br#"{"smart_status":{"passed":"yes"},"temperature":{"current":"hot"}}"#,
        br#"{"smartctl":{"exit_status":2,"messages":[{"string":"Device is in STANDBY mode"}]}}"#,
    ] {
        let d = parse(name, broken).unwrap();
        assert_eq!(
            d.read,
            if broken.starts_with(br#"{"smartctl"#) {
                Read::Standby
            } else {
                Read::Unreadable
            },
            "{}",
            String::from_utf8_lossy(broken)
        );
    }
    let wild = br#"{"device":{"protocol":"ATA"},"model_name":"evil\u001b[2Jdisk\u0000 with a very long name that goes on and on and on beyond sixty-four bytes",
        "smart_status":{"passed":true},"temperature":{"current":9999},"power_on_time":{"hours":-5},
        "power_cycle_count":1e30,"ata_smart_attributes":{"table":[{"id":5,"raw":{"value":4294967298}},{"id":197,"raw":{"value":-1}}]},
        "nvme_smart_health_information_log":{"media_errors":5}}"#;
    let d = parse(name, wild).unwrap();
    assert_eq!(d.read, Read::Data);
    assert!(
        d.model.starts_with("evil[2Jdisk with") && d.model.len() <= MAX_MODEL,
        "{}",
        d.model
    );
    assert_eq!(
        (d.temperature, d.power_on_hours, d.power_cycles),
        (None, None, None)
    );
    assert_eq!(
        (d.reallocated, d.pending),
        (Some(2), None),
        "the low 32 bits"
    );
    assert_eq!(d.media_errors, None, "an ATA disk has no NVMe log");
    let big = [
        b"{\"smart_status\":{\"passed\":true},\"x\":\"".as_slice(),
        &vec![b'a'; MAX_OUTPUT],
        b"\"}",
    ]
    .concat();
    assert_eq!(parse(name, &big).unwrap().read, Read::Unreadable);
    // Only whole disks of /dev, as the helper names them.
    for name in [
        "smartctl /dev/sda1",
        "smartctl /dev/../etc/passwd",
        "smartctl sda",
        "smartctl /dev/nvme0",
        "",
    ] {
        assert!(parse(name, b"{}").is_none(), "{name}");
    }
}

#[test]
fn samples_stay_within_the_runtimes_bounds() {
    let disks: Vec<Disk> = (0..30)
        .map(|i| Disk {
            device: format!("sd{}", char::from(b'a' + u8::try_from(i % 26).unwrap())),
            protocol: "ata",
            read: Read::Data,
            passed: Some(true),
            temperature: Some(30),
            power_on_hours: Some(2),
            power_cycles: Some(1),
            reallocated: Some(0),
            pending: Some(0),
            uncorrectable: Some(0),
            crc_errors: Some(0),
            ..Disk::default()
        })
        .collect();
    let samples = samples(&disks, false);
    assert!(
        samples.len() <= crate::runtime::MAX_SAMPLES,
        "{}",
        samples.len()
    );
    assert_eq!(samples[0].value, V::Int(i64::try_from(MAX_DISKS).unwrap()));
    assert_eq!(samples[1].value, V::Int(1), "truncated");
    let one = samples_of(&read("7.5", "nvme", "nvme0n1"));
    assert_eq!(one["sermon_smart_power_on_seconds"], V::Int(9213 * 3600));
    assert_eq!(one["sermon_smart_endurance_used_ratio"], V::Int(30_000));
    assert_eq!(one["sermon_smart_spare_ratio"], V::Int(1_000_000));
    assert_eq!(one["sermon_smart_readable"], V::Int(1));
    assert_eq!(one.len(), 10);
    let asleep = samples_of(&read("7.4", "standby", "sdc"));
    assert_eq!(
        (
            asleep["sermon_smart_standby"],
            asleep["sermon_smart_readable"],
            asleep.len()
        ),
        (V::Int(1), V::Int(0), 2)
    );
}

fn samples_of(d: &Disk) -> BTreeMap<&'static str, V> {
    samples(std::slice::from_ref(d), false)
        .into_iter()
        .skip(2)
        .map(|s| {
            assert_eq!(s.labels[0], ("device", d.device.clone()));
            (s.metric, s.value)
        })
        .collect()
}

/// A helper that answers every request with `answer`.
struct Fake {
    answer: Mutex<Result<HelperResponse, String>>,
    asked: Mutex<Vec<HelperRequest>>,
}

impl Helper for Fake {
    fn call(&self, request: &HelperRequest) -> Result<HelperResponse, String> {
        self.asked.lock().unwrap().push(request.clone());
        self.answer.lock().unwrap().clone()
    }
}

fn instance(
    answer: Result<HelperResponse, String>,
    config: &str,
) -> (Box<dyn Instance>, Arc<Fake>) {
    let fake = Arc::new(Fake {
        answer: Mutex::new(answer),
        asked: Mutex::default(),
    });
    let host = Host {
        helper: Some(fake.clone()),
        ..Host::default()
    };
    let config = Schema::parse(SCHEMA)
        .unwrap()
        .validate(config, &[])
        .unwrap();
    let setup = Setup {
        veto: &Veto::default(),
        secrets: &Secrets::default(),
        host: &host,
    };
    (Smart.build(&config, &setup).unwrap(), fake)
}

fn answer(status: Status, reason: &str, smart: Option<Answered>) -> HelperResponse {
    HelperResponse {
        status: status.into(),
        reason: reason.into(),
        result: smart.map(Answer::Smart),
        ..HelperResponse::default()
    }
}

#[tokio::test]
async fn asks_the_helper_and_reports_its_refusals() {
    let disks = Answered {
        disks: vec![
            Command {
                name: "/usr/sbin/smartctl --json=c -i -H -A -n standby /dev/sda".into(),
                output: fixture("7.4", "sata-ssd"),
                ..Command::default()
            },
            Command {
                name: "/usr/sbin/smartctl --json=c -i -H -A -n standby /dev/sdb".into(),
                output: fixture("7.4", "hdd"),
                ..Command::default()
            },
        ],
        truncated: false,
    };
    let (mut ok, fake) = instance(
        Ok(answer(Status::Ok, "", Some(disks.clone()))),
        r#"{"exclude":["sdb"]}"#,
    );
    let samples = ok.collect(&Run::default()).await.unwrap();
    assert_eq!(samples[0].value, V::Int(1), "sdb left out");
    assert!(samples.iter().skip(2).all(|s| s.labels[0].1 == "sda"));
    assert!(matches!(
        fake.asked.lock().unwrap()[0].operation,
        Some(Operation::ReadSmart(_))
    ));
    let (mut vetoed, _) = instance(
        Ok(answer(
            Status::Refused,
            "allow_smart = false in /etc/sermon/helper.toml",
            None,
        )),
        "{}",
    );
    assert!(
        matches!(vetoed.collect(&Run::default()).await, Err(RunError::Vetoed(r)) if r.0.contains("allow_smart"))
    );
    for (response, why) in [
        (
            Ok(answer(
                Status::Unsupported,
                "no /usr/sbin/smartctl: install smartmontools",
                None,
            )),
            "smartmontools",
        ),
        (
            Ok(answer(
                Status::Unsupported,
                "unknown operation: update the helper",
                None,
            )),
            "update the helper",
        ),
        (
            Ok(answer(
                Status::Busy,
                "SMART was read less than a minute ago",
                None,
            )),
            "a minute",
        ),
        (Ok(answer(Status::Ok, "", None)), "no SMART data"),
        (
            Err("no root helper at /run/sermon/helper.sock".into()),
            "no root helper",
        ),
    ] {
        let (mut m, _) = instance(response, "{}");
        let Err(RunError::Failed(reason)) = m.collect(&Run::default()).await else {
            panic!("{why}");
        };
        assert!(reason.contains(why), "{reason}");
    }
}

#[test]
fn needs_a_helper() {
    let config = Schema::parse(SCHEMA).unwrap().validate("{}", &[]).unwrap();
    let setup = Setup {
        veto: &Veto::default(),
        secrets: &Secrets::default(),
        host: &Host::default(),
    };
    assert!(
        matches!(Smart.build(&config, &setup), Err(BuildError::Invalid(why)) if why.contains("root helper"))
    );
}

// M5-04: smartd (Debian names its unit smartmontools.service) suggests the module.
#[test]
fn smartd_suggests_the_module() {
    use crate::discovery::{HostFacts, Reason};
    assert!(Smart.discover(&HostFacts::default()).is_none());
    let facts = HostFacts {
        processes: ["smartd".to_owned()].into(),
        units: ["smartmontools.service".to_owned()].into(),
        ..HostFacts::default()
    };
    let found = Smart.discover(&facts).unwrap();
    assert_eq!(
        found.reasons,
        [
            Reason::Process("smartd"),
            Reason::Unit("smartmontools.service")
        ]
    );
}

// ADR 0039, M5-06: every metric the module sends is in metrics/smart.json, which the server takes.
#[test]
fn every_metric_is_in_the_catalog() {
    let disk = Disk {
        device: "nvme0n1".into(),
        protocol: "nvme",
        model: "Disk".into(),
        read: Read::Data,
        passed: Some(true),
        temperature: Some(40),
        power_on_hours: Some(1),
        power_cycles: Some(1),
        reallocated: Some(0),
        pending: Some(0),
        uncorrectable: Some(0),
        crc_errors: Some(0),
        media_errors: Some(0),
        critical_warning: Some(0),
        spare_percent: Some(100),
        used_percent: Some(3),
    };
    let samples = samples(&[disk], false);
    assert_eq!(samples.len(), 16);
    crate::assert_in_catalog("smart", &samples);
}
