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
	"strconv"
	"strings"
	"time"
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
