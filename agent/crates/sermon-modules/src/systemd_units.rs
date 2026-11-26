//! Selected systemd units (M5-10): whether they are loaded, active or failed and how often
//! systemd restarted them, building on the collector of the failed units (M2-22). The agent asks
//! the same `systemctl` – fixed properties, checked names, no shell, a timeout; unprivileged it
//! asks systemd over D-Bus, no extra rights.
//!
//! | Metric | Labels | Unit |
//! |---|---|---|
//! | `sermon_systemd_units_loaded` | `unit` | 1 if systemd knows the unit (`LoadState=loaded`) |
//! | `sermon_systemd_units_active` | `unit` | 1 if `ActiveState=active` |
//! | `sermon_systemd_units_failed` | `unit` | 1 if `ActiveState=failed` |
//! | `sermon_systemd_units_restarts` | `unit` | services: automatic restarts since the last start by hand (`NRestarts`) |
//!
//! `unit` is the configured name, at most 32. A service in a restart loop is mostly neither
//! active nor failed (`activating`, `auto-restart`), at moments active; its restarts climb.

use std::sync::Arc;
use std::time::Duration;

use sermon_core::Sample;

use crate::fields::strings;
use crate::{BoxFuture, BuildError, Config, Instance, Module, Run, RunError, Setup, Systemd};

const SCHEMA: &str = include_str!("../schemas/systemd_units.json");

/// Most units of the module.
pub const MAX_UNITS: usize = 32;
/// Longest value taken from systemctl.
const MAX_VALUE: usize = 255;

/// The module `systemd_units`.
#[derive(Debug, Clone, Copy, Default)]
pub struct SystemdUnits;

impl Module for SystemdUnits {
    fn id(&self) -> &'static str {
        "systemd_units"
    }

    fn schema(&self) -> &'static str {
        SCHEMA
    }

    fn build(&self, config: &Config, setup: &Setup<'_>) -> Result<Box<dyn Instance>, BuildError> {
        let mut units: Vec<String> = Vec::new();
        for unit in strings(config, "units") {
            if units.iter().any(|u| u == unit) {
                return Err(BuildError::Invalid(format!("{unit}: twice")));
            }
            units.push(unit.to_owned());
        }
        if units.is_empty() || units.len() > MAX_UNITS {
            return Err(BuildError::Invalid(format!("1 to {MAX_UNITS} units")));
        }
        let systemd = setup
            .host
            .systemd
            .clone()
            .ok_or_else(|| BuildError::Invalid("this host runs no systemd".into()))?;
        Ok(Box::new(Units { systemd, units }))
    }
}

/// A unit as `systemctl show` describes it.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Unit {
    /// `Id`: the unit's name (an alias resolves to its target).
    pub id: String,
    /// `LoadState`, e.g. `loaded`, `not-found`.
    pub load: String,
    /// `ActiveState`, e.g. `active`, `failed`, `activating`.
    pub active: String,
    /// `SubState`, e.g. `running`, `auto-restart`.
    pub sub: String,
    /// `NRestarts` (services only).
    pub restarts: Option<u64>,
}

/// The blocks of `systemctl show` (separated by empty lines), at most [`MAX_UNITS`]; unknown
/// keys and overlong values are skipped.
#[must_use]
pub fn parse_show(text: &str) -> Vec<Unit> {
    let mut out = Vec::new();
    let mut unit = Unit::default();
    let mut seen = false;
    for line in text.lines().chain(std::iter::once("")) {
        if line.trim().is_empty() {
            if seen && out.len() < MAX_UNITS {
                out.push(std::mem::take(&mut unit));
            }
            seen = false;
            continue;
        }
        let Some((key, value)) = line.split_once('=') else {
            continue;
        };
        if value.len() > MAX_VALUE || value.chars().any(char::is_control) {
            continue;
        }
        seen = true;
        match key {
            "Id" => value.clone_into(&mut unit.id),
            "LoadState" => value.clone_into(&mut unit.load),
            "ActiveState" => value.clone_into(&mut unit.active),
            "SubState" => value.clone_into(&mut unit.sub),
            "NRestarts" => unit.restarts = value.parse().ok(),
            _ => {}
        }
    }
    out
}

struct Units {
    systemd: Arc<dyn Systemd>,
    units: Vec<String>,
}

impl Units {
    /// The block of each configured unit: by position if systemctl answered for all (an alias
    /// has another `Id`), else by `Id`.
    fn matched<'a>(&self, blocks: &'a [Unit]) -> Vec<Option<&'a Unit>> {
        if blocks.len() == self.units.len() {
            return blocks.iter().map(Some).collect();
        }
        self.units
            .iter()
            .map(|name| blocks.iter().find(|b| b.id == *name))
            .collect()
    }
}

impl Instance for Units {
    fn timeout(&self) -> Duration {
        // systemctl has 10 s of its own.
        Duration::from_secs(12)
    }

    fn collect<'a>(&'a mut self, run: &'a Run) -> BoxFuture<'a, Result<Vec<Sample>, RunError>> {
        Box::pin(async move {
            let (systemd, units) = (self.systemd.clone(), self.units.clone());
            let text = run
                .blocking(move || systemd.show(&units))
                .await?
                .map_err(RunError::Failed)?;
            let blocks = parse_show(&text);
            let mut samples = Vec::new();
            for (name, block) in self.units.iter().zip(self.matched(&blocks)) {
                let Some(b) = block else {
                    continue;
                };
                let flag = |metric: &'static str, on: bool| {
                    Sample::int(metric, i64::from(on)).label("unit", name)
                };
                samples.push(flag("sermon_systemd_units_loaded", b.load == "loaded"));
                samples.push(flag("sermon_systemd_units_active", b.active == "active"));
                samples.push(flag("sermon_systemd_units_failed", b.active == "failed"));
                if let Some(n) = b.restarts {
                    samples.push(
                        Sample::int(
                            "sermon_systemd_units_restarts",
                            i64::try_from(n).unwrap_or(i64::MAX),
                        )
                        .label("unit", name),
                    );
                }
            }
            Ok(samples)
        })
    }
}

#[cfg(test)]
mod tests {
    use std::path::Path;
    use std::sync::Mutex;

    use sermon_core::Value;

    use super::*;
    use crate::{Host, Schema, Secrets, Veto};

    const DISTROS: [&str; 5] = [
        "debian-13",
        "ubuntu-24.04",
        "rockylinux-9",
        "almalinux-10",
        "fedora-44",
    ];
    const ASKED: [&str; 6] = [
        "dbus.service",
        "crashy.service",
        "broken.service",
        "worker@a1.service",
        "systemd-tmpfiles-clean.timer",
        "missing.service",
    ];

    fn fixture(distro: &str) -> String {
        std::fs::read_to_string(
            Path::new(env!("CARGO_MANIFEST_DIR"))
                .join("../../fixtures/modules/systemd_units")
                .join(distro)
                .join("show.txt"),
        )
        .unwrap()
    }

    /// Answers with a fixed text and keeps what it was asked.
    struct Fake {
        answer: Result<String, String>,
        asked: Mutex<Vec<Vec<String>>>,
    }

    impl Systemd for Fake {
        fn show(&self, units: &[String]) -> Result<String, String> {
            self.asked.lock().unwrap().push(units.to_vec());
            self.answer.clone()
        }
    }

    fn build(units: &[&str], answer: Result<String, String>) -> (Box<dyn Instance>, Arc<Fake>) {
        let fake = Arc::new(Fake {
            answer,
            asked: Mutex::default(),
        });
        let host = Host {
            systemd: Some(fake.clone()),
            ..Host::default()
        };
        let list: Vec<String> = units.iter().map(|u| format!("\"{u}\"")).collect();
        let config = Schema::parse(SCHEMA)
            .unwrap()
            .validate(&format!(r#"{{"units":[{}]}}"#, list.join(",")), &[])
            .unwrap();
        let instance = SystemdUnits
            .build(
                &config,
                &Setup {
                    veto: &Veto::default(),
                    secrets: &Secrets::default(),
                    host: &host,
                },
            )
            .unwrap();
        (instance, fake)
    }

    fn value(samples: &[Sample], metric: &str, unit: &str) -> Option<Value> {
        samples
            .iter()
            .find(|s| s.metric == metric && s.labels.iter().any(|(_, v)| v == unit))
            .map(|s| s.value)
    }

    #[test]
    fn parses_systemctl_show_of_every_distro() {
        for distro in DISTROS {
            let units = parse_show(&fixture(distro));
            assert_eq!(units.len(), 6, "{distro}");
            let crashy = &units[1];
            assert_eq!(crashy.id, "crashy.service");
            // Caught between two restarts or in its moment of running.
            assert!(
                ["activating", "active"].contains(&crashy.active.as_str()),
                "{distro}: {crashy:?}"
            );
            assert!(crashy.restarts.unwrap() >= 2, "{distro}: {crashy:?}");
            assert_eq!(units[2].active, "failed", "{distro}");
            assert_eq!(units[3].sub, "running", "{distro}");
            assert_eq!(units[4].restarts, None, "a timer: {distro}");
            assert_eq!(units[5].load, "not-found", "{distro}");
        }
        // Rocky 9 and Alma 10 run dbus-broker: the alias answers with its target's Id.
        assert_eq!(
            parse_show(&fixture("rockylinux-9"))[0].id,
            "dbus-broker.service"
        );
    }

    #[test]
    fn skips_what_is_no_property() {
        let text = format!(
            "Id=a.service\nnonsense\nActiveState=active\nSubState={}\nNRestarts=x\n\n\n\nId=b.service\nActiveState=\u{7}bell\n",
            "y".repeat(MAX_VALUE + 1)
        );
        let units = parse_show(&text);
        assert_eq!(units.len(), 2);
        assert_eq!(
            units[0],
            Unit {
                id: "a.service".into(),
                active: "active".into(),
                ..Unit::default()
            }
        );
        assert_eq!(units[1].active, "", "a control character");
        let many = "Id=u.service\n\n".repeat(MAX_UNITS + 5);
        assert_eq!(parse_show(&many).len(), MAX_UNITS);
        assert_eq!(parse_show(""), []);
    }

    // The plan's goal: state and restarts of the units asked for, by their configured names.
    #[tokio::test]
    async fn reports_state_and_restarts() {
        for distro in DISTROS {
            let (mut units, fake) = build(&ASKED, Ok(fixture(distro)));
            let s = units.collect(&Run::default()).await.unwrap();
            crate::assert_in_catalog("systemd_units", &s);
            assert_eq!(fake.asked.lock().unwrap()[0], ASKED.map(String::from));
            let active = |u| value(&s, "sermon_systemd_units_active", u);
            let failed = |u| value(&s, "sermon_systemd_units_failed", u);
            if distro == "debian-13" || distro == "ubuntu-24.04" {
                assert_eq!(active("dbus.service"), Some(Value::Int(1)), "{distro}");
            }
            assert_eq!(failed("crashy.service"), Some(Value::Int(0)), "{distro}");
            assert!(
                matches!(value(&s, "sermon_systemd_units_restarts", "crashy.service"), Some(Value::Int(n)) if n >= 2)
            );
            assert_eq!(failed("broken.service"), Some(Value::Int(1)), "{distro}");
            assert_eq!(active("worker@a1.service"), Some(Value::Int(1)), "{distro}");
            assert_eq!(
                value(
                    &s,
                    "sermon_systemd_units_restarts",
                    "systemd-tmpfiles-clean.timer"
                ),
                None
            );
            assert_eq!(
                value(&s, "sermon_systemd_units_loaded", "missing.service"),
                Some(Value::Int(0))
            );
            assert_eq!(s.len(), 6 * 3 + 5, "{distro}");
        }
    }

    #[tokio::test]
    async fn matches_by_id_when_blocks_are_missing() {
        let (mut units, _) = build(
            &["a.service", "b.service"],
            Ok("Id=b.service\nActiveState=failed\n".into()),
        );
        let s = units.collect(&Run::default()).await.unwrap();
        assert_eq!(
            value(&s, "sermon_systemd_units_failed", "b.service"),
            Some(Value::Int(1))
        );
        assert_eq!(value(&s, "sermon_systemd_units_failed", "a.service"), None);
        let (mut broken, _) = build(&["a.service"], Err("systemctl: timed out".into()));
        assert!(matches!(
            broken.collect(&Run::default()).await,
            Err(RunError::Failed(_))
        ));
    }

    #[test]
    fn checks_the_configuration() {
        let schema = Schema::parse(SCHEMA).unwrap();
        for ok in [
            r#"{"units":["nginx.service","app@web\\x2d01.service","backup.timer"]}"#,
            r#"{"units":["postgresql@17-main.service"]}"#,
        ] {
            assert!(schema.validate(ok, &[]).is_ok(), "{ok}");
        }
        for bad in [
            r#"{"units":["nginx"]}"#,
            r#"{"units":["--now.service"]}"#,
            r#"{"units":["a b.service"]}"#,
            r#"{"units":["../x.service"]}"#,
            "{}",
        ] {
            assert!(schema.validate(bad, &[]).is_err(), "{bad}");
        }
        let config = schema
            .validate(r#"{"units":["a.service","a.service"]}"#, &[])
            .unwrap();
        let (veto, secrets, host) = (Veto::default(), Secrets::default(), Host::default());
        let setup = Setup {
            veto: &veto,
            secrets: &secrets,
            host: &host,
        };
        let with_systemd = Host {
            systemd: Some(Arc::new(Fake {
                answer: Ok(String::new()),
                asked: Mutex::default(),
            })),
            ..Host::default()
        };
        let twice = SystemdUnits.build(
            &config,
            &Setup {
                host: &with_systemd,
                ..setup
            },
        );
        assert!(matches!(twice, Err(BuildError::Invalid(why)) if why.contains("twice")));
        let config = schema.validate(r#"{"units":["a.service"]}"#, &[]).unwrap();
        let no_systemd = SystemdUnits.build(&config, &setup);
        assert!(matches!(no_systemd, Err(BuildError::Invalid(why)) if why.contains("no systemd")));
    }
}
