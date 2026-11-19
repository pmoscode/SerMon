//go:build integration

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/pgtest"
	agentv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1/agentv1connect"
)

// Fixture enrollment token (not a secret).
const fixtureToken = "sme_AgentApiIntegrationTestToken0123456789abcde" // betterleaks:allow

func seedEnrollmentToken(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, pgtest.Shared(t).OwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	org, group := uuid.New(), uuid.New()
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{"SELECT set_config('sermon.org_id', $1, false)", []any{org.String()}},
		{"INSERT INTO organizations (id, name) VALUES ($1, 'Agents')", []any{org}},
		{"INSERT INTO node_groups (id, org_id, name) VALUES ($1, $2, 'servers')", []any{group, org}},
		{"DELETE FROM enrollment_tokens WHERE token_hash = sha256(convert_to($1, 'UTF8'))", []any{fixtureToken}},
		{`INSERT INTO enrollment_tokens (id, org_id, group_id, token_hash, expires_at, max_uses)
		  VALUES ($1, $2, $3, sha256(convert_to($4, 'UTF8')), now() + interval '1 hour', 1)`, []any{uuid.New(), org, group, fixtureToken}},
	} {
		if _, err := conn.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("%s: %v", stmt.sql, err)
		}
	}
}

func TestAgentAPI_EnrolledAgentConnectsRotatesAndIsLockedOnParallelUse(t *testing.T) {
	keysFile := sharedKeysFile()
	if _, err := ensureKeyFile(keysFile); err != nil {
		t.Fatal(err)
	}
	src := dbSource(t, keysFile, map[string]string{
		"SERMON_HTTP_ADDR": "127.0.0.1:0", "SERMON_AGENT_ADDR": "127.0.0.1:0", "SERMON_AGENT_HOSTNAMES": "localhost",
	})
	seedEnrollmentToken(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logs syncBuffer
	done := make(chan int, 1)
	go func() { done <- run(ctx, nil, src, &logs, &logs) }()
	webAddr := waitFor(t, &logs, regexp.MustCompile(`msg=listening .*addr=(\S+)`))
	agentAddr := waitFor(t, &logs, regexp.MustCompile(`msg="agent API listening" .*addr=(\S+)`))

	// 1. Enroll over the web listener (gRPC over h2c, like behind Caddy).
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	h2c := &http.Client{Transport: &http.Transport{Protocols: protocols}, Timeout: 30 * time.Second}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csr, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	enrolled, err := agentv1connect.NewEnrollmentServiceClient(h2c, "http://"+webAddr, connect.WithGRPC()).Enroll(ctx, connect.NewRequest(&agentv1.EnrollRequest{
		Token: fixtureToken, Csr: csr, AgentVersion: "0.0.0", HostFacts: &agentv1.HostFacts{
			Hostname: "web-01", MachineId: strings.ReplaceAll(uuid.NewString(), "-", ""), OsId: "debian", OsVersion: "13",
			KernelRelease: "6.12.0", Architecture: agentv1.Architecture_ARCHITECTURE_ARM64, CpuCount: 2, MemoryBytes: 1 << 30,
		},
	}))
	if err != nil {
		t.Fatalf("Enroll() error = %v\n%s", err, logs.String())
	}

	// 2. Connect to the mTLS agent API with the issued certificate, trusting only the trust anchor.
	roots := x509.NewCertPool()
	anchor, _ := x509.ParseCertificate(enrolled.Msg.GetTrustAnchor())
	roots.AddCert(anchor)
	clientCert := tls.Certificate{Certificate: append([][]byte{enrolled.Msg.GetCertificate()}, enrolled.Msg.GetIntermediates()...), PrivateKey: key}
	agent := func(cert tls.Certificate) agentv1connect.AgentServiceClient {
		tr := &http.Transport{ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{
			RootCAs: roots, Certificates: []tls.Certificate{cert}, ServerName: "localhost", MinVersion: tls.VersionTLS13,
		}}
		return agentv1connect.NewAgentServiceClient(&http.Client{Transport: tr, Timeout: 30 * time.Second}, "https://"+agentAddr, connect.WithGRPC())
	}
	hello := &agentv1.SessionRequest{Payload: &agentv1.SessionRequest_Hello{Hello: &agentv1.Hello{
		ProtocolVersion: 1, AgentVersion: "0.0.0", HostFacts: &agentv1.HostFacts{
			Hostname: "web-01", MachineId: strings.Repeat("a", 32), OsId: "debian", OsVersion: "13", KernelRelease: "6.12.0",
			Architecture: agentv1.Architecture_ARCHITECTURE_ARM64, CpuCount: 2, MemoryBytes: 1 << 30,
		},
	}}}
	// sessionWith connects with cert, optionally rotates, and returns the new certificate.
	sessionWith := func(cert tls.Certificate, rotate bool) (*tls.Certificate, error) {
		stream := agent(cert).Session(ctx)
		defer func() { _ = stream.CloseResponse() }()
		if err := stream.Send(hello); err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		var next *tls.Certificate
		if rotate {
			newKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			newCSR, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, newKey)
			if err := stream.Send(&agentv1.SessionRequest{Payload: &agentv1.SessionRequest_RotateCertificate{
				RotateCertificate: &agentv1.RotateCertificate{Csr: newCSR},
			}}); err != nil && !errors.Is(err, io.EOF) {
				return nil, err
			}
			res, err := stream.Receive()
			if err != nil {
				return nil, err
			}
			issued := res.GetCertificateIssued()
			next = &tls.Certificate{Certificate: append([][]byte{issued.GetCertificate()}, issued.GetIntermediates()...), PrivateKey: newKey}
		}
		_ = stream.CloseRequest()
		if _, err := stream.Receive(); !errors.Is(err, io.EOF) {
			return next, err
		}
		return next, nil
	}
	session := func() error { _, err := sessionWith(clientCert, false); return err }
	if err := session(); err != nil {
		t.Fatalf("session with the enrolled certificate: %v\n%s", err, logs.String())
	}
	waitFor(t, &logs, regexp.MustCompile(`msg="agent connected" .*node=(`+enrolled.Msg.GetNodeId()+`)`))

	// 3. The agent rotates its certificate in the stream; the new one works for a new connection.
	rotated, err := sessionWith(clientCert, true)
	if err != nil || rotated == nil {
		t.Fatalf("rotation: %v\n%s", err, logs.String())
	}
	if _, err := sessionWith(*rotated, false); err != nil {
		t.Fatalf("session with the rotated certificate: %v", err)
	}

	// 4. Someone else still holding the old key (valid during the grace period) also rotates:
	//    the certificate was already replaced, so the identity is locked – both keys stop working.
	if _, err := sessionWith(clientCert, true); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("rotation with the replaced certificate: %v, want PERMISSION_DENIED", err)
	}
	for name, cert := range map[string]tls.Certificate{"old": clientCert, "rotated": *rotated} {
		if _, err := sessionWith(cert, false); err == nil {
			t.Errorf("%s certificate still works after the lock", name)
		}
	}
	waitFor(t, &logs, regexp.MustCompile(`(certificate revoked or unknown)`))
	conn, err := pgx.Connect(context.Background(), pgtest.Shared(t).OwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	var reason string
	if err := conn.QueryRow(context.Background(), "SELECT lock_reason FROM nodes WHERE id = $1", enrolled.Msg.GetNodeId()).Scan(&reason); err != nil ||
		!strings.Contains(reason, "already replaced") {
		t.Errorf("lock reason = %q, %v", reason, err)
	}

	cancel()
	select {
	case code := <-done:
		if code != exitOK {
			t.Errorf("exit code %d\n%s", code, logs.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("server did not stop")
	}
}
