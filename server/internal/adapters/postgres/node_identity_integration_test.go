//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/agentca"
	"github.com/pmoscode/SerMon/server/internal/app/enrollment"
	"github.com/pmoscode/SerMon/server/internal/app/secevents"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/secevent"
)

// Takeover (M2-23, T5): only within the window an operator opened, only with a token of the node's
// group; the node keeps its ID, the old certificate dies, window and lock close. Unlock lifts a lock.
func TestNodeIdentity_TakeoverAndUnlock(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	db := open(t, f.appDSN)
	tokens := postgres.NewEnrollmentTokenStore(db)
	o := newTokenOrg(t, f)
	_, hash := issue(t, tokens, o, o.group, 10)
	st, _, _ := tokens.Lookup(ctx, hash, time.Now())
	_, otherHash := issue(t, tokens, o, o.other, 10)
	otherGroup, _, _ := tokens.Lookup(ctx, otherHash, time.Now())
	registry := postgres.NewNodeRegistry(db)
	rotation := postgres.NewRotationStore(db)
	ca := postgres.NewAgentCAStore(db)
	org := o.scope.OrgID()

	machine := strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := registerMachine(registry, st, machine); err != nil {
		t.Fatal(err)
	}
	_, found, err := registry.TakeoverCandidate(ctx, st, machine, time.Now())
	if err != nil || found {
		t.Fatalf("candidate without a window: %v, %v", found, err)
	}
	var old string
	var nodeID uuid.UUID
	ownerQueryRow(t, f.db.OwnerDSN, "SELECT n.id, c.serial FROM nodes n JOIN agent_certificates c ON c.node_id = n.id WHERE n.machine_id = $1", []any{machine}, &nodeID, &old)
	node := identity.NodeID(nodeID)

	// Locked by a second session, then the window opens.
	if err := db.InTenant(ctx, o.scope, func(q *sqlcdb.Queries) error {
		_, err := q.LockNode(ctx, sqlcdb.LockNodeParams{ID: nodeID, At: ptr(time.Now()), Reason: pgtype.Text{String: "second session", Valid: true}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Hour)
	if err := rotation.AllowTakeover(ctx, org, node, until, entry(o.admin, "node.takeover_allow")); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := registry.TakeoverCandidate(ctx, otherGroup, machine, time.Now()); found {
		t.Error("a token of another group must not take the node over")
	}
	if _, found, _ := registry.TakeoverCandidate(ctx, st, machine, until.Add(time.Second)); found {
		t.Error("the window must end")
	}
	got, found, err := registry.TakeoverCandidate(ctx, st, machine, time.Now())
	if err != nil || !found || got != node {
		t.Fatalf("candidate = %v, %v, %v", got, found, err)
	}

	now := time.Now()
	cert := agentca.CertificateRecord{Serial: randomSerial(), Org: org, Node: node, Authority: registryAuthority, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour)}
	n := enrollment.Node{ID: node, Group: st.Group, Token: st.ID, Facts: enrollment.NodeFacts{
		Hostname: "web-reinstalled", MachineID: machine, OSID: "debian", OSVersion: "13", KernelRelease: "6.12.0", Architecture: "amd64", CPUCount: 4, MemoryBytes: 2 << 30,
	}}
	if err := registry.TakeOver(ctx, st, n, cert, now, []access.AuditEntry{entry(o.admin, "enrollment_token.use"), entry(o.admin, "node.takeover")}); err != nil {
		t.Fatalf("TakeOver: %v", err)
	}
	if usable, _ := ca.Usable(ctx, cert.Serial, org, node, now.Add(time.Second)); !usable {
		t.Error("the new certificate must be usable")
	}
	if usable, _ := ca.Usable(ctx, old, org, node, now.Add(time.Second)); usable {
		t.Error("the old certificate must be revoked")
	}
	var hostname string
	var locked, window bool
	ownerQueryRow(t, f.db.OwnerDSN, "SELECT hostname, locked_at IS NOT NULL, takeover_until IS NOT NULL FROM nodes WHERE id = $1", []any{nodeID}, &hostname, &locked, &window)
	if hostname != "web-reinstalled" || locked || window {
		t.Errorf("node after takeover: %q locked %v window %v", hostname, locked, window)
	}
	// The window is used up: a second takeover with the same machine-id is a conflict again.
	if err := registry.TakeOver(ctx, st, n, cert, now, nil); !errors.Is(err, enrollment.ErrMachineIDConflict) {
		t.Errorf("second takeover: %v", err)
	}

	// Unlock: once, with audit; again it changes nothing.
	if err := db.InTenant(ctx, o.scope, func(q *sqlcdb.Queries) error {
		_, err := q.LockNode(ctx, sqlcdb.LockNodeParams{ID: nodeID, At: ptr(time.Now()), Reason: pgtype.Text{String: "second session", Valid: true}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if ok, err := rotation.Unlock(ctx, org, node, entry(o.admin, "node.unlock")); err != nil || !ok {
		t.Errorf("unlock = %v, %v", ok, err)
	}
	if ok, err := rotation.Unlock(ctx, org, node, entry(o.admin, "node.unlock")); err != nil || ok {
		t.Errorf("second unlock = %v, %v", ok, err)
	}
	actions := auditActions(t, postgres.NewAccessStore(db), o.scope)
	if strings.Count(strings.Join(actions, ","), "node.unlock") != 1 || !strings.Contains(strings.Join(actions, ","), "node.takeover_allow") {
		t.Errorf("audit = %v", actions)
	}
	// Another organization's node is not touched.
	if err := rotation.AllowTakeover(ctx, newTokenOrg(t, f).scope.OrgID(), node, until, entry(o.admin, "x")); err == nil {
		t.Error("a node of another organization must not be found")
	}
}

func ptr[T any](v T) *T { return &v }

func ownerQueryRow(t *testing.T, dsn, sql string, args []any, dest ...any) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if err := conn.QueryRow(ctx, sql, args...).Scan(dest...); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
}

// The server's own security events (M2-24, T3, T4) are written in the transaction of what they
// report: exactly one per enrollment, lock and rotation from a new address, in the node's org.
func TestServerSecurityEvents_OnePerOccurrence(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	db := open(t, f.appDSN)
	tokens := postgres.NewEnrollmentTokenStore(db)
	o := newTokenOrg(t, f)
	tok, hash := issue(t, tokens, o, o.group, 5)
	st, _, _ := tokens.Lookup(ctx, hash, time.Now())
	registry := postgres.NewNodeRegistry(db)
	rotation := postgres.NewRotationStore(db)
	events := postgres.NewSecurityEventStore(db)
	org := o.scope.OrgID()

	node := identity.NodeID(uuid.New())
	now := time.Now()
	n := enrollment.Node{ID: node, Group: st.Group, Token: st.ID, Facts: enrollment.NodeFacts{
		Hostname: "web", MachineID: strings.ReplaceAll(uuid.NewString(), "-", ""), OSID: "debian", OSVersion: "13",
		KernelRelease: "6.12.0", Architecture: "amd64", CPUCount: 2, MemoryBytes: 1 << 30,
		ClientIP: netip.MustParseAddr("192.0.2.30"),
	}}
	cert := agentca.CertificateRecord{Serial: randomSerial(), Org: org, Node: node, Authority: registryAuthority, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour)}
	if err := registry.Register(ctx, st, n, cert, now, nil); err != nil {
		t.Fatal(err)
	}
	if err := rotation.Lock(ctx, org, node, "second session from 192.0.2.66", time.Now(), entry(o.admin, "security.node_locked")); err != nil {
		t.Fatal(err)
	}
	if err := rotation.NoteNewAddress(ctx, org, node, "192.0.2.31", "192.0.2.30", time.Now(), entry(o.admin, "security.rotation_new_ip")); err != nil {
		t.Fatal(err)
	}

	got, err := events.List(ctx, o.scope, secevents.Filter{From: now.Add(-time.Hour), To: time.Now().Add(time.Minute), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	byKind := map[secevent.Kind]secevent.Event{}
	for _, e := range got {
		if _, dup := byKind[e.Kind]; dup {
			t.Errorf("%s twice", e.Kind)
		}
		byKind[e.Kind] = e
	}
	used := byKind[secevent.EnrollmentTokenUsed]
	if used.Node != node || used.Details.TokenID != tok.ID.String() || used.Remote == nil || used.Remote.IP != netip.MustParseAddr("192.0.2.30") {
		t.Errorf("token used = %+v", used)
	}
	if e := byKind[secevent.NodeLocked]; e.Node != node || e.Details.Reason != "second session from 192.0.2.66" {
		t.Errorf("locked = %+v", e)
	}
	if e := byKind[secevent.NodeRotationNewIP]; e.Node != node || e.Remote == nil || e.Remote.IP != netip.MustParseAddr("192.0.2.31") || e.Details.PreviousIP != "192.0.2.30" {
		t.Errorf("new address = %+v", e)
	}
	// Another organization sees none of them.
	other := newTokenOrg(t, f)
	if got, _ := events.List(ctx, other.scope, secevents.Filter{From: now.Add(-time.Hour), To: time.Now().Add(time.Minute), Limit: 10}); len(got) != 0 {
		t.Errorf("another org sees %v", got)
	}
}

// Self-monitoring (M2-24): the application role reads the superadmins' addresses only through the
// definer function, and the size of the database.
func TestSelfMonitorStore_SuperadminsAndSize(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	store := postgres.NewSelfMonitorStore(open(t, f.appDSN))
	user := uuid.New()
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO users (id, email, display_name) VALUES ($1, $2, 'Root')", user, "root-"+user.String()[:8]+"@example.org")
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO superadmins (user_id) VALUES ($1)", user)
	emails, err := store.Superadmins(ctx)
	if err != nil || !slices.Contains(emails, "root-"+user.String()[:8]+"@example.org") {
		t.Errorf("Superadmins() = %v, %v", emails, err)
	}
	if size, err := store.DatabaseSize(ctx); err != nil || size < 1<<20 {
		t.Errorf("DatabaseSize() = %d, %v", size, err)
	}
	// The users table itself stays closed without a tenant.
	conn, err := pgx.Connect(ctx, f.appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var n int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&n); err == nil && n > 0 {
		t.Errorf("the application role reads %d users without a tenant", n)
	}
}
