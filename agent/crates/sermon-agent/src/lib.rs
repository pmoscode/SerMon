//! `SerMon` agent: collects metrics, logs and security events and streams them to the server.
//!
//! The binary (`main.rs`) only parses the command line; everything else lives here so that it can
//! be tested.

pub mod agent_config;
pub mod cli;
pub mod config;
pub mod discovery;
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
pub mod modules;
pub mod reboot;
pub mod runs;
pub mod scheduler;
pub mod security;
#[cfg(feature = "simulate")]
pub mod simulate;
pub mod state;
pub mod updates;

use std::sync::Arc;
use std::time::Duration;

use anyhow::{Context, bail};
use sermon_buffer::{Wal, WalConfig};
use sermon_collectors::default_collectors;
use sermon_collectors::host::read_host_facts;
use sermon_collectors::systemd::{Systemctl, SystemdCollector, parse_units};
use sermon_core::{Clock, ProcRoot, SystemClock};
use sermon_transport::outbox::Outbox;
use sermon_transport::session::{FactsSource, Transport, TransportConfig};
use tokio_util::sync::CancellationToken;

use crate::cli::{Cli, Command, HelperCommand, UninstallArgs};
use crate::config::AgentConfig;
use crate::identity::FileIdentity;
use crate::scheduler::Scheduler;
use crate::state::StateLayout;

/// The agent's version: `SERMON_VERSION` at build time (the release, as in its signature), else
/// the crate's. The server compares it with the releases of the channel (ADR 0036).
pub const VERSION: &str = match option_env!("SERMON_VERSION") {
    Some(v) => v,
    None => env!("CARGO_PKG_VERSION"),
};

/// Executes a parsed command line.
///
/// # Errors
///
/// Any error that ends the command; `main` prints it and exits with status 1.
pub fn execute(cli: Cli) -> anyhow::Result<()> {
    match cli.command {
        Command::Version => {
            println!("sermon-agent {VERSION}");
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
    let journal_layout = layout.clone();
    let scheduler_root = ProcRoot::new(&config.host_root);
    let facts = facts_source(config);
    // The configuration from the server (ADR 0030); the log pipeline follows it (M3-04).
    let agent_config = Arc::new(agent_config::ConfigStore::open(
        layout.agent_config_file(),
        config.local.allow_logs,
        config.local.modules.allow,
    ));
    // Log shipping (M3-04) for the one source that runs.
    let log_stats = Arc::new(logship::ShipStats::default());
    let config_updates = agent_config.subscribe();
    let shipper = logship::Shipper::new(
        agent_config.subscribe(),
        log_stats.clone(),
        logship::into_outbox(outbox.clone()),
    );
    let updates = UpdateSignals::default();
    // The server's commands for the root helper (M4-12).
    let (inbox, ports) = run_ports(config, &layout, &clock);
    let (module_runtime, discovery) = module_runtime(config, &outbox, &clock, &agent_config);
    let transport = transport(config, layout, facts, &outbox, &clock, &agent_config)
        .with_modules(module_runtime.ids())
        .with_update_refresh(updates.refresh.clone())
        .with_commands(Arc::new(inbox));

    // One thread is enough for < 1 % CPU: collectors read small /proc files, the rest is I/O.
    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .context("cannot start the async runtime")?;
    runtime.block_on(async {
        let shutdown = CancellationToken::new();
        let signals = tokio::spawn(cancel_on_signal(shutdown.clone()));
        tracing::info!(
            version = VERSION,
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
        let reports = spawn_updates(config, &outbox, &clock, &updates, &shutdown);
        let runner = spawn_runs(ports, &outbox, &clock, &agent_config, &updates, &shutdown);
        let integrations = spawn_modules(module_runtime, discovery, &agent_config, &shutdown);
        transport.run(shutdown.clone()).await;
        maintenance.await.context("maintenance task failed")?;
        reports.await.context("update task failed")?;
        runner.await.context("run task failed")?;
        integrations.await.context("module task failed")?;
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
    let mut transport_config = TransportConfig::new(config.agent_url.uri().clone(), VERSION);
    transport_config.install_method = install_method();
    Transport::new(
        transport_config,
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

/// The modules built in (ADR 0039), bounded by the veto of `agent.toml`; their reasons redacted
/// with the organization's rules. With them their discovery.
fn module_runtime(
    config: &AgentConfig,
    outbox: &Outbox,
    clock: &Arc<dyn Clock>,
    agent_config: &Arc<agent_config::ConfigStore>,
) -> (
    sermon_modules::runtime::Runtime,
    Option<discovery::Discovery>,
) {
    let store = agent_config.clone();
    let sink = modules::OutboxSink::new(
        outbox.clone(),
        Arc::new(move |text: &str| store.current().redactor.redact(text).into_owned()),
    );
    let runtime = sermon_modules::runtime::Runtime::new(
        sermon_modules::builtin(),
        config.local.modules.clone(),
        Arc::new(sink),
        clock.clone(),
    );
    (runtime, discover(config, outbox, clock))
}

/// Starts the modules (ADR 0039): they follow the server's configuration; their discovery
/// alongside.
fn spawn_modules(
    runtime: sermon_modules::runtime::Runtime,
    discovery: Option<discovery::Discovery>,
    agent_config: &agent_config::ConfigStore,
    shutdown: &CancellationToken,
) -> tokio::task::JoinHandle<()> {
    let modules = modules::run(runtime, agent_config.subscribe(), shutdown.clone());
    let shutdown = shutdown.clone();
    tokio::spawn(async move {
        let discover = async {
            if let Some(d) = discovery {
                discovery::run(d, shutdown).await;
            }
        };
        tokio::join!(modules, discover);
    })
}

/// The discovery of the modules (M5-04); none if `agent.toml` forbids modules.
fn discover(
    config: &AgentConfig,
    outbox: &Outbox,
    clock: &Arc<dyn Clock>,
) -> Option<discovery::Discovery> {
    if !config.local.modules.allow {
        return None;
    }
    let systemctl = Systemctl::on_host(&config.host_root);
    let units: discovery::Units = Arc::new(move || {
        let Some(systemctl) = &systemctl else {
            return Some(Vec::new());
        };
        match systemctl.running_services() {
            Ok(text) => Some(parse_units(&text, sermon_modules::discovery::MAX_FACTS)),
            Err(error) => {
                tracing::debug!("cannot list the running services: {error}");
                None
            }
        }
    });
    Some(discovery::Discovery {
        modules: sermon_modules::builtin(),
        root: ProcRoot::new(&config.host_root),
        units,
        sink: Arc::new(outbox.clone()),
        clock: clock.clone(),
    })
}

/// The root helper of the runs, with the release fetcher of the self-update, and the run book of
/// this process: a self-update's new agent confirms the run the old one took (ADR 0036).
fn run_ports(
    config: &AgentConfig,
    layout: &StateLayout,
    clock: &Arc<dyn Clock>,
) -> (runs::Inbox, RunPorts) {
    let started = clock
        .now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap_or_default();
    let instance = format!("{}x{}", std::process::id(), started.as_nanos());
    let helper = runs::SocketHelper {
        socket: helper::SOCKET.into(),
        fetcher: Some(sermon_transport::release::ReleaseFetcher::new(
            config.agent_url.uri().clone(),
            Arc::new(FileIdentity::new(layout.clone())),
        )),
        staging: layout.root().join("self-update"),
    };
    let (inbox, commands) = runs::Inbox::new();
    let book = runs::Book::open(&layout.runs_file(), &instance);
    (inbox, (helper, book, commands))
}

/// What the runner works with.
type RunPorts = (runs::SocketHelper, runs::Book, runs::Commands);

/// How this agent was installed, from the path of its binary (ADR 0036).
fn install_method() -> sermon_transport::proto::agent::v1::InstallMethod {
    use sermon_transport::proto::agent::v1::InstallMethod;
    match std::env::current_exe()
        .ok()
        .as_deref()
        .and_then(std::path::Path::to_str)
    {
        Some("/usr/local/bin/sermon-agent") => InstallMethod::Script,
        Some("/usr/bin/sermon-agent") => InstallMethod::Package,
        _ => InstallMethod::Unspecified,
    }
}

/// What wakes the update information (M4-07): the server's `RefreshUpdateInfo` and the end of a
/// run of the root helper (M4-28).
#[derive(Default)]
struct UpdateSignals {
    refresh: Arc<tokio::sync::Notify>,
    ran: Arc<tokio::sync::Notify>,
}

/// Starts the update reports (M4-07): listed by the root helper, buffered like the metrics.
fn spawn_updates(
    config: &AgentConfig,
    outbox: &Outbox,
    clock: &Arc<dyn Clock>,
    signals: &UpdateSignals,
    shutdown: &CancellationToken,
) -> tokio::task::JoinHandle<()> {
    let (outbox, clock) = (outbox.clone(), clock.clone());
    tokio::spawn(updates::run(
        Arc::new(updates::HelperSource::new(
            helper::SOCKET,
            &config.host_root,
        )),
        config.host_root.clone(),
        move |report| {
            if let Err(error) = outbox.push_update_report(report, clock.now()) {
                tracing::warn!("cannot buffer the update report: {error}");
            }
        },
        signals.refresh.clone(),
        signals.ran.clone(),
        updates::Schedule::default(),
        updates::Spread::random(),
        shutdown.clone(),
    ))
}

/// The host facts of the Hello, read at each connect.
fn facts_source(config: &AgentConfig) -> FactsSource {
    let host_root = ProcRoot::new(&config.host_root);
    let facts_root = config.host_root.clone();
    Arc::new(move || {
        let mut facts = read_host_facts(&host_root)?;
        facts.security_source = logfile::security_source(&facts_root);
        Ok(facts)
    })
}

/// Starts the runs of the root helper (M4-12): their results are buffered like the metrics, their
/// output redacted like the logs.
fn spawn_runs(
    (helper, book, commands): RunPorts,
    outbox: &Outbox,
    clock: &Arc<dyn Clock>,
    agent_config: &Arc<agent_config::ConfigStore>,
    signals: &UpdateSignals,
    shutdown: &CancellationToken,
) -> tokio::task::JoinHandle<()> {
    let (outbox, sink_clock, now_clock, config) = (
        outbox.clone(),
        clock.clone(),
        clock.clone(),
        agent_config.clone(),
    );
    tokio::spawn(runs::run(
        Arc::new(helper),
        book,
        commands,
        move |result| {
            if let Err(error) = outbox.push_command_result(result, sink_clock.now()) {
                tracing::warn!("cannot buffer the result of a run: {error}");
            }
        },
        move |text: &str| config.current().redactor.redact(text).into_owned(),
        move || now_clock.now(),
        signals.ran.clone(),
        runs::Schedule::default(),
        shutdown.clone(),
    ))
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
