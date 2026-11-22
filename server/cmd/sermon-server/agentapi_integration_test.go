//go:build integration

package main

import (
	"context"
	"crypto/tls"
	"regexp"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/pgtest"
)

// Fixture enrollment token (not a secret).
const fixtureToken = "sme_AgentApiIntegrationTestToken0123456789abcde" // betterleaks:allow

func seedEnrollmentToken(t *testing.T) {
	t.Helper()
	seedEnrollmentTokenWithUses(t, 1)
}

// seedEnrollmentTokenWithUses stores fixtureToken for a new organization and returns its ID.
func seedEnrollmentTokenWithUses(t *testing.T, uses int) uuid.UUID {
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
		{"INSERT INTO organizations (id, name) VALUES ($1, 'Agents ' || $1::uuid::text)", []any{org}},
		{"INSERT INTO node_groups (id, org_id, name) VALUES ($1, $2, 'servers')", []any{group, org}},
		{"DELETE FROM enrollment_tokens WHERE token_hash = sha256(convert_to($1, 'UTF8'))", []any{fixtureToken}},
		{`INSERT INTO enrollment_tokens (id, org_id, group_id, token_hash, expires_at, max_uses)
		  VALUES ($1, $2, $3, sha256(convert_to($4, 'UTF8')), now() + interval '1 hour', $5)`, []any{uuid.New(), org, group, fixtureToken, uses}},
	} {
		if _, err := conn.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("%s: %v", stmt.sql, err)
		}
	}
	return org
}

func TestAgentAPI_EnrolledAgentConnectsRotatesAndIsLockedOnParallelUse(t *testing.T) {
	srv := startTestServer(t, nil) // migrates the database
	defer srv.stop()
	seedEnrollmentToken(t)

	// 1. Enroll, 2. connect to the mTLS agent API with the issued certificate.
	agent := srv.enroll(t, fixtureToken)
	first, err := srv.session(t, agent.cert, agent.roots, false)
	if err != nil {
		t.Fatalf("session with the enrolled certificate: %v\n%s", err, srv.logs.String())
	}
	if len(first.bundle) != 1 {
		t.Errorf("trust bundle = %d roots, want 1", len(first.bundle))
	}
	waitFor(t, srv.logs, regexp.MustCompile(`msg="agent connected" .*node=(`+agent.nodeID+`)`))

	// 3. The agent rotates its certificate in the stream; the new one works for a new connection.
	res, err := srv.session(t, agent.cert, agent.roots, true)
	if err != nil || res.rotated == nil {
		t.Fatalf("rotation: %v\n%s", err, srv.logs.String())
	}
	if _, err := srv.session(t, *res.rotated, agent.roots, false); err != nil {
		t.Fatalf("session with the rotated certificate: %v", err)
	}

	// 4. Someone else still holding the old key (valid during the grace period) also rotates:
	//    the certificate was already replaced, so the identity is locked – both keys stop working.
	if _, err := srv.session(t, agent.cert, agent.roots, true); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("rotation with the replaced certificate: %v, want PERMISSION_DENIED", err)
	}
	for name, cert := range map[string]tls.Certificate{"old": agent.cert, "rotated": *res.rotated} {
		if _, err := srv.session(t, cert, agent.roots, false); err == nil {
			t.Errorf("%s certificate still works after the lock", name)
		}
	}
	waitFor(t, srv.logs, regexp.MustCompile(`(certificate revoked or unknown)`))
	conn, err := pgx.Connect(context.Background(), pgtest.Shared(t).OwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	var reason string
	if err := conn.QueryRow(context.Background(), "SELECT lock_reason FROM nodes WHERE id = $1", agent.nodeID).Scan(&reason); err != nil ||
		!strings.Contains(reason, "already replaced") {
		t.Errorf("lock reason = %q, %v", reason, err)
	}
}
