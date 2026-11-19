//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

func TestAccessStore_LoadsMembershipAndBindingsOfTheOrganizationOnly(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	db := open(t, f.appDSN)
	store := postgres.NewAccessStore(db)
	group := uuid.New()
	ownerExec(t, f.db.OwnerDSN, "DELETE FROM role_bindings WHERE user_id = $1", uuid.UUID(alice)) // repeatable test

	err := db.InTenant(ctx, scopeFor(t, orgA), func(q *sqlcdb.Queries) error {
		if _, err := q.CreateNodeGroup(ctx, sqlcdb.CreateNodeGroupParams{ID: group, Name: "web-" + group.String()[:8]}); err != nil {
			return err
		}
		if err := q.CreateRoleBinding(ctx, sqlcdb.CreateRoleBindingParams{ID: uuid.New(), UserID: uuid.UUID(alice), GroupID: &group, Role: "operator"}); err != nil {
			return err
		}
		return q.CreateRoleBinding(ctx, sqlcdb.CreateRoleBindingParams{ID: uuid.New(), UserID: uuid.UUID(alice), Role: "viewer"})
	})
	if err != nil {
		t.Fatal(err)
	}

	member, bindings, err := store.MembershipAndBindings(ctx, tenant.OrgID(orgA), alice)
	if err != nil || !member {
		t.Fatalf("MembershipAndBindings(orgA) = %v, %v", member, err)
	}
	want := map[authz.Binding]bool{
		{Group: tenant.GroupID(group), Role: role.Operator}: true,
		{AllGroups: true, Role: role.Viewer}:                true,
	}
	for _, b := range bindings {
		delete(want, b)
	}
	if len(want) != 0 {
		t.Errorf("bindings = %+v, missing %+v", bindings, want)
	}

	member, bindings, err = store.MembershipAndBindings(ctx, tenant.OrgID(orgB), alice)
	if err != nil || member || len(bindings) != 0 {
		t.Errorf("alice in Org B = %v, %+v, %v; want no membership", member, bindings, err)
	}
}

func TestRoleBindings_CannotReferenceAGroupOfAnotherOrganization(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	db := open(t, f.appDSN)
	groupB := uuid.New()
	if err := db.InTenant(ctx, scopeFor(t, orgB), func(q *sqlcdb.Queries) error {
		_, err := q.CreateNodeGroup(ctx, sqlcdb.CreateNodeGroupParams{ID: groupB, Name: "b-" + groupB.String()[:8]})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	err := db.InTenant(ctx, scopeFor(t, orgA), func(q *sqlcdb.Queries) error {
		return q.CreateRoleBinding(ctx, sqlcdb.CreateRoleBindingParams{ID: uuid.New(), UserID: uuid.UUID(alice), GroupID: &groupB, Role: "admin"})
	})
	if err == nil {
		t.Error("a role binding in Org A must not reference a group of Org B")
	}
}

func TestNodeGroups_AreIsolatedAndWriteProtected(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	db := open(t, f.appDSN)
	groupA := uuid.New()
	if err := db.InTenant(ctx, scopeFor(t, orgA), func(q *sqlcdb.Queries) error {
		_, err := q.CreateNodeGroup(ctx, sqlcdb.CreateNodeGroupParams{ID: groupA, Name: "a-" + groupA.String()[:8]})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	err := db.InTenant(ctx, scopeFor(t, orgB), func(q *sqlcdb.Queries) error {
		groups, err := q.ListNodeGroups(ctx)
		if err != nil {
			return err
		}
		for _, g := range groups {
			if g.ID == groupA {
				t.Error("Org B sees a group of Org A")
			}
		}
		if n, err := q.RenameNodeGroup(ctx, sqlcdb.RenameNodeGroupParams{ID: groupA, Name: "pwned"}); err != nil || n != 0 {
			t.Errorf("rename from Org B = %d, %v; want 0 rows", n, err)
		}
		if n, err := q.DeleteNodeGroup(ctx, groupA); err != nil || n != 0 {
			t.Errorf("delete from Org B = %d, %v; want 0 rows", n, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Inserting a row for another organization is rejected by WITH CHECK.
	conn, err := pgx.Connect(ctx, f.appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, "SELECT set_config('sermon.org_id', $1, false)", orgB.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "INSERT INTO node_groups (id, org_id, name) VALUES ($1, $2, 'x')", uuid.New(), orgA); err == nil {
		t.Error("Org B inserted a group into Org A")
	}
}

func TestAuditLog_ChainVerifiesAndDetectsTampering(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	store := postgres.NewAccessStore(open(t, f.appDSN))
	actor := alice
	for i, action := range []string{"group.create", "member.add", "role.grant"} {
		if err := store.AppendAudit(ctx, scopeFor(t, orgA), &actor, action, "target-"+string(rune('a'+i)), map[string]any{"n": i}); err != nil {
			t.Fatalf("AppendAudit() error = %v", err)
		}
	}
	if err := store.VerifyAuditChain(ctx, scopeFor(t, orgA)); err != nil {
		t.Fatalf("VerifyAuditChain() error = %v", err)
	}

	// A database administrator edits history directly.
	ownerExec(t, f.db.OwnerDSN, `UPDATE audit_log SET details = '{"n": 99}' WHERE id = (SELECT min(id) FROM audit_log WHERE org_id = $1 AND action = 'member.add')`, orgA)
	if err := store.VerifyAuditChain(ctx, scopeFor(t, orgA)); !errors.Is(err, postgres.ErrAuditChainBroken) {
		t.Errorf("after tampering: VerifyAuditChain() error = %v, want ErrAuditChainBroken", err)
	}
	ownerExec(t, f.db.OwnerDSN, `UPDATE audit_log SET details = '{"n": 1}' WHERE org_id = $1 AND action = 'member.add' AND details = '{"n": 99}'`, orgA)
}

func TestAuditLog_IsAppendOnlyForTheApplication(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, f.appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, "SELECT set_config('sermon.org_id', $1, false)", orgA.String()); err != nil {
		t.Fatal(err)
	}

	for _, sql := range []string{"UPDATE audit_log SET action = 'x'", "DELETE FROM audit_log", "TRUNCATE audit_log"} {
		if _, err := conn.Exec(ctx, sql); err == nil {
			t.Errorf("%q must be denied for sermon_app", sql)
		}
	}
}

func TestAuditLog_ConcurrentAppendsKeepASingleChain(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	store := postgres.NewAccessStore(open(t, f.appDSN))
	org := uuid.New()
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO organizations (id, name) VALUES ($1, 'Concurrent')", org)
	scope := scopeFor(t, org)

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- store.AppendAudit(ctx, scope, nil, "concurrent", "t", map[string]any{"i": i})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := store.VerifyAuditChain(ctx, scope); err != nil {
		t.Errorf("chain after concurrent appends: %v", err)
	}
}
