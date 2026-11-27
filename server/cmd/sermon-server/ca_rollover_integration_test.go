//go:build integration

package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/pgtest"
	"github.com/pmoscode/SerMon/server/internal/app/auth/password"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// Test fixture, not a secret.
const superPassword = "superadmin rollover passphrase 1" // betterleaks:allow

// cookieClient sends the Origin and the session cookie by hand: the cookie is Secure and the
// test talks plain HTTP to the server (TLS ends at the proxy in production).
type cookieClient struct {
	origin string
	cookie string
}

func (c *cookieClient) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Origin", c.origin)
	if c.cookie != "" {
		r.Header.Set("Cookie", c.cookie)
	}
	res, err := http.DefaultTransport.RoundTrip(r)
	if err == nil {
		for _, sc := range res.Cookies() {
			if sc.Name == "__Host-sermon_session" && sc.Value != "" {
				c.cookie = sc.Name + "=" + sc.Value
			}
		}
	}
	return res, err
}

func seedSuperadmin(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, pgtest.Shared(t).OwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	h, _ := password.NewHasher(password.Params{Memory: 8 * 1024, Time: 1, Threads: 1, SaltLength: 16, KeyLength: 32})
	hash, _ := h.Hash(superPassword)
	id := uuid.New()
	email := "super-" + id.String()[:8] + "@example.org"
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO users (id, email, display_name) VALUES ($1, $2, 'Super')", []any{id, email}},
		{"INSERT INTO user_credentials (user_id, password_hash) VALUES ($1, $2)", []any{id, hash}},
		{"INSERT INTO superadmins (user_id) VALUES ($1)", []any{id}},
	} {
		if _, err := conn.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("%s: %v", stmt.sql, err)
		}
	}
	return email
}

func TestAgentCARollover_EndToEnd(t *testing.T) {
	srv := startTestServer(t, map[string]string{"SERMON_ALLOWED_ORIGINS": "http://sermon.test"}) // migrates
	defer srv.stop()
	seedEnrollmentTokenWithUses(t, 2) // one agent before the rollover, one during it
	ctx := context.Background()

	// The agent enrolls under the current CA and trusts only its root.
	agent := srv.enroll(t, fixtureToken)
	oldRoots := agent.roots
	if len(oldRoots) != 1 {
		t.Fatalf("trust anchors at enrollment = %d, want 1", len(oldRoots))
	}

	// A superadmin starts the rollover over the web API.
	rt := &cookieClient{origin: "http://sermon.test"}
	hc := &http.Client{Transport: rt, Timeout: 30 * time.Second}
	email := seedSuperadmin(t)
	if _, err := webv1connect.NewAuthServiceClient(hc, "http://"+srv.web).Login(ctx, connect.NewRequest(&webv1.LoginRequest{Email: email, Password: superPassword})); err != nil {
		t.Fatalf("login: %v", err)
	}
	ca := webv1connect.NewAgentCAServiceClient(hc, "http://"+srv.web)
	if _, err := ca.StartAgentCARollover(ctx, connect.NewRequest(&webv1.StartAgentCARolloverRequest{})); err != nil {
		t.Fatalf("StartAgentCARollover() = %v\n%s", err, srv.logs.String())
	}
	status, _ := ca.GetAgentCAStatus(ctx, connect.NewRequest(&webv1.GetAgentCAStatusRequest{}))
	cas := status.Msg.GetAuthorities()
	var old, current *webv1.AgentCA
	for _, a := range cas {
		if a.GetRetireTime() == nil && a.GetIssuing() {
			current = a
		} else if a.GetRetireTime() == nil {
			old = a
		}
	}
	if old == nil || current == nil || old.GetCertificatesInUse() < 1 {
		t.Fatalf("status = %v", cas)
	}

	// The agent still trusts only the old root: it connects (server certificate from the old CA)
	// and receives the trust bundle with both roots.
	res, err := srv.session(t, agent.cert, oldRoots, false)
	if err != nil || len(res.bundle) != 2 {
		t.Fatalf("session during the rollover: %v, bundle %d roots", err, len(res.bundle))
	}
	bundle := res.bundle

	// V1-19: a new host enrolls during the rollover: certificate of the new CA, both roots, and it
	// connects (the server certificate still comes from the old CA).
	fresh := srv.enroll(t, fixtureToken)
	if len(fresh.roots) != 2 || len(fresh.cert.Certificate) < 2 || bytes.Equal(fresh.cert.Certificate[1], agent.cert.Certificate[1]) {
		t.Fatalf("enrolled during the rollover: %d roots, chain %d, same intermediate as before: %v",
			len(fresh.roots), len(fresh.cert.Certificate), len(fresh.cert.Certificate) > 1 && bytes.Equal(fresh.cert.Certificate[1], agent.cert.Certificate[1]))
	}
	if _, err := srv.session(t, fresh.cert, fresh.roots, false); err != nil {
		t.Fatalf("session of the agent enrolled during the rollover: %v", err)
	}
	// The existing agent keeps connecting with its old certificate until it rotates.
	if _, err := srv.session(t, agent.cert, bundle, false); err != nil {
		t.Fatalf("existing agent during the rollover: %v", err)
	}

	// It rotates and gets a certificate of the new CA.
	res, err = srv.session(t, agent.cert, bundle, true)
	if err != nil || res.rotated == nil {
		t.Fatalf("rotation: %v", err)
	}
	rotated := *res.rotated

	// The old CA cannot be retired while the old certificate is in its grace period ...
	if _, err := ca.RetireAgentCA(ctx, connect.NewRequest(&webv1.RetireAgentCARequest{Id: old.GetId()})); connect.CodeOf(err) != connect.CodeFailedPrecondition ||
		!strings.Contains(err.Error(), "valid agent certificates") {
		t.Errorf("retire during the grace period: %v, want FAILED_PRECONDITION", err)
	}
	// ... after it (simulated), it can.
	conn, err := pgx.Connect(ctx, pgtest.Shared(t).OwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, "UPDATE agent_certificates SET revoked_at = now() - interval '1 second' WHERE authority_id = $1 AND revoked_at IS NOT NULL", old.GetId()); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "UPDATE agent_certificates SET not_after = now() - interval '1 second' WHERE authority_id = $1", old.GetId()); err != nil {
		t.Fatal(err)
	}
	if _, err := ca.RetireAgentCA(ctx, connect.NewRequest(&webv1.RetireAgentCARequest{Id: old.GetId()})); err != nil {
		t.Fatalf("RetireAgentCA() = %v", err)
	}

	// Both actions are in the instance audit log with the superadmin as actor (M1-11b).
	me, err := webv1connect.NewAuthServiceClient(hc, "http://"+srv.web).GetSession(ctx, connect.NewRequest(&webv1.GetSessionRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	log, err := webv1connect.NewInstanceAuditServiceClient(hc, "http://"+srv.web).ListInstanceAuditLog(ctx, connect.NewRequest(&webv1.ListInstanceAuditLogRequest{}))
	if err != nil {
		t.Fatalf("ListInstanceAuditLog() = %v", err)
	}
	logged := map[string]bool{}
	for _, e := range log.Msg.GetEntries() {
		if e.GetActorId() == me.Msg.GetSession().GetUserId() {
			logged[e.GetAction()+" "+e.GetTarget()] = true
		}
	}
	for _, want := range []string{fmt.Sprintf("pki.rollover_started ca:%d", current.GetId()), fmt.Sprintf("pki.authority_retired ca:%d", old.GetId())} {
		if !logged[want] {
			t.Errorf("instance audit log lacks %q: %v", want, log.Msg.GetEntries())
		}
	}

	// Now the server certificate comes from the new CA: an agent trusting only the new root connects.
	var newRoot [][]byte
	for _, r := range bundle {
		if !strings.EqualFold(string(r), string(oldRoots[0])) {
			newRoot = append(newRoot, r)
		}
	}
	res, err = srv.session(t, rotated, newRoot, false)
	if err != nil || len(res.bundle) != 1 {
		t.Errorf("session after the retirement trusting only the new root: %v, bundle %d", err, len(res.bundle))
	}
	if _, err := srv.session(t, fresh.cert, newRoot, false); err != nil {
		t.Errorf("the agent enrolled during the rollover after the retirement: %v", err)
	}
	if _, err := srv.session(t, rotated, oldRoots, false); err == nil {
		t.Error("the retired root must no longer verify the server")
	}
}
