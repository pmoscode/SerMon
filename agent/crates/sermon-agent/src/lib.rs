//! `SerMon` agent: collects metrics, logs and security events and streams them to the server.
//!
//! The binary (`main.rs`) only parses the command line; everything else lives here so that it can
//! be tested.

pub mod agent_config;
pub mod cli;
pub mod config;
pub mod enroll;
pub mod helper;
pub mod identity;
pub mod journal;
pub mod liveview;
pub mod logfile;
pub mod logging;
pub mod logglob;
pub mod logs;
pub mod logship;
pub mod reboot;
pub mod scheduler;
pub mod security;
#[cfg(feature = "simulate")]
pub mod simulate;
pub mod state;

use std::sync::Arc;
use std::time::Duration;

use anyhow::{Context, bail};
use sermon_buffer::{Wal, WalConfig};
use sermon_collectors::default_collectors;
use sermon_collectors::host::read_host_facts;
use sermon_collectors::systemd::SystemdCollector;
use sermon_core::{Clock, ProcRoot, SystemClock};
use sermon_transport::outbox::Outbox;
use sermon_transport::session::{FactsSource, Transport, TransportConfig};
use tokio_util::sync::CancellationToken;

use crate::cli::{Cli, Command, HelperCommand, UninstallArgs};
use crate::config::AgentConfig;
use crate::identity::FileIdentity;
use crate::scheduler::Scheduler;
use crate::state::StateLayout;

/// Name and version as printed by `--version`.
pub const VERSION_LINE: &str = concat!("sermon-agent ", env!("CARGO_PKG_VERSION"));

/// Executes a parsed command line.
///
/// # Errors
///
/// Any error that ends the command; `main` prints it and exits with status 1.
pub fn execute(cli: Cli) -> anyhow::Result<()> {
    match cli.command {
        Command::Version => {
            println!("{VERSION_LINE}");
            Ok(())
        }
        Command::Run => {
            let config = AgentConfig::load(&cli.config)?;
            logging::init(config.log_level);
            run(&config)
        }
        Command::Enroll(args) => {
            let config = AgentConfig::load(&cli.config)?;
            enroll::enroll(&config, &args)
        }
        Command::Collect(args) => {
            let config = AgentConfig::load(&cli.config)?;
            collect(&config, args.seconds)
        }
        #[cfg(feature = "simulate")]
        Command::Simulate(args) => {
            if let Some(span) = args.dump {
                return simulate::dump(&args, span, &mut std::io::stdout().lock());
            }
            let config = AgentConfig::load(&cli.config)?;
            logging::init(config.log_level);
            simulate::simulate(&config, &args)
        }
        Command::Uninstall(args) => {
            let config = AgentConfig::load(&cli.config)?;
            uninstall(&StateLayout::new(&config.state_dir), &args)
        }
        Command::Helper(HelperCommand::Status) => {
            print!("{}", helper::status(std::path::Path::new(helper::SOCKET))?);
            Ok(())
        }
    }
}

/// Runs the agent until SIGTERM or SIGINT: buffer, transport and housekeeping.
///
/// # Errors
///
/// If the host is not enrolled, the identity or the buffer can't be opened, or the runtime or the
/// signal handlers can't be set up. Connection problems are not errors: the transport retries.
pub fn run(config: &AgentConfig) -> anyhow::Result<()> {
    let layout = StateLayout::new(&config.state_dir);
    if !identity::is_enrolled(&layout) {
        bail!("this host is not enrolled – run `sermon-agent enroll` with a token from the UI");
    }
    // Broken identity files fail loudly at the start instead of being retried forever.
    identity::load(&layout).context("cannot load the agent identity")?;
    let clock: Arc<dyn Clock> = Arc::new(SystemClock::new());
    let (wal, recovery) = Wal::open(&layout.wal_dir(), WalConfig::default(), clock.now())
        .context("cannot open the buffer")?;
    let outbox = Outbox::new(wal);
    let host_root = ProcRoot::new(&config.host_root);
    let journal_layout = layout.clone();
    let scheduler_root = host_root.clone();
    let facts_root = config.host_root.clone();
    let facts: FactsSource = Arc::new(move || {
        let mut facts = read_host_facts(&host_root)?;
        facts.security_source = logfile::security_source(&facts_root);
        Ok(facts)
    });
    // The configuration from the server (ADR 0030); the log pipeline follows it (M3-04).
    let agent_config = Arc::new(agent_config::ConfigStore::open(
        layout.agent_config_file(),
        config.local.allow_logs,
    ));
    // Log shipping (M3-04) for the one source that runs.
    let log_stats = Arc::new(logship::ShipStats::default());
    let config_updates = agent_config.subscribe();
    let shipper = logship::Shipper::new(
        agent_config.subscribe(),
        log_stats.clone(),
        logship::into_outbox(outbox.clone()),
    );
    let transport = transport(config, layout, facts, &outbox, &clock, &agent_config);

    // One thread is enough for < 1 % CPU: collectors read small /proc files, the rest is I/O.
    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .context("cannot start the async runtime")?;
    runtime.block_on(async {
        let shutdown = CancellationToken::new();
        let signals = tokio::spawn(cancel_on_signal(shutdown.clone()));
        tracing::info!(
            version = env!("CARGO_PKG_VERSION"),
            server = %config.agent_url,
            "agent started"
        );
        if !recovery.truncated.is_empty() || !recovery.removed.is_empty() {
            tracing::warn!(
                truncated = recovery.truncated.len(),
                removed = recovery.removed.len(),
                "buffer repaired after an unclean shutdown"
            );
        }
        let maintenance = tokio::spawn(maintain(outbox.clone(), clock.clone(), shutdown.clone()));
        let mut collectors = default_collectors();
        collectors.push(Box::new(logship::LogMetrics(log_stats)));
        let globs = start_log_files(
            config,
            &journal_layout,
            shipper.sibling(logship::into_outbox(outbox.clone())),
            config_updates,
            &mut collectors,
            &shutdown,
        );
        collectors.push(Box::new(SystemdCollector::for_host(
            &config.host_root,
            clock.clone(),
            journal_layout.systemd_failed_file(),
        )));
        let journal = start_log_source(
            config,
            &journal_layout,
            &outbox,
            &clock,
            shipper,
            &mut collectors,
            &shutdown,
        );
        let scheduler = Scheduler::new(collectors, scheduler_root, scheduler::SERIES_LIMIT);
        let collection = tokio::spawn(scheduler::run(
            scheduler,
            outbox.clone(),
            clock.clone(),
            shutdown.clone(),
        ));
        transport.run(shutdown.clone()).await;
        maintenance.await.context("maintenance task failed")?;
        collection.await.context("collection task failed")?;
        journal.await.context("journal task failed")?;
        globs.await.context("log file task failed")?;
        if let Err(error) = outbox.maintain(clock.now()) {
            tracing::warn!("cannot flush the buffer: {error}");
        }
        tracing::info!("agent stopped");
        signals.await.context("signal handler failed")?
    })
}

/// The transport with the server's configuration and the live view (M3-13: on demand, its
/// snapshots never buffered).
fn transport(
    config: &AgentConfig,
    layout: StateLayout,
    facts: FactsSource,
    outbox: &Outbox,
    clock: &Arc<dyn Clock>,
    agent_config: &Arc<agent_config::ConfigStore>,
) -> Transport {
    Transport::new(
        TransportConfig::new(config.agent_url.uri().clone(), env!("CARGO_PKG_VERSION")),
        Arc::new(FileIdentity::new(layout)),
        facts,
        outbox.clone(),
        clock.clone(),
    )
    .with_config(agent_config.clone())
    .with_live(Arc::new(liveview::LiveView::new(
        ProcRoot::new(&config.host_root),
        config.local.allow_live_view,
        agent_config.subscribe(),
        clock.clone(),
    )))
}

/// Interval of buffer housekeeping (fsync, age limit).
const MAINTENANCE_INTERVAL: Duration = Duration::from_secs(5);

/// Starts the log files of the server's globs (M3-05), within the same rate as the journal.
fn start_log_files(
    config: &AgentConfig,
    layout: &StateLayout,
    shipper: logship::Shipper,
    updates: tokio::sync::watch::Receiver<Arc<agent_config::Active>>,
    collectors: &mut Vec<Box<dyn sermon_collectors::Collector>>,
    shutdown: &CancellationToken,
) -> tokio::task::JoinHandle<()> {
    let stats = Arc::new(logglob::GlobStats::default());
    collectors.push(Box::new(logglob::GlobMetrics(stats.clone())));
    logglob::spawn(
        logglob::GlobConfig {
            host_root: config.host_root.clone(),
            prefixes: config.local.log_file_prefixes.clone(),
            denied: vec![config.state_dir.clone()],
            position_file: layout.log_files_position_file(),
            poll: logglob::POLL,
            rescan: logglob::RESCAN,
        },
        shipper,
        updates,
        stats,
        shutdown.clone(),
    )
}

/// Starts the source of security events and logs. The journal runs regardless of `allow_logs`:
/// that veto is about shipping logs. Without journald (Alpine) a classic log file takes its place
/// (ADR 0025).
fn start_log_source(
    config: &AgentConfig,
    layout: &StateLayout,
    outbox: &Outbox,
    clock: &Arc<dyn Clock>,
    shipper: logship::Shipper,
    collectors: &mut Vec<Box<dyn sermon_collectors::Collector>>,
    shutdown: &CancellationToken,
) -> tokio::task::JoinHandle<()> {
    if logfile::journald_runs(&config.host_root) {
        let (metrics, journal) = journal::spawn(layout, outbox.clone(), shipper, shutdown.clone());
        collectors.push(Box::new(metrics));
        return journal;
    }
    // Without journald no boot IDs come with the lines: compare the kernel's (M2-31).
    reboot::report(
        &ProcRoot::new(&config.host_root),
        &layout.boot_id_file(),
        |event| {
            outbox
                .push_security_events(vec![security::to_proto(event)], clock.now())
                .inspect_err(|error| {
                    tracing::warn!("cannot buffer the reboot, retrying at the next start: {error}");
                })
                .is_ok()
        },
    );
    logfile::spawn(
        layout,
        &config.host_root,
        outbox.clone(),
        shipper,
        shutdown.clone(),
    )
}

async fn maintain(outbox: Outbox, clock: Arc<dyn Clock>, shutdown: CancellationToken) {
    let mut tick = tokio::time::interval(MAINTENANCE_INTERVAL);
    loop {
        tokio::select! {
            () = shutdown.cancelled() => return,
            _ = tick.tick() => {
                if let Err(error) = outbox.maintain(clock.now()) {
                    tracing::warn!("buffer housekeeping failed: {error}");
                }
            }
        }
    }
}

/// Runs the collectors for `seconds` without sending anything and prints the last samples
/// (troubleshooting; also used to measure the resource budget).
///
/// # Errors
///
/// If the runtime can't be started.
pub fn collect(config: &AgentConfig, seconds: u32) -> anyhow::Result<()> {
    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .context("cannot start the async runtime")?;
    let clock = SystemClock::new();
    let mut scheduler = Scheduler::new(
        default_collectors(),
        ProcRoot::new(&config.host_root),
        scheduler::SERIES_LIMIT,
    );
    let samples = runtime.block_on(async {
        let mut interval = tokio::time::interval(scheduler::TICK);
        let mut samples = Vec::new();
        for _ in 0..seconds.max(2) {
            interval.tick().await;
            samples = scheduler.tick(clock.monotonic());
        }
        samples
    });
    for sample in samples {
        let labels: Vec<String> = sample
            .labels
            .iter()
            .map(|(name, value)| format!("{name}={value:?}"))
            .collect();
        let value = match sample.value {
            sermon_core::Value::Int(value) => value.to_string(),
            sermon_core::Value::Float(value) => value.to_string(),
        };
        println!("{}{{{}}} {value}", sample.metric, labels.join(","));
    }
    Ok(())
}

/// Cancels `shutdown` on SIGTERM (systemd stop) or SIGINT (Ctrl+C).
pub(crate) async fn cancel_on_signal(shutdown: CancellationToken) -> anyhow::Result<()> {
    use tokio::signal::unix::{SignalKind, signal};

    let mut terminate =
        signal(SignalKind::terminate()).context("cannot install the SIGTERM handler")?;
    let mut interrupt =
        signal(SignalKind::interrupt()).context("cannot install the SIGINT handler")?;
    tokio::select! {
        _ = terminate.recv() => tracing::info!("received SIGTERM"),
        _ = interrupt.recv() => tracing::info!("received SIGINT"),
        () = shutdown.cancelled() => {}
    }
    shutdown.cancel();
    Ok(())
}

/// Removes the agent's identity and buffered data after explicit confirmation.
///
/// # Errors
///
/// Without `--yes`, or if removing fails.
pub fn uninstall(layout: &StateLayout, args: &UninstallArgs) -> anyhow::Result<()> {
    if !args.yes {
        bail!(
            "this removes the identity and buffered data in {} – the host must be enrolled again; \
             confirm with --yes",
            layout.root().display()
        );
    }
    let removed = layout
        .remove_agent_data()
        .with_context(|| format!("cannot remove agent data in {}", layout.root().display()))?;
    if removed.is_empty() {
        println!("nothing to remove in {}", layout.root().display());
    }
    for path in removed {
        println!("removed {}", path.display());
    }
    Ok(())
}
