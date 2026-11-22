//! `sermon-agent simulate`: virtual nodes with realistic metrics for development, end-to-end and
//! load tests (M1-34). Every virtual node is a real agent towards the server: it enrolls with a
//! token, holds its own identity and mTLS session, buffers in its own WAL and sends the metrics of
//! the catalog. Only the values are synthetic: a daily pattern, per-node character, noise,
//! occasional spikes and slowly filling disks – deterministic per seed and node. Scenarios
//! ([`scenario`], M2-30) add problems on a fixed cycle for trying the default alert rules.

pub mod scenario;

use std::collections::BTreeMap;
use std::f64::consts::TAU;
use std::io::Write;
use std::path::Path;
use std::sync::Arc;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use anyhow::{Context, bail};
use sermon_buffer::{FsyncPolicy, Wal, WalConfig};
use sermon_collectors::rate::{round_to_i64, to_f64};
use sermon_core::{Architecture, Clock, HostFacts, MachineId, Sample, SystemClock, Value};
use sermon_security::{Address, EventKind, SecurityEvent};
use sermon_transport::identity::AgentKey;
use sermon_transport::outbox::Outbox;
use sermon_transport::session::{FactsSource, Transport, TransportConfig};
use sermon_transport::tls::{ServerTrust, enrollment_config};
use sermon_transport::{channel, enrollment};
use tokio_util::sync::CancellationToken;

use crate::cli::{EnrollmentToken, SimulateArgs};
use crate::config::AgentConfig;
use crate::identity::{self, FileIdentity};
use crate::security;
use crate::state::StateLayout;
use scenario::{Attacker, Busy, Scenarios};

/// Collection interval of the virtual nodes (like the real scheduler's base tick).
pub const TICK: Duration = Duration::from_secs(1);
/// Slow values (file systems, boot time) are sent every this many ticks, like the collectors.
const SLOW_EVERY: u64 = 10;
/// Most virtual nodes per process.
pub const MAX_NODES: u32 = 2000;
const GIB: f64 = 1024.0 * 1024.0 * 1024.0;
const PPM: f64 = 1_000_000.0;

/// Small deterministic generator (SplitMix64): reproducible values without a dependency.
#[derive(Debug, Clone)]
struct Rng(u64);

impl Rng {
    fn next_u64(&mut self) -> u64 {
        self.0 = self.0.wrapping_add(0x9E37_79B9_7F4A_7C15);
        let mut z = self.0;
        z = (z ^ (z >> 30)).wrapping_mul(0xBF58_476D_1CE4_E5B9);
        z = (z ^ (z >> 27)).wrapping_mul(0x94D0_49BB_1331_11EB);
        z ^ (z >> 31)
    }

    /// Uniform in [0, 1).
    fn unit(&mut self) -> f64 {
        to_f64(self.next_u64() >> 11) / to_f64(1 << 53)
    }

    /// Uniform index below `len`.
    fn index(&mut self, len: usize) -> usize {
        usize::try_from(self.next_u64() % u64::try_from(len).unwrap_or(1)).unwrap_or(0)
    }

    /// Uniform in [low, high).
    fn range(&mut self, low: f64, high: f64) -> f64 {
        low + (high - low) * self.unit()
    }

    /// Roughly normal noise with standard deviation 1 (sum of uniforms).
    fn noise(&mut self) -> f64 {
        (0..6)
            .map(|_| self.unit())
            .sum::<f64>()
            .mul_add(2.0_f64.sqrt(), -6.0 * 2.0_f64.sqrt() / 2.0)
    }
}

/// The character of a virtual node: size and how busy it is.
#[derive(Debug, Clone, PartialEq)]
pub struct Profile {
    /// Index of the node (0-based), part of the host name.
    pub index: u32,
    /// Logical CPUs.
    pub cpus: u32,
    /// Physical memory in bytes.
    pub memory_bytes: u64,
    /// Size of the root file system in bytes.
    pub disk_bytes: u64,
    /// CPU busy share at night and the additional share at the daily peak.
    pub cpu_base: f64,
    /// Additional CPU busy share at the daily peak.
    pub cpu_peak: f64,
    /// Memory used share at night.
    pub memory_base: f64,
    /// Network receive rate at the peak in bytes per second.
    pub network_peak: f64,
    /// Bytes the root file system grows per second (some nodes visibly fill up).
    pub disk_growth: f64,
    /// Start fill level of the root file system.
    pub disk_fill: f64,
    /// Shift of the daily pattern in seconds (nodes in other time zones).
    pub phase: f64,
}

impl Profile {
    /// The profile of node `index` for `seed`: the same seed gives the same fleet.
    #[must_use]
    pub fn new(seed: u64, index: u32) -> Self {
        let mut rng = Rng(seed ^ (u64::from(index) + 1).wrapping_mul(0xA24B_AED4_963E_E407));
        let cpus = [2, 2, 4, 4, 8, 16][rng.index(6)];
        let memory_gib = [2_u64, 4, 8, 16, 32][rng.index(5)];
        // Every fifth node fills its disk within days, so the forecast has something to show.
        let filling = index % 5 == 4;
        Self {
            index,
            cpus,
            memory_bytes: memory_gib << 30,
            disk_bytes: (20 + rng.next_u64() % 180) << 30,
            cpu_base: rng.range(0.02, 0.15),
            cpu_peak: rng.range(0.05, 0.6),
            memory_base: rng.range(0.25, 0.55),
            network_peak: rng.range(50_000.0, 20_000_000.0),
            disk_growth: if filling {
                rng.range(50_000.0, 200_000.0)
            } else {
                rng.range(0.0, 2_000.0)
            },
            disk_fill: rng.range(0.2, 0.7),
            phase: rng.range(-3.0, 3.0) * 3600.0,
        }
    }

    /// Host name of the node.
    #[must_use]
    pub fn hostname(&self) -> String {
        format!("sim-{:04}", self.index)
    }

    /// Host facts as the real agent reports them.
    #[must_use]
    pub fn facts(&self, seed: u64) -> HostFacts {
        let machine_id = format!("{:016x}{:016x}", seed, u64::from(self.index) + 1);
        HostFacts {
            hostname: self.hostname(),
            machine_id: MachineId::parse(&machine_id)
                .unwrap_or_else(|_| unreachable!("32 hex digits")),
            os_id: "debian".to_owned(),
            os_version: "13".to_owned(),
            kernel_release: "6.12.0-sim".to_owned(),
            architecture: Architecture::Amd64,
            cpu_count: self.cpus,
            memory_bytes: self.memory_bytes,
        }
    }
}

/// Activity over the day, 0 at night and 1 at the peak (14:00 local time of the node).
fn daily(now: SystemTime, phase: f64) -> f64 {
    let seconds = now
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs_f64()
        + phase;
    let peak = 14.0 * 3600.0;
    0.5 + 0.5 * (TAU * (seconds - peak) / 86_400.0).cos()
}

/// Produces the samples of one virtual node.
#[derive(Debug)]
pub struct Generator {
    profile: Profile,
    rng: Rng,
    boot: SystemTime,
    started: SystemTime,
    load: [f64; 3],
    spike_left: u32,
    scenarios: Scenarios,
    attacker: Attacker,
}

fn ppm(share: f64) -> i64 {
    round(share.clamp(0.0, 1.0) * PPM)
}

fn round(value: f64) -> i64 {
    round_to_i64(value)
}

impl Generator {
    /// A generator for the profile; the node booted `uptime` before `now`.
    #[must_use]
    pub fn new(profile: Profile, seed: u64, now: SystemTime, uptime: Duration) -> Self {
        let rng = Rng(seed.rotate_left(17) ^ u64::from(profile.index));
        Self {
            profile,
            rng,
            boot: now - uptime,
            started: now,
            load: [0.0; 3],
            spike_left: 0,
            scenarios: Scenarios::default(),
            attacker: Attacker::default(),
        }
    }

    /// The generator with scenarios; their cycles start at the generator's start.
    #[must_use]
    pub fn with_scenarios(mut self, scenarios: Scenarios) -> Self {
        self.scenarios = scenarios;
        self
    }

    fn elapsed(&self, now: SystemTime) -> Duration {
        now.duration_since(self.started).unwrap_or_default()
    }

    /// Whether the node is connected at `now` (scenario `offline`).
    #[must_use]
    pub fn online(&self, now: SystemTime) -> bool {
        self.scenarios.online(self.elapsed(now))
    }

    /// The security events of the second at `now` (scenario `ssh-bruteforce`).
    pub fn events(&mut self, now: SystemTime) -> Vec<SecurityEvent> {
        let at_us = now
            .duration_since(UNIX_EPOCH)
            .unwrap_or_default()
            .as_micros();
        let port = 40_000 + u16::try_from(self.profile.index % 100).unwrap_or(0) * 200;
        let elapsed = self.elapsed(now);
        self.attacker.events(
            &self.scenarios,
            elapsed,
            u64::try_from(at_us).unwrap_or(u64::MAX),
            port,
        )
    }

    /// The profile.
    #[must_use]
    pub fn profile(&self) -> &Profile {
        &self.profile
    }

    /// The samples of one tick (slow values only every [`SLOW_EVERY`] ticks).
    #[must_use]
    pub fn samples(&mut self, now: SystemTime, tick: u64) -> Vec<Sample> {
        let day = daily(now, self.profile.phase);
        if self.spike_left == 0 && self.rng.unit() < 0.002 {
            self.spike_left = 20 + u32::try_from(self.rng.next_u64() % 100).unwrap_or(0);
        }
        let spike = if self.spike_left > 0 {
            self.spike_left -= 1;
            0.4
        } else {
            0.0
        };
        let natural =
            (self.profile.cpu_base + self.profile.cpu_peak * day + spike + 0.02 * self.rng.noise())
                .clamp(0.005, 0.99);
        let busy = match self.scenarios.busy(self.elapsed(now)) {
            Some(Busy::Fixed(share)) => (share + 0.003 * self.rng.noise()).clamp(0.005, 0.99),
            Some(Busy::AtMost(share)) => natural.min(share),
            None => natural,
        };
        let mut out = Vec::with_capacity(64);
        self.cpu(busy, &mut out);
        self.memory(day, &mut out);
        self.network(day, busy, &mut out);
        let uptime = now.duration_since(self.boot).unwrap_or_default().as_secs();
        out.push(Sample::int(
            "sermon_system_uptime_seconds",
            i64::try_from(uptime).unwrap_or(i64::MAX),
        ));
        out.push(Sample::int(
            "sermon_system_processes",
            round(120.0 + 80.0 * day + 3.0 * self.rng.noise()),
        ));
        out.push(Sample::int(
            "sermon_system_threads",
            round(400.0 + 300.0 * day + 10.0 * self.rng.noise()),
        ));
        if tick.is_multiple_of(SLOW_EVERY) {
            self.filesystem(now, &mut out);
            let boot = self
                .boot
                .duration_since(UNIX_EPOCH)
                .unwrap_or_default()
                .as_secs();
            out.push(Sample::int(
                "sermon_system_boot_time_seconds",
                i64::try_from(boot).unwrap_or(i64::MAX),
            ));
        }
        out
    }

    fn cpu(&mut self, busy: f64, out: &mut Vec<Sample>) {
        let cpus = self.profile.cpus;
        let modes = [
            ("user", busy * 0.68),
            ("nice", 0.0),
            ("system", busy * 0.2),
            ("iowait", busy * 0.07),
            ("irq", busy * 0.01),
            ("softirq", busy * 0.02),
            ("steal", busy * 0.02),
        ];
        let used: i64 = modes.iter().map(|(_, share)| ppm(*share)).sum();
        for (mode, share) in modes {
            out.push(Sample::int("sermon_cpu_utilization_ratio", ppm(share)).label("mode", mode));
        }
        out.push(
            Sample::int("sermon_cpu_utilization_ratio", 1_000_000 - used).label("mode", "idle"),
        );
        for cpu in 0..cpus {
            let core = (busy + 0.08 * self.rng.noise()).clamp(0.0, 1.0);
            out.push(
                Sample::int("sermon_cpu_core_busy_ratio", ppm(core)).label("cpu", cpu.to_string()),
            );
        }
        out.push(Sample::int("sermon_cpu_logical_count", i64::from(cpus)));
        // Load follows the busy CPUs with the kernel's damping (1, 5, 15 minutes).
        let runnable = busy * f64::from(cpus) * 1.1;
        for (i, (period, minutes)) in [("1m", 1.0), ("5m", 5.0), ("15m", 15.0)]
            .into_iter()
            .enumerate()
        {
            let alpha = 1.0 - (-1.0 / (minutes * 60.0_f64)).exp();
            self.load[i] += alpha * (runnable - self.load[i]);
            out.push(Sample::float("sermon_load_average", self.load[i]).label("period", period));
            out.push(
                Sample::float(
                    "sermon_load_average_per_cpu",
                    self.load[i] / f64::from(cpus),
                )
                .label("period", period),
            );
        }
    }

    fn memory(&mut self, day: f64, out: &mut Vec<Sample>) {
        let total = to_f64(self.profile.memory_bytes);
        let used_share =
            (self.profile.memory_base + 0.25 * day + 0.01 * self.rng.noise()).clamp(0.05, 0.98);
        let used_bytes = total * used_share;
        out.push(Sample::int("sermon_memory_total_bytes", round(total)));
        out.push(Sample::int("sermon_memory_used_bytes", round(used_bytes)));
        out.push(Sample::int(
            "sermon_memory_available_bytes",
            round(total - used_bytes),
        ));
        out.push(Sample::int(
            "sermon_memory_utilization_ratio",
            ppm(used_share),
        ));
        let swap_total = 2.0 * GIB;
        let swap_used = (swap_total * (used_share - 0.85).max(0.0) * 3.0).min(swap_total);
        out.push(Sample::int("sermon_swap_total_bytes", round(swap_total)));
        out.push(Sample::int("sermon_swap_used_bytes", round(swap_used)));
        out.push(Sample::int(
            "sermon_swap_utilization_ratio",
            ppm(swap_used / swap_total),
        ));
    }

    fn network(&mut self, day: f64, busy: f64, out: &mut Vec<Sample>) {
        let receive =
            (self.profile.network_peak * (0.05 + 0.95 * day) * (1.0 + 0.15 * self.rng.noise()))
                .max(0.0);
        let transmit = receive * 0.35;
        for (metric, value) in [
            ("sermon_network_receive_bytes_per_second", receive),
            ("sermon_network_transmit_bytes_per_second", transmit),
            ("sermon_network_receive_packets_per_second", receive / 900.0),
            (
                "sermon_network_transmit_packets_per_second",
                transmit / 600.0,
            ),
        ] {
            out.push(Sample::float(metric, value).label("interface", "eth0"));
        }
        out.push(Sample::int("sermon_network_up", 1).label("interface", "eth0"));
        out.push(
            Sample::int("sermon_network_speed_bits_per_second", 1_000_000_000)
                .label("interface", "eth0"),
        );
        out.push(Sample::int(
            "sermon_tcp_established",
            round(20.0 + 400.0 * day * self.rng.range(0.8, 1.2)),
        ));
        let disk_rate = busy * 40_000_000.0 * self.rng.range(0.5, 1.5);
        for (metric, value) in [
            ("sermon_disk_read_bytes_per_second", disk_rate * 0.6),
            ("sermon_disk_write_bytes_per_second", disk_rate * 0.4),
        ] {
            out.push(Sample::float(metric, value).label("device", "sda"));
        }
        out.push(Sample::int("sermon_disk_busy_ratio", ppm(busy * 0.5)).label("device", "sda"));
    }

    fn filesystem(&self, now: SystemTime, out: &mut Vec<Sample>) {
        let p = &self.profile;
        let size = to_f64(p.disk_bytes);
        let elapsed = self.elapsed(now);
        let reserved = size * 0.05;
        let capacity = size - reserved;
        let start = size * p.disk_fill;
        let used = if let Some(usage) = self.scenarios.usage(elapsed) {
            capacity * usage
        } else if let Some(filled) = self.scenarios.filled(elapsed) {
            start + (capacity - start) * filled
        } else {
            (start + p.disk_growth * elapsed.as_secs_f64()).min(capacity)
        };
        let avail = size - reserved - used;
        let mountpoint = |sample: Sample| sample.label("mountpoint", "/");
        out.push(mountpoint(Sample::int(
            "sermon_filesystem_size_bytes",
            round(size),
        )));
        out.push(mountpoint(Sample::int(
            "sermon_filesystem_used_bytes",
            round(used),
        )));
        out.push(mountpoint(Sample::int(
            "sermon_filesystem_avail_bytes",
            round(avail),
        )));
        out.push(mountpoint(Sample::int(
            "sermon_filesystem_utilization_ratio",
            ppm(used / (used + avail)),
        )));
        out.push(mountpoint(Sample::int("sermon_filesystem_readonly", 0)));
        out.push(mountpoint(Sample::int(
            "sermon_filesystem_inodes_total",
            round(size / 16_384.0),
        )));
        out.push(mountpoint(Sample::int(
            "sermon_filesystem_inodes_used",
            round(used / 65_536.0),
        )));
    }
}

/// Buffer of a virtual node: small, without fsync (simulated data is not precious).
fn wal_config() -> WalConfig {
    WalConfig {
        max_bytes: 16 * 1024 * 1024,
        fsync: FsyncPolicy::Never,
        ..WalConfig::default()
    }
}

/// Enrolls the virtual nodes that have no identity yet and runs all of them until SIGTERM or
/// SIGINT. Identities live in `<state_dir>/simulate/node-NNNN`, so a restart reuses them.
///
/// # Errors
///
/// Invalid arguments, a failed enrollment, or a buffer that can't be opened.
pub fn simulate(config: &AgentConfig, args: &SimulateArgs) -> anyhow::Result<()> {
    if args.nodes == 0 || args.nodes > MAX_NODES {
        bail!("--nodes must be between 1 and {MAX_NODES}");
    }
    let root = config.state_dir.join("simulate");
    let layouts: Vec<StateLayout> = (0..args.nodes)
        .map(|i| StateLayout::new(root.join(format!("node-{i:04}"))))
        .collect();
    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .context("cannot start the async runtime")?;
    runtime.block_on(async {
        enroll_missing(config, args, &layouts).await?;
        let clock: Arc<dyn Clock> = Arc::new(SystemClock::new());
        let shutdown = CancellationToken::new();
        let signals = tokio::spawn(crate::cancel_on_signal(shutdown.clone()));
        let scenarios = Scenarios::new(&args.scenario);
        let started = clock.now();
        let mut tasks = Vec::new();
        for (index, layout) in (0_u32..).zip(layouts) {
            tasks.push(tokio::spawn(run_node(
                NodeRun {
                    agent_url: config.agent_url.uri().clone(),
                    seed: args.seed,
                    index,
                    scenarios: scenarios.clone(),
                    started,
                },
                layout,
                clock.clone(),
                shutdown.clone(),
            )?));
        }
        tracing::info!(nodes = args.nodes, seed = args.seed, scenarios = ?args.scenario, server = %config.agent_url, "simulation started");
        for task in tasks {
            task.await.context("simulated node failed")?;
        }
        tracing::info!("simulation stopped");
        signals.await.context("signal handler failed")?
    })
}

async fn enroll_missing(
    config: &AgentConfig,
    args: &SimulateArgs,
    layouts: &[StateLayout],
) -> anyhow::Result<()> {
    let missing: Vec<(u32, &StateLayout)> = (0_u32..)
        .zip(layouts)
        .filter(|(_, layout)| !identity::is_enrolled(layout))
        .collect();
    if missing.is_empty() {
        return Ok(());
    }
    let Some(secret) = &args.token else {
        bail!(
            "{} virtual nodes are not enrolled yet – pass an enrollment token with at least {} uses (SERMON_ENROLLMENT_TOKEN)",
            missing.len(),
            missing.len()
        );
    };
    let token = EnrollmentToken::parse(secret)?;
    let trust = match (config.server_url.is_tls(), args.ca_fingerprint) {
        (true, Some(pin)) => Some(ServerTrust::Pinned(pin)),
        (true, None) => Some(ServerTrust::SystemRoots),
        (false, Some(_)) => bail!("--ca-fingerprint needs an https:// server URL"),
        (false, None) => None,
    };
    let tls = trust.as_ref().map(enrollment_config).transpose()?;
    for (index, layout) in missing {
        let facts = Profile::new(args.seed, index).facts(args.seed);
        let key = AgentKey::generate()?;
        let channel = channel::connect(
            config.server_url.uri().clone(),
            tls.clone(),
            Some(Duration::from_secs(30)),
        )
        .await?;
        let issued = enrollment::enroll(
            channel,
            &key,
            token.expose(),
            &facts,
            env!("CARGO_PKG_VERSION"),
            &SystemClock::new(),
        )
        .await
        .with_context(|| format!("cannot enroll virtual node {}", facts.hostname))?;
        create_dir(layout.root())?;
        identity::store(layout, &key, &issued, SystemTime::now())
            .with_context(|| format!("cannot store the identity of {}", facts.hostname))?;
        tracing::info!(node = %issued.identity.node_id, host = facts.hostname, "virtual node enrolled");
    }
    Ok(())
}

fn create_dir(path: &Path) -> anyhow::Result<()> {
    std::fs::create_dir_all(path).with_context(|| format!("cannot create {}", path.display()))
}

/// What a virtual node is: which one, with which scenarios since when, against which server.
struct NodeRun {
    agent_url: http::Uri,
    seed: u64,
    index: u32,
    scenarios: Scenarios,
    started: SystemTime,
}

/// Starts one virtual node: its transport and its sample generator.
fn run_node(
    node: NodeRun,
    layout: StateLayout,
    clock: Arc<dyn Clock>,
    shutdown: CancellationToken,
) -> anyhow::Result<impl Future<Output = ()>> {
    let NodeRun {
        agent_url,
        seed,
        index,
        scenarios,
        started,
    } = node;
    let profile = Profile::new(seed, index);
    let facts = profile.facts(seed);
    let hostname = facts.hostname.clone();
    let (wal, _) = Wal::open(&layout.wal_dir(), wal_config(), clock.now())
        .with_context(|| format!("cannot open the buffer of {hostname}"))?;
    let outbox = Outbox::new(wal);
    let facts_source: FactsSource = Arc::new(move || Ok(facts.clone()));
    let transport = Transport::new(
        TransportConfig::new(agent_url, env!("CARGO_PKG_VERSION")),
        Arc::new(FileIdentity::new(layout)),
        facts_source,
        outbox.clone(),
        clock.clone(),
    );
    let uptime = Duration::from_secs(3600 * (1 + u64::from(index) % 400));
    let mut generator =
        Generator::new(profile, seed, started, uptime).with_scenarios(scenarios.clone());
    let elapsed = {
        let clock = clock.clone();
        move || clock.now().duration_since(started).unwrap_or_default()
    };
    Ok(async move {
        let generate = async {
            // Spread the nodes over the second instead of sending all batches at once.
            tokio::time::sleep(TICK.mul_f64(f64::from(index % 100) / 100.0)).await;
            let mut interval = tokio::time::interval(TICK);
            interval.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);
            for tick in 0_u64.. {
                tokio::select! {
                    () = shutdown.cancelled() => break,
                    _ = interval.tick() => {}
                }
                let now = clock.now();
                if let Err(error) = outbox.push_samples(&generator.samples(now, tick), now) {
                    tracing::warn!(host = hostname, "cannot buffer the samples: {error}");
                }
                let events: Vec<_> = generator
                    .events(now)
                    .iter()
                    .map(security::to_proto)
                    .collect();
                if let Err(error) = outbox.push_security_events(events, now) {
                    tracing::warn!(
                        host = hostname,
                        "cannot buffer the security events: {error}"
                    );
                }
                if tick.is_multiple_of(5)
                    && let Err(error) = outbox.maintain(now)
                {
                    tracing::warn!(host = hostname, "buffer housekeeping failed: {error}");
                }
            }
        };
        tokio::join!(
            generate,
            connect(&transport, &scenarios, elapsed, &shutdown, &hostname)
        );
    })
}

/// Runs the transport while the node is online; scenario `offline` ends the session for its
/// silence (the samples wait in the buffer) and starts a new one afterwards.
async fn connect(
    transport: &Transport,
    scenarios: &Scenarios,
    elapsed: impl Fn() -> Duration,
    shutdown: &CancellationToken,
    hostname: &str,
) {
    let mut check = tokio::time::interval(TICK);
    check.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);
    loop {
        while !scenarios.online(elapsed()) {
            tokio::select! {
                () = shutdown.cancelled() => return,
                _ = check.tick() => {}
            }
        }
        let session = shutdown.child_token();
        let run = transport.run(session.clone());
        tokio::pin!(run);
        loop {
            tokio::select! {
                () = &mut run => return,
                _ = check.tick() => {}
            }
            if !scenarios.online(elapsed()) {
                tracing::info!(
                    host = hostname,
                    "virtual node goes silent (scenario offline)"
                );
                session.cancel();
                run.await;
                break;
            }
        }
    }
}

/// One line of [`dump`]: a sample, a security event or a change of the connection.
#[derive(Debug, serde::Serialize)]
#[serde(untagged)]
enum DumpLine<'a> {
    Sample {
        node: u32,
        at: u64,
        metric: &'a str,
        labels: BTreeMap<&'a str, &'a str>,
        value: f64,
    },
    Event {
        node: u32,
        at: u64,
        event: &'static str,
        user: &'a str,
        ip: String,
        port: u16,
    },
    Online {
        node: u32,
        at: u64,
        online: bool,
    },
}

/// Start of a dump: a fixed time, so a dump is the same for the same arguments.
pub const DUMP_START: u64 = 1_795_000_000 / 86_400 * 86_400;

/// Writes what the virtual nodes would do over `span` from [`DUMP_START`] as JSON lines, without
/// a server: the samples every [`SLOW_EVERY`] seconds (the server keeps 10-s windows), every
/// security event, and when a node goes offline or comes back. For tests of the scenarios
/// against the alert rules in fast motion.
///
/// # Errors
///
/// Invalid arguments or a failed write.
pub fn dump(args: &SimulateArgs, span: Duration, out: &mut impl Write) -> anyhow::Result<()> {
    if args.nodes == 0 || args.nodes > MAX_NODES {
        bail!("--nodes must be between 1 and {MAX_NODES}");
    }
    let scenarios = Scenarios::new(&args.scenario);
    let start = UNIX_EPOCH + Duration::from_secs(DUMP_START);
    for node in 0..args.nodes {
        let uptime = Duration::from_secs(3600 * (1 + u64::from(node) % 400));
        let mut generator = Generator::new(Profile::new(args.seed, node), args.seed, start, uptime)
            .with_scenarios(scenarios.clone());
        let mut online = None;
        for tick in 0..=span.as_secs() {
            let now = start + Duration::from_secs(tick);
            let at = DUMP_START + tick;
            let samples = generator.samples(now, tick);
            let events = generator.events(now);
            let state = generator.online(now);
            if online != Some(state) {
                online = Some(state);
                write_line(
                    out,
                    &DumpLine::Online {
                        node,
                        at,
                        online: state,
                    },
                )?;
            }
            if tick.is_multiple_of(SLOW_EVERY) {
                for sample in &samples {
                    let value = match sample.value {
                        Value::Int(v) => to_f64_i64(v),
                        Value::Float(v) => v,
                    };
                    let labels = sample
                        .labels
                        .iter()
                        .map(|(k, v)| (*k, v.as_str()))
                        .collect();
                    write_line(
                        out,
                        &DumpLine::Sample {
                            node,
                            at,
                            metric: sample.metric,
                            labels,
                            value,
                        },
                    )?;
                }
            }
            for event in &events {
                if let EventKind::SshLoginFailed { user, remote, .. } = &event.kind {
                    let ip = match &remote.address {
                        Address::Ip(ip) => ip.to_string(),
                        Address::Host(host) => host.clone(),
                    };
                    write_line(
                        out,
                        &DumpLine::Event {
                            node,
                            at,
                            event: event.kind.name(),
                            user,
                            ip,
                            port: remote.port.unwrap_or(0),
                        },
                    )?;
                }
            }
        }
    }
    out.flush().context("cannot write the dump")
}

#[allow(
    clippy::cast_precision_loss,
    clippy::as_conversions,
    reason = "dumped values are far below 2^53"
)]
fn to_f64_i64(v: i64) -> f64 {
    v as f64
}

fn write_line(out: &mut impl Write, line: &DumpLine<'_>) -> anyhow::Result<()> {
    serde_json::to_writer(&mut *out, line).context("cannot write the dump")?;
    out.write_all(b"\n").context("cannot write the dump")
}

#[cfg(test)]
#[allow(
    clippy::as_conversions,
    clippy::cast_precision_loss,
    reason = "test values are small"
)]
mod tests {
    use super::*;
    use sermon_core::Value;

    fn value(samples: &[Sample], metric: &str, label: Option<(&str, &str)>) -> f64 {
        let sample = samples
            .iter()
            .find(|s| {
                s.metric == metric
                    && label
                        .is_none_or(|(name, v)| s.labels.iter().any(|(n, x)| *n == name && x == v))
            })
            .unwrap_or_else(|| panic!("{metric} missing"));
        match sample.value {
            Value::Int(v) => v as f64,
            Value::Float(v) => v,
        }
    }

    fn at(hour: u64) -> SystemTime {
        UNIX_EPOCH + Duration::from_secs(1_795_000_000 / 86_400 * 86_400 + hour * 3600)
    }

    #[test]
    fn the_same_seed_gives_the_same_fleet_and_nodes_differ() {
        assert_eq!(Profile::new(7, 3), Profile::new(7, 3));
        assert_ne!(Profile::new(7, 3), Profile::new(7, 4));
        assert_ne!(Profile::new(7, 3), Profile::new(8, 3));
        let facts = Profile::new(7, 12).facts(7);
        assert_eq!(facts.hostname, "sim-0012");
        assert_ne!(facts.machine_id, Profile::new(7, 13).facts(7).machine_id);
    }

    #[test]
    fn values_are_plausible() {
        for index in 0..50 {
            let profile = Profile::new(1, index);
            let mut generator =
                Generator::new(profile.clone(), 1, at(0), Duration::from_secs(7200));
            for tick in 0..300 {
                let now = at(0) + Duration::from_secs(tick * 300);
                let samples = generator.samples(now, tick * SLOW_EVERY);
                let modes: f64 = samples
                    .iter()
                    .filter(|s| s.metric == "sermon_cpu_utilization_ratio")
                    .map(|s| match s.value {
                        Value::Int(v) => v as f64,
                        Value::Float(v) => v,
                    })
                    .sum();
                assert!((modes - PPM).abs() < 1.0, "modes sum to {modes}");
                let used = value(&samples, "sermon_filesystem_used_bytes", None);
                let avail = value(&samples, "sermon_filesystem_avail_bytes", None);
                assert!(avail >= 0.0 && used + avail <= profile.disk_bytes as f64);
                let memory = value(&samples, "sermon_memory_utilization_ratio", None);
                assert!((0.0..=PPM).contains(&memory));
                assert!(value(&samples, "sermon_load_average", Some(("period", "1m"))) >= 0.0);
                assert!(value(&samples, "sermon_network_receive_bytes_per_second", None) >= 0.0);
            }
        }
    }

    #[test]
    fn nodes_are_busier_in_the_afternoon_than_at_night() {
        let average = |hour: u64| {
            let mut sum = 0.0;
            for index in 0..30 {
                let mut profile = Profile::new(9, index);
                profile.phase = 0.0;
                let mut generator = Generator::new(profile, 9, at(hour), Duration::from_secs(3600));
                let samples = generator.samples(at(hour), 1);
                sum += PPM
                    - value(
                        &samples,
                        "sermon_cpu_utilization_ratio",
                        Some(("mode", "idle")),
                    );
            }
            sum / 30.0
        };
        assert!(
            average(14) > 2.0 * average(3),
            "14:00 {} vs 03:00 {}",
            average(14),
            average(3)
        );
    }

    #[test]
    fn filling_nodes_grow_towards_full() {
        let profile = Profile::new(5, 4);
        assert!(profile.disk_growth >= 50_000.0, "every fifth node fills up");
        let mut generator = Generator::new(profile, 5, at(0), Duration::from_secs(60));
        let first = value(
            &generator.samples(at(0), 0),
            "sermon_filesystem_used_bytes",
            None,
        );
        let later = value(
            &generator.samples(at(6), SLOW_EVERY),
            "sermon_filesystem_used_bytes",
            None,
        );
        assert!(later > first + 1e9, "{first} → {later}");
    }

    fn idle(samples: &[Sample]) -> f64 {
        value(
            samples,
            "sermon_cpu_utilization_ratio",
            Some(("mode", "idle")),
        ) / PPM
    }

    // cpu-high (M2-30): every node, whatever its own pattern, goes over the threshold, holds
    // between threshold and clear, then calms down.
    #[test]
    fn cpu_high_drives_every_node_through_its_phases() {
        use scenario::{CPU_HOLD, CPU_OVER, Scenario};
        for index in 0..20 {
            let mut generator =
                Generator::new(Profile::new(3, index), 3, at(14), Duration::from_secs(60))
                    .with_scenarios(Scenarios::new(&[Scenario::CpuHigh]));
            let over = idle(&generator.samples(at(14) + CPU_OVER / 2, 1));
            let hold = idle(&generator.samples(at(14) + CPU_OVER + CPU_HOLD / 2, 1));
            let calm = idle(&generator.samples(at(14) + CPU_OVER + CPU_HOLD + CPU_HOLD, 1));
            assert!(over < 0.05, "node {index}: {over}");
            assert!(hold > 0.15 && hold < 0.2, "node {index}: {hold}");
            assert!(calm >= 0.5, "node {index}: {calm}");
        }
    }

    #[test]
    fn disk_filling_and_flapping_set_the_root_file_system() {
        use scenario::{FILL_TIME, FLAP_HIGH, Scenario};
        let usage =
            |samples: &[Sample]| value(samples, "sermon_filesystem_utilization_ratio", None) / PPM;
        let mut filling = Generator::new(Profile::new(2, 0), 2, at(0), Duration::from_secs(60))
            .with_scenarios(Scenarios::new(&[Scenario::DiskFilling]));
        let start = value(
            &filling.samples(at(0), 0),
            "sermon_filesystem_avail_bytes",
            None,
        );
        let half = value(
            &filling.samples(at(0) + FILL_TIME / 2, 0),
            "sermon_filesystem_avail_bytes",
            None,
        );
        let full = filling.samples(at(0) + FILL_TIME, 0);
        assert!((half - start / 2.0).abs() < 1e6, "{start} → {half}");
        assert!(value(&full, "sermon_filesystem_avail_bytes", None).abs() < 1.0);
        assert!(usage(&full) > 0.999);

        let mut flapping = Generator::new(Profile::new(2, 0), 2, at(0), Duration::from_secs(60))
            .with_scenarios(Scenarios::new(&[Scenario::Flapping, Scenario::DiskFilling]));
        assert!((usage(&flapping.samples(at(0), 0)) - 0.85).abs() < 1e-3);
        assert!((usage(&flapping.samples(at(0) + FLAP_HIGH, 0)) - 0.70).abs() < 1e-3);
    }

    #[test]
    fn ssh_bruteforce_and_offline_reach_the_generator() {
        use scenario::{ONLINE, Scenario};
        let mut generator =
            Generator::new(Profile::new(1, 7), 1, at(0), Duration::from_secs(60)).with_scenarios(
                Scenarios::new(&[Scenario::SshBruteforce, Scenario::Offline]),
            );
        let events = generator.events(at(0) + Duration::from_secs(4));
        assert_eq!(events.len(), 1);
        assert_eq!(
            events[0].at_us,
            u64::try_from(
                (at(0) + Duration::from_secs(4))
                    .duration_since(UNIX_EPOCH)
                    .unwrap()
                    .as_micros()
            )
            .unwrap()
        );
        assert_eq!(generator.events(at(0) + Duration::from_secs(5)), []);
        assert!(generator.online(at(0)));
        assert!(!generator.online(at(0) + ONLINE));
        let plain = Generator::new(Profile::new(1, 7), 1, at(0), Duration::from_secs(60));
        assert!(plain.online(at(0) + ONLINE));
    }

    // The dump: JSON lines with samples every tenth second, every event and the connection
    // changes, the same for the same arguments.
    #[test]
    fn dump_writes_samples_events_and_connection_changes() {
        use scenario::Scenario;
        let args = SimulateArgs {
            nodes: 2,
            seed: 4,
            token: None,
            ca_fingerprint: None,
            scenario: vec![Scenario::SshBruteforce, Scenario::Offline],
            dump: None,
        };
        let mut out = Vec::new();
        dump(&args, Duration::from_secs(40), &mut out).unwrap();
        let mut again = Vec::new();
        dump(&args, Duration::from_secs(40), &mut again).unwrap();
        assert_eq!(out, again);
        let lines: Vec<serde_json::Value> = String::from_utf8(out)
            .unwrap()
            .lines()
            .map(|l| serde_json::from_str(l).unwrap())
            .collect();
        let of = |key: &str, node: u64| {
            lines
                .iter()
                .filter(|l| l.get(key).is_some() && l["node"] == node)
                .collect::<Vec<_>>()
        };
        for node in 0..2 {
            let states: Vec<_> = of("online", node)
                .iter()
                .map(|l| {
                    (
                        l["at"].as_u64().unwrap() - DUMP_START,
                        l["online"].as_bool().unwrap(),
                    )
                })
                .collect();
            assert_eq!(states, [(0, true), (30, false)]);
            assert_eq!(of("event", node).len(), 21, "one every 2 s for 0..=40 s");
            let event = of("event", node)[0];
            assert_eq!(event["event"], "ssh.login.failed");
            assert_eq!(event["ip"], scenario::ATTACKER);
            let ticks: std::collections::BTreeSet<u64> = of("metric", node)
                .iter()
                .map(|l| l["at"].as_u64().unwrap() - DUMP_START)
                .collect();
            assert_eq!(ticks.into_iter().collect::<Vec<_>>(), [0, 10, 20, 30, 40]);
        }
        let idle = lines
            .iter()
            .find(|l| {
                l["metric"] == "sermon_cpu_utilization_ratio" && l["labels"]["mode"] == "idle"
            })
            .unwrap();
        assert!(idle["value"].as_f64().unwrap() > 0.0);
        let mut empty = Vec::new();
        assert!(
            dump(
                &SimulateArgs { nodes: 0, ..args },
                Duration::from_secs(1),
                &mut empty
            )
            .is_err()
        );
    }

    #[test]
    fn slow_values_come_every_tenth_tick() {
        let mut generator = Generator::new(Profile::new(1, 0), 1, at(0), Duration::from_secs(60));
        let has_fs = |s: &[Sample]| s.iter().any(|s| s.metric == "sermon_filesystem_size_bytes");
        assert!(has_fs(&generator.samples(at(0), 0)));
        assert!(!has_fs(&generator.samples(at(0), 1)));
        assert!(has_fs(&generator.samples(at(0), 10)));
    }
}
