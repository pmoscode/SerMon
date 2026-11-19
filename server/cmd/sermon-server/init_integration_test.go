//go:build integration

package main

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/pgtest"
	"github.com/pmoscode/SerMon/server/internal/config"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// Test fixture, not a secret.
const adminPassword = "first admin passphrase 2026" // betterleaks:allow

var tokenPattern = regexp.MustCompile(`(?m)^\s+([A-Za-z0-9_-]{43})$`)

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func dbSource(t *testing.T, keysFile string, extra map[string]string) config.Source {
	t.Helper()
	db := pgtest.Shared(t)
	if err := postgres.Migrate(context.Background(), db.OwnerDSN); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"SERMON_DATABASE_URL":         pgtest.EnableAppRole(t, db),
		"SERMON_DATABASE_MIGRATE_URL": db.OwnerDSN,
		"SERMON_LOG_FORMAT":           "text",
	}
	if keysFile != "" {
		env["SERMON_MASTER_KEYS_FILE"] = keysFile
	}
	for k, v := range extra {
		env[k] = v
	}
	return config.Source{
		LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok },
		ReadFile:  os.ReadFile,
	}
}

func runInitOnce(t *testing.T, keysFile string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"init", "--master-keys-file", keysFile}, dbSource(t, "", nil), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestFirstInstallation_EndToEnd(t *testing.T) {
	keysFile := filepath.Join(t.TempDir(), "master_keys")

	// 1. init creates the key and prints a token; a second run keeps the key and replaces the token.
	code, out1, errOut := runInitOnce(t, keysFile)
	if code != exitOK || !tokenPattern.MatchString(out1) {
		t.Fatalf("init = %d\nstdout %s\nstderr %s", code, out1, errOut)
	}
	key1, _ := os.ReadFile(keysFile)
	code, out2, _ := runInitOnce(t, keysFile)
	key2, _ := os.ReadFile(keysFile)
	if code != exitOK || !bytes.Equal(key1, key2) || tokenPattern.FindStringSubmatch(out1)[1] == tokenPattern.FindStringSubmatch(out2)[1] {
		t.Fatalf("second init = %d, key unchanged %v, token replaced %v", code, bytes.Equal(key1, key2), out1 != out2)
	}

	// 2. The server logs a fresh token while the setup is open.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logs syncBuffer
	src := dbSource(t, keysFile, map[string]string{"SERMON_HTTP_ADDR": "127.0.0.1:0", "SERMON_ALLOWED_ORIGINS": "http://sermon.test"})
	done := make(chan int, 1)
	go func() { done <- run(ctx, nil, src, &logs, &logs) }()
	addr := waitFor(t, &logs, regexp.MustCompile(`msg=listening .*addr=(\S+)`))
	token := waitFor(t, &logs, regexp.MustCompile(`setup_token=([A-Za-z0-9_-]{43})`))

	hc := &httpClient{origin: "http://sermon.test"}
	setupClient := webv1connect.NewSetupServiceClient(hc.client(), "http://"+addr)
	status, err := setupClient.GetSetupStatus(ctx, connect.NewRequest(&webv1.GetSetupStatusRequest{}))
	if err != nil || !status.Msg.GetSetupRequired() {
		t.Fatalf("GetSetupStatus() = %v, %v", status, err)
	}
	oldToken := tokenPattern.FindStringSubmatch(out2)[1]
	req := &webv1.CompleteSetupRequest{Token: oldToken, Email: "ann@example.org", DisplayName: "Ann", Password: adminPassword, OrganizationName: "Example"}
	if _, err := setupClient.CompleteSetup(ctx, connect.NewRequest(req)); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("token replaced by the server start must be invalid: %v", err)
	}
	req.Token = token
	if _, err := setupClient.CompleteSetup(ctx, connect.NewRequest(req)); err != nil {
		t.Fatalf("CompleteSetup() error = %v", err)
	}
	if _, err := setupClient.CompleteSetup(ctx, connect.NewRequest(req)); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("second CompleteSetup(): %v, want UNAUTHENTICATED", err)
	}

	// 3. The superadmin logs in and acts in the first organization.
	authClient := webv1connect.NewAuthServiceClient(hc.client(), "http://"+addr)
	login, err := authClient.Login(ctx, connect.NewRequest(&webv1.LoginRequest{Email: "ann@example.org", Password: adminPassword}))
	if err != nil || login.Msg.GetSession().GetActiveOrganizationId() == "" || login.Msg.GetSession().GetOrganizations()[0].GetName() != "Example" {
		t.Fatalf("Login() = %v, %v", login, err)
	}
	cancel()
	if code := <-done; code != exitOK {
		t.Errorf("serve exit = %d\n%s", code, logs.String())
	}

	// 4. init now refuses and changes nothing; exactly one agent CA exists.
	code, _, errOut = runInitOnce(t, keysFile)
	if code != exitError || !strings.Contains(errOut, "already initialized") {
		t.Errorf("init after setup = %d, %q", code, errOut)
	}
	conn, err := pgx.Connect(context.Background(), pgtest.Shared(t).OwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	var authorities int
	_ = conn.QueryRow(context.Background(), "SELECT count(*) FROM pki_authorities").Scan(&authorities)
	if authorities != 1 {
		t.Errorf("%d agent CAs, want 1", authorities)
	}
}

func waitFor(t *testing.T, logs *syncBuffer, pattern *regexp.Regexp) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if m := pattern.FindStringSubmatch(logs.String()); m != nil {
			return m[1]
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s not found in logs:\n%s", pattern, logs.String())
	return ""
}

type httpClient struct{ origin string }

type originRoundTripper struct {
	next   http.RoundTripper
	origin string
}

func (o originRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Origin", o.origin)
	return o.next.RoundTrip(r)
}

func (h *httpClient) client() *http.Client {
	return &http.Client{Transport: originRoundTripper{next: http.DefaultTransport, origin: h.origin}, Timeout: 30 * time.Second}
}
