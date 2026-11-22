//! Software RAID from `/proc/mdstat` (M2-22, KONZEPT §8.1): degraded arrays and the progress of a
//! resync or recovery. Runs every 10 s. Arrays without redundancy (raid0, linear) and inactive
//! ones have no `[n/m]` and yield nothing; hosts without md have no file and yield nothing.
//!
//! | Metric | Labels | Unit |
//! |---|---|---|
//! | `sermon_raid_degraded` | `device` (e.g. `md0`, at most 32) | boolean: fewer active disks than the array needs |
//! | `sermon_raid_disks_missing` | `device` | count |
//! | `sermon_raid_sync_ratio` | `device` | ppm of a running resync, recovery, reshape or check |

use sermon_core::Sample;

use crate::{CollectError, Collector, Context, optional};

const MAX_ARRAYS: usize = 32;

/// One array with redundancy.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Array {
    /// The device name, e.g. `md0`.
    pub device: String,
    /// Disks the array needs (`n` of `[n/m]`).
    pub disks: u32,
    /// Disks active now (`m` of `[n/m]`).
    pub active: u32,
    /// Progress of a running resync, recovery, reshape or check in ppm.
    pub sync_ppm: Option<i64>,
}

impl Array {
    /// Fewer active disks than needed.
    #[must_use]
    pub fn degraded(&self) -> bool {
        self.active < self.disks
    }
}

/// Parses `/proc/mdstat`: arrays with `[n/m]`, at most [`MAX_ARRAYS`].
#[must_use]
pub fn parse_mdstat(text: &str) -> Vec<Array> {
    let mut out: Vec<Array> = Vec::new();
    let mut current: Option<String> = None;
    for line in text.lines() {
        if !line.starts_with(char::is_whitespace) {
            // `md0 : active raid5 …` starts an array; other lines (Personalities, unused) end it.
            current = line
                .split_once(" : ")
                .map(|(name, _)| name.trim())
                .filter(|name| valid_device(name))
                .map(str::to_owned);
            continue;
        }
        let Some(device) = current.as_deref() else {
            continue;
        };
        if let Some((disks, active)) = counts(line) {
            if out.len() < MAX_ARRAYS {
                out.push(Array {
                    device: device.to_owned(),
                    disks,
                    active,
                    sync_ppm: None,
                });
            }
        } else if let Some(ppm) = progress(line)
            && let Some(array) = out.last_mut().filter(|a| a.device == device)
        {
            array.sync_ppm = Some(ppm);
        }
    }
    out
}

fn valid_device(name: &str) -> bool {
    name.len() <= 32
        && name.starts_with("md")
        && name
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'_' || b == b'-')
}

/// `[4/3]` in a status line.
fn counts(line: &str) -> Option<(u32, u32)> {
    line.split_whitespace().find_map(|word| {
        let (n, m) = word.strip_prefix('[')?.strip_suffix(']')?.split_once('/')?;
        Some((n.parse().ok()?, m.parse().ok()?))
    })
}

/// `recovery =  7.3%` (also resync, reshape, check) → ppm.
fn progress(line: &str) -> Option<i64> {
    let (_, rest) = line.split_once(" = ")?;
    let percent: f64 = rest.trim_start().split('%').next()?.trim().parse().ok()?;
    (0.0..=100.0)
        .contains(&percent)
        .then(|| crate::rate::round_to_i64(percent * 10_000.0))
}

/// RAID collector.
#[derive(Debug, Default)]
pub struct RaidCollector;

impl Collector for RaidCollector {
    fn name(&self) -> &'static str {
        "raid"
    }

    fn interval_ticks(&self) -> u32 {
        10
    }

    fn collect(
        &mut self,
        context: &Context<'_>,
        out: &mut Vec<Sample>,
    ) -> Result<(), CollectError> {
        let Some(text) = optional(context.root.read_proc("mdstat"))? else {
            return Ok(());
        };
        for array in parse_mdstat(&text) {
            let device = array.device.clone();
            out.push(
                Sample::int("sermon_raid_degraded", i64::from(array.degraded()))
                    .label("device", device.clone()),
            );
            out.push(
                Sample::int(
                    "sermon_raid_disks_missing",
                    i64::from(array.disks.saturating_sub(array.active)),
                )
                .label("device", device.clone()),
            );
            if let Some(ppm) = array.sync_ppm {
                out.push(Sample::int("sermon_raid_sync_ratio", ppm).label("device", device));
            }
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use sermon_core::Value;

    use super::*;
    use crate::testing::{at, fixture, value};

    #[test]
    fn reads_redundant_arrays_with_their_state_and_progress() {
        let text = fixture("states/raid").read_proc("mdstat").unwrap();
        let arrays = parse_mdstat(&text);
        assert_eq!(
            arrays,
            [
                Array {
                    device: "md1".into(),
                    disks: 2,
                    active: 2,
                    sync_ppm: None
                },
                Array {
                    device: "md0".into(),
                    disks: 4,
                    active: 3,
                    sync_ppm: Some(73_000)
                },
                Array {
                    device: "md4".into(),
                    disks: 4,
                    active: 4,
                    sync_ppm: Some(210_000)
                },
            ]
        );
        assert!(arrays[1].degraded() && !arrays[0].degraded() && !arrays[2].degraded());
    }

    #[test]
    fn reports_per_array_and_nothing_without_md() {
        let root = fixture("states/raid");
        let context = Context {
            root: &root,
            now: at(0),
        };
        let mut out = Vec::new();
        RaidCollector.collect(&context, &mut out).unwrap();
        assert_eq!(
            value(&out, "sermon_raid_degraded", &[("device", "md0")]),
            Some(Value::Int(1))
        );
        assert_eq!(
            value(&out, "sermon_raid_degraded", &[("device", "md1")]),
            Some(Value::Int(0))
        );
        assert_eq!(
            value(&out, "sermon_raid_disks_missing", &[("device", "md0")]),
            Some(Value::Int(1))
        );
        assert_eq!(
            value(&out, "sermon_raid_sync_ratio", &[("device", "md4")]),
            Some(Value::Int(210_000))
        );
        assert_eq!(
            value(&out, "sermon_raid_sync_ratio", &[("device", "md1")]),
            None
        );
        assert_eq!(
            value(&out, "sermon_raid_degraded", &[("device", "md2")]),
            None,
            "raid0 has no redundancy"
        );

        let root = fixture("debian-13");
        let context = Context {
            root: &root,
            now: at(0),
        };
        let mut out = Vec::new();
        RaidCollector.collect(&context, &mut out).unwrap();
        assert!(out.is_empty(), "{out:?}");
    }

    #[test]
    fn ignores_garbage_and_bounds_the_arrays() {
        assert_eq!(
            parse_mdstat("md0 : active raid1\n   [x/y] [U_]\n  recovery = 200%\n"),
            []
        );
        assert_eq!(
            parse_mdstat("../etc : active raid1 a[0]\n      1 blocks [2/1] [U_]\n"),
            []
        );
        let mut many = String::new();
        for i in 0..40 {
            many += "md";
            many += &i.to_string();
            many += " : active raid1 a[0]\n      1 blocks [2/2] [UU]\n";
        }
        assert_eq!(parse_mdstat(&many).len(), MAX_ARRAYS);
        // The progress of an array without [n/m] (a raid0 reshape) is not the previous array's.
        let mixed = "md0 : active raid1 a[0] b[1]\n      1 blocks [2/2] [UU]\n\nmd2 : active raid0 c[0] d[1]\n      1 blocks 512k chunks\n      [=>...]  reshape = 9.0% (1/10)\n";
        assert_eq!(parse_mdstat(mixed)[0].sync_ppm, None);
        // A progress line before any status line of its array belongs to nothing.
        assert_eq!(parse_mdstat("md0 : active raid1\n  recovery = 5.0%\n"), []);
    }
}
