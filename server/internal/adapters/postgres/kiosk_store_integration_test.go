//go:build integration

package postgres_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/dashboards"
	"github.com/pmoscode/SerMon/server/internal/app/kiosk"
	"github.com/pmoscode/SerMon/server/internal/domain/dashboard"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

type kioskEnv struct {
	dashboardEnv
	kiosk *postgres.KioskStore
}

func newKioskEnv(t *testing.T) kioskEnv {
	t.Helper()
	e := newDashboardEnv(t)
	return kioskEnv{dashboardEnv: e, kiosk: postgres.NewKioskStore(open(t, e.f.appDSN))}
}

// sharedBoard is a shared dashboard of the organization with an alert list.
func (e kioskEnv) sharedBoard(t *testing.T, o mgmtOrg) dashboards.Dashboard {
	t.Helper()
	ctx := context.Background()
	c := member(o, o.admin, allAdmin)
	d, err := e.svc.Create(ctx, c, dashboard.Spec{Version: 1, Title: "Hall", Widgets: []dashboard.Widget{{
		ID: "alerts", Kind: dashboard.KindAlertList, Title: "Alerts", Position: dashboard.Position{W: 4, H: 3},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if d, err = e.svc.SetShared(ctx, c, d.ID, true); err != nil {
		t.Fatal(err)
	}
	return d
}

func (e kioskEnv) issue(t *testing.T, scope tenant.Scope, d dashboards.Dashboard, expires time.Time) ([sha256.Size]byte, kiosk.Token) {
	t.Helper()
	hash := sha256.Sum256([]byte(uuid.NewString()))
	tok := kiosk.Token{ID: kiosk.TokenID(uuid.New()), Dashboard: d.ID, Name: "Hall", CreatedBy: d.Owner, CreatedAt: time.Now().Add(-time.Minute), ExpiresAt: expires}
	if err := e.kiosk.Create(context.Background(), scope, tok, hash, access.AuditEntry{Action: "kiosk_token.create", Target: "kiosk_token:" + tok.ID.String()}); err != nil {
		t.Fatal(err)
	}
	return hash, tok
}

func TestKioskStore_LookupFindsOnlyUsableTokens(t *testing.T) {
	e := newKioskEnv(t)
	ctx := context.Background()
	o := newMgmtOrg(t, e.f)
	d := e.sharedBoard(t, o)
	hash, tok := e.issue(t, o.scope, d, time.Now().Add(time.Hour))

	found, ok, err := e.kiosk.Lookup(ctx, hash, time.Now())
	if err != nil || !ok || found.Token != tok.ID || found.Org != o.scope.OrgID() || found.Dashboard != d.ID || found.Creator != o.admin || found.Hash != hash {
		t.Fatalf("Lookup() = %+v, %v, %v", found, ok, err)
	}
	if _, ok, _ := e.kiosk.Lookup(ctx, sha256.Sum256([]byte("unknown")), time.Now()); ok {
		t.Error("an unknown hash must find nothing")
	}
	if _, ok, _ := e.kiosk.Lookup(ctx, hash, time.Now().Add(2*time.Hour)); ok {
		t.Error("an expired token must find nothing")
	}
	ownerExec(t, e.f.db.OwnerDSN, "UPDATE dashboards SET shared = false WHERE id = $1", uuid.UUID(d.ID))
	if _, ok, _ := e.kiosk.Lookup(ctx, hash, time.Now()); ok {
		t.Error("a token of a private dashboard must find nothing")
	}
	ownerExec(t, e.f.db.OwnerDSN, "UPDATE dashboards SET shared = true WHERE id = $1", uuid.UUID(d.ID))
	if err := e.kiosk.Touch(ctx, o.scope, tok.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	list, err := e.kiosk.List(ctx, o.scope, d.ID)
	if err != nil || len(list) != 1 || list[0].LastUsedAt == nil || list[0].CreatedByName != "User" {
		t.Errorf("List() = %+v, %v", list, err)
	}

	if err := e.kiosk.Revoke(ctx, o.scope, tok.ID, time.Now(), access.AuditEntry{Action: "kiosk_token.revoke", Target: "kiosk_token:" + tok.ID.String()}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := e.kiosk.Lookup(ctx, hash, time.Now()); ok {
		t.Error("a revoked token must find nothing")
	}
	other := newMgmtOrg(t, e.f)
	if err := e.kiosk.Revoke(ctx, other.scope, tok.ID, time.Now(), access.AuditEntry{Action: "x"}); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("revoke from another organization: %v", err)
	}
}

func TestKioskStore_UnsharingLockingAndDeletingEndTokens(t *testing.T) {
	e := newKioskEnv(t)
	ctx := context.Background()
	o := newMgmtOrg(t, e.f)
	c := member(o, o.admin, allAdmin)

	d := e.sharedBoard(t, o)
	hash, _ := e.issue(t, o.scope, d, time.Now().Add(time.Hour))
	if _, err := e.svc.SetShared(ctx, c, d.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetShared(ctx, c, d.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := e.kiosk.Lookup(ctx, hash, time.Now()); ok {
		t.Error("sharing again must not revive the tokens of before")
	}
	list, _ := e.kiosk.List(ctx, o.scope, d.ID)
	if len(list) != 1 || list[0].RevokedAt == nil {
		t.Errorf("tokens after unsharing = %+v", list)
	}

	locked, _ := e.issue(t, o.scope, d, time.Now().Add(time.Hour))
	ownerExec(t, e.f.db.OwnerDSN, "UPDATE organizations SET locked_at = now() WHERE id = $1", uuid.UUID(o.scope.OrgID()))
	if _, ok, _ := e.kiosk.Lookup(ctx, locked, time.Now()); ok {
		t.Error("a locked organization's token must find nothing")
	}
	ownerExec(t, e.f.db.OwnerDSN, "UPDATE organizations SET locked_at = NULL WHERE id = $1", uuid.UUID(o.scope.OrgID()))
	if _, ok, _ := e.kiosk.Lookup(ctx, locked, time.Now()); !ok {
		t.Fatal("the token works again once the organization is unlocked")
	}
	if err := e.svc.Delete(ctx, c, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := e.kiosk.Lookup(ctx, locked, time.Now()); ok {
		t.Error("deleting the dashboard must delete its tokens")
	}
}

func TestKioskStore_RejectsPrivateDashboardsAndTheEleventhToken(t *testing.T) {
	e := newKioskEnv(t)
	ctx := context.Background()
	o := newMgmtOrg(t, e.f)
	d := e.sharedBoard(t, o)
	for range kiosk.MaxOpen {
		e.issue(t, o.scope, d, time.Now().Add(time.Hour))
	}
	tok := kiosk.Token{ID: kiosk.TokenID(uuid.New()), Dashboard: d.ID, Name: "x", CreatedBy: o.admin, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	if err := e.kiosk.Create(ctx, o.scope, tok, sha256.Sum256([]byte("x")), access.AuditEntry{Action: "x"}); !errors.Is(err, kiosk.ErrTooMany) {
		t.Errorf("eleventh token: %v", err)
	}
	private, err := e.svc.Create(ctx, member(o, o.admin, allAdmin), dashboard.Spec{Version: 1, Title: "Private"})
	if err != nil {
		t.Fatal(err)
	}
	tok.Dashboard = private.ID
	if err := e.kiosk.Create(ctx, o.scope, tok, sha256.Sum256([]byte("y")), access.AuditEntry{Action: "x"}); !errors.Is(err, kiosk.ErrNotShared) {
		t.Errorf("private dashboard: %v", err)
	}
	other := newMgmtOrg(t, e.f)
	tok.Dashboard = d.ID
	if err := e.kiosk.Create(ctx, other.scope, tok, sha256.Sum256([]byte("z")), access.AuditEntry{Action: "x"}); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("dashboard of another organization: %v", err)
	}
}

func TestKioskStore_TenantIsolation(t *testing.T) {
	e := newKioskEnv(t)
	ctx := context.Background()
	o := newMgmtOrg(t, e.f)
	d := e.sharedBoard(t, o)
	e.issue(t, o.scope, d, time.Now().Add(time.Hour))
	other := newMgmtOrg(t, e.f)
	if list, err := e.kiosk.List(ctx, other.scope, d.ID); err != nil || len(list) != 0 {
		t.Errorf("List from another organization = %v, %v", list, err)
	}
	var enabled, forced bool
	queryOwner(t, e.f.db.OwnerDSN, "SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE relname = 'kiosk_tokens'", nil, &enabled, &forced)
	if !enabled || !forced {
		t.Errorf("kiosk_tokens: RLS enabled %v, forced %v", enabled, forced)
	}
	conn, err := pgx.Connect(ctx, e.f.appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var n int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM kiosk_tokens").Scan(&n); err != nil || n != 0 {
		t.Errorf("without tenant: %d rows, %v", n, err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT set_config('sermon.org_id', $1, true)", other.scope.OrgID().String()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO kiosk_tokens (org_id, id, dashboard_id, name, token_hash, created_by, created_at, expires_at)
		VALUES ($1, $2, $3, 'x', $4, $5, now(), now() + interval '1 day')`,
		uuid.UUID(o.scope.OrgID()), uuid.New(), uuid.UUID(d.ID), make([]byte, 32), uuid.UUID(o.admin)); err == nil {
		t.Error("a token for another organization must be rejected")
	}
}

// M5-33: a creator who is no longer admin of all groups has their tokens revoked when the right
// goes – lowered, taken or with the membership – and getting it back revives none of them. A
// change that ends with the admin role again (in one transaction) revokes nothing; the tokens of
// other admins stay.
func TestKioskStore_TokensEndWithTheCreatorsAdminRole(t *testing.T) {
	e := newKioskEnv(t)
	ctx := context.Background()
	o := newMgmtOrg(t, e.f)
	rights := postgres.NewAccessStore(open(t, e.f.appDSN))
	d := e.sharedBoard(t, o)
	usable := func(hash [sha256.Size]byte) bool {
		t.Helper()
		_, ok, err := e.kiosk.Lookup(ctx, hash, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	// A second admin, so that the first may lose the role (the last admin can't).
	otherBinding, err := rights.GrantRole(ctx, o.scope, grant(o.other, role.Admin), entry(o.admin, "role.grant"))
	if err != nil {
		t.Fatal(err)
	}
	mine, _ := e.issue(t, o.scope, d, time.Now().Add(time.Hour))
	theirs := e.issueBy(t, o.scope, d, o.other)

	// Delete and insert the admin role in one transaction: nothing revoked at the commit.
	conn, err := pgx.Connect(ctx, e.f.appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"SELECT set_config('sermon.org_id', $1, true)",
		"DELETE FROM role_bindings WHERE org_id = $1::uuid AND user_id = $2 AND group_id IS NULL",
		"INSERT INTO role_bindings (id, org_id, user_id, role) VALUES (gen_random_uuid(), $1::uuid, $2, 'admin')",
	} {
		args := []any{o.scope.OrgID().String()}
		if strings.Contains(q, "$2") {
			args = append(args, uuid.UUID(o.admin))
		}
		if _, err := tx.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if !usable(mine) {
		t.Fatal("still admin of all groups after the transaction: the token must stay")
	}

	// Lowered to operator: revoked; admin again: still revoked.
	if _, err := rights.GrantRole(ctx, o.scope, grant(o.admin, role.Operator), entry(o.other, "role.grant")); err != nil {
		t.Fatal(err)
	}
	if usable(mine) {
		t.Error("the token of a creator lowered to operator must be revoked")
	}
	if _, err := rights.GrantRole(ctx, o.scope, grant(o.admin, role.Admin), entry(o.other, "role.grant")); err != nil {
		t.Fatal(err)
	}
	if usable(mine) {
		t.Error("getting the role back must not revive the token")
	}
	if !usable(theirs) {
		t.Error("the other admin's token must stay")
	}
	list, err := e.kiosk.List(ctx, o.scope, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range list {
		if tok.CreatedBy == o.admin && tok.RevokedAt == nil {
			t.Errorf("the list shows %v as open", tok.ID)
		}
	}

	// The role taken, and the membership ended (its bindings go by cascade).
	if err := rights.RevokeRole(ctx, o.scope, otherBinding, entry(o.admin, "role.revoke")); err != nil {
		t.Fatal(err)
	}
	if usable(theirs) {
		t.Error("the token of a creator whose admin role was taken must be revoked")
	}
	if _, err := rights.GrantRole(ctx, o.scope, grant(o.other, role.Admin), entry(o.admin, "role.grant")); err != nil {
		t.Fatal(err)
	}
	again := e.issueBy(t, o.scope, d, o.admin)
	if err := rights.RemoveMember(ctx, o.scope, o.admin, entry(o.other, "member.remove")); err != nil {
		t.Fatal(err)
	}
	if usable(again) {
		t.Error("the token of a creator who left must be revoked")
	}
}

func grant(user identity.UserID, r role.Role) access.Grant {
	return access.Grant{ID: access.BindingID(uuid.New()), User: user, Role: r}
}

func (e kioskEnv) issueBy(t *testing.T, scope tenant.Scope, d dashboards.Dashboard, creator identity.UserID) [sha256.Size]byte {
	t.Helper()
	d.Owner = creator
	hash, _ := e.issue(t, scope, d, time.Now().Add(time.Hour))
	return hash
}

// M5-33: a member who leaves takes their private dashboards along; the shared ones stay.
func TestDashboards_PrivateOnesLeaveWithTheMember(t *testing.T) {
	e := newKioskEnv(t)
	ctx := context.Background()
	o := newMgmtOrg(t, e.f)
	c := member(o, o.other, authz.Binding{AllGroups: true, Role: role.Viewer})
	private, err := e.svc.Create(ctx, c, dashboard.Spec{Version: 1, Title: "Mine"})
	if err != nil {
		t.Fatal(err)
	}
	shared, err := e.svc.Create(ctx, c, dashboard.Spec{Version: 1, Title: "Ours"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetShared(ctx, c, shared.ID, true); err != nil {
		t.Fatal(err)
	}
	adminsOwn, err := e.svc.Create(ctx, member(o, o.admin, allAdmin), dashboard.Spec{Version: 1, Title: "Admin's"})
	if err != nil {
		t.Fatal(err)
	}
	if err := postgres.NewAccessStore(open(t, e.f.appDSN)).RemoveMember(ctx, o.scope, o.other, entry(o.admin, "member.remove")); err != nil {
		t.Fatal(err)
	}
	var left []uuid.UUID
	rows := func() {
		left = nil
		conn, err := pgx.Connect(ctx, e.f.appDSN)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(ctx)
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, "SELECT set_config('sermon.org_id', $1, true)", o.scope.OrgID().String()); err != nil {
			t.Fatal(err)
		}
		r, err := tx.Query(ctx, "SELECT id FROM dashboards ORDER BY title")
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		for r.Next() {
			var id uuid.UUID
			if err := r.Scan(&id); err != nil {
				t.Fatal(err)
			}
			left = append(left, id)
		}
	}
	rows()
	if len(left) != 2 || left[0] != uuid.UUID(adminsOwn.ID) || left[1] != uuid.UUID(shared.ID) {
		t.Errorf("dashboards after the member left = %v; want the admin's and the shared one, not %v", left, private.ID)
	}
}
