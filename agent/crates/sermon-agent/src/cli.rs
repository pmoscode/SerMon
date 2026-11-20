//! Command line of `sermon-agent`.

use std::fmt;
use std::path::PathBuf;

use clap::{Args, Parser, Subcommand};

use sermon_transport::fingerprint::Fingerprint;

use crate::config::DEFAULT_CONFIG_PATH;

/// SerMon agent: collects metrics, logs and security events and streams them to the server.
#[derive(Debug, Parser)]
#[command(name = "sermon-agent", version, propagate_version = true)]
pub struct Cli {
    /// Path of the agent configuration.
    #[arg(
        long,
        global = true,
        value_name = "PATH",
        default_value = DEFAULT_CONFIG_PATH
    )]
    pub config: PathBuf,

    /// What to do.
    #[command(subcommand)]
    pub command: Command,
}

/// The subcommands.
#[derive(Debug, Subcommand)]
pub enum Command {
    /// Run the agent (started by systemd or OpenRC).
    Run,
    /// Register this host with the server using an enrollment token from the UI.
    Enroll(EnrollArgs),
    /// Run the collectors locally and print the samples (nothing is sent).
    Collect(CollectArgs),
    /// Remove the local identity and buffered data of this agent.
    Uninstall(UninstallArgs),
    /// Run virtual nodes with synthetic metrics against a server (development and load tests).
    #[cfg(feature = "simulate")]
    Simulate(SimulateArgs),
    /// Print the version.
    Version,
}

/// Arguments of `enroll`.
#[derive(Debug, Args)]
pub struct EnrollArgs {
    /// Enrollment token ("sme_…"). Prefer the environment variable: command line arguments are
    /// visible to other users in the process list.
    #[arg(
        long,
        env = "SERMON_ENROLLMENT_TOKEN",
        hide_env_values = true,
        value_name = "TOKEN"
    )]
    pub token: SecretArg,

    /// SHA-256 fingerprint of the server certificate, only needed if it is self-signed
    /// ("sha256:AB:CD:…").
    #[arg(long, value_name = "FINGERPRINT")]
    pub ca_fingerprint: Option<Fingerprint>,

    /// Replace an existing enrollment of this host.
    #[arg(long)]
    pub force: bool,
}

/// Arguments of `collect`.
#[derive(Debug, Args)]
pub struct CollectArgs {
    /// Collect for this many seconds (at least 2, rates need two runs).
    #[arg(long, default_value_t = 2)]
    pub seconds: u32,
}

/// Arguments of `simulate`.
#[cfg(feature = "simulate")]
#[derive(Debug, Args)]
pub struct SimulateArgs {
    /// Number of virtual nodes.
    #[arg(long, default_value_t = 10)]
    pub nodes: u32,

    /// Seed of the synthetic fleet: the same seed gives the same nodes and patterns.
    #[arg(long, default_value_t = 1)]
    pub seed: u64,

    /// Enrollment token for nodes without identity yet ("sme_…", needs enough uses). Prefer the
    /// environment variable.
    #[arg(
        long,
        env = "SERMON_ENROLLMENT_TOKEN",
        hide_env_values = true,
        value_name = "TOKEN"
    )]
    pub token: Option<SecretArg>,

    /// SHA-256 fingerprint of a self-signed server certificate ("sha256:AB:CD:…").
    #[arg(long, value_name = "FINGERPRINT")]
    pub ca_fingerprint: Option<Fingerprint>,
}

/// Arguments of `uninstall`.
#[derive(Debug, Args)]
pub struct UninstallArgs {
    /// Confirm the removal: the host has to be enrolled again afterwards.
    #[arg(long)]
    pub yes: bool,
}

/// A secret command line value. It never appears in `Debug` output, and clap never echoes it
/// in error messages because parsing can't fail; checking happens afterwards.
#[derive(Clone, PartialEq, Eq)]
pub struct SecretArg(String);

impl SecretArg {
    /// The secret value.
    #[must_use]
    pub fn expose(&self) -> &str {
        &self.0
    }
}

impl std::str::FromStr for SecretArg {
    type Err = std::convert::Infallible;

    fn from_str(value: &str) -> Result<Self, Self::Err> {
        Ok(Self(value.to_owned()))
    }
}

impl fmt::Debug for SecretArg {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("SecretArg(***)")
    }
}

/// A syntactically valid enrollment token ("sme_" + base64url). Never shown in `Debug` output.
#[derive(Clone, PartialEq, Eq)]
pub struct EnrollmentToken(String);

/// The value is not an enrollment token. The message never contains the value: it may be a
/// mistyped real token.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
#[error("--token is not an enrollment token (expected sme_… as shown in the UI)")]
pub struct InvalidToken;

impl EnrollmentToken {
    /// Longest token the server accepts.
    pub const MAX_LEN: usize = 64;
    const PREFIX: &str = "sme_";

    /// Checks the form of a token from the command line.
    ///
    /// # Errors
    ///
    /// [`InvalidToken`] if it doesn't look like an enrollment token.
    pub fn parse(secret: &SecretArg) -> Result<Self, InvalidToken> {
        let value = secret.expose();
        let valid = value.starts_with(Self::PREFIX)
            && value.len() > Self::PREFIX.len()
            && value.len() <= Self::MAX_LEN
            && value
                .bytes()
                .all(|byte| byte.is_ascii_alphanumeric() || byte == b'_' || byte == b'-');
        if valid {
            Ok(Self(value.to_owned()))
        } else {
            Err(InvalidToken)
        }
    }

    /// The token for the enrollment request.
    #[must_use]
    pub fn expose(&self) -> &str {
        &self.0
    }
}

impl fmt::Debug for EnrollmentToken {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("EnrollmentToken(***)")
    }
}

#[cfg(test)]
mod tests {
    use clap::CommandFactory;

    use super::*;

    const FINGERPRINT: &str = "sha256:E3:B0:C4:42:98:FC:1C:14:9A:FB:F4:C8:99:6F:B9:24:27:AE:41:E4:64:9B:93:4C:A4:95:99:1B:78:52:B8:55";
    const TOKEN: &str = "sme_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_abcde";

    fn parse(args: &[&str]) -> Result<Cli, clap::Error> {
        Cli::try_parse_from(std::iter::once("sermon-agent").chain(args.iter().copied()))
    }

    #[test]
    fn command_definition_is_consistent() {
        Cli::command().debug_assert();
    }

    #[test]
    fn run_uses_the_default_config_path() {
        let cli = parse(&["run"]).unwrap();

        assert!(matches!(cli.command, Command::Run));
        assert_eq!(cli.config, PathBuf::from("/etc/sermon/agent.toml"));
    }

    #[test]
    fn config_path_is_accepted_before_and_after_the_subcommand() {
        assert_eq!(
            parse(&["--config", "/tmp/a.toml", "run"]).unwrap().config,
            PathBuf::from("/tmp/a.toml")
        );
        assert_eq!(
            parse(&["run", "--config", "/tmp/b.toml"]).unwrap().config,
            PathBuf::from("/tmp/b.toml")
        );
    }

    #[test]
    fn enroll_takes_token_fingerprint_and_force() {
        let cli = parse(&[
            "enroll",
            "--token",
            TOKEN,
            "--ca-fingerprint",
            FINGERPRINT,
            "--force",
        ])
        .unwrap();

        let Command::Enroll(args) = cli.command else {
            panic!("expected enroll");
        };
        assert_eq!(args.token.expose(), TOKEN);
        assert_eq!(TOKEN.len(), 4 + 43);
        assert_eq!(args.ca_fingerprint, Some(FINGERPRINT.parse().unwrap()));
        assert!(args.force);
    }

    #[test]
    fn enroll_rejects_a_malformed_fingerprint() {
        let error = parse(&[
            "enroll",
            "--token",
            TOKEN,
            "--ca-fingerprint",
            "sha256:AB:CD",
        ])
        .unwrap_err();

        assert_eq!(error.kind(), clap::error::ErrorKind::ValueValidation);
    }

    #[test]
    fn enroll_requires_a_token() {
        let error = parse(&["enroll"]).unwrap_err();

        assert_eq!(
            error.kind(),
            clap::error::ErrorKind::MissingRequiredArgument
        );
    }

    #[test]
    fn accepts_a_token_in_the_format_of_the_server() {
        // "sme_" + 256 bits base64url without padding, as created by the server.
        let token = EnrollmentToken::parse(&secret(TOKEN)).unwrap();

        assert_eq!(token.expose(), TOKEN);
    }

    #[test]
    fn rejects_malformed_tokens_without_echoing_them() {
        let too_long = format!("sme_{}", "a".repeat(EnrollmentToken::MAX_LEN));
        for token in [
            "",
            "sme_",
            "abc_123",
            "sme_with space",
            "sme_ä",
            too_long.as_str(),
        ] {
            let error = EnrollmentToken::parse(&secret(token)).unwrap_err();
            if token.len() > "sme_".len() {
                assert!(!error.to_string().contains(token), "{token:?} echoed");
            }
        }
    }

    #[test]
    fn secrets_are_redacted_in_debug_output() {
        let cli = parse(&["enroll", "--token", "sme_typo with space"]).unwrap();

        assert!(!format!("{cli:?}").contains("typo"), "{cli:?}");
        assert_eq!(
            format!("{:?}", EnrollmentToken::parse(&secret(TOKEN)).unwrap()),
            "EnrollmentToken(***)"
        );
    }

    fn secret(value: &str) -> SecretArg {
        value.parse().unwrap()
    }

    /// The simulator enrolls many identities with one token: release builds don't contain it.
    #[cfg(not(feature = "simulate"))]
    #[test]
    fn release_builds_have_no_simulator() {
        let error = parse(&["simulate", "--nodes", "3"]).unwrap_err();
        assert_eq!(error.kind(), clap::error::ErrorKind::InvalidSubcommand);
    }

    #[test]
    fn uninstall_needs_explicit_confirmation_flag() {
        let Command::Uninstall(args) = parse(&["uninstall"]).unwrap().command else {
            panic!("expected uninstall");
        };
        assert!(!args.yes);

        let Command::Uninstall(args) = parse(&["uninstall", "--yes"]).unwrap().command else {
            panic!("expected uninstall");
        };
        assert!(args.yes);
    }

    #[test]
    fn a_subcommand_is_required() {
        let error = parse(&[]).unwrap_err();

        assert_eq!(
            error.kind(),
            clap::error::ErrorKind::DisplayHelpOnMissingArgumentOrSubcommand
        );
    }
}
