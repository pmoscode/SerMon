//! The TCP check (M5-08): whether ports the host's veto allows accept a connection, and how fast.
//! Up to 16 targets, asked at once; nothing is sent, the connection is closed at once.
//!
//! | Metric | Labels | Unit |
//! |---|---|---|
//! | `sermon_tcp_check_up` | `target` | 1 if a connection was made, else 0 |
//! | `sermon_tcp_check_duration_seconds` | `target` | until the connection stood (only if up) |
//!
//! `target` is the configured `host:port`. A name is resolved once through the veto and every
//! address checked; the check connects to those addresses, one after the other. An address
//! outside the veto is a refusal: the module reports `vetoed`.

use std::time::Duration;

use sermon_core::Sample;
use tokio::net::TcpStream;
use tokio::task::JoinSet;
use tokio::time::Instant;

use crate::fields::{strings, timeout};
use crate::{BoxFuture, BuildError, Config, Instance, Module, Run, RunError, Setup, Veto, schema};

const SCHEMA: &str = include_str!("../schemas/tcp_check.json");

/// The module `tcp_check`.
#[derive(Debug, Clone, Copy, Default)]
pub struct TcpCheck;

impl Module for TcpCheck {
    fn id(&self) -> &'static str {
        "tcp_check"
    }

    fn schema(&self) -> &'static str {
        SCHEMA
    }

    fn build(&self, config: &Config, setup: &Setup<'_>) -> Result<Box<dyn Instance>, BuildError> {
        let mut targets: Vec<Target> = Vec::new();
        for text in strings(config, "targets") {
            let (host, port) =
                schema::host_port(text).map_err(|e| BuildError::Invalid(format!("{text}: {e}")))?;
            if targets.iter().any(|t| t.label == text) {
                return Err(BuildError::Invalid(format!("{text}: twice")));
            }
            targets.push(Target {
                host: host.to_owned(),
                port,
                label: text.to_owned(),
            });
        }
        if targets.is_empty() {
            return Err(BuildError::Invalid("no target to check".into()));
        }
        Ok(Box::new(Checks {
            veto: setup.veto.clone(),
            targets,
            timeout: timeout(config, "timeout", Duration::from_secs(3))?,
        }))
    }
}

#[derive(Debug, Clone)]
struct Target {
    host: String,
    port: u16,
    label: String,
}

struct Checks {
    veto: Veto,
    targets: Vec<Target>,
    timeout: Duration,
}

/// Why a target is down.
enum Down {
    Vetoed(crate::Refused),
    Unreachable,
}

/// The time to connect to `target` inside `veto`.
async fn probe(veto: Veto, target: Target, limit: Duration) -> Result<Duration, Down> {
    let start = Instant::now();
    let connect = async {
        let addresses = veto
            .resolve(&target.host, target.port)
            .await
            .map_err(Down::Vetoed)?;
        for address in addresses {
            if TcpStream::connect(address).await.is_ok() {
                return Ok(start.elapsed());
            }
        }
        Err(Down::Unreachable)
    };
    tokio::time::timeout(limit, connect)
        .await
        .unwrap_or(Err(Down::Unreachable))
}

impl Instance for Checks {
    fn timeout(&self) -> Duration {
        self.timeout + Duration::from_secs(2)
    }

    fn collect<'a>(&'a mut self, _: &'a Run) -> BoxFuture<'a, Result<Vec<Sample>, RunError>> {
        Box::pin(async move {
            let mut asks = JoinSet::new();
            for (i, target) in self.targets.iter().enumerate() {
                let (veto, target, limit) = (self.veto.clone(), target.clone(), self.timeout);
                asks.spawn(async move { (i, probe(veto, target, limit).await) });
            }
            let mut answers = Vec::with_capacity(self.targets.len());
            while let Some(done) = asks.join_next().await {
                answers.push(done.map_err(|e| RunError::Failed(format!("a check failed: {e}")))?);
            }
            answers.sort_by_key(|(i, _)| *i);
            let mut samples = Vec::new();
            for (i, answer) in answers {
                let label = &self.targets[i].label;
                match answer {
                    Ok(took) => {
                        samples.push(Sample::int("sermon_tcp_check_up", 1).label("target", label));
                        samples.push(
                            Sample::float("sermon_tcp_check_duration_seconds", took.as_secs_f64())
                                .label("target", label),
                        );
                    }
                    Err(Down::Vetoed(r)) => return Err(RunError::Vetoed(r)),
                    Err(Down::Unreachable) => {
                        samples.push(Sample::int("sermon_tcp_check_up", 0).label("target", label));
                    }
                }
            }
            Ok(samples)
        })
    }
}

#[cfg(test)]
mod tests {
    use sermon_core::Value;
    use tokio::net::TcpListener;

    use super::*;
    use crate::veto::Net;
    use crate::{Schema, Secrets};

    fn build(config: &str, veto: &Veto) -> Result<Box<dyn Instance>, BuildError> {
        let config = Schema::parse(SCHEMA)
            .unwrap()
            .validate(config, &[])
            .unwrap();
        TcpCheck.build(
            &config,
            &Setup {
                veto,
                secrets: &Secrets::default(),
                host: &crate::Host::default(),
            },
        )
    }

    fn value(samples: &[Sample], metric: &str, target: &str) -> Option<Value> {
        samples
            .iter()
            .find(|s| s.metric == metric && s.labels.iter().any(|(_, v)| v == target))
            .map(|s| s.value)
    }

    #[tokio::test]
    async fn reports_open_and_closed_ports() {
        let open = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let open_addr = open.local_addr().unwrap();
        let closed = TcpListener::bind("127.0.0.1:0")
            .await
            .unwrap()
            .local_addr()
            .unwrap();
        let (a, b, c) = (
            open_addr.to_string(),
            closed.to_string(),
            format!("localhost:{}", open_addr.port()),
        );
        let mut check = build(
            &format!(r#"{{"targets":["{a}","{b}","{c}"],"timeout":"1s"}}"#),
            &Veto::default().any_port(),
        )
        .unwrap();
        assert_eq!(check.timeout(), Duration::from_secs(3));
        let samples = check.collect(&Run::default()).await.unwrap();
        crate::assert_in_catalog("tcp_check", &samples);
        assert_eq!(
            value(&samples, "sermon_tcp_check_up", &a),
            Some(Value::Int(1))
        );
        assert_eq!(
            value(&samples, "sermon_tcp_check_up", &b),
            Some(Value::Int(0))
        );
        assert_eq!(
            value(&samples, "sermon_tcp_check_up", &c),
            Some(Value::Int(1)),
            "by name"
        );
        assert!(matches!(
            value(&samples, "sermon_tcp_check_duration_seconds", &a),
            Some(Value::Float(s)) if (0.0..1.0).contains(&s)
        ));
        assert_eq!(
            value(&samples, "sermon_tcp_check_duration_seconds", &b),
            None
        );
        assert_eq!(samples.len(), 5);
        drop(open);
    }

    // ADR 0039: a target outside the veto isn't probed – the module reports why.
    #[tokio::test]
    async fn refuses_targets_outside_the_veto() {
        let veto = Veto {
            targets: vec![Net::parse("127.0.0.2/32").unwrap()],
            ..Veto::default().any_port()
        };
        let mut check = build(r#"{"targets":["127.0.0.1:22"]}"#, &veto).unwrap();
        assert!(matches!(
            check.collect(&Run::default()).await,
            Err(RunError::Vetoed(_))
        ));
    }

    #[tokio::test(start_paused = true)]
    async fn a_silent_target_is_down_within_its_timeout() {
        // 192.0.2.1 (TEST-NET) is routed nowhere: the connection hangs until the timeout.
        let veto = Veto {
            targets: vec![Net::parse("192.0.2.0/24").unwrap()],
            ..Veto::default().any_port()
        };
        let mut check = build(r#"{"targets":["192.0.2.1:9"],"timeout":"2s"}"#, &veto).unwrap();
        let samples = tokio::time::timeout(Duration::from_secs(3), check.collect(&Run::default()))
            .await
            .expect("within the timeout")
            .unwrap();
        assert_eq!(
            value(&samples, "sermon_tcp_check_up", "192.0.2.1:9"),
            Some(Value::Int(0))
        );
    }

    #[test]
    fn checks_the_configuration() {
        let veto = Veto::default().any_port();
        for bad in [
            r#"{"targets":[]}"#,
            r#"{"targets":["localhost:1","localhost:1"]}"#,
            r#"{"targets":["localhost:1"],"timeout":"26s"}"#,
            r#"{"targets":["localhost:1"],"timeout":"0s"}"#,
        ] {
            assert!(
                matches!(build(bad, &veto), Err(BuildError::Invalid(_))),
                "{bad}"
            );
        }
        let schema = Schema::parse(SCHEMA).unwrap();
        assert!(schema.validate(r#"{"targets":["no-port"]}"#, &[]).is_err());
        assert!(schema.validate("{}", &[]).is_err(), "targets are required");
    }
}
