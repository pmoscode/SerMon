//! SMART of the host's disks (M5-18, ADR 0035 addendum): the root helper runs a fixed,
//! read-only `smartctl --json` for each whole disk; the output is parsed here, without rights.
//! A disk in standby stays asleep and is reported as such.
//!
//! Metrics (label `device`, e.g. `sda`; `sermon_smart_readable` also `protocol` and `model`):
//!
//! | Metric | Meaning |
//! |---|---|
//! | `sermon_smart_disks` | disks the helper read (no label) |
//! | `sermon_smart_truncated` | 1: the helper's bounds (24 disks, 20 s) left disks out (no label) |
//! | `sermon_smart_readable` | 1: SMART data read; 0: in standby or not readable |
//! | `sermon_smart_standby` | 1: the disk sleeps and was not woken |
//! | `sermon_smart_passed` | the disk's overall self-assessment: 1 passed, 0 failed |
//! | `sermon_smart_temperature_celsius` | current temperature |
//! | `sermon_smart_power_on_seconds` | power-on time |
//! | `sermon_smart_power_cycles` | power cycles |
//! | `sermon_smart_reallocated_sectors` | ATA 5 (SCSI: grown defects) |
//! | `sermon_smart_pending_sectors` | ATA 197 |
//! | `sermon_smart_uncorrectable_sectors` | ATA 198 |
//! | `sermon_smart_crc_errors` | ATA 199 (cable, connector) |
//! | `sermon_smart_media_errors` | NVMe media and data integrity errors |
//! | `sermon_smart_critical_warning` | NVMe critical warning (bit field, 0 is fine) |
//! | `sermon_smart_spare_ratio` | NVMe available spare, ppm |
//! | `sermon_smart_endurance_used_ratio` | NVMe percentage used, ppm (may pass 1 000 000) |

use std::sync::Arc;
use std::time::Duration;

use serde_json::Value;
use sermon_core::Sample;
use sermon_helper_proto::v1::{Status, helper_response::Result as Answer};

use crate::discovery::{Discovered, HostFacts};
use crate::{
    BoxFuture, BuildError, Config, Helper, Instance, Module, Refused, Run, RunError, Setup,
};

const SCHEMA: &str = include_str!("../schemas/smart.json");
/// Most disks reported: 10 samples each stay below the runtime's 256.
pub const MAX_DISKS: usize = 24;
/// Largest output of one disk taken (the helper sends at most this).
const MAX_OUTPUT: usize = 128 << 10;
const MAX_MODEL: usize = 64;
/// Largest count taken: 48 bits, as SMART's raw values.
const COUNT: i64 = 1 << 48;

/// The module `smart`.
#[derive(Debug, Clone, Copy, Default)]
pub struct Smart;

impl Module for Smart {
    fn id(&self) -> &'static str {
        "smart"
    }

    fn schema(&self) -> &'static str {
        SCHEMA
    }

    fn default_interval(&self) -> Duration {
        Duration::from_mins(10)
    }

    fn build(&self, config: &Config, setup: &Setup<'_>) -> Result<Box<dyn Instance>, BuildError> {
        let helper = setup.host.helper.clone().ok_or_else(|| {
            BuildError::Invalid("this agent has no root helper to read SMART".into())
        })?;
        let exclude = config
            .get("exclude")
            .and_then(Value::as_array)
            .map_or_else(Vec::new, |a| {
                a.iter()
                    .filter_map(Value::as_str)
                    .map(str::to_owned)
                    .collect()
            });
        Ok(Box::new(Reader { helper, exclude }))
    }

    /// smartd runs: the host has smartmontools and disks that know SMART.
    fn discover(&self, facts: &HostFacts) -> Option<Discovered> {
        Discovered::evidence(
            facts,
            &["smartd"],
            &["smartd.service", "smartmontools.service"],
            &[],
        )
    }
}

struct Reader {
    helper: Arc<dyn Helper>,
    exclude: Vec<String>,
}

impl Instance for Reader {
    /// The helper takes up to 20 s for all disks.
    fn timeout(&self) -> Duration {
        Duration::from_secs(30)
    }

    fn collect<'a>(&'a mut self, run: &'a Run) -> BoxFuture<'a, Result<Vec<Sample>, RunError>> {
        Box::pin(async move {
            let helper = self.helper.clone();
            let response = run
                .blocking(move || helper.read_smart())
                .await?
                .map_err(RunError::Failed)?;
            let smart = match (response.status(), response.result) {
                (Status::Ok, Some(Answer::Smart(smart))) => smart,
                (Status::Refused, _) => return Err(RunError::Vetoed(Refused(response.reason))),
                (Status::Ok, _) => {
                    return Err(RunError::Failed(
                        "the root helper sent no SMART data".into(),
                    ));
                }
                (_, _) => return Err(RunError::Failed(response.reason)),
            };
            let disks: Vec<Disk> = smart
                .disks
                .iter()
                .filter_map(|c| parse(&c.name, &c.output))
                .filter(|d| !self.exclude.contains(&d.device))
                .collect();
            Ok(samples(&disks, smart.truncated))
        })
    }
}

/// How smartctl read a disk.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub enum Read {
    /// SMART data came.
    Data,
    /// The disk sleeps; smartctl didn't wake it.
    Standby,
    /// Nothing usable: the device didn't open, has no SMART or the output is broken.
    #[default]
    Unreadable,
}

/// One disk as smartctl read it; values out of range are left out.
#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub struct Disk {
    /// The device below `/dev`, e.g. `sda`.
    pub device: String,
    /// `ata`, `nvme`, `scsi`, or empty.
    pub protocol: &'static str,
    /// The model, printable ASCII, at most 64 bytes.
    pub model: String,
    /// How it was read.
    pub read: Read,
    /// The overall self-assessment.
    pub passed: Option<bool>,
    pub temperature: Option<i64>,
    pub power_on_hours: Option<i64>,
    pub power_cycles: Option<i64>,
    pub reallocated: Option<i64>,
    pub pending: Option<i64>,
    pub uncorrectable: Option<i64>,
    pub crc_errors: Option<i64>,
    pub media_errors: Option<i64>,
    pub critical_warning: Option<i64>,
    /// NVMe available spare, percent.
    pub spare_percent: Option<i64>,
    /// NVMe percentage used.
    pub used_percent: Option<i64>,
}

/// The disk of a helper's command (named `… /dev/<disk>`) and its output; `None` if the name
/// ends with no whole disk.
#[must_use]
pub fn parse(name: &str, output: &[u8]) -> Option<Disk> {
    let device = name.rsplit(' ').next()?.strip_prefix("/dev/")?;
    if !disk(device) {
        return None;
    }
    let mut d = Disk {
        device: device.to_owned(),
        ..Disk::default()
    };
    let json: Value = match serde_json::from_slice(output) {
        Ok(v) if output.len() <= MAX_OUTPUT => v,
        _ => return Some(d),
    };
    d.protocol = match json.pointer("/device/protocol").and_then(Value::as_str) {
        Some("ATA") => "ata",
        Some("NVMe") => "nvme",
        Some("SCSI") => "scsi",
        _ => "",
    };
    d.model = model(json.get("model_name").and_then(Value::as_str).unwrap_or(""));
    let asleep = json
        .pointer("/smartctl/messages")
        .and_then(Value::as_array)
        .is_some_and(|m| {
            m.iter()
                .filter_map(|m| m.get("string")?.as_str())
                .any(|s| s.starts_with("Device is in ") && s.contains(" mode"))
        });
    d.passed = json
        .pointer("/smart_status/passed")
        .and_then(Value::as_bool);
    d.temperature = int(&json, "/temperature/current", -60, 200);
    d.read = if d.passed.is_some() || d.temperature.is_some() {
        Read::Data
    } else if asleep {
        Read::Standby
    } else {
        Read::Unreadable
    };
    if d.read != Read::Data {
        return Some(d);
    }
    d.power_on_hours = int(&json, "/power_on_time/hours", 0, COUNT);
    d.power_cycles = int(&json, "/power_cycle_count", 0, COUNT);
    // Only the values of its protocol: at most 10 samples a disk.
    match d.protocol {
        "ata" => {
            d.reallocated = attribute(&json, 5);
            d.pending = attribute(&json, 197);
            d.uncorrectable = attribute(&json, 198);
            d.crc_errors = attribute(&json, 199);
        }
        "scsi" => d.reallocated = int(&json, "/scsi_grown_defect_list", 0, COUNT),
        "nvme" => {
            let log = |f: &str, max| {
                int(
                    &json,
                    &format!("/nvme_smart_health_information_log/{f}"),
                    0,
                    max,
                )
            };
            d.media_errors = log("media_errors", COUNT);
            d.critical_warning = log("critical_warning", 255);
            d.spare_percent = log("available_spare", 100);
            d.used_percent = log("percentage_used", 255);
        }
        _ => {}
    }
    Some(d)
}

/// A whole disk as the helper reads them: `sd` and 1 to 4 letters, `nvme<n>n<n>`.
fn disk(name: &str) -> bool {
    let digits = |s: &str| (1..=3).contains(&s.len()) && s.bytes().all(|b| b.is_ascii_digit());
    match (name.strip_prefix("sd"), name.strip_prefix("nvme")) {
        (Some(l), _) => (1..=4).contains(&l.len()) && l.bytes().all(|b| b.is_ascii_lowercase()),
        (_, Some(n)) => n
            .split_once('n')
            .is_some_and(|(c, n)| digits(c) && digits(n)),
        _ => false,
    }
}

/// An integer at `pointer` within `min..=max`.
fn int(json: &Value, pointer: &str, min: i64, max: i64) -> Option<i64> {
    json.pointer(pointer)
        .and_then(Value::as_i64)
        .filter(|v| (min..=max).contains(v))
}

/// The raw count of the ATA attribute `id`: its low 32 bits (vendors put more into the high
/// ones).
fn attribute(json: &Value, id: i64) -> Option<i64> {
    json.pointer("/ata_smart_attributes/table")?
        .as_array()?
        .iter()
        .take(256)
        .find(|a| a.get("id").and_then(Value::as_i64) == Some(id))?
        .pointer("/raw/value")?
        .as_i64()
        .filter(|v| *v >= 0)
        .map(|v| v & 0xffff_ffff)
}

/// Printable ASCII of the model, trimmed, at most [`MAX_MODEL`] bytes.
fn model(name: &str) -> String {
    let clean: String = name
        .chars()
        .filter(|c| c.is_ascii_graphic() || *c == ' ')
        .take(MAX_MODEL)
        .collect();
    clean.trim().to_owned()
}

/// The samples of `disks`, at most [`MAX_DISKS`].
#[must_use]
pub fn samples(disks: &[Disk], truncated: bool) -> Vec<Sample> {
    let shown = &disks[..disks.len().min(MAX_DISKS)];
    let mut out = vec![
        Sample::int(
            "sermon_smart_disks",
            i64::try_from(shown.len()).unwrap_or(0),
        ),
        Sample::int(
            "sermon_smart_truncated",
            i64::from(truncated || disks.len() > MAX_DISKS),
        ),
    ];
    for d in shown {
        let dev = |s: Sample| s.label("device", d.device.clone());
        out.push(
            dev(Sample::int(
                "sermon_smart_readable",
                i64::from(d.read == Read::Data),
            ))
            .label("protocol", d.protocol)
            .label("model", d.model.clone()),
        );
        out.push(dev(Sample::int(
            "sermon_smart_standby",
            i64::from(d.read == Read::Standby),
        )));
        let ints = [
            ("sermon_smart_passed", d.passed.map(i64::from)),
            ("sermon_smart_temperature_celsius", d.temperature),
            (
                "sermon_smart_power_on_seconds",
                d.power_on_hours.map(|h| h * 3600),
            ),
            ("sermon_smart_power_cycles", d.power_cycles),
            ("sermon_smart_reallocated_sectors", d.reallocated),
            ("sermon_smart_pending_sectors", d.pending),
            ("sermon_smart_uncorrectable_sectors", d.uncorrectable),
            ("sermon_smart_crc_errors", d.crc_errors),
            ("sermon_smart_media_errors", d.media_errors),
            ("sermon_smart_critical_warning", d.critical_warning),
            // Ratios in ppm like every ratio (KONZEPT §8); wear may pass 100 %.
            (
                "sermon_smart_spare_ratio",
                d.spare_percent.map(|p| p * 10_000),
            ),
            (
                "sermon_smart_endurance_used_ratio",
                d.used_percent.map(|p| p * 10_000),
            ),
        ];
        out.extend(
            ints.into_iter()
                .filter_map(|(m, v)| Some(dev(Sample::int(m, v?)))),
        );
    }
    out
}

#[cfg(test)]
mod tests;
