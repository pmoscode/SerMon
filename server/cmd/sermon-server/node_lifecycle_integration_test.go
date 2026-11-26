//go:build integration

package main

import (
	"context"
	"crypto/tls"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/pgtest"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/nodelife"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	agentv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1/agentv1connect"
)

// enrollAs enrolls with fixtureToken and the given host facts (a fixed machine-id for a takeover).
func (s runningServer) enrollAs(t *testing.T, facts *agentv1.HostFacts) (testAgent, error) {
	t.Helper()
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	h2c := &http.Client{Transport: &http.Transport{Protocols: protocols}, Timeout: 30 * time.Second}
	key, csr := newKeyAndCSR(t)
	res, err := agentv1connect.NewEnrollmentServiceClient(h2c, "http://"+s.web, connect.WithGRPC()).Enroll(context.Background(),
		connect.NewRequest(&agentv1.EnrollRequest{Token: fixtureToken, Csr: csr, AgentVersion: "0.0.0", HostFacts: facts}))
	if err != nil {
		return testAgent{}, err
	}
	return testAgent{
		nodeID: res.Msg.GetNodeId(),
		cert:   tls.Certificate{Certificate: append([][]byte{res.Msg.GetCertificate()}, res.Msg.GetIntermediates()...), PrivateKey: key},
		roots:  res.Msg.GetTrustAnchors(),
	}, nil
}

// M5-29 (ADR 0041): a deactivated node is refused at once; it comes back only by an allowed
// takeover and a new enrollment (same ID). A deleted node is refused during the grace, cannot be
// taken over, and after the purge neither the node nor its certificates exist.
func TestAgentAPI_DeactivatedAndDeletedNodesCannotConnect(t *testing.T) {
	srv := startTestServer(t, nil)
	defer srv.stop()
	ctx := context.Background()
	orgID := seedEnrollmentTokenWithUses(t, 5)
	org := tenant.OrgID(orgID)
	facts := hostFacts()
	agent, err := srv.enrollAs(t, facts)
	if err != nil {
		t.Fatal(err)
	}
	node := identity.NodeID(uuid.MustParse(agent.nodeID))
	if _, err := srv.session(t, agent.cert, agent.roots, false); err != nil {
		t.Fatalf("session before: %v", err)
	}
	db := pgtest.Shared(t)
	pg, err := postgres.Open(ctx, pgtest.EnableAppRole(t, db))
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	store := postgres.NewNodeLifecycleStore(pg)
	audit := access.AuditEntry{Action: "node.deactivate", Target: "node:" + node.String()}
	scope, err := tenant.ForAllGroups(org)
	if err != nil {
		t.Fatal(err)
	}
	change := func(ch nodelife.Change) error {
		return store.Change(ctx, scope, node, func(nodelife.State) (nodelife.Change, error) { return ch, nil })
	}

	// Deactivated: refused.
	if err := change(nodelife.Change{Op: nodelife.OpDeactivate, At: time.Now(), Audit: audit}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.session(t, agent.cert, agent.roots, false); err == nil {
		t.Fatal("a deactivated node connected")
	}
	// Back by a takeover: the next enrollment with its machine-id keeps the ID and works.
	if err := postgres.NewRotationStore(pg).AllowTakeover(ctx, org, node, time.Now().Add(time.Hour), access.AuditEntry{Action: "node.takeover_allow"}); err != nil {
		t.Fatal(err)
	}
	again, err := srv.enrollAs(t, facts)
	if err != nil || again.nodeID != agent.nodeID {
		t.Fatalf("takeover = %v, %v; want node %s", again.nodeID, err, agent.nodeID)
	}
	if _, err := srv.session(t, again.cert, again.roots, false); err != nil {
		t.Fatalf("session after the takeover: %v\n%s", err, srv.logs.String())
	}

	// Scheduled for deletion: refused, no takeover, and after the grace it is gone.
	if err := change(nodelife.Change{Op: nodelife.OpScheduleDeletion, At: time.Now(), Due: time.Now().Add(time.Hour), Audit: audit}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.session(t, again.cert, again.roots, false); err == nil {
		t.Fatal("a node scheduled for deletion connected")
	}
	if err := postgres.NewRotationStore(pg).AllowTakeover(ctx, org, node, time.Now().Add(time.Hour), access.AuditEntry{Action: "node.takeover_allow"}); err == nil {
		t.Error("a node scheduled for deletion may be taken over")
	}
	if _, err := srv.enrollAs(t, facts); err == nil {
		t.Error("an enrollment took a node scheduled for deletion over")
	}
	later := func() time.Time { return time.Now().Add(2 * time.Hour) }
	svc, err := nodelife.NewService(store, authz.NewPolicy(), later, nodelife.MinDeletionGrace)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, db.OwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var nodes, certs int
	if err := conn.QueryRow(ctx, "SELECT (SELECT count(*) FROM nodes WHERE id = $1), (SELECT count(*) FROM agent_certificates WHERE node_id = $1)",
		agent.nodeID).Scan(&nodes, &certs); err != nil || nodes != 0 || certs != 0 {
		t.Errorf("after the purge: %d nodes, %d certificates (%v)", nodes, certs, err)
	}
	if _, err := srv.session(t, again.cert, again.roots, false); err == nil {
		t.Error("a deleted node connected")
	}
	// The host may enroll anew: a new node.
	fresh, err := srv.enrollAs(t, facts)
	if err != nil || fresh.nodeID == agent.nodeID {
		t.Errorf("new enrollment after the purge = %v, %v; want a new node", fresh.nodeID, err)
	}
}
