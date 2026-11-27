//! A module for tests and demos (feature `simulate`, never in a release): it reaches nothing and
//! behaves as configured – fine, failing, hanging, crashing or flooding – so the runtime, the
//! server and the UI can be tried without a service on the host.

use std::time::Duration;

use sermon_core::Sample;

use crate::runtime::MAX_SAMPLES;
use crate::{
    BoxFuture, BuildError, Config, Discovered, HostFacts, Instance, Module, Run, RunError, Setup,
};

/// The process a virtual node pretends to run, so the discovery suggests the module.
pub const PROCESS: &str = "sermon-simulate";

const SCHEMA: &str = include_str!("../schemas/simulate.json");

/// How long a hanging run blocks its thread: longer than any budget (in tests real time, while
/// the test's clock runs virtually).
const HANG: Duration = if cfg!(test) {
    Duration::from_millis(200)
} else {
    Duration::from_mins(1)
};

/// The module `simulate`.
#[derive(Debug, Clone, Copy, Default)]
pub struct Simulate;

impl Module for Simulate {
    fn id(&self) -> &'static str {
        "simulate"
    }

    fn schema(&self) -> &'static str {
        SCHEMA
    }

    fn default_interval(&self) -> Duration {
        Duration::from_secs(10)
    }

    fn build(&self, config: &Config, setup: &Setup<'_>) -> Result<Box<dyn Instance>, BuildError> {
        let behavior = match config.get("behavior").and_then(|v| v.as_str()) {
            Some("ok") | None => Behavior::Ok,
            Some("fail") => Behavior::Fail,
            Some("hang") => Behavior::Hang,
            Some("panic") => Behavior::Panic,
            Some("flood") => Behavior::Flood,
            Some(other) => return Err(BuildError::Invalid(format!("behavior {other}"))),
        };
        Ok(Box::new(Simulated {
            behavior,
            value: config
                .get("value")
                .and_then(serde_json::Value::as_i64)
                .unwrap_or(1),
            token: setup.secrets.get("token").is_some(),
        }))
    }

    fn discover(&self, facts: &HostFacts) -> Option<Discovered> {
        Discovered::evidence(facts, &[PROCESS], &[], &[])
    }
}

#[derive(Debug, Clone, Copy)]
enum Behavior {
    Ok,
    Fail,
    Hang,
    Panic,
    Flood,
}

struct Simulated {
    behavior: Behavior,
    value: i64,
    token: bool,
}

impl Instance for Simulated {
    fn collect<'a>(&'a mut self, run: &'a Run) -> BoxFuture<'a, Result<Vec<Sample>, RunError>> {
        Box::pin(async move {
            #[cfg(test)]
            tests::RUNS.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
            let samples = || {
                vec![
                    Sample::int("sermon_simulate_value", self.value),
                    Sample::int("sermon_simulate_token", i64::from(self.token)),
                ]
            };
            match self.behavior {
                Behavior::Ok => Ok(samples()),
                Behavior::Fail => Err(RunError::Failed("simulated failure".into())),
                Behavior::Hang => {
                    run.blocking(|| std::thread::sleep(HANG)).await?;
                    Ok(samples())
                }
                // What a bug in a module does; the runtime keeps it to the module.
                #[allow(clippy::panic)]
                Behavior::Panic => panic!("simulated crash"),
                Behavior::Flood => Ok((0..=MAX_SAMPLES)
                    .map(|i| {
                        Sample::int("sermon_simulate_value", self.value).label("n", i.to_string())
                    })
                    .collect()),
            }
        })
    }
}

#[cfg(test)]
mod tests {
    use std::sync::atomic::{AtomicUsize, Ordering};

    /// Runs started by the module in this test binary.
    pub(super) static RUNS: AtomicUsize = AtomicUsize::new(0);

    use std::collections::BTreeMap;
    use std::sync::{Arc, Mutex};
    use std::time::SystemTime;

    use sermon_core::{SystemClock, Value};

    use super::*;
    use crate::runtime::{Health, Runtime, Sink, Spec, State};
    use crate::{Schema, Secrets, Veto};

    #[derive(Default)]
    struct Out {
        samples: Mutex<Vec<Sample>>,
        states: Mutex<Vec<Health>>,
    }

    impl Sink for Out {
        fn samples(&self, samples: Vec<Sample>, _: SystemTime) {
            self.samples.lock().unwrap().extend(samples);
        }
        fn health(&self, report: Vec<Health>, _: SystemTime) {
            *self.states.lock().unwrap() = report;
        }
    }

    #[test]
    fn it_is_suggested_for_its_process() {
        let facts = HostFacts {
            processes: [PROCESS.to_owned()].into(),
            ..HostFacts::default()
        };
        let hints = crate::discovery::hints(&[Arc::new(Simulate)], &facts);
        assert_eq!(hints[0].reasons, ["process:sermon-simulate"]);
        assert!(Simulate.discover(&HostFacts::default()).is_none());
    }

    #[test]
    fn its_schema_is_in_the_subset() {
        let schema = Schema::parse(SCHEMA).unwrap();
        assert_eq!(schema.secrets().collect::<Vec<_>>(), ["token"]);
    }

    // Every behavior ends in the state it simulates.
    #[tokio::test(start_paused = true)]
    async fn behaves_as_configured() {
        for (behavior, state) in [
            ("ok", State::Ok),
            ("fail", State::Error),
            ("hang", State::Timeout),
            ("panic", State::Crashed),
            ("flood", State::Error),
        ] {
            let out = Arc::new(Out::default());
            let mut runtime = Runtime::new(
                vec![Arc::new(Simulate)],
                Veto::default(),
                out.clone(),
                Arc::new(SystemClock::new()),
            );
            let secrets = BTreeMap::from([(
                "simulate".to_owned(),
                Secrets::new([("token".to_owned(), "t".to_owned())]),
            )]);
            runtime.apply(
                &[Spec {
                    id: "simulate".into(),
                    enabled: true,
                    interval: None,
                    config_json: format!(r#"{{"behavior":"{behavior}","value":7}}"#),
                }],
                &secrets,
            );
            // The module is built on a blocking thread, which the paused clock doesn't wait for:
            // wait in real time until its first run started, then virtual time passes the budget.
            let before = RUNS.load(Ordering::SeqCst);
            let deadline = std::time::Instant::now() + Duration::from_secs(10);
            while RUNS.load(Ordering::SeqCst) == before {
                assert!(
                    std::time::Instant::now() < deadline,
                    "{behavior}: no run started"
                );
                std::thread::sleep(Duration::from_millis(5));
                tokio::task::yield_now().await;
            }
            tokio::time::advance(Duration::from_secs(6)).await;
            for _ in 0..10 {
                tokio::task::yield_now().await;
            }
            let states = out.states.lock().unwrap().clone();
            assert_eq!(states.len(), 1, "{behavior}");
            assert_eq!(states[0].state, state, "{behavior}: {}", states[0].reason);
            let samples = out.samples.lock().unwrap().clone();
            if behavior == "ok" {
                crate::assert_in_catalog("simulate", &samples);
                assert_eq!(samples[0].value, Value::Int(7));
                assert_eq!(samples[1].value, Value::Int(1), "the token arrived");
            } else {
                assert!(samples.is_empty(), "{behavior}");
            }
            runtime.stop();
        }
    }
}
