//! `sermon-agent enroll`: register the host with the server (KONZEPT §6, ADR 0009).

use std::time::{Duration, SystemTime};

use anyhow::{Context, bail};
use sermon_collectors::host::read_host_facts;
use sermon_core::{ProcRoot, SystemClock};
use sermon_transport::identity::AgentKey;
use sermon_transport::tls::{ServerTrust, enrollment_config};
use sermon_transport::{channel, enrollment};

use crate::cli::{EnrollArgs, EnrollmentToken};
use crate::config::AgentConfig;
use crate::identity;
use crate::state::StateLayout;

/// Upper bound for the enrollment request (the server signs one CSR).
const ENROLL_TIMEOUT: Duration = Duration::from_secs(30);

/// Enrolls the host and stores the new identity.
///
/// Order matters for safety: everything that can be checked locally (token format, existing
/// enrollment, host facts) is checked before anything is sent, and nothing is stored before the
/// issued certificate has been verified.
///
/// # Errors
///
/// Any reason the enrollment failed, with a hint what to do.
pub fn enroll(config: &AgentConfig, args: &EnrollArgs) -> anyhow::Result<()> {
    let token = EnrollmentToken::parse(&args.token)?;
    let layout = StateLayout::new(&config.state_dir);
    if identity::is_enrolled(&layout) && !args.force {
        bail!(
            "this host is already enrolled ({}) – use --force to replace the identity",
            layout.identity_dir().display()
        );
    }
    let trust = match (config.server_url.is_tls(), args.ca_fingerprint) {
        (true, Some(pin)) => Some(ServerTrust::Pinned(pin)),
        (true, None) => Some(ServerTrust::SystemRoots),
        (false, Some(_)) => bail!("--ca-fingerprint needs an https:// server URL"),
        (false, None) => None,
    };
    let facts =
        read_host_facts(&ProcRoot::new(&config.host_root)).context("cannot read the host facts")?;
    let tls = trust.as_ref().map(enrollment_config).transpose()?;
    let key = AgentKey::generate()?;

    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .context("cannot start the async runtime")?;
    let issued = runtime.block_on(async {
        let channel =
            channel::connect(config.server_url.uri().clone(), tls, ENROLL_TIMEOUT).await?;
        let issued = enrollment::enroll(
            channel,
            &key,
            token.expose(),
            &facts,
            env!("CARGO_PKG_VERSION"),
            &SystemClock::new(),
        )
        .await?;
        anyhow::Ok(issued)
    })?;

    identity::store(&layout, &key, &issued).context("cannot store the new identity")?;
    let days = issued
        .not_after
        .duration_since(SystemTime::now())
        .unwrap_or_default()
        .as_secs()
        / (24 * 60 * 60);
    println!(
        "enrolled as node {} (organization {}); certificate valid for {days} days, renewed automatically",
        issued.identity.node_id, issued.identity.org_id
    );
    Ok(())
}
