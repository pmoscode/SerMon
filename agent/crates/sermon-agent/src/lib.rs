//! `SerMon` agent: collects metrics, logs and security events and streams them to the server.
//!
//! The binary (`main.rs`) only parses the command line; everything else lives here so that it can
//! be tested.

pub mod cli;
pub mod config;
pub mod logging;
pub mod state;

use anyhow::{Context, bail};
use tokio_util::sync::CancellationToken;

use crate::cli::{Cli, Command, UninstallArgs};
use crate::config::AgentConfig;
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
        Command::Enroll(_) => bail!("enroll is not implemented yet (M1-13)"),
        Command::Uninstall(args) => {
            let config = AgentConfig::load(&cli.config)?;
            uninstall(&StateLayout::new(&config.state_dir), &args)
        }
    }
}

/// Runs the agent until SIGTERM or SIGINT.
///
/// # Errors
///
/// If the runtime or the signal handlers can't be set up.
pub fn run(config: &AgentConfig) -> anyhow::Result<()> {
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
        shutdown.cancelled().await;
        tracing::info!("agent stopped");
        signals.await.context("signal handler failed")?
    })
}

/// Cancels `shutdown` on SIGTERM (systemd stop) or SIGINT (Ctrl+C).
async fn cancel_on_signal(shutdown: CancellationToken) -> anyhow::Result<()> {
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
