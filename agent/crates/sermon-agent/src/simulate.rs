//! `sermon-agent simulate`: virtual nodes with realistic metrics for development, end-to-end and
//! load tests (M1-34). Every virtual node is a real agent towards the server: it enrolls with a
//! token, holds its own identity and mTLS session, buffers in its own WAL and sends the metrics of
//! the catalog. Only the values are synthetic: a daily pattern, per-node character, noise,
//! occasional spikes and slowly filling disks – deterministic per seed and node.

use std::f64::consts::TAU;
use std::path::Path;
use std::sync::Arc;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use anyhow::{Context, bail};
use sermon_buffer::{FsyncPolicy, Wal, WalConfig};
use sermon_collectors::rate::{round_to_i64, to_f64};
use sermon_core::{Architecture, Clock, HostFacts, MachineId, Sample, SystemClock};
use sermon_transport::identity::AgentKey;
use sermon_transport::outbox::Outbox;
use sermon_transport::session::{FactsSource, Transport, TransportConfig};
use sermon_transport::tls::{ServerTrust, enrollment_config};
use sermon_transport::{channel, enrollment};
use tokio_util::sync::CancellationToken;

use crate::cli::{EnrollmentToken, SimulateArgs};
use crate::config::AgentConfig;
use crate::identity::{self, FileIdentity};
use crate::state::StateLayout;

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
#[derive(Debug, Clone)]
pub struct Generator {
    profile: Profile,
    rng: Rng,
    boot: SystemTime,
    started: SystemTime,
    load: [f64; 3],
    spike_left: u32,
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
        }
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
        let busy =
            (self.profile.cpu_base + self.profile.cpu_peak * day + spike + 0.02 * self.rng.noise())
                .clamp(0.005, 0.99);
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
        let elapsed = now
            .duration_since(self.started)
            .unwrap_or_default()
            .as_secs_f64();
        let reserved = size * 0.05;
        let used = (size * p.disk_fill + p.disk_growth * elapsed).min(size - reserved);
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
        let mut tasks = Vec::new();
        for (index, layout) in (0_u32..).zip(layouts) {
            tasks.push(tokio::spawn(run_node(
                config.agent_url.uri().clone(),
                args.seed,
                index,
                layout,
                clock.clone(),
                shutdown.clone(),
            )?));
        }
        tracing::info!(nodes = args.nodes, seed = args.seed, server = %config.agent_url, "simulation started");
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

/// Starts one virtual node: its transport and its sample generator.
fn run_node(
    agent_url: http::Uri,
    seed: u64,
    index: u32,
    layout: StateLayout,
    clock: Arc<dyn Clock>,
    shutdown: CancellationToken,
) -> anyhow::Result<impl Future<Output = ()>> {
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
    let mut generator = Generator::new(profile, seed, clock.now(), uptime);
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
                if tick.is_multiple_of(5)
                    && let Err(error) = outbox.maintain(now)
                {
                    tracing::warn!(host = hostname, "buffer housekeeping failed: {error}");
                }
            }
        };
        tokio::join!(generate, transport.run(shutdown.clone()));
    })
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

    #[test]
    fn slow_values_come_every_tenth_tick() {
        let mut generator = Generator::new(Profile::new(1, 0), 1, at(0), Duration::from_secs(60));
        let has_fs = |s: &[Sample]| s.iter().any(|s| s.metric == "sermon_filesystem_size_bytes");
        assert!(has_fs(&generator.samples(at(0), 0)));
        assert!(!has_fs(&generator.samples(at(0), 1)));
        assert!(has_fs(&generator.samples(at(0), 10)));
    }
}
