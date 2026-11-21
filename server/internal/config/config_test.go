package config_test

import (
	"errors"
	"io/fs"
	"log/slog"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/secrets"

	"github.com/pmoscode/SerMon/server/internal/config"
)

// testDBPassword is a test fixture (not a secret); URLs are built from it so that no credential
// URI literal appears in the tests.
const testDBPassword = "fixture-password" // betterleaks:allow

func dbURL(user string) string { return "postgres://" + user + ":" + testDBPassword + "@db/sermon" }

// fakeSource builds a config.Source from in-memory environment variables and files.
func fakeSource(env, files map[string]string) config.Source {
	return config.Source{
		LookupEnv: func(key string) (string, bool) {
			v, ok := env[key]
			return v, ok
		},
		ReadFile: func(name string) ([]byte, error) {
			content, ok := files[name]
			if !ok {
				return nil, fs.ErrNotExist
			}
			return []byte(content), nil
		},
	}
}

func TestLoad_Defaults(t *testing.T) {
	cfg, err := config.Load(fakeSource(nil, nil))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	want := config.Config{
		Mode:            config.ModeProduction,
		HTTPAddr:        ":8080",
		LogLevel:        slog.LevelInfo,
		LogFormat:       config.LogFormatJSON,
		ShutdownTimeout: 15 * time.Second,
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("Load() = %+v, want %+v", cfg, want)
	}
}

func TestLoad_EnvironmentOverridesDefaults(t *testing.T) {
	env := map[string]string{
		"SERMON_HTTP_ADDR":        "127.0.0.1:9000",
		"SERMON_LOG_LEVEL":        "debug",
		"SERMON_LOG_FORMAT":       "text",
		"SERMON_SHUTDOWN_TIMEOUT": "3s",
	}

	cfg, err := config.Load(fakeSource(env, nil))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	want := config.Config{
		Mode:            config.ModeProduction,
		HTTPAddr:        "127.0.0.1:9000",
		LogLevel:        slog.LevelDebug,
		LogFormat:       config.LogFormatText,
		ShutdownTimeout: 3 * time.Second,
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("Load() = %+v, want %+v", cfg, want)
	}
}

func TestLoad_FileVariantReadsValueAndTrimsTrailingNewline(t *testing.T) {
	env := map[string]string{"SERMON_LOG_LEVEL_FILE": "/run/secrets/level"}
	files := map[string]string{"/run/secrets/level": "warn\n"}

	cfg, err := config.Load(fakeSource(env, files))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.LogLevel != slog.LevelWarn {
		t.Errorf("LogLevel = %v, want %v", cfg.LogLevel, slog.LevelWarn)
	}
}

func TestLoad_Errors(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{
			name:    "value and file variant both set",
			env:     map[string]string{"SERMON_LOG_LEVEL": "info", "SERMON_LOG_LEVEL_FILE": "/x"},
			wantErr: "SERMON_LOG_LEVEL and SERMON_LOG_LEVEL_FILE are mutually exclusive",
		},
		{
			name:    "file variant points to missing file",
			env:     map[string]string{"SERMON_LOG_LEVEL_FILE": "/missing"},
			wantErr: "SERMON_LOG_LEVEL_FILE",
		},
		{
			name:    "unknown log level",
			env:     map[string]string{"SERMON_LOG_LEVEL": "verbose"},
			wantErr: "SERMON_LOG_LEVEL",
		},
		{
			name:    "unknown log format",
			env:     map[string]string{"SERMON_LOG_FORMAT": "xml"},
			wantErr: "SERMON_LOG_FORMAT",
		},
		{
			name:    "address without port",
			env:     map[string]string{"SERMON_HTTP_ADDR": "localhost"},
			wantErr: "SERMON_HTTP_ADDR",
		},
		{
			name:    "address with invalid port",
			env:     map[string]string{"SERMON_HTTP_ADDR": ":70000"},
			wantErr: "SERMON_HTTP_ADDR",
		},
		{
			name:    "unparsable shutdown timeout",
			env:     map[string]string{"SERMON_SHUTDOWN_TIMEOUT": "soon"},
			wantErr: "SERMON_SHUTDOWN_TIMEOUT",
		},
		{
			name:    "non-positive shutdown timeout",
			env:     map[string]string{"SERMON_SHUTDOWN_TIMEOUT": "0s"},
			wantErr: "SERMON_SHUTDOWN_TIMEOUT",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := config.Load(fakeSource(tt.env, nil))
			if err == nil {
				t.Fatalf("Load() error = nil, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Load() error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoad_ReportsAllErrorsAtOnce(t *testing.T) {
	env := map[string]string{"SERMON_LOG_LEVEL": "verbose", "SERMON_LOG_FORMAT": "xml"}

	_, err := config.Load(fakeSource(env, nil))

	for _, key := range []string{"SERMON_LOG_LEVEL", "SERMON_LOG_FORMAT"} {
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("Load() error = %v, want it to mention %s", err, key)
		}
	}
	if !errors.Is(err, config.ErrInvalid) {
		t.Errorf("Load() error = %v, want errors.Is(err, ErrInvalid)", err)
	}
}

func TestLoad_DatabaseAndWebAPISettings(t *testing.T) {
	env := map[string]string{
		"SERMON_MODE":                      "development",
		"SERMON_DATABASE_URL_FILE":         "/run/secrets/db",
		"SERMON_DATABASE_MIGRATE_URL_FILE": "/run/secrets/db-migrate",
		"SERMON_ALLOWED_ORIGINS":           "https://sermon.example.com, http://127.0.0.1:5173",
		"SERMON_TRUSTED_PROXIES":           "172.18.0.0/16,10.0.0.1/32",
		"SERMON_OUTBOUND_ALLOWLIST":        "10.20.0.0/16, 192.168.5.7",
	}
	env["SERMON_MASTER_KEYS_FILE"] = "/run/secrets/master-keys"
	files := map[string]string{
		"/run/secrets/db":          dbURL("sermon_app") + "\n",
		"/run/secrets/db-migrate":  dbURL("sermon"),
		"/run/secrets/master-keys": masterKeyLine(t) + "\n",
	}

	cfg, err := config.Load(fakeSource(env, files))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.MasterKeys == nil {
		t.Error("MasterKeys not loaded")
	}
	if cfg.DatabaseURL != dbURL("sermon_app") || cfg.DatabaseMigrateURL != dbURL("sermon") {
		t.Errorf("database URLs = %q / %q", cfg.DatabaseURL, cfg.DatabaseMigrateURL)
	}
	if want := []string{"https://sermon.example.com", "http://127.0.0.1:5173"}; !reflect.DeepEqual(cfg.AllowedOrigins, want) {
		t.Errorf("AllowedOrigins = %v, want %v", cfg.AllowedOrigins, want)
	}
	wantProxies := []netip.Prefix{netip.MustParsePrefix("172.18.0.0/16"), netip.MustParsePrefix("10.0.0.1/32")}
	if !reflect.DeepEqual(cfg.TrustedProxies, wantProxies) {
		t.Errorf("TrustedProxies = %v, want %v", cfg.TrustedProxies, wantProxies)
	}
	wantAllow := []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16"), netip.MustParsePrefix("192.168.5.7/32")}
	if !reflect.DeepEqual(cfg.OutboundAllowlist, wantAllow) {
		t.Errorf("OutboundAllowlist = %v, want %v", cfg.OutboundAllowlist, wantAllow)
	}
}

func TestLoad_WebAPISettingErrors(t *testing.T) {
	cases := map[string]map[string]string{
		"origin with path":      {"SERMON_ALLOWED_ORIGINS": "https://sermon.example.com/app"},
		"origin without scheme": {"SERMON_ALLOWED_ORIGINS": "sermon.example.com"},
		"origin with ftp":       {"SERMON_ALLOWED_ORIGINS": "ftp://sermon.example.com"},
		"wildcard origin":       {"SERMON_ALLOWED_ORIGINS": "*"},
		"proxy without prefix":  {"SERMON_TRUSTED_PROXIES": "10.0.0.1"},
		"proxy garbage":         {"SERMON_TRUSTED_PROXIES": "everyone"},
		"allowlist everything":  {"SERMON_OUTBOUND_ALLOWLIST": "0.0.0.0/0"},
		"allowlist metadata":    {"SERMON_OUTBOUND_ALLOWLIST": "169.254.0.0/16"},
		"allowlist host name":   {"SERMON_OUTBOUND_ALLOWLIST": "intranet.example"},
		"migrate without db":    {"SERMON_DATABASE_MIGRATE_URL": "postgres://x"},
	}
	for name, env := range cases {
		if _, err := config.Load(fakeSource(env, nil)); err == nil {
			t.Errorf("%s: Load() error = nil", name)
		}
	}
}

func masterKeyLine(t *testing.T) string {
	t.Helper()
	line, err := secrets.GenerateKeyLine(1)
	if err != nil {
		t.Fatal(err)
	}
	return line
}

func TestLoad_MasterKeys(t *testing.T) {
	line := masterKeyLine(t)
	// With a database the master keys are required (agent CA, channel secrets).
	if _, err := config.Load(fakeSource(map[string]string{"SERMON_DATABASE_URL": dbURL("sermon_app")}, nil)); err == nil ||
		!strings.Contains(err.Error(), "SERMON_MASTER_KEYS") {
		t.Errorf("database without master keys: %v, want an error naming SERMON_MASTER_KEYS", err)
	}
	// Broken key files are rejected without echoing key material.
	material := strings.SplitN(line, ":", 2)[1]
	_, err := config.Load(fakeSource(map[string]string{"SERMON_MASTER_KEYS": "x" + material}, nil))
	if err == nil || strings.Contains(err.Error(), material) {
		t.Errorf("broken key file: %v", err)
	}
	cfg, err := config.Load(fakeSource(map[string]string{"SERMON_MODE": "development", "SERMON_DATABASE_URL": dbURL("sermon_app"), "SERMON_MASTER_KEYS": line}, nil))
	if err != nil || cfg.MasterKeys == nil {
		t.Errorf("valid keys: %v, %v", cfg.MasterKeys, err)
	}
}

func TestLoad_ErrorsNeverContainDatabaseCredentials(t *testing.T) {
	env := map[string]string{"SERMON_DATABASE_MIGRATE_URL": dbURL("sermon")}
	_, err := config.Load(fakeSource(env, nil))
	if err == nil || strings.Contains(err.Error(), testDBPassword) {
		t.Errorf("error %v must not contain the password", err)
	}
}

func TestLoad_AgentAPI(t *testing.T) {
	line := masterKeyLine(t)
	base := map[string]string{"SERMON_MODE": "development", "SERMON_DATABASE_URL": dbURL("sermon_app"), "SERMON_MASTER_KEYS": line}
	with := func(extra map[string]string) map[string]string {
		env := map[string]string{}
		for k, v := range base {
			env[k] = v
		}
		for k, v := range extra {
			env[k] = v
		}
		return env
	}

	cfg, err := config.Load(fakeSource(with(map[string]string{
		"SERMON_AGENT_ADDR": ":8443", "SERMON_AGENT_HOSTNAMES": "agents.example.org, 192.0.2.10",
	}), nil))
	if err != nil || cfg.AgentAddr != ":8443" || !reflect.DeepEqual(cfg.AgentHostnames, []string{"agents.example.org", "192.0.2.10"}) {
		t.Errorf("Load() = %q %v, %v", cfg.AgentAddr, cfg.AgentHostnames, err)
	}
	if _, err := config.Load(fakeSource(with(map[string]string{
		"SERMON_HTTP_ADDR": "127.0.0.1:0", "SERMON_AGENT_ADDR": "127.0.0.1:0", "SERMON_AGENT_HOSTNAMES": "localhost",
	}), nil)); err != nil {
		t.Errorf("two random ports do not conflict: %v", err)
	}
	if cfg, err := config.Load(fakeSource(base, nil)); err != nil || cfg.AgentAddr != "" {
		t.Errorf("without SERMON_AGENT_ADDR the agent API is off: %q, %v", cfg.AgentAddr, err)
	}

	invalid := map[string]map[string]string{
		"no hostnames":     with(map[string]string{"SERMON_AGENT_ADDR": ":8443"}),
		"bad address":      with(map[string]string{"SERMON_AGENT_ADDR": "nope", "SERMON_AGENT_HOSTNAMES": "a.example.org"}),
		"bad hostname":     with(map[string]string{"SERMON_AGENT_ADDR": ":8443", "SERMON_AGENT_HOSTNAMES": "under_score!.example"}),
		"without database": {"SERMON_AGENT_ADDR": ":8443", "SERMON_AGENT_HOSTNAMES": "a.example.org"},
		"same as http":     with(map[string]string{"SERMON_AGENT_ADDR": ":8080", "SERMON_AGENT_HOSTNAMES": "a.example.org"}),
	}
	for name, env := range invalid {
		if _, err := config.Load(fakeSource(env, nil)); err == nil {
			t.Errorf("%s: Load() error = nil", name)
		}
	}
}

func TestLoad_MetricsEndpoint(t *testing.T) {
	cfg, err := config.Load(fakeSource(map[string]string{"SERMON_METRICS_ADDR": "127.0.0.1:9464"}, nil))
	if err != nil || cfg.MetricsAddr != "127.0.0.1:9464" {
		t.Errorf("Load() = %q, %v", cfg.MetricsAddr, err)
	}
	if cfg, err := config.Load(fakeSource(map[string]string{}, nil)); err != nil || cfg.MetricsAddr != "" {
		t.Errorf("off by default: %q, %v", cfg.MetricsAddr, err)
	}
	for name, env := range map[string]map[string]string{
		"bad address":  {"SERMON_METRICS_ADDR": "nope"},
		"same as http": {"SERMON_METRICS_ADDR": ":8080"},
	} {
		if _, err := config.Load(fakeSource(env, nil)); err == nil {
			t.Errorf("%s: Load() error = nil", name)
		}
	}
}

func TestLoad_AgentDownloadsDir(t *testing.T) {
	dir := t.TempDir()
	if cfg, err := config.Load(fakeSource(map[string]string{"SERMON_AGENT_DOWNLOADS_DIR": dir}, nil)); err != nil || cfg.AgentDownloadsDir != dir {
		t.Errorf("Load() = %q, %v", cfg.AgentDownloadsDir, err)
	}
	if _, err := config.Load(fakeSource(map[string]string{"SERMON_AGENT_DOWNLOADS_DIR": dir + "/missing"}, nil)); err == nil {
		t.Error("a missing directory must be rejected")
	}
}

// fullServer is the environment of a server with database (the case that sends invitations).
func fullServer(t *testing.T, extra map[string]string) (map[string]string, map[string]string) {
	t.Helper()
	env := map[string]string{
		"SERMON_DATABASE_URL":     dbURL("sermon_app"),
		"SERMON_MASTER_KEYS_FILE": "/run/secrets/master-keys",
	}
	for k, v := range extra {
		env[k] = v
	}
	return env, map[string]string{"/run/secrets/master-keys": masterKeyLine(t), "/run/secrets/smtp": "smtp-fixture-secret\n"} // betterleaks:allow
}

// Production is the default: an operator who forgets the variable gets the strict rules.
// Invitations then go out only by mail – the address is proven (M1-39) – and the UI is served
// over HTTPS only.
func TestLoad_ProductionRequiresSMTPAndHTTPSOrigins(t *testing.T) {
	env, files := fullServer(t, map[string]string{"SERMON_ALLOWED_ORIGINS": "http://sermon.example.com"})
	_, err := config.Load(fakeSource(env, files))
	for _, want := range []string{"SERMON_SMTP_HOST: required in production", "SERMON_ALLOWED_ORIGINS: only https:// origins in production"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Load() error = %v, want %q", err, want)
		}
	}

	env, files = fullServer(t, map[string]string{
		"SERMON_ALLOWED_ORIGINS":    "https://sermon.example.com",
		"SERMON_SMTP_HOST":          "mail.example.com",
		"SERMON_SMTP_FROM":          "SerMon <sermon@example.com>",
		"SERMON_SMTP_USERNAME":      "sermon",
		"SERMON_SMTP_PASSWORD_FILE": "/run/secrets/smtp",
	})
	cfg, err := config.Load(fakeSource(env, files))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	smtp := cfg.SMTP
	if cfg.Mode != config.ModeProduction || smtp == nil || smtp.Host != "mail.example.com" || smtp.Port != 587 ||
		smtp.TLS != config.SMTPStartTLS || smtp.From != "sermon@example.com" || smtp.FromName != "SerMon" ||
		smtp.Username != "sermon" || smtp.Password != "smtp-fixture-secret" {
		t.Errorf("SMTP = %+v", smtp)
	}
}

func TestLoad_SMTPSettings(t *testing.T) {
	ok := map[string]string{"SERMON_ALLOWED_ORIGINS": "https://sermon.example.com", "SERMON_SMTP_HOST": "mail.example.com", "SERMON_SMTP_FROM": "sermon@example.com"}
	with := func(extra map[string]string) map[string]string {
		out := map[string]string{}
		for k, v := range ok {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	env, files := fullServer(t, with(map[string]string{"SERMON_SMTP_TLS": "tls"}))
	if cfg, err := config.Load(fakeSource(env, files)); err != nil || cfg.SMTP.Port != 465 || cfg.SMTP.TLS != config.SMTPImplicitTLS {
		t.Errorf("implicit TLS: %+v, %v; want port 465", cfg.SMTP, err)
	}
	for name, extra := range map[string]map[string]string{
		"plain SMTP in production": {"SERMON_SMTP_TLS": "none"},
		"unknown TLS mode":         {"SERMON_SMTP_TLS": "maybe"},
		"bad port":                 {"SERMON_SMTP_PORT": "70000"},
		"bad from":                 {"SERMON_SMTP_FROM": "not an address"},
		"header injection":         {"SERMON_SMTP_FROM": "a@example.com\r\nBcc: x@example.com"},
		"user without password":    {"SERMON_SMTP_USERNAME": "sermon"},
		"missing CA file":          {"SERMON_SMTP_CA_FILE": "/nope.pem"},
		"no origin for the links":  {"SERMON_ALLOWED_ORIGINS": ""},
		"unknown mode":             {"SERMON_MODE": "staging"},
	} {
		env, files := fullServer(t, with(extra))
		if _, err := config.Load(fakeSource(env, files)); err == nil {
			t.Errorf("%s: Load() error = nil", name)
		}
	}
	env, files = fullServer(t, with(map[string]string{"SERMON_SMTP_PASSWORD_FILE": "/run/secrets/smtp", "SERMON_SMTP_USERNAME": "u", "SERMON_SMTP_PORT": "x"}))
	if _, err := config.Load(fakeSource(env, files)); err == nil || strings.Contains(err.Error(), "smtp-fixture-secret") {
		t.Errorf("errors must never contain the SMTP password: %v", err)
	}
}

// Development (just dev) may run without SMTP – invitation links are shown in the UI then and
// prove nothing – and with plain HTTP origins; with SMTP (Mailpit) plain SMTP is allowed.
func TestLoad_DevelopmentRelaxesSMTPAndOrigins(t *testing.T) {
	env, files := fullServer(t, map[string]string{"SERMON_MODE": "development", "SERMON_ALLOWED_ORIGINS": "http://127.0.0.1:5173"})
	cfg, err := config.Load(fakeSource(env, files))
	if err != nil || cfg.Mode != config.ModeDevelopment || cfg.SMTP != nil {
		t.Fatalf("Load() = %+v, %v", cfg, err)
	}
	env, files = fullServer(t, map[string]string{
		"SERMON_MODE": "development", "SERMON_ALLOWED_ORIGINS": "http://127.0.0.1:5173",
		"SERMON_SMTP_HOST": "127.0.0.1", "SERMON_SMTP_PORT": "1025",
		"SERMON_SMTP_TLS": "none", "SERMON_SMTP_FROM": "sermon@localhost",
	})
	if cfg, err := config.Load(fakeSource(env, files)); err != nil || cfg.SMTP == nil || cfg.SMTP.TLS != config.SMTPNone || cfg.SMTP.Port != 1025 {
		t.Errorf("Mailpit in development: %+v, %v", cfg.SMTP, err)
	}
}
