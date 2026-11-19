//! Command line of `sermon-agent`.

use std::fmt;
use std::path::PathBuf;

use clap::{Args, Parser, Subcommand};

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
    /// Remove the local identity and buffered data of this agent.
    Uninstall(UninstallArgs),
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
    pub ca_fingerprint: Option<String>,

    /// Replace an existing enrollment of this host.
    #[arg(long)]
    pub force: bool,
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
            "sha256:AB:CD",
            "--force",
        ])
        .unwrap();

        let Command::Enroll(args) = cli.command else {
            panic!("expected enroll");
        };
        assert_eq!(args.token.expose(), TOKEN);
        assert_eq!(TOKEN.len(), 4 + 43);
        assert_eq!(args.ca_fingerprint.as_deref(), Some("sha256:AB:CD"));
        assert!(args.force);
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
