// Package config loads the server configuration from environment variables.
//
// Every variable SERMON_X can alternatively be provided as a file via SERMON_X_FILE
// (e.g. Docker secrets). Setting both is an error. All problems are reported at once.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

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

// Config is the validated server configuration.
type Config struct {
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
	envMasterKeys      = "SERMON_MASTER_KEYS"
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
		HTTPAddr:        l.string(envHTTPAddr, defaultHTTPAddr, validateAddr),
		LogLevel:        parseLevel(&l, l.string(envLogLevel, defaultLogLevel, nil)),
		LogFormat:       parseFormat(&l, l.string(envLogFormat, defaultLogFormat, nil)),
		ShutdownTimeout: parsePositiveDuration(&l, envShutdownTimeout, defaultShutdownTimeout),
		DatabaseURL:     l.string(envDatabaseURL, "", nil),
		// Validation messages must never echo the value: it contains a password.
		DatabaseMigrateURL: l.string(envMigrateURL, "", nil),
		AllowedOrigins:     parseOrigins(&l),
		TrustedProxies:     parsePrefixes(&l),
		MasterKeys:         parseMasterKeys(&l),
	}
	if cfg.DatabaseURL != "" && cfg.MasterKeys == nil {
		l.fail(envMasterKeys, "required together with %s (set %s_FILE; create the file with \"sermon-server init\")", envDatabaseURL, envMasterKeys)
	}
	if cfg.DatabaseMigrateURL != "" && cfg.DatabaseURL == "" {
		l.fail(envMigrateURL, "requires %s (migrations run only together with the database connection)", envDatabaseURL)
	}

	if len(l.errs) > 0 {
		return Config{}, fmt.Errorf("%w: %w", ErrInvalid, errors.Join(l.errs...))
	}
	return cfg, nil
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
