package config_test

import (
	"errors"
	"io/fs"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/config"
)

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
		HTTPAddr:        ":8080",
		LogLevel:        slog.LevelInfo,
		LogFormat:       config.LogFormatJSON,
		ShutdownTimeout: 15 * time.Second,
	}
	if cfg != want {
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
		HTTPAddr:        "127.0.0.1:9000",
		LogLevel:        slog.LevelDebug,
		LogFormat:       config.LogFormatText,
		ShutdownTimeout: 3 * time.Second,
	}
	if cfg != want {
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
