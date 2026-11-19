//go:build integration

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/pgtest"
)

// agentBinary returns the sermon-agent binary from SERMON_AGENT_BIN (set by `just e2e-agent`);
// the test is skipped without it, so that `just test-integration` needs no Rust toolchain.
func agentBinary(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("SERMON_AGENT_BIN")
	if bin == "" {
		t.Skip("SERMON_AGENT_BIN not set – run `just e2e-agent`")
	}
	abs, err := filepath.Abs(bin)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// TestAgentBinary_EnrollsAgainstTheRealServer runs the Rust agent's `enroll` against this server
// (gRPC over h2c on the web listener, as behind Caddy): the cross-language contract of M1-09/M1-13.
func TestAgentBinary_EnrollsAgainstTheRealServer(t *testing.T) {
	bin := agentBinary(t)
	srv := startTestServer(t, nil)
	defer srv.stop()
	seedEnrollmentToken(t)

	dir := t.TempDir()
	fixture, err := filepath.Abs("../../../agent/fixtures/debian-13")
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "agent.toml")
	state := filepath.Join(dir, "state")
	toml := "[server]\nurl = \"http://" + srv.web + "\"\n[paths]\nstate_dir = \"" + state + "\"\nhost_root = \"" + fixture + "\"\n"
	if err := os.WriteFile(config, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	enroll := func() (string, error) {
		cmd := exec.CommandContext(ctx, bin, "enroll", "--config", config)
		cmd.Env = append(os.Environ(), "SERMON_ENROLLMENT_TOKEN="+fixtureToken)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	out, err := enroll()
	if err != nil {
		t.Fatalf("sermon-agent enroll: %v\n%s\nserver:\n%s", err, out, srv.logs.String())
	}
	nodeID := regexp.MustCompile(`enrolled as node ([0-9a-f-]{36})`).FindStringSubmatch(out)
	if nodeID == nil {
		t.Fatalf("no node ID in output:\n%s", out)
	}
	conn, err := pgx.Connect(ctx, pgtest.Shared(t).OwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	var hostname, machineID string
	if err := conn.QueryRow(ctx, "SELECT hostname, machine_id FROM nodes WHERE id = $1", nodeID[1]).Scan(&hostname, &machineID); err != nil ||
		hostname != "web-01" || machineID != "0123456789abcdef0123456789abcdef" {
		t.Errorf("node = %q, %q, %v; want the host facts of the fixture", hostname, machineID, err)
	}
	for _, name := range []string{"agent.key", "agent.crt", "trust.pem", "identity.toml"} {
		info, err := os.Stat(filepath.Join(state, "identity", name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v, mode %v", name, err, info)
		}
	}

	// The token allowed one use; a forced second enrollment is refused and keeps the identity.
	before, _ := os.ReadFile(filepath.Join(state, "identity", "agent.key"))
	out, err = enroll()
	if err == nil || !strings.Contains(out, "--force") {
		t.Errorf("second enroll without --force: %v\n%s", err, out)
	}
	cmd := exec.CommandContext(ctx, bin, "enroll", "--config", config, "--force")
	cmd.Env = append(os.Environ(), "SERMON_ENROLLMENT_TOKEN="+fixtureToken)
	forced, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(forced), "invalid, expired, revoked or used up") {
		t.Errorf("forced enroll with a used-up token: %v\n%s", err, forced)
	}
	after, _ := os.ReadFile(filepath.Join(state, "identity", "agent.key"))
	if string(before) != string(after) {
		t.Error("a failed enrollment replaced the stored identity")
	}
}
