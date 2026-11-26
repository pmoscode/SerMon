//! Discovery of the modules (ADR 0039, M5-04): every 10 min the agent reads what runs on the
//! host and asks each module built in whether it fits. The hints go to the server only when they
//! changed (and once after the start, the server can't know the last list); nothing is
//! activated. Without `allow_modules` there is no discovery either.

use std::sync::Arc;
use std::time::{Duration, SystemTime};

use sermon_core::{Clock, ProcRoot};
use sermon_modules::Module;
use sermon_modules::discovery::{self, Hint, HostFacts};
use sermon_transport::outbox::Outbox;
use sermon_transport::proto::agent::v1::{DiscoveryHint, DiscoveryHints};
use tokio::time::MissedTickBehavior;
use tokio_util::sync::CancellationToken;

/// How often the host is looked at (ADR 0039).
pub const EVERY: Duration = Duration::from_mins(10);

/// Where the hints go (the outbox).
pub trait HintSink: Send + Sync + 'static {
    /// The hints after a change.
    fn hints(&self, hints: DiscoveryHints, at: SystemTime);
}

impl HintSink for Outbox {
    fn hints(&self, hints: DiscoveryHints, at: SystemTime) {
        if let Err(error) = self.push_discovery_hints(hints, at) {
            tracing::warn!("cannot buffer the discovery hints: {error}");
        }
    }
}

/// The running units; `None` if they can't be asked for now (the last answer stays).
pub type Units = Arc<dyn Fn() -> Option<Vec<String>> + Send + Sync>;

/// What the discovery needs.
pub struct Discovery {
    /// The modules built in.
    pub modules: Vec<Arc<dyn Module>>,
    /// The host's root.
    pub root: ProcRoot,
    /// The running units (systemd); none on other hosts.
    pub units: Units,
    /// Where the hints go.
    pub sink: Arc<dyn HintSink>,
    /// The agent's clock.
    pub clock: Arc<dyn Clock>,
}

impl std::fmt::Debug for Discovery {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Discovery")
            .field("root", &self.root)
            .finish_non_exhaustive()
    }
}

/// The protocol's list of `hints`.
#[must_use]
pub fn to_proto(hints: &[Hint], at: SystemTime) -> DiscoveryHints {
    DiscoveryHints {
        collected_at: Some(at.into()),
        hints: hints
            .iter()
            .map(|h| DiscoveryHint {
                module_id: h.module.clone(),
                reasons: h.reasons.clone(),
                suggested_config_json: h.suggested_config_json.clone(),
            })
            .collect(),
    }
}

/// Looks at the host every [`EVERY`] until `shutdown`; the hints go out when they changed.
pub async fn run(d: Discovery, shutdown: CancellationToken) {
    let modules: Arc<[Arc<dyn Module>]> = d.modules.into();
    let mut tick = tokio::time::interval(EVERY);
    tick.set_missed_tick_behavior(MissedTickBehavior::Delay);
    let mut units: Vec<String> = Vec::new();
    let mut last: Option<Vec<Hint>> = None;
    loop {
        tokio::select! {
            () = shutdown.cancelled() => return,
            _ = tick.tick() => {}
        }
        let (root, ask, known, modules) = (
            d.root.clone(),
            d.units.clone(),
            units.clone(),
            modules.clone(),
        );
        // /proc and systemctl block; systemctl has a timeout of its own.
        let job = tokio::task::spawn_blocking(move || {
            let units = ask().unwrap_or(known);
            let facts = HostFacts::read(&root, units.clone());
            (units, discovery::hints(&modules, &facts))
        });
        let found = tokio::select! {
            () = shutdown.cancelled() => return,
            found = job => found,
        };
        let hints = match found {
            Ok((now_units, hints)) => {
                units = now_units;
                hints
            }
            Err(error) => {
                tracing::warn!("the discovery of the modules failed: {error}");
                continue;
            }
        };
        if last.as_ref() == Some(&hints) {
            continue;
        }
        let at = d.clock.now();
        d.sink.hints(to_proto(&hints, at), at);
        last = Some(hints);
    }
}

#[cfg(test)]
mod tests {
    use std::path::Path;
    use std::sync::Mutex;

    use sermon_buffer::{Wal, WalConfig};
    use sermon_core::SystemClock;
    use sermon_modules::{BuildError, Config, Discovered, Instance, Setup};

    use super::*;

    /// Finds nginx by its process, unit and port.
    struct Nginx;

    impl Module for Nginx {
        fn id(&self) -> &'static str {
            "nginx"
        }
        fn schema(&self) -> &'static str {
            "{}"
        }
        fn build(&self, _: &Config, _: &Setup<'_>) -> Result<Box<dyn Instance>, BuildError> {
            Err(BuildError::Invalid("a fake".into()))
        }
        fn discover(&self, facts: &HostFacts) -> Option<Discovered> {
            Discovered::evidence(facts, &["nginx"], &["nginx.service"], &[80])
        }
    }

    #[derive(Default)]
    struct Sent(Mutex<Vec<DiscoveryHints>>);

    impl HintSink for Sent {
        fn hints(&self, hints: DiscoveryHints, _: SystemTime) {
            self.0.lock().unwrap().push(hints);
        }
    }

    impl Sent {
        fn reasons(&self) -> Vec<Vec<String>> {
            self.0
                .lock()
                .unwrap()
                .iter()
                .map(|h| h.hints.iter().flat_map(|h| h.reasons.clone()).collect())
                .collect()
        }
    }

    fn fixture(distro: &str) -> ProcRoot {
        ProcRoot::new(
            Path::new(env!("CARGO_MANIFEST_DIR"))
                .join("../../fixtures/discovery")
                .join(distro),
        )
    }

    /// Lets the blocking part of a run finish while the clock stands still.
    async fn settle() {
        for _ in 0..50 {
            tokio::task::yield_now().await;
            std::thread::sleep(Duration::from_millis(2));
        }
    }

    // ADR 0039: hints go out once after the start and then only on change, at most every
    // 10 min; a failing systemctl keeps the last units.
    #[tokio::test(start_paused = true)]
    async fn sends_the_hints_on_change_only() {
        let units: Arc<Mutex<Option<Vec<String>>>> =
            Arc::new(Mutex::new(Some(vec!["nginx.service".into()])));
        let ask = units.clone();
        let sent = Arc::new(Sent::default());
        let shutdown = CancellationToken::new();
        let task = tokio::spawn(run(
            Discovery {
                modules: vec![Arc::new(Nginx)],
                root: fixture("debian-13"),
                units: Arc::new(move || ask.lock().unwrap().clone()),
                sink: sent.clone(),
                clock: Arc::new(SystemClock::new()),
            },
            shutdown.clone(),
        ));
        settle().await;
        let all = ["process:nginx", "unit:nginx.service", "port:80"].map(String::from);
        assert_eq!(sent.reasons(), [all.to_vec()]);
        assert_eq!(sent.0.lock().unwrap()[0].hints[0].module_id, "nginx");

        tokio::time::advance(EVERY).await;
        settle().await;
        assert_eq!(sent.reasons().len(), 1, "no change, nothing sent");

        *units.lock().unwrap() = None;
        tokio::time::advance(EVERY).await;
        settle().await;
        assert_eq!(
            sent.reasons().len(),
            1,
            "systemctl failed: the last units stay"
        );

        *units.lock().unwrap() = Some(Vec::new());
        tokio::time::advance(EVERY.saturating_sub(Duration::from_secs(1))).await;
        settle().await;
        assert_eq!(sent.reasons().len(), 1, "not before 10 min");
        tokio::time::advance(Duration::from_secs(1)).await;
        settle().await;
        let reasons = sent.reasons();
        assert_eq!(reasons.len(), 2);
        assert_eq!(reasons[1], ["process:nginx", "port:80"]);

        shutdown.cancel();
        task.await.unwrap();
    }

    // Nothing found is a list too: the server learns that nginx is gone.
    #[tokio::test(start_paused = true)]
    async fn an_empty_list_goes_out_once() {
        let sent = Arc::new(Sent::default());
        let shutdown = CancellationToken::new();
        let task = tokio::spawn(run(
            Discovery {
                modules: vec![Arc::new(Nginx)],
                root: ProcRoot::new("/nonexistent"),
                units: Arc::new(|| None),
                sink: sent.clone(),
                clock: Arc::new(SystemClock::new()),
            },
            shutdown.clone(),
        ));
        settle().await;
        tokio::time::advance(EVERY).await;
        settle().await;
        let lists = sent.0.lock().unwrap().clone();
        assert_eq!(lists.len(), 1);
        assert_eq!(lists[0].hints, []);
        shutdown.cancel();
        task.await.unwrap();
    }

    #[test]
    fn the_outbox_buffers_the_hints() {
        let dir = tempfile::tempdir().unwrap();
        let now = SystemTime::now();
        let (wal, _) = Wal::open(dir.path(), WalConfig::default(), now).unwrap();
        let outbox = Outbox::new(wal);
        let hints = [Hint {
            module: "nginx".into(),
            reasons: vec!["port:80".into()],
            suggested_config_json: r#"{"port":80}"#.into(),
        }];
        let proto = to_proto(&hints, now);
        assert_eq!(proto.collected_at, Some(now.into()));
        assert_eq!(proto.hints[0].module_id, "nginx");
        assert_eq!(proto.hints[0].reasons, ["port:80"]);
        assert_eq!(proto.hints[0].suggested_config_json, r#"{"port":80}"#);
        outbox.hints(proto, now);
        assert_eq!(outbox.stats().pending_records, 1);
    }
}
