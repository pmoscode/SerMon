//go:build integration

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
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

// TestAgentBinary_RunsAndRotatesAgainstTheRealServer runs `sermon-agent run` against the real mTLS
// agent API: session with Hello and trust bundle, a due certificate rotation, the reconnect with
// the new certificate and a clean stop (M1-11 agent side, M1-14).
func TestAgentBinary_RunsAndRotatesAgainstTheRealServer(t *testing.T) {
	bin := agentBinary(t)
	srv := startTestServer(t, nil)
	defer srv.stop()
	seedEnrollmentToken(t)

	dir := t.TempDir()
	fixture, err := filepath.Abs("../../../agent/fixtures/debian-13")
	if err != nil {
		t.Fatal(err)
	}
	_, agentPort, _ := strings.Cut(srv.agent, ":")
	config := filepath.Join(dir, "agent.toml")
	state := filepath.Join(dir, "state")
	toml := "[server]\nurl = \"http://" + srv.web + "\"\nagent_url = \"https://localhost:" + agentPort + "\"\n" +
		"[paths]\nstate_dir = \"" + state + "\"\nhost_root = \"" + fixture + "\"\n"
	if err := os.WriteFile(config, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	enroll := exec.CommandContext(ctx, bin, "enroll", "--config", config)
	enroll.Env = append(os.Environ(), "SERMON_ENROLLMENT_TOKEN="+fixtureToken)
	out, err := enroll.CombinedOutput()
	if err != nil {
		t.Fatalf("enroll: %v\n%s", err, out)
	}
	nodeID := regexp.MustCompile(`enrolled as node ([0-9a-f-]{36})`).FindStringSubmatch(string(out))[1]

	// Pretend 25 of 30 days are over, so that the agent rotates right after connecting.
	identityFile := filepath.Join(state, "identity", "identity.toml")
	meta, err := os.ReadFile(identityFile)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	meta = regexp.MustCompile(`(?m)^issued_at = \d+$`).ReplaceAll(meta, []byte(fmt.Sprintf("issued_at = %d", now-25*24*3600)))
	meta = regexp.MustCompile(`(?m)^not_after = \d+$`).ReplaceAll(meta, []byte(fmt.Sprintf("not_after = %d", now+5*24*3600)))
	if err := os.WriteFile(identityFile, meta, 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(state, "identity", "agent.crt"))

	agentLogs := &syncBuffer{}
	run := exec.CommandContext(ctx, bin, "run", "--config", config)
	run.Stdout, run.Stderr = agentLogs, agentLogs
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = run.Process.Kill() }()

	node := regexp.QuoteMeta(nodeID)
	waitFor(t, srv.logs, regexp.MustCompile(`msg="agent certificate rotated" .*node=(`+node+`)`))
	waitFor(t, srv.logs, regexp.MustCompile(`(?s)msg="agent connected" [^\n]*node=`+node+`.*msg="agent connected" [^\n]*node=(`+node+`)`))
	after, _ := os.ReadFile(filepath.Join(state, "identity", "agent.crt"))
	if string(before) == string(after) {
		t.Errorf("certificate not replaced after the rotation\nagent:\n%s", agentLogs.String())
	}
	if strings.Contains(srv.logs.String(), "security:") {
		t.Errorf("rotation raised a security event:\n%s", srv.logs.String())
	}

	if err := run.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := run.Wait(); err != nil {
		t.Errorf("agent exit: %v\n%s", err, agentLogs.String())
	}
	if !strings.Contains(agentLogs.String(), "agent stopped") {
		t.Errorf("agent did not stop cleanly:\n%s", agentLogs.String())
	}
}

// TestAgentBinary_DeliversMetricsIntoTheDatabase is the M1 data path end to end: the Rust agent
// collects every second, buffers and sends; the server aggregates 10 s windows, writes them to
// TimescaleDB and acknowledges (M1-14..M1-24).
func TestAgentBinary_DeliversMetricsIntoTheDatabase(t *testing.T) {
	bin := agentBinary(t)
	srv := startTestServer(t, nil)
	defer srv.stop()
	seedEnrollmentToken(t)

	dir := t.TempDir()
	fixture, err := filepath.Abs("../../../agent/fixtures/debian-13")
	if err != nil {
		t.Fatal(err)
	}
	_, agentPort, _ := strings.Cut(srv.agent, ":")
	config := filepath.Join(dir, "agent.toml")
	toml := "[server]\nurl = \"http://" + srv.web + "\"\nagent_url = \"https://localhost:" + agentPort + "\"\n" +
		"[paths]\nstate_dir = \"" + filepath.Join(dir, "state") + "\"\nhost_root = \"" + fixture + "\"\n"
	if err := os.WriteFile(config, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	enroll := exec.CommandContext(ctx, bin, "enroll", "--config", config)
	enroll.Env = append(os.Environ(), "SERMON_ENROLLMENT_TOKEN="+fixtureToken)
	out, err := enroll.CombinedOutput()
	if err != nil {
		t.Fatalf("enroll: %v\n%s", err, out)
	}
	nodeID := regexp.MustCompile(`enrolled as node ([0-9a-f-]{36})`).FindStringSubmatch(string(out))[1]

	agentLogs := &syncBuffer{}
	run := exec.CommandContext(ctx, bin, "run", "--config", config)
	run.Stdout, run.Stderr = agentLogs, agentLogs
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = run.Process.Kill() }()

	conn, err := pgx.Connect(ctx, pgtest.Shared(t).OwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	var series, windows, seq int64
	var version string
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		err := conn.QueryRow(ctx, `SELECT (SELECT count(*) FROM series WHERE node_id = $1),
		       (SELECT count(*) FROM metrics_raw r JOIN series s ON s.id = r.series_id WHERE s.node_id = $1),
		       n.ingest_seq, coalesce(n.agent_version, '')
		FROM nodes n WHERE n.id = $1`, nodeID).Scan(&series, &windows, &seq, &version)
		if err != nil {
			t.Fatal(err)
		}
		if windows > 0 && seq > 0 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if series < 40 || windows < series || seq == 0 || version == "" {
		t.Fatalf("series %d, windows %d, ingest_seq %d, agent_version %q\nagent:\n%s\nserver:\n%s",
			series, windows, seq, version, agentLogs.String(), srv.logs.String())
	}
	// The fixture is static: rates are absent, gauges like the memory size are exact.
	var memory float64
	if err := conn.QueryRow(ctx, `SELECT r.avg FROM metrics_raw r JOIN series s ON s.id = r.series_id
		WHERE s.node_id = $1 AND s.metric = 'sermon_memory_total_bytes' LIMIT 1`, nodeID).Scan(&memory); err != nil || memory != 8_123_740*1024 {
		t.Errorf("memory window = %v, %v; want the fixture's MemTotal", memory, err)
	}
	if strings.Contains(srv.logs.String(), "level=ERROR") {
		t.Errorf("server logged errors:\n%s", srv.logs.String())
	}

	if err := run.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := run.Wait(); err != nil {
		t.Errorf("agent exit: %v\n%s", err, agentLogs.String())
	}
}
