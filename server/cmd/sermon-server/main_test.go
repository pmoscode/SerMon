package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/pmoscode/SerMon/server/internal/config"
)

func envSource(env map[string]string) config.Source {
	return config.Source{
		LookupEnv: func(key string) (string, bool) { v, ok := env[key]; return v, ok },
		ReadFile:  func(string) ([]byte, error) { return nil, nil },
	}
}

func TestRun_PrintsVersion(t *testing.T) {
	for _, flag := range []string{"--version", "-V"} {
		var stdout, stderr bytes.Buffer

		code := run(context.Background(), []string{flag}, envSource(nil), &stdout, &stderr)

		if code != 0 {
			t.Errorf("run(%s) = %d, want 0", flag, code)
		}
		if got := strings.TrimSpace(stdout.String()); got != "sermon-server "+version {
			t.Errorf("run(%s) stdout = %q, want %q", flag, got, "sermon-server "+version)
		}
	}
}

func TestRun_RejectsUnknownArgumentsWithUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run(context.Background(), []string{"--bogus"}, envSource(nil), &stdout, &stderr)

	if code != 2 {
		t.Errorf("run() = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "usage: sermon-server") {
		t.Errorf("stderr = %q, want usage", stderr.String())
	}
}

func TestRun_FailsOnInvalidConfiguration(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run(context.Background(), nil, envSource(map[string]string{"SERMON_LOG_LEVEL": "verbose"}), &stdout, &stderr)

	if code != 1 {
		t.Errorf("run() = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "SERMON_LOG_LEVEL") {
		t.Errorf("stderr = %q, want config error", stderr.String())
	}
}

func TestRun_StopsCleanlyWhenContextIsCancelled(t *testing.T) {
	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled: server must start, drain and stop without error

	code := run(ctx, nil, envSource(map[string]string{"SERMON_HTTP_ADDR": "127.0.0.1:0"}), &stdout, &stderr)

	if code != 0 {
		t.Errorf("run() = %d, want 0; stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	for _, msg := range []string{`"msg":"listening"`, `"msg":"stopped"`} {
		if !strings.Contains(stdout.String(), msg) {
			t.Errorf("log output %q does not contain %s", stdout.String(), msg)
		}
	}
}
