//go:build integration

package postgres_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/agentca"
	"github.com/pmoscode/SerMon/server/internal/app/enrollment"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

type tokenOrg struct {
	mgmtOrg
	group tenant.GroupID
	other tenant.GroupID
}

// registryAuthority is the CA of certificates registered by register (set by newTokenOrg).
var registryAuthority int32

func newTokenOrg(t *testing.T, f fixture) tokenOrg {
	t.Helper()
	registryAuthority = issuingAuthority(t, f.db.OwnerDSN)
	o := newMgmtOrg(t, f)
	store := postgres.NewAccessStore(open(t, f.appDSN))
	to := tokenOrg{mgmtOrg: o, group: tenant.GroupID(uuid.New()), other: tenant.GroupID(uuid.New())}
	for i, g := range []tenant.GroupID{to.group, to.other} {
		if err := store.CreateGroup(context.Background(), o.scope, access.Group{ID: g, Name: string(rune('a' + i))}, entry(o.admin, "group.create")); err != nil {
			t.Fatal(err)
		}
	}
	return to
}

// issue stores a token valid for one hour.
func issue(t *testing.T, s *postgres.EnrollmentTokenStore, o tokenOrg, group tenant.GroupID, uses int) (enrollment.Token, [32]byte) {
	t.Helper()
	now := time.Now()
	tok := enrollment.Token{ID: enrollment.TokenID(uuid.New()), Group: group, CreatedBy: o.admin, CreatedAt: now, ExpiresAt: now.Add(time.Hour), MaxUses: uses}
	hash := sha256.Sum256([]byte(uuid.NewString()))
	scope, _ := tenant.ForGroups(o.scope.OrgID(), group)
	if err := s.Create(context.Background(), scope, tok, hash, entry(o.admin, "enrollment_token.create")); err != nil {
		t.Fatal(err)
	}
	return tok, hash
}

func TestEnrollmentTokenStore_LookupHonorsExpiryUsesAndRevocation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	s := postgres.NewEnrollmentTokenStore(open(t, f.appDSN))
	o := newTokenOrg(t, f)
	tok, hash := issue(t, s, o, o.group, 2)

	st, found, err := s.Lookup(ctx, hash, time.Now())
	if err != nil || !found || st.ID != tok.ID || st.Org != o.scope.OrgID() || st.Group != o.group || st.Hash != hash {
		t.Fatalf("Lookup() = %+v, %v, %v", st, found, err)
	}
	if _, found, _ := s.Lookup(ctx, hash, tok.ExpiresAt.Add(time.Second)); found {
		t.Error("expired tokens must not be found")
	}
	registry := postgres.NewNodeRegistry(open(t, f.appDSN))
	for i := range 2 {
		if err := register(registry, st); err != nil {
			t.Fatalf("use %d: %v", i+1, err)
		}
	}
	if err := register(registry, st); !errors.Is(err, enrollment.ErrInvalidToken) {
		t.Errorf("third use of a 2-use token: %v, want ErrInvalidToken", err)
	}
	if _, found, _ := s.Lookup(ctx, hash, time.Now()); found {
		t.Error("a used-up token must not be found")
	}

	other, otherHash := issue(t, s, o, o.group, 1)
	if err := s.Revoke(ctx, o.scope, other.ID, entry(o.admin, "enrollment_token.revoke")); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.Lookup(ctx, otherHash, time.Now()); found {
		t.Error("a revoked token must not be found")
	}
	if got := auditActions(t, postgres.NewAccessStore(open(t, f.appDSN)), o.scope); !equalStrings(got, []string{
		"group.create", "group.create", "enrollment_token.create",
		"enrollment_token.use", "node.enroll", "enrollment_token.use", "node.enroll",
		"enrollment_token.create", "enrollment_token.revoke",
	}) {
		t.Errorf("audit = %v", got)
	}
}

// register enrolls a new node with a random machine-id using the token.
func register(r *postgres.NodeRegistry, st enrollment.Stored) error {
	return registerMachine(r, st, strings.ReplaceAll(uuid.NewString(), "-", ""))
}

func registerMachine(r *postgres.NodeRegistry, st enrollment.Stored, machineID string) error {
	node := identity.NodeID(uuid.New())
	now := time.Now()
	n := enrollment.Node{ID: node, Group: st.Group, Token: st.ID, Facts: enrollment.NodeFacts{
		Hostname: "web", MachineID: machineID, OSID: "debian", OSVersion: "13", KernelRelease: "6.12.0",
		Architecture: "amd64", CPUCount: 2, MemoryBytes: 1 << 30,
	}}
	cert := agentca.CertificateRecord{
		Serial: randomSerial(), Org: st.Org, Node: node, Authority: registryAuthority,
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
	}
	return r.Register(context.Background(), st, n, cert, now, []access.AuditEntry{
		{Action: "enrollment_token.use", Target: "t"}, {Action: "node.enroll", Target: "n"},
	})
}

func TestNodeRegistry_CreatesTheNodeWithAUsableCertificate(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	db := open(t, f.appDSN)
	s := postgres.NewEnrollmentTokenStore(db)
	o := newTokenOrg(t, f)
	_, hash := issue(t, s, o, o.group, 5)
	st, _, _ := s.Lookup(ctx, hash, time.Now())
	registry := postgres.NewNodeRegistry(db)
	machine := strings.ReplaceAll(uuid.NewString(), "-", "")
	node := identity.NodeID(uuid.New())
	now := time.Now()
	cert := agentca.CertificateRecord{
		Serial: randomSerial(), Org: st.Org, Node: node, Authority: issuingAuthority(t, f.db.OwnerDSN),
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
	}
	n := enrollment.Node{ID: node, Group: st.Group, Token: st.ID, Facts: enrollment.NodeFacts{
		Hostname: "db-01", MachineID: machine, OSID: "debian", OSVersion: "13", KernelRelease: "6.12.0", Architecture: "arm64", CPUCount: 8, MemoryBytes: 16 << 30,
	}}

	if err := registry.Register(ctx, st, n, cert, now, []access.AuditEntry{{Action: "node.enroll", Target: "node:" + node.String()}}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if usable, err := postgres.NewAgentCAStore(db).Usable(ctx, cert.Serial, now); err != nil || !usable {
		t.Errorf("recorded certificate usable = %v, %v", usable, err)
	}
	var hostname, arch string
	if err := db.InTenant(ctx, o.scope, func(q *sqlcdb.Queries) error {
		row, err := q.GetNode(ctx, uuid.UUID(node))
		hostname, arch = row.Hostname, row.Architecture
		return err
	}); err != nil || hostname != "db-01" || arch != "arm64" {
		t.Errorf("node = %q/%q, %v", hostname, arch, err)
	}

	// The same machine-id again: conflict, nothing created, the token use is rolled back.
	if err := registerMachine(registry, st, machine); !errors.Is(err, enrollment.ErrMachineIDConflict) {
		t.Errorf("duplicate machine-id: %v, want ErrMachineIDConflict", err)
	}
	list, _ := s.List(ctx, o.scope)
	if len(list) != 1 || list[0].Uses != 1 {
		t.Errorf("token uses = %+v, want 1 (conflict must not use the token)", list)
	}
}

func TestEnrollmentTokenStore_ConcurrentRedemptionsNeverExceedMaxUses(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	s := postgres.NewEnrollmentTokenStore(open(t, f.appDSN))
	o := newTokenOrg(t, f)
	_, hash := issue(t, s, o, o.group, 3)
	st, _, _ := s.Lookup(ctx, hash, time.Now())
	registry := postgres.NewNodeRegistry(open(t, f.appDSN))

	var wg sync.WaitGroup
	errs := make([]error, 10)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = register(registry, st)
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		}
	}
	if ok != 3 {
		t.Errorf("%d redemptions succeeded, want exactly 3", ok)
	}
}

func TestEnrollmentTokenStore_ScopesByGroupAndOrganization(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	s := postgres.NewEnrollmentTokenStore(open(t, f.appDSN))
	a, b := newTokenOrg(t, f), newTokenOrg(t, f)
	inGroup, _ := issue(t, s, a, a.group, 1)
	inOther, _ := issue(t, s, a, a.other, 1)

	onlyGroup, _ := tenant.ForGroups(a.scope.OrgID(), a.group)
	list, err := s.List(ctx, onlyGroup)
	if err != nil || len(list) != 1 || list[0].ID != inGroup.ID || list[0].CreatedBy != a.admin || list[0].MaxUses != 1 {
		t.Errorf("List(group) = %+v, %v; want only the group's token", list, err)
	}
	if err := s.Revoke(ctx, onlyGroup, inOther.ID, entry(a.admin, "x")); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("revoking a token of another group: %v, want ErrNotFound", err)
	}
	if foreign, _ := s.List(ctx, b.scope); len(foreign) != 0 {
		t.Errorf("Org B sees %d tokens of Org A", len(foreign))
	}
	if err := s.Revoke(ctx, b.scope, inGroup.ID, entry(b.admin, "x")); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("foreign revoke: %v, want ErrNotFound", err)
	}
	// A token cannot be bound to a group of another organization.
	foreignGroup, _ := tenant.ForGroups(b.scope.OrgID(), a.group)
	now := time.Now()
	err = s.Create(ctx, foreignGroup, enrollment.Token{ID: enrollment.TokenID(uuid.New()), Group: a.group, CreatedAt: now, ExpiresAt: now.Add(time.Hour), MaxUses: 1},
		sha256.Sum256([]byte(uuid.NewString())), entry(b.admin, "x"))
	if !errors.Is(err, access.ErrNotFound) {
		t.Errorf("token for a foreign group: %v, want ErrNotFound", err)
	}
}
