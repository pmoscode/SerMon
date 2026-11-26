//! Local agent configuration `/etc/sermon/agent.toml` (KONZEPT §4.1).
//!
//! Almost nothing is configured locally: the server URLs, paths and local vetoes. Everything else
//! comes versioned from the server. Vetoes in this file can't be overridden by the server.
//!
//! ```toml
//! log_level = "info"                              # error | warn | info | debug | trace
//!
//! [server]
//! url = "https://sermon.example.com"              # public HTTPS endpoint (enrollment)
//! agent_url = "https://sermon.example.com:8443"   # optional: mTLS agent API (default: port 8443)
//!
//! [paths]
//! state_dir = "/var/lib/sermon-agent"             # optional: identity and buffer
//! host_root = "/"                                 # optional: root with /proc, /sys, /etc
//!
//! [local]                                         # optional vetoes, default: allowed
//! allow_live_view = true
//! allow_logs = true
//! log_file_prefixes = ["/var/log"]                # where the server's file globs may read
//! allow_modules = true                            # modules (ADR 0039) at all
//! module_targets = ["127.0.0.0/8", "::1/128"]     # networks modules may connect to
//! module_sockets = ["/run", "/var/run"]           # Unix sockets modules may use
//! module_file_prefixes = ["/etc/ssl", "/etc/pki", "/etc/letsencrypt"]  # files modules may read
//! allow_docker_socket = false                     # root-equivalent (O4)
//! ```

use std::fmt;
use std::net::IpAddr;
use std::path::{Path, PathBuf};

use http::Uri;
use serde::Deserialize;

/// Where the agent looks for its configuration by default.
pub const DEFAULT_CONFIG_PATH: &str = "/etc/sermon/agent.toml";
/// Where the agent keeps its identity and buffer by default.
pub const DEFAULT_STATE_DIR: &str = "/var/lib/sermon-agent";
/// Port of the mTLS agent API if `server.agent_url` is not set.
pub const DEFAULT_AGENT_PORT: u16 = 8443;

/// Errors when loading the configuration. Messages never contain secrets: the file has none.
#[derive(Debug, thiserror::Error)]
pub enum ConfigError {
    /// The file could not be read.
    #[error("cannot read {path}: {source}")]
    Read {
        /// Path of the file.
        path: PathBuf,
        /// Cause.
        #[source]
        source: std::io::Error,
    },
    /// The file is not valid TOML or has unknown keys or wrong types.
    #[error("invalid configuration: {0}")]
    Syntax(String),
    /// One or more values are invalid; all problems are listed at once.
    #[error("invalid configuration: {}", .0.join("; "))]
    Invalid(Vec<String>),
}

/// The validated agent configuration.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AgentConfig {
    /// Public HTTPS endpoint of the server, used for enrollment (ADR 0009).
    pub server_url: ServerUrl,
    /// mTLS agent API of the server.
    pub agent_url: ServerUrl,
    /// Directory for identity (key, certificates) and buffer.
    pub state_dir: PathBuf,
    /// Root with `proc/`, `sys/` and `etc/` (`/` on a host, the mounted host in a container).
    pub host_root: PathBuf,
    /// Minimum level of the agent's own log output.
    pub log_level: LogLevel,
    /// Local vetoes.
    pub local: LocalPolicy,
}

/// Local vetoes: things the server may ask for, but the host's admin forbids.
#[derive(Debug, Clone, PartialEq, Eq)]
#[allow(
    clippy::struct_excessive_bools,
    reason = "independent switches from the config file"
)]
pub struct LocalPolicy {
    /// The live process view may be started from the server (§9).
    pub allow_live_view: bool,
    /// Log lines may be shipped to the server (§10). Security events are not affected.
    pub allow_logs: bool,
    /// The directories below which the server's log file globs may read (M3-05); the state
    /// directory, `/etc/sermon`, `/proc`, `/sys` and `/dev` never.
    pub log_file_prefixes: Vec<PathBuf>,
    /// What the modules may reach (ADR 0039).
    pub modules: sermon_modules::Veto,
}

/// Where the server's log file globs may read by default.
pub const DEFAULT_LOG_FILE_PREFIX: &str = "/var/log";

impl Default for LocalPolicy {
    fn default() -> Self {
        Self {
            allow_live_view: true,
            allow_logs: true,
            log_file_prefixes: vec![PathBuf::from(DEFAULT_LOG_FILE_PREFIX)],
            modules: sermon_modules::Veto::default(),
        }
    }
}

/// Minimum level of the agent's own log output.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum LogLevel {
    /// Errors only.
    Error,
    /// Warnings and errors.
    Warn,
    /// Normal operation (default).
    #[default]
    Info,
    /// Details for troubleshooting.
    Debug,
    /// Everything.
    Trace,
}

impl AgentConfig {
    /// Reads and validates the configuration file at `path`.
    ///
    /// # Errors
    ///
    /// [`ConfigError`] if the file can't be read or is invalid.
    pub fn load(path: &Path) -> Result<Self, ConfigError> {
        let text = std::fs::read_to_string(path).map_err(|source| ConfigError::Read {
            path: path.to_owned(),
            source,
        })?;
        Self::parse(&text)
    }

    /// Parses and validates the configuration from TOML text.
    ///
    /// # Errors
    ///
    /// [`ConfigError::Syntax`] for malformed TOML, unknown keys or wrong types,
    /// [`ConfigError::Invalid`] listing every invalid value.
    pub fn parse(text: &str) -> Result<Self, ConfigError> {
        let raw: RawConfig =
            toml::from_str(text).map_err(|error| ConfigError::Syntax(error.to_string()))?;
        raw.validate()
    }
}

#[derive(Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
struct RawConfig {
    log_level: Option<LogLevel>,
    server: Option<RawServer>,
    #[serde(default)]
    paths: RawPaths,
    #[serde(default)]
    local: RawLocal,
}

#[derive(Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
struct RawServer {
    url: Option<String>,
    agent_url: Option<String>,
}

#[derive(Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
struct RawPaths {
    state_dir: Option<PathBuf>,
    host_root: Option<PathBuf>,
}

#[derive(Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
struct RawLocal {
    allow_live_view: Option<bool>,
    allow_logs: Option<bool>,
    log_file_prefixes: Option<Vec<PathBuf>>,
    allow_modules: Option<bool>,
    module_targets: Option<Vec<String>>,
    module_sockets: Option<Vec<PathBuf>>,
    module_file_prefixes: Option<Vec<PathBuf>>,
    allow_docker_socket: Option<bool>,
}

impl RawConfig {
    fn validate(self) -> Result<AgentConfig, ConfigError> {
        let mut problems = Vec::new();
        let server = self.server.unwrap_or_default();

        let server_url = match server.url {
            None => {
                problems.push("server.url: required".to_owned());
                None
            }
            Some(raw) => ServerUrl::parse(&raw, PlainHttp::LoopbackOnly)
                .map_err(|reason| problems.push(format!("server.url: {reason}")))
                .ok(),
        };
        let agent_url = match server.agent_url {
            Some(raw) => ServerUrl::parse(&raw, PlainHttp::Never)
                .map_err(|reason| problems.push(format!("server.agent_url: {reason}")))
                .ok(),
            None => server_url
                .as_ref()
                .map(|url| url.tls_on_port(DEFAULT_AGENT_PORT)),
        };
        let state_dir = checked_dir(
            "paths.state_dir",
            self.paths.state_dir,
            DEFAULT_STATE_DIR,
            &mut problems,
        );
        let host_root = checked_dir("paths.host_root", self.paths.host_root, "/", &mut problems);
        if state_dir == Path::new("/") {
            problems.push("paths.state_dir: must not be the root directory".to_owned());
        }
        let log_file_prefixes = self
            .local
            .log_file_prefixes
            .clone()
            .unwrap_or_else(|| vec![PathBuf::from(DEFAULT_LOG_FILE_PREFIX)]);
        check_prefixes("local.log_file_prefixes", &log_file_prefixes, &mut problems);
        let modules = module_veto(&self.local, &mut problems);

        match (server_url, agent_url) {
            (Some(server_url), Some(agent_url)) if problems.is_empty() => Ok(AgentConfig {
                server_url,
                agent_url,
                state_dir,
                host_root,
                log_level: self.log_level.unwrap_or_default(),
                local: LocalPolicy {
                    allow_live_view: self.local.allow_live_view.unwrap_or(true),
                    allow_logs: self.local.allow_logs.unwrap_or(true),
                    log_file_prefixes,
                    modules,
                },
            }),
            _ => Err(ConfigError::Invalid(problems)),
        }
    }
}

/// Prefixes are absolute and plain: compared by segment, `.` and `..` would mislead.
fn check_prefixes(key: &str, prefixes: &[PathBuf], problems: &mut Vec<String>) {
    for prefix in prefixes {
        // By text: `Path::components` drops `.` in the middle.
        let normal = prefix
            .to_str()
            .is_some_and(|p| p.split('/').skip(1).all(|s| !matches!(s, "." | "..")));
        if !prefix.is_absolute() || !normal {
            problems.push(format!(
                "{key}: {} must be an absolute path without . and ..",
                prefix.display()
            ));
        }
    }
}

/// The modules' veto (ADR 0039); what isn't set keeps the default.
fn module_veto(local: &RawLocal, problems: &mut Vec<String>) -> sermon_modules::Veto {
    let mut veto = sermon_modules::Veto::default();
    if let Some(allow) = local.allow_modules {
        veto.allow = allow;
    }
    if let Some(targets) = &local.module_targets {
        veto.targets = targets
            .iter()
            .filter_map(|t| {
                sermon_modules::veto::Net::parse(t)
                    .map_err(|why| problems.push(format!("local.module_targets: {why}")))
                    .ok()
            })
            .collect();
    }
    if let Some(sockets) = &local.module_sockets {
        check_prefixes("local.module_sockets", sockets, problems);
        veto.sockets.clone_from(sockets);
    }
    if let Some(prefixes) = &local.module_file_prefixes {
        check_prefixes("local.module_file_prefixes", prefixes, problems);
        veto.file_prefixes.clone_from(prefixes);
    }
    if let Some(docker) = local.allow_docker_socket {
        veto.docker_socket = docker;
    }
    veto
}

fn checked_dir(
    key: &str,
    value: Option<PathBuf>,
    default: &str,
    problems: &mut Vec<String>,
) -> PathBuf {
    let path = value.unwrap_or_else(|| PathBuf::from(default));
    if !path.is_absolute() {
        problems.push(format!("{key}: must be an absolute path"));
    }
    path
}

/// Whether a URL may use plain HTTP.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum PlainHttp {
    /// Only HTTPS (mTLS agent API).
    Never,
    /// HTTP only to a literal loopback address (development without TLS, `just dev`).
    LoopbackOnly,
}

/// Base URL of a server endpoint: `https://host[:port]`, without user info, path or query.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ServerUrl {
    uri: Uri,
}

impl ServerUrl {
    fn parse(raw: &str, plain_http: PlainHttp) -> Result<Self, String> {
        let uri: Uri = raw
            .parse()
            .map_err(|_| "not a valid URL (expected https://host[:port])".to_owned())?;
        let authority = uri
            .authority()
            .ok_or_else(|| "missing host (expected https://host[:port])".to_owned())?;
        if authority.as_str().contains('@') {
            return Err("must not contain user info".to_owned());
        }
        if authority.host().is_empty() {
            return Err("missing host".to_owned());
        }
        if uri
            .path_and_query()
            .is_some_and(|path| path.as_str() != "/")
        {
            return Err("must not contain a path or query".to_owned());
        }
        match uri.scheme_str() {
            Some("https") => {}
            Some("http")
                if plain_http == PlainHttp::LoopbackOnly && is_loopback(authority.host()) => {}
            Some("http") if plain_http == PlainHttp::LoopbackOnly => {
                return Err("http:// is only allowed for loopback addresses".to_owned());
            }
            _ => return Err("must use https://".to_owned()),
        }
        let base = format!("{}://{authority}", uri.scheme_str().unwrap_or("https"));
        let uri = base
            .parse()
            .map_err(|_| "not a valid URL (expected https://host[:port])".to_owned())?;
        Ok(Self { uri })
    }

    /// The URL as [`Uri`] (for the gRPC channel).
    #[must_use]
    pub fn uri(&self) -> &Uri {
        &self.uri
    }

    /// Whether the connection uses TLS (`https`).
    #[must_use]
    pub fn is_tls(&self) -> bool {
        self.uri.scheme_str() == Some("https")
    }

    /// Host name or IP address without brackets (for SNI and certificate checks).
    #[must_use]
    pub fn host(&self) -> &str {
        let host = self.uri.host().unwrap_or_default();
        host.strip_prefix('[')
            .and_then(|inner| inner.strip_suffix(']'))
            .unwrap_or(host)
    }

    /// The port, explicit or the default of the scheme.
    #[must_use]
    pub fn port(&self) -> u16 {
        self.uri
            .port_u16()
            .unwrap_or(if self.is_tls() { 443 } else { 80 })
    }

    fn tls_on_port(&self, port: u16) -> Self {
        let host = self.uri.host().unwrap_or_default();
        let uri = format!("https://{host}:{port}")
            .parse()
            .unwrap_or_else(|_| self.uri.clone());
        Self { uri }
    }
}

impl fmt::Display for ServerUrl {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let text = self.uri.to_string();
        f.write_str(text.strip_suffix('/').unwrap_or(&text))
    }
}

/// Only literal loopback addresses: a name like `localhost` could resolve elsewhere (resolver,
/// `/etc/hosts`) and the enrollment token would then travel in plain text.
fn is_loopback(host: &str) -> bool {
    let bare = host
        .strip_prefix('[')
        .and_then(|inner| inner.strip_suffix(']'))
        .unwrap_or(host);
    bare.parse::<IpAddr>().is_ok_and(|ip| ip.is_loopback())
}

#[cfg(test)]
mod tests {
    use super::*;

    const MINIMAL: &str = "[server]\nurl = \"https://sermon.example.com\"\n";

    fn invalid(text: &str) -> Vec<String> {
        match AgentConfig::parse(text) {
            Err(ConfigError::Invalid(problems)) => problems,
            other => panic!("expected Invalid, got {other:?}"),
        }
    }

    // ADR 0039: the modules' veto – by default loopback only, the Docker socket off; what the file
    // sets replaces the default; a bad network or path is refused.
    #[test]
    fn the_modules_veto() {
        let config = AgentConfig::parse(MINIMAL).unwrap();
        assert_eq!(config.local.modules, sermon_modules::Veto::default());
        let config = AgentConfig::parse(&format!(
            "{MINIMAL}[local]\nallow_modules = false\nmodule_targets = [\"192.0.2.0/24\"]\n\
             module_sockets = [\"/run/postgresql\"]\nmodule_file_prefixes = []\nallow_docker_socket = true\n"
        ))
        .unwrap();
        let veto = &config.local.modules;
        assert!(!veto.allow && veto.docker_socket && veto.file_prefixes.is_empty());
        assert!(veto.address("192.0.2.9".parse().unwrap()).is_ok());
        assert!(
            veto.address("127.0.0.1".parse().unwrap()).is_err(),
            "replaced, not added"
        );
        assert_eq!(veto.sockets, [PathBuf::from("/run/postgresql")]);
        for (line, key) in [
            ("module_targets = [\"localhost\"]", "local.module_targets"),
            ("module_targets = [\"10.0.0.0/40\"]", "local.module_targets"),
            ("module_sockets = [\"run\"]", "local.module_sockets"),
            (
                "module_file_prefixes = [\"/etc/../root\"]",
                "local.module_file_prefixes",
            ),
        ] {
            let problems = invalid(&format!("{MINIMAL}[local]\n{line}\n"));
            assert!(problems[0].starts_with(key), "{line}: {problems:?}");
        }
    }

    #[test]
    fn minimal_config_uses_defaults() {
        let config = AgentConfig::parse(MINIMAL).unwrap();

        assert_eq!(config.server_url.to_string(), "https://sermon.example.com");
        assert_eq!(
            config.agent_url.to_string(),
            "https://sermon.example.com:8443"
        );
        assert_eq!(config.state_dir, PathBuf::from("/var/lib/sermon-agent"));
        assert_eq!(config.host_root, PathBuf::from("/"));
        assert_eq!(config.log_level, LogLevel::Info);
        assert_eq!(config.local, LocalPolicy::default());
        assert!(config.local.allow_live_view && config.local.allow_logs);
    }

    #[test]
    fn full_config_is_taken_over() {
        let config = AgentConfig::parse(
            r#"
            log_level = "debug"
            [server]
            url = "https://sermon.example.com:4443/"
            agent_url = "https://agents.example.com:9443"
            [paths]
            state_dir = "/srv/sermon"
            host_root = "/host"
            [local]
            allow_live_view = false
            allow_logs = false
            log_file_prefixes = ["/var/log", "/srv/app/logs"]
            "#,
        )
        .unwrap();

        assert_eq!(
            config.server_url.to_string(),
            "https://sermon.example.com:4443"
        );
        assert_eq!(config.server_url.port(), 4443);
        assert_eq!(config.agent_url.host(), "agents.example.com");
        assert_eq!(config.agent_url.port(), 9443);
        assert_eq!(config.state_dir, PathBuf::from("/srv/sermon"));
        assert_eq!(config.host_root, PathBuf::from("/host"));
        assert_eq!(config.log_level, LogLevel::Debug);
        assert_eq!(
            config.local,
            LocalPolicy {
                allow_live_view: false,
                allow_logs: false,
                log_file_prefixes: vec![PathBuf::from("/var/log"), PathBuf::from("/srv/app/logs")],
                modules: sermon_modules::Veto::default(),
            }
        );
    }

    #[test]
    fn log_file_prefixes_must_be_absolute_and_plain() {
        for prefix in ["var/log", "/var/log/../../etc", "/var/./log"] {
            let problems = invalid(&format!(
                "{MINIMAL}[local]\nlog_file_prefixes = [\"{prefix}\"]\n"
            ));
            assert_eq!(problems.len(), 1, "{prefix}: {problems:?}");
            assert!(
                problems[0].starts_with("local.log_file_prefixes:"),
                "{problems:?}"
            );
        }
        // None at all: the server's globs read nothing.
        let none =
            AgentConfig::parse(&format!("{MINIMAL}[local]\nlog_file_prefixes = []\n")).unwrap();
        assert_eq!(none.local.log_file_prefixes, Vec::<PathBuf>::new());
    }

    #[test]
    fn missing_server_url_is_reported() {
        assert_eq!(invalid(""), vec!["server.url: required"]);
    }

    #[test]
    fn rejects_invalid_server_urls() {
        let cases = [
            ("http://sermon.example.com", "only allowed for loopback"),
            // A name may resolve anywhere (resolver, /etc/hosts): only literal addresses count.
            ("http://localhost:8080", "only allowed for loopback"),
            (
                "http://localhost.sermon.example.com",
                "only allowed for loopback",
            ),
            ("ftp://sermon.example.com", "must use https://"),
            ("sermon.example.com", "must use https://"),
            ("https://alice:secret@sermon.example.com", "user info"),
            ("https://sermon.example.com/api", "path or query"),
            ("https://sermon.example.com/?x=1", "path or query"),
            ("https://", "not a valid URL"),
            ("https://exa mple.com", "not a valid URL"),
        ];
        for (url, expected) in cases {
            let problems = invalid(&format!("[server]\nurl = \"{url}\"\n"));
            assert_eq!(problems.len(), 1, "{url}: {problems:?}");
            assert!(
                problems[0].starts_with("server.url: ") && problems[0].contains(expected),
                "{url}: {problems:?}"
            );
        }
    }

    #[test]
    fn plain_http_is_allowed_for_loopback_enrollment_only() {
        for url in [
            "http://127.0.0.1:8080",
            "http://127.0.0.2:8080",
            "http://[::1]:8080",
        ] {
            let config = AgentConfig::parse(&format!("[server]\nurl = \"{url}\"\n")).unwrap();
            assert!(!config.server_url.is_tls(), "{url}");
            assert!(config.agent_url.is_tls(), "{url}");
            assert_eq!(config.agent_url.port(), DEFAULT_AGENT_PORT);
        }

        let problems = invalid(
            "[server]\nurl = \"http://127.0.0.1:8080\"\nagent_url = \"http://127.0.0.1:8443\"\n",
        );
        assert_eq!(problems, vec!["server.agent_url: must use https://"]);
    }

    #[test]
    fn ipv6_hosts_keep_their_brackets_in_the_url_only() {
        let config = AgentConfig::parse("[server]\nurl = \"https://[2001:db8::1]\"\n").unwrap();

        assert_eq!(config.server_url.host(), "2001:db8::1");
        assert_eq!(config.server_url.port(), 443);
        assert_eq!(config.agent_url.to_string(), "https://[2001:db8::1]:8443");
    }

    #[test]
    fn collects_all_problems_at_once() {
        let problems = invalid(
            r#"
            [server]
            url = "http://sermon.example.com"
            agent_url = "https://agents.example.com/x"
            [paths]
            state_dir = "relative/dir"
            host_root = "host"
            "#,
        );

        assert_eq!(
            problems,
            vec![
                "server.url: http:// is only allowed for loopback addresses",
                "server.agent_url: must not contain a path or query",
                "paths.state_dir: must be an absolute path",
                "paths.host_root: must be an absolute path",
            ]
        );
    }

    #[test]
    fn state_dir_must_not_be_the_root_directory() {
        let problems = invalid(&format!("{MINIMAL}[paths]\nstate_dir = \"/\"\n"));

        assert_eq!(
            problems,
            vec!["paths.state_dir: must not be the root directory"]
        );
    }

    #[test]
    fn unknown_keys_and_wrong_types_are_syntax_errors() {
        for text in [
            format!("{MINIMAL}colour = \"blue\"\n"),
            format!("{MINIMAL}[local]\nallow_reboot = true\n"),
            format!("{MINIMAL}[local]\nallow_logs = \"no\"\n"),
            format!("{MINIMAL}[local]\nlog_file_prefixes = \"/var/log\"\n"),
            format!("{MINIMAL}log_level = \"verbose\"\n"),
            "[server\n".to_owned(),
        ] {
            let result = AgentConfig::parse(&text);
            assert!(
                matches!(result, Err(ConfigError::Syntax(_))),
                "{text}: {result:?}"
            );
        }
    }

    #[test]
    fn load_reports_the_path_of_a_missing_file() {
        let error = AgentConfig::load(Path::new("/nonexistent/agent.toml")).unwrap_err();

        assert!(matches!(error, ConfigError::Read { .. }));
        assert!(error.to_string().contains("/nonexistent/agent.toml"));
    }

    #[test]
    fn load_reads_a_file() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("agent.toml");
        std::fs::write(&path, MINIMAL).unwrap();

        let config = AgentConfig::load(&path).unwrap();

        assert_eq!(config.server_url.host(), "sermon.example.com");
    }
}
