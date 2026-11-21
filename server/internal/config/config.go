// Package config loads the server configuration from environment variables.
//
// Every variable SERMON_X can alternatively be provided as a file via SERMON_X_FILE
// (e.g. Docker secrets). Setting both is an error. All problems are reported at once.
package config

import (
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/mail"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
	"github.com/pmoscode/SerMon/server/internal/secrets"
)

// ErrInvalid is wrapped by every error returned from Load.
var ErrInvalid = errors.New("invalid configuration")

// LogFormat selects the log output encoding.
type LogFormat string

// Supported log formats.
const (
	LogFormatJSON LogFormat = "json"
	LogFormatText LogFormat = "text"
)

// Mode selects the rules for the environment (M1-39).
type Mode string

// Modes. Production is the default: whoever forgets SERMON_MODE gets the strict rules.
const (
	// ModeProduction requires SMTP (invitations go out only by mail, proving the address) and
	// https:// origins.
	ModeProduction Mode = "production"
	// ModeDevelopment allows running without SMTP (invitation links are shown in the UI and prove
	// nothing), plain SMTP (Mailpit) and http:// origins.
	ModeDevelopment Mode = "development"
)

// SMTPTLS is how the connection to the mail server is protected.
type SMTPTLS string

// TLS modes of the mail connection.
const (
	SMTPStartTLS    SMTPTLS = "starttls" // port 587, STARTTLS required
	SMTPImplicitTLS SMTPTLS = "tls"      // port 465
	SMTPNone        SMTPTLS = "none"     // development only (Mailpit)
)

// SMTP configures outgoing mail.
type SMTP struct {
	Host     string
	Port     int
	TLS      SMTPTLS
	From     string // address
	FromName string // display name, may be empty
	Username string // empty: no authentication
	Password string
	// RootCAs verify the mail server instead of the system roots (SERMON_SMTP_CA_FILE).
	RootCAs *x509.CertPool
}

// Config is the validated server configuration.
type Config struct {
	// Mode is production unless SERMON_MODE=development.
	Mode Mode
	// HTTPAddr is the listen address of the web API and UI (host:port).
	HTTPAddr string
	// LogLevel is the minimum level that is logged.
	LogLevel slog.Level
	// LogFormat is the encoding of log lines.
	LogFormat LogFormat
	// ShutdownTimeout bounds how long in-flight requests may take after a stop signal.
	ShutdownTimeout time.Duration
	// DatabaseURL connects as the unprivileged runtime role sermon_app (empty: no database, UI only).
	DatabaseURL string
	// DatabaseMigrateURL connects as the schema owner to apply migrations at startup (optional).
	DatabaseMigrateURL string
	// AllowedOrigins are the exact origins the web UI is served from (CSRF protection).
	AllowedOrigins []string
	// TrustedProxies may set X-Forwarded-For (reverse proxy networks).
	TrustedProxies []netip.Prefix
	// OutboundAllowlist are the internal networks requests to user-configured targets (webhooks,
	// chat channels) may reach (safehttp, KONZEPT §18.6); empty: public addresses only.
	OutboundAllowlist []netip.Prefix
	// AgentAddr is the listen address of the mTLS agent API (empty: agent API off).
	AgentAddr string
	// AgentHostnames are the DNS names or IPs agents use to reach the agent API (server certificate).
	AgentHostnames []string
	// MetricsAddr is the listen address of the server's own /metrics (empty: off; ADR 0011).
	MetricsAddr string
	// AgentDownloadsDir holds the signed agent binaries served under /downloads (ADR 0012).
	AgentDownloadsDir string
	// SMTP sends mail (invitations; notifications later). Required in production with a
	// database; nil without.
	SMTP *SMTP
	// MasterKeys encrypt secrets at rest (key file "<id>:<base64>" per line, newest first).
	// Required together with the database; create the file with "sermon-server init".
	MasterKeys *secrets.Box
}

// Source abstracts access to the environment and the file system for testability.
type Source struct {
	LookupEnv func(key string) (string, bool)
	ReadFile  func(name string) ([]byte, error)
}

// Environment variable names.
const (
	envHTTPAddr        = "SERMON_HTTP_ADDR"
	envLogLevel        = "SERMON_LOG_LEVEL"
	envLogFormat       = "SERMON_LOG_FORMAT"
	envShutdownTimeout = "SERMON_SHUTDOWN_TIMEOUT"
	envDatabaseURL     = "SERMON_DATABASE_URL"
	envMigrateURL      = "SERMON_DATABASE_MIGRATE_URL"
	envAllowedOrigins  = "SERMON_ALLOWED_ORIGINS"
	envTrustedProxies  = "SERMON_TRUSTED_PROXIES"
	envOutboundAllow   = "SERMON_OUTBOUND_ALLOWLIST"
	envMasterKeys      = "SERMON_MASTER_KEYS"
	envAgentAddr       = "SERMON_AGENT_ADDR"
	envAgentHostnames  = "SERMON_AGENT_HOSTNAMES"
	envMetricsAddr     = "SERMON_METRICS_ADDR"
	envDownloadsDir    = "SERMON_AGENT_DOWNLOADS_DIR"
	envMode            = "SERMON_MODE"
	envSMTPHost        = "SERMON_SMTP_HOST"
	envSMTPPort        = "SERMON_SMTP_PORT"
	envSMTPTLS         = "SERMON_SMTP_TLS"
	envSMTPFrom        = "SERMON_SMTP_FROM"
	envSMTPUsername    = "SERMON_SMTP_USERNAME"
	envSMTPPassword    = "SERMON_SMTP_PASSWORD" //nolint:gosec // G101: the name of a variable, not a credential
	envSMTPCAFile      = "SERMON_SMTP_CA_FILE"
	fileSuffix         = "_FILE"
)

// Defaults.
const (
	defaultHTTPAddr        = ":8080"
	defaultLogLevel        = "info"
	defaultLogFormat       = string(LogFormatJSON)
	defaultShutdownTimeout = 15 * time.Second
	maxPort                = 65535
)

// Load reads and validates the configuration from src.
func Load(src Source) (Config, error) {
	l := loader{src: src}

	cfg := Config{
		Mode:            parseMode(&l),
		HTTPAddr:        l.string(envHTTPAddr, defaultHTTPAddr, validateAddr),
		LogLevel:        parseLevel(&l, l.string(envLogLevel, defaultLogLevel, nil)),
		LogFormat:       parseFormat(&l, l.string(envLogFormat, defaultLogFormat, nil)),
		ShutdownTimeout: parsePositiveDuration(&l, envShutdownTimeout, defaultShutdownTimeout),
		DatabaseURL:     l.string(envDatabaseURL, "", nil),
		// Validation messages must never echo the value: it contains a password.
		DatabaseMigrateURL: l.string(envMigrateURL, "", nil),
		AllowedOrigins:     parseOrigins(&l),
		TrustedProxies:     parsePrefixes(&l),
		OutboundAllowlist:  parseOutboundAllowlist(&l),
		MasterKeys:         parseMasterKeys(&l),
		AgentAddr:          l.string(envAgentAddr, "", validateAddr),
		AgentHostnames:     parseHostnames(&l),
		MetricsAddr:        l.string(envMetricsAddr, "", validateAddr),
		AgentDownloadsDir:  l.string(envDownloadsDir, "", validateDir),
	}
	if cfg.MetricsAddr != "" && !strings.HasSuffix(cfg.MetricsAddr, ":0") &&
		(cfg.MetricsAddr == cfg.HTTPAddr || cfg.MetricsAddr == cfg.AgentAddr) {
		l.fail(envMetricsAddr, "must differ from %s and %s", envHTTPAddr, envAgentAddr)
	}
	if cfg.AgentAddr != "" {
		switch {
		case len(cfg.AgentHostnames) == 0:
			l.fail(envAgentHostnames, "required with %s (names in the agent API's server certificate)", envAgentAddr)
		case cfg.DatabaseURL == "":
			l.fail(envAgentAddr, "requires %s (the agent CA lives in the database)", envDatabaseURL)
		case cfg.AgentAddr == cfg.HTTPAddr && !strings.HasSuffix(cfg.AgentAddr, ":0"): // port 0 = random port each
			l.fail(envAgentAddr, "must differ from %s", envHTTPAddr)
		}
	}
	if cfg.DatabaseURL != "" && cfg.MasterKeys == nil {
		l.fail(envMasterKeys, "required together with %s (set %s_FILE; create the file with \"sermon-server init\")", envDatabaseURL, envMasterKeys)
	}
	if cfg.DatabaseMigrateURL != "" && cfg.DatabaseURL == "" {
		l.fail(envMigrateURL, "requires %s (migrations run only together with the database connection)", envDatabaseURL)
	}

	cfg.SMTP = parseSMTP(&l, cfg.Mode)
	if cfg.SMTP != nil && len(cfg.AllowedOrigins) == 0 {
		l.fail(envAllowedOrigins, "required with %s (mailed links point to the first origin)", envSMTPHost)
	}
	if cfg.Mode == ModeProduction {
		if cfg.DatabaseURL != "" && cfg.SMTP == nil {
			l.fail(envSMTPHost, "required in production (invitations are sent by mail to prove the address; SERMON_MODE=development allows running without)")
		}
		for _, origin := range cfg.AllowedOrigins {
			if !strings.HasPrefix(origin, "https://") {
				l.fail(envAllowedOrigins, "only https:// origins in production (got %s; SERMON_MODE=development allows http://)", origin)
			}
		}
	}

	if len(l.errs) > 0 {
		return Config{}, fmt.Errorf("%w: %w", ErrInvalid, errors.Join(l.errs...))
	}
	return cfg, nil
}

func parseMode(l *loader) Mode {
	switch value := Mode(l.string(envMode, string(ModeProduction), nil)); value {
	case ModeProduction, ModeDevelopment:
		return value
	default:
		l.fail(envMode, "must be production or development, got %q", value)
		return ModeProduction
	}
}

// parseSMTP reads the mail settings; nil if SERMON_SMTP_HOST is not set.
func parseSMTP(l *loader, mode Mode) *SMTP {
	host := l.string(envSMTPHost, "", nil)
	if host == "" {
		for _, key := range []string{envSMTPPort, envSMTPTLS, envSMTPFrom, envSMTPUsername, envSMTPPassword, envSMTPCAFile} {
			if _, ok := l.raw(key); ok {
				l.fail(key, "requires %s", envSMTPHost)
			}
		}
		return nil
	}
	if net.ParseIP(host) == nil && !validDNSName(host) {
		l.fail(envSMTPHost, "invalid host name or IP %q", host)
	}
	smtp := &SMTP{Host: host, TLS: SMTPTLS(l.string(envSMTPTLS, string(SMTPStartTLS), nil))}
	defaultPort := 587
	switch smtp.TLS {
	case SMTPStartTLS:
	case SMTPImplicitTLS:
		defaultPort = 465
	case SMTPNone:
		defaultPort = 25
		if mode != ModeDevelopment {
			l.fail(envSMTPTLS, "none (plain SMTP) only in development")
		}
	default:
		l.fail(envSMTPTLS, "must be starttls, tls or none, got %q", smtp.TLS)
	}
	smtp.Port = defaultPort
	if value, ok := l.raw(envSMTPPort); ok {
		port, err := strconv.Atoi(value)
		if err != nil || port < 1 || port > maxPort {
			l.fail(envSMTPPort, "must be a port number (1-%d)", maxPort)
		} else {
			smtp.Port = port
		}
	}
	from := l.string(envSMTPFrom, "", nil)
	addr, err := mail.ParseAddress(from)
	switch {
	case from == "":
		l.fail(envSMTPFrom, "required with %s (sender address)", envSMTPHost)
	case strings.ContainsAny(from, "\r\n") || err != nil:
		l.fail(envSMTPFrom, "not a mail address (e.g. \"SerMon <sermon@example.com>\")")
	default:
		smtp.From, smtp.FromName = addr.Address, addr.Name
	}
	smtp.Username = l.string(envSMTPUsername, "", nil)
	// Validation messages must never echo the password.
	smtp.Password = l.string(envSMTPPassword, "", nil)
	if (smtp.Username == "") != (smtp.Password == "") {
		l.fail(envSMTPUsername, "%s and %s go together", envSMTPUsername, envSMTPPassword)
	}
	if path := l.string(envSMTPCAFile, "", nil); path != "" {
		pem, err := l.src.ReadFile(path)
		pool := x509.NewCertPool()
		if err != nil || !pool.AppendCertsFromPEM(pem) {
			l.fail(envSMTPCAFile, "cannot read PEM certificates from %s", path)
		} else {
			smtp.RootCAs = pool
		}
	}
	return smtp
}

// loader collects values and errors so that all problems are reported together.
type loader struct {
	src  Source
	errs []error
}

func (l *loader) fail(key string, format string, args ...any) {
	l.errs = append(l.errs, fmt.Errorf("%s: %s", key, fmt.Sprintf(format, args...)))
}

// raw returns the value of key or key_FILE and whether one of them was set.
func (l *loader) raw(key string) (string, bool) {
	value, hasValue := l.src.LookupEnv(key)
	path, hasFile := l.src.LookupEnv(key + fileSuffix)

	switch {
	case hasValue && hasFile:
		l.fail(key, "%s and %s are mutually exclusive", key, key+fileSuffix)
		return "", false
	case hasFile:
		content, err := l.src.ReadFile(path)
		if err != nil {
			l.fail(key+fileSuffix, "cannot read file: %v", err)
			return "", false
		}
		return strings.TrimRight(string(content), "\r\n"), true
	default:
		return value, hasValue
	}
}

func (l *loader) string(key, fallback string, validate func(string) error) string {
	value, ok := l.raw(key)
	if !ok {
		return fallback
	}
	if validate != nil {
		if err := validate(value); err != nil {
			l.fail(key, "%v", err)
			return fallback
		}
	}
	return value
}

// parseHostnames reads DNS names (RFC 1123 labels) or IP addresses.
func parseHostnames(l *loader) []string {
	value, ok := l.raw(envAgentHostnames)
	if !ok {
		return nil
	}
	var hosts []string
	for _, h := range list(value) {
		if net.ParseIP(h) == nil && !validDNSName(h) {
			l.fail(envAgentHostnames, "invalid host name or IP %q", h)
			continue
		}
		hosts = append(hosts, strings.ToLower(h))
	}
	return hosts
}

func validDNSName(name string) bool {
	if len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		if strings.ContainsFunc(label, func(r rune) bool { return !isLabelRune(r) }) {
			return false
		}
	}
	return true
}

func isLabelRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-'
}

// parseMasterKeys reads the key file. Errors never contain key material (secrets.ParseKeys).
func parseMasterKeys(l *loader) *secrets.Box {
	value, ok := l.raw(envMasterKeys)
	if !ok {
		return nil
	}
	box, err := secrets.ParseKeys(value)
	if err != nil {
		l.fail(envMasterKeys, "%v", err)
		return nil
	}
	return box
}

func parseLevel(l *loader, value string) slog.Level {
	var level slog.Level
	if err := level.UnmarshalText([]byte(value)); err != nil {
		l.fail(envLogLevel, "unknown level %q (want debug, info, warn or error)", value)
		return slog.LevelInfo
	}
	return level
}

func parseFormat(l *loader, value string) LogFormat {
	switch LogFormat(value) {
	case LogFormatJSON, LogFormatText:
		return LogFormat(value)
	default:
		l.fail(envLogFormat, "unknown format %q (want json or text)", value)
		return LogFormatJSON
	}
}

func parsePositiveDuration(l *loader, key string, fallback time.Duration) time.Duration {
	value, ok := l.raw(key)
	if !ok {
		return fallback
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		l.fail(key, "invalid duration %q (e.g. 15s)", value)
		return fallback
	}
	if d <= 0 {
		l.fail(key, "must be positive, got %s", d)
		return fallback
	}
	return d
}

func validateAddr(addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid address %q (want host:port): %w", addr, err)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > maxPort {
		return fmt.Errorf("invalid port %q", port)
	}
	return nil
}

func list(value string) []string {
	var items []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

func parseOrigins(l *loader) []string {
	value, ok := l.raw(envAllowedOrigins)
	if !ok {
		return nil
	}
	var origins []string
	for _, origin := range list(value) {
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" ||
			u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			l.fail(envAllowedOrigins, "%q is not an origin (want scheme://host[:port])", origin)
			continue
		}
		origins = append(origins, origin)
	}
	return origins
}

// parseOutboundAllowlist reads the internal networks outgoing requests may reach.
func parseOutboundAllowlist(l *loader) []netip.Prefix {
	value, ok := l.raw(envOutboundAllow)
	if !ok {
		return nil
	}
	prefixes, err := safehttp.ParseAllowlist(value)
	if err != nil {
		l.fail(envOutboundAllow, "%v (networks like 10.20.0.0/16 or addresses, separated by commas)", err)
	}
	return prefixes
}

func parsePrefixes(l *loader) []netip.Prefix {
	value, ok := l.raw(envTrustedProxies)
	if !ok {
		return nil
	}
	var prefixes []netip.Prefix
	for _, item := range list(value) {
		p, err := netip.ParsePrefix(item)
		if err != nil {
			l.fail(envTrustedProxies, "%q is not a CIDR prefix (e.g. 172.18.0.0/16)", item)
			continue
		}
		prefixes = append(prefixes, p.Masked())
	}
	return prefixes
}

func validateDir(value string) error {
	info, err := os.Stat(value)
	if err != nil || !info.IsDir() {
		return errors.New("must be an existing directory")
	}
	return nil
}
