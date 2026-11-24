package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/pmoscode/SerMon/server/internal/config"
	"github.com/pmoscode/SerMon/server/internal/transport/webapi"
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

// UI ("/") and the web API (method-less prefix patterns) must register on the same mux without
// a conflict – ServeMux panics on conflicting patterns at startup.
func TestRoutes_UIAndAPIDoNotConflict(_ *testing.T) {
	mux := http.NewServeMux()
	webapi.Mount(mux, webapi.Config{}, webapi.Handlers{
		Auth:          webapi.NewAuthHandler(nil, nil, nil, nil, slog.New(slog.DiscardHandler)),
		System:        webapi.NewSystemHandler("test"),
		Access:        webapi.NewAccessHandler(nil, nil, slog.New(slog.DiscardHandler)),
		Authenticator: webapi.NewAuthenticator(nil, nil, slog.New(slog.DiscardHandler)),
	})
	registerUI(mux, http.NotFoundHandler()) // panics on a pattern conflict
}

func TestRun_ServesMetricsOnItsOwnListener(t *testing.T) {
	logs := &syncBuffer{}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, nil, envSource(map[string]string{
			"SERMON_HTTP_ADDR": "127.0.0.1:0", "SERMON_METRICS_ADDR": "127.0.0.1:0",
		}), logs, logs)
	}()
	addr := waitFor(t, logs, regexp.MustCompile(`"msg":"metrics listening"[^\n]*?"addr":"([^"]+)"`))

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+"/metrics", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	for _, want := range []string{"sermon_server_build_info{", "sermon_server_goroutines "} {
		if !strings.Contains(string(body), want) {
			t.Errorf("/metrics lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(logs.String(), "has no authentication") {
		t.Error("a loopback address must not warn")
	}

	cancel()
	if code := <-done; code != 0 {
		t.Errorf("run() = %d:\n%s", code, logs.String())
	}
}

func TestRun_ServesTheInstaller(t *testing.T) {
	logs := &syncBuffer{}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, nil, envSource(map[string]string{"SERMON_HTTP_ADDR": "127.0.0.1:0"}), logs, logs)
	}()
	addr := waitFor(t, logs, regexp.MustCompile(`"msg":"listening"[^\n]*?"addr":"([^"]+)"`))

	get := func(path string) (*http.Response, string) {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+path, nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		body, _ := io.ReadAll(res.Body)
		return res, string(body)
	}
	res, body := get("/install.sh")
	if res.Header.Get("Strict-Transport-Security") != "" {
		t.Error("no HSTS without https origins")
	}
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(body, "#!/bin/sh") || !strings.Contains(body, "[Service]") {
		t.Errorf("GET /install.sh = %d %q…", res.StatusCode, body[:min(len(body), 40)])
	}
	if !strings.Contains(logs.String(), "built without release key") {
		t.Error("a server without release key must say so")
	}
	if res, _ := get("/downloads/sermon-agent-linux-amd64"); res.StatusCode != http.StatusNotFound {
		t.Errorf("downloads without directory = %d", res.StatusCode)
	}
	cancel()
	<-done
}

func TestRun_SendsHSTSBehindTLS(t *testing.T) {
	logs := &syncBuffer{}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, nil, envSource(map[string]string{
			"SERMON_HTTP_ADDR": "127.0.0.1:0", "SERMON_ALLOWED_ORIGINS": "https://sermon.example.com",
		}), logs, logs)
	}()
	addr := waitFor(t, logs, regexp.MustCompile(`"msg":"listening"[^\n]*?"addr":"([^"]+)"`))
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+"/healthz", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if got := res.Header.Get("Strict-Transport-Security"); got != "max-age=31536000; includeSubDomains" {
		t.Errorf("HSTS = %q", got)
	}
	cancel()
	<-done
}

// Passkeys never stop the server: reached by an IP address or a host name without a dot it runs
// without them and says why; with a domain name they are on.
func TestNewPasskeys_OffWithoutADomainNameInsteadOfFailing(t *testing.T) {
	for _, tc := range []struct {
		origins []string
		on      bool
	}{
		{[]string{"https://192.168.1.10"}, false},
		{[]string{"https://monitor:8443"}, false},
		{nil, false},
		{[]string{"https://10.0.0.5", "https://sermon.lan"}, true},
	} {
		var logs bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logs, nil))
		svc := newPasskeys(context.Background(), config.Config{AllowedOrigins: tc.origins}, nil, nil, nil, nil, logger)
		if svc.Available() != tc.on {
			t.Errorf("%v: available %v, want %v", tc.origins, svc.Available(), tc.on)
		}
		if !tc.on && !strings.Contains(logs.String(), "passkeys are off") {
			t.Errorf("%v: no warning in %q", tc.origins, logs.String())
		}
	}
}
