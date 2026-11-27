//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/account"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
)

// The accounts of these tests are "old" by an orphaned_since in 2000: the purge with a cutoff in
// 2000 reaches only them, never the accounts of other tests (orphaned since their creation).
var (
	longAgo = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	cutoff  = time.Date(2000, 6, 1, 0, 0, 0, 0, time.UTC)
)

func newAccountOrg(t *testing.T, f fixture) uuid.UUID {
	t.Helper()
	org := uuid.New()
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO organizations (id, name) VALUES ($1, 'Accounts ' || $1::uuid::text)", org)
	return org
}

func newAccount(t *testing.T, f fixture, orgs ...uuid.UUID) uuid.UUID {
	t.Helper()
	u := uuid.New()
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO users (id, email, display_name) VALUES ($1, $2, 'Account')", u, u.String()+"@example.org")
	for _, org := range orgs {
		ownerExec(t, f.db.OwnerDSN, "INSERT INTO memberships (org_id, user_id) VALUES ($1, $2)", org, u)
	}
	return u
}

func orphanedSince(t *testing.T, f fixture, u uuid.UUID) *time.Time {
	t.Helper()
	var at *time.Time
	ownerQueryRow(t, f.db.OwnerDSN, "SELECT orphaned_since FROM users WHERE id = $1", []any{u}, &at)
	return at
}

func countRows(t *testing.T, f fixture, sql string, args ...any) int {
	t.Helper()
	var n int
	ownerQueryRow(t, f.db.OwnerDSN, sql, args, &n)
	return n
}

// The trigger on memberships keeps orphaned_since: set when the last membership ends, cleared
// with a new one.
func TestAccountStore_MembershipsMarkOrphanedAccounts(t *testing.T) {
	f := setup(t)
	orgA, orgB := newAccountOrg(t, f), newAccountOrg(t, f)
	u := newAccount(t, f, orgA, orgB)
	if orphanedSince(t, f, u) != nil {
		t.Fatal("member counted as orphaned")
	}
	ownerExec(t, f.db.OwnerDSN, "DELETE FROM memberships WHERE org_id = $1 AND user_id = $2", orgA, u)
	if orphanedSince(t, f, u) != nil {
		t.Fatal("orphaned while still a member of another organization")
	}
	ownerExec(t, f.db.OwnerDSN, "DELETE FROM memberships WHERE org_id = $1 AND user_id = $2", orgB, u)
	if orphanedSince(t, f, u) == nil {
		t.Fatal("not orphaned after the last membership")
	}
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO memberships (org_id, user_id) VALUES ($1, $2)", orgA, u)
	if orphanedSince(t, f, u) != nil {
		t.Fatal("still orphaned after joining again")
	}
	if fresh := newAccount(t, f); orphanedSince(t, f, fresh) == nil {
		t.Error("an account created without membership is not orphaned")
	}
}

func TestAccountStore_PurgesAccountsWithoutMembershipAfterTheGrace(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	store := postgres.NewAccountStore(open(t, f.appDSN))
	org := newAccountOrg(t, f)
	leaver, stayer, super, recent := newAccount(t, f, org), newAccount(t, f, org), newAccount(t, f), newAccount(t, f)
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO superadmins (user_id) VALUES ($1)", super)
	// Everything an account has: password, session, second factors, passkey, identity, e-mail change.
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO user_credentials (user_id, password_hash) VALUES ($1, '$argon2id$v=19$m=8,t=1,p=1$x$y')", leaver)
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO sessions (id, user_id, org_id, created_at, last_seen_at, step_up_at) VALUES ($1, $2, $3, now(), now(), now())",
		strings.Repeat("a", 32)+strings.ReplaceAll(leaver.String(), "-", ""), leaver, org)
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO user_totp (user_id, secret_sealed, confirmed_at) VALUES ($1, '\\x00', now())", leaver)
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO user_recovery_codes (user_id, code_hash) VALUES ($1, decode(repeat('ab', 32), 'hex'))", leaver)
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO webauthn_credentials (id, user_id, name, credential, created_at) VALUES ($1, $2, 'Key', '{}', now())", []byte(leaver.String()), leaver)
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO user_identities (issuer, subject, user_id) VALUES ('https://idp.example', $1, $2)", leaver.String(), leaver)
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO email_changes (user_id, new_email, code_hash, expires_at) VALUES ($1, 'new@example.org', decode(repeat('cd', 32), 'hex'), now())", leaver)
	scope := scopeFor(t, org)
	if err := postgres.NewAccessStore(open(t, f.appDSN)).AppendAudit(ctx, scope, ptrUser(leaver), "group.create", "group:x", nil); err != nil {
		t.Fatal(err)
	}
	ownerExec(t, f.db.OwnerDSN, "DELETE FROM memberships WHERE user_id = $1", leaver)
	ownerExec(t, f.db.OwnerDSN, "UPDATE users SET orphaned_since = $1 WHERE id = ANY ($2)", longAgo, []uuid.UUID{leaver, super})

	// Not before the grace.
	if n, err := store.PurgeAccounts(ctx, longAgo.Add(-time.Hour), 500); err != nil || n != 0 {
		t.Fatalf("before the grace: %d, %v", n, err)
	}
	if n, err := store.PurgeAccounts(ctx, cutoff, 500); err != nil || n != 1 {
		t.Fatalf("after the grace: %d, %v", n, err)
	}
	for _, u := range []uuid.UUID{stayer, super, recent} {
		if countRows(t, f, "SELECT count(*) FROM users WHERE id = $1", u) != 1 {
			t.Errorf("%s deleted", u)
		}
	}
	for _, table := range []string{
		"users WHERE id", "user_credentials WHERE user_id", "sessions WHERE user_id", "user_totp WHERE user_id",
		"user_recovery_codes WHERE user_id", "webauthn_credentials WHERE user_id", "user_identities WHERE user_id", "email_changes WHERE user_id",
	} {
		if n := countRows(t, f, "SELECT count(*) FROM "+table+" = $1", leaver); n != 0 {
			t.Errorf("%s: %d rows left", table, n)
		}
	}
	// The audit entry keeps only the opaque ID; the instance's log names the deletion.
	if n := countRows(t, f, "SELECT count(*) FROM audit_log WHERE org_id = $1 AND actor_id = $2", org, leaver); n != 1 {
		t.Errorf("audit entries of the account: %d", n)
	}
	if n := countRows(t, f, "SELECT count(*) FROM instance_audit_log WHERE action = 'account.purge' AND target = $1 AND actor_id IS NULL", "user:"+leaver.String()); n != 1 {
		t.Errorf("instance audit: %d", n)
	}
	if err := postgres.NewAccessStore(open(t, f.appDSN)).VerifyAuditChain(ctx, scope); err != nil {
		t.Error(err)
	}
}

func ptrUser(u uuid.UUID) *identity.UserID {
	id := identity.UserID(u)
	return &id
}

func TestAccountStore_DeleteOnRequest(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	db := open(t, f.appDSN)
	store := postgres.NewAccountStore(db)
	orgA, orgB := newAccountOrg(t, f), newAccountOrg(t, f)
	admin, member, super, sam := newAccount(t, f, orgA), newAccount(t, f, orgA, orgB), newAccount(t, f), newAccount(t, f)
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO superadmins (user_id) VALUES ($1), ($2)", super, sam)
	ownerExec(t, f.db.OwnerDSN, "SELECT set_config('sermon.org_id', $1, false)", orgA.String())
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO role_bindings (id, org_id, user_id, role) VALUES ($1, $2, $3, 'admin')", uuid.New(), orgA, admin)
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO sessions (id, user_id, created_at, last_seen_at, step_up_at) VALUES ($1, $2, now(), now(), now())",
		strings.Repeat("b", 32)+strings.ReplaceAll(member.String(), "-", ""), member)
	audit := func(u uuid.UUID) access.AuditEntry {
		return access.AuditEntry{Actor: identity.UserID(sam), Action: "account.delete", Target: "user:" + u.String(), Details: map[string]string{"reason": "request"}}
	}
	if _, err := store.Delete(ctx, identity.UserID(admin), audit(admin)); !errors.Is(err, access.ErrLastAdmin) {
		t.Errorf("last admin: %v", err)
	}
	if _, err := store.Delete(ctx, identity.UserID(super), audit(super)); !errors.Is(err, account.ErrSuperadmin) {
		t.Errorf("superadmin: %v", err)
	}
	if _, err := store.Delete(ctx, identity.UserID(uuid.New()), audit(uuid.New())); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	n, err := store.Delete(ctx, identity.UserID(member), audit(member))
	if err != nil || n != 2 {
		t.Fatalf("delete: %d, %v", n, err)
	}
	if countRows(t, f, "SELECT count(*) FROM users WHERE id = $1", member)+countRows(t, f, "SELECT count(*) FROM sessions WHERE user_id = $1", member) != 0 {
		t.Error("account or session left")
	}
	for _, org := range []uuid.UUID{orgA, orgB} {
		if n := countRows(t, f, "SELECT count(*) FROM audit_log WHERE org_id = $1 AND action = 'member.account_deleted' AND actor_id = $2 AND target = $3",
			org, sam, "user:"+member.String()); n != 1 {
			t.Errorf("org %s: %d entries", org, n)
		}
		if err := postgres.NewAccessStore(db).VerifyAuditChain(ctx, scopeFor(t, org)); err != nil {
			t.Error(err)
		}
	}
	var details string
	ownerQueryRow(t, f.db.OwnerDSN, "SELECT details::text FROM instance_audit_log WHERE action = 'account.delete' AND target = $1", []any{"user:" + member.String()}, &details)
	if !strings.Contains(details, `"memberships": "2"`) || strings.Contains(details, "@") {
		t.Errorf("instance details = %s", details)
	}
	if countRows(t, f, "SELECT count(*) FROM users WHERE id = $1", admin) != 1 {
		t.Error("the refused deletion went through")
	}
}

func TestAccountStore_ExpiredInvitationsGoWithTheirAddress(t *testing.T) {
	f := setup(t)
	store := postgres.NewAccountStore(open(t, f.appDSN))
	org := newAccountOrg(t, f)
	ownerExec(t, f.db.OwnerDSN, "SELECT set_config('sermon.org_id', $1, false)", org.String())
	for i, expires := range []time.Time{longAgo, time.Now().Add(time.Hour)} {
		ownerExec(t, f.db.OwnerDSN, `INSERT INTO invitations (id, org_id, email, role, token_hash, expires_at)
		      VALUES ($1, $2, $3, 'viewer', decode(repeat($4, 32), 'hex'), $5)`,
			uuid.New(), org, fmt.Sprintf("invited%d-%s@example.org", i, org), fmt.Sprintf("%02x", i+0xa0)+"", expires)
	}
	if n, err := store.PurgeInvitations(context.Background(), cutoff); err != nil || n != 1 {
		t.Fatalf("purged %d, %v", n, err)
	}
	if n := countRows(t, f, "SELECT count(*) FROM invitations WHERE org_id = $1", org); n != 1 {
		t.Errorf("%d invitations left", n)
	}
	if n := countRows(t, f, "SELECT count(*) FROM invitations WHERE org_id = $1 AND email LIKE 'invited0-%'", org); n != 0 {
		t.Error("the expired invitation kept its address")
	}
}

func TestAccountStore_ProfileAndEmailChange(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	store := postgres.NewAccountStore(open(t, f.appDSN))
	u, other := newAccount(t, f), newAccount(t, f)
	user := identity.UserID(u)
	self := access.AuditEntry{Actor: user, Action: "account.rename", Target: "user:" + u.String()}
	if err := store.SetDisplayName(ctx, user, "Alice B.", self); err != nil {
		t.Fatal(err)
	}
	p, found, err := store.Profile(ctx, user)
	if err != nil || !found || p.DisplayName != "Alice B." || p.Email != u.String()+"@example.org" {
		t.Fatalf("profile %+v, %v, %v", p, found, err)
	}
	if err := store.SavePendingChange(ctx, user, account.Pending{NewEmail: "alice@example.net", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if n, err := store.CountAttempt(ctx, user); err != nil || n != 1 {
		t.Errorf("attempt %d, %v", n, err)
	}
	if pending, found, err := store.PendingChange(ctx, user); err != nil || !found || pending.Attempts != 1 || pending.NewEmail != "alice@example.net" {
		t.Errorf("pending %+v, %v, %v", pending, found, err)
	}
	change := access.AuditEntry{Actor: user, Action: "account.email_change", Target: "user:" + u.String()}
	if ok, err := store.SetEmail(ctx, user, other.String()+"@example.org", change); err != nil || ok {
		t.Errorf("taken address: %v, %v", ok, err)
	}
	if ok, err := store.SetEmail(ctx, user, "alice@example.net", change); err != nil || !ok {
		t.Fatalf("set: %v, %v", ok, err)
	}
	if _, found, _ := store.PendingChange(ctx, user); found {
		t.Error("pending change kept")
	}
	if id, found, err := store.FindUser(ctx, "alice@example.net"); err != nil || !found || id != user {
		t.Errorf("find %v %v %v", id, found, err)
	}
	if n := countRows(t, f, "SELECT count(*) FROM instance_audit_log WHERE target = $1 AND action IN ('account.rename', 'account.email_change')", "user:"+u.String()); n != 2 {
		t.Errorf("instance audit: %d", n)
	}
	if err := store.SavePendingChange(ctx, user, account.Pending{NewEmail: "x@example.net", ExpiresAt: longAgo}); err != nil {
		t.Fatal(err)
	}
	if n, err := store.PurgeEmailChanges(ctx, cutoff); err != nil || n != 1 {
		t.Errorf("purged %d, %v", n, err)
	}
}

// Art. 15: the export holds the account's own data across its organizations, nothing of other
// members, no secrets.
func TestAccountStore_ExportHoldsOnlyTheOwnData(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	db := open(t, f.appDSN)
	store := postgres.NewAccountStore(db)
	orgA, orgB := newAccountOrg(t, f), newAccountOrg(t, f)
	me, mate := newAccount(t, f, orgA, orgB), newAccount(t, f, orgA)
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO user_credentials (user_id, password_hash) VALUES ($1, '$argon2id$v=19$m=8,t=1,p=1$c2FsdA$aGFzaA')", me)
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO user_totp (user_id, secret_sealed, confirmed_at) VALUES ($1, '\\xdeadbeef', now())", me)
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO webauthn_credentials (id, user_id, name, credential, created_at) VALUES ($1, $2, 'Laptop', '{\"publicKey\": \"secret-key\"}', now())", []byte(me.String()), me)
	accessStore := postgres.NewAccessStore(db)
	for _, org := range []uuid.UUID{orgA, orgB} {
		ownerExec(t, f.db.OwnerDSN, "SELECT set_config('sermon.org_id', $1, false)", org.String())
		ownerExec(t, f.db.OwnerDSN, "INSERT INTO role_bindings (id, org_id, user_id, role) VALUES ($1, $2, $3, 'operator')", uuid.New(), org, me)
		ownerExec(t, f.db.OwnerDSN, `INSERT INTO dashboards (org_id, id, owner_id, title, spec, created_at, updated_at)
		      VALUES ($1, $2, $3, 'Mine', '{}', now(), now())`, org, uuid.New(), me)
		if err := accessStore.AppendAudit(ctx, scopeFor(t, org), ptrUser(me), "dashboard.create", "dashboard:x", map[string]any{"email": mate.String() + "@example.org"}); err != nil {
			t.Fatal(err)
		}
	}
	ownerExec(t, f.db.OwnerDSN, "SELECT set_config('sermon.org_id', $1, false)", orgA.String())
	ownerExec(t, f.db.OwnerDSN, `INSERT INTO dashboards (org_id, id, owner_id, title, spec, created_at, updated_at)
	      VALUES ($1, $2, $3, 'Theirs', '{}', now(), now())`, orgA, uuid.New(), mate)
	if err := accessStore.AppendAudit(ctx, scopeFor(t, orgA), ptrUser(mate), "member.invite", "invitation:x", nil); err != nil {
		t.Fatal(err)
	}

	out, err := store.Export(ctx, identity.UserID(me), exportEntry(me))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Memberships) != 2 || len(out.Dashboards) != 2 || len(out.Audit) != 2 || len(out.Passkeys) != 1 || !out.TwoFactor.TOTP || out.Password == nil {
		t.Fatalf("export = %+v", out)
	}
	for _, m := range out.Memberships {
		if len(m.Roles) != 1 || m.Roles[0].Role != "operator" || m.Roles[0].Group != "*" {
			t.Errorf("roles = %+v", m.Roles)
		}
	}
	raw, _ := json.Marshal(out)
	for _, foreign := range []string{mate.String(), "Theirs", "member.invite", "argon2id", "deadbeef", "secret-key"} {
		if strings.Contains(string(raw), foreign) {
			t.Errorf("export contains %q: %s", foreign, raw)
		}
	}
	if n := countRows(t, f, "SELECT count(*) FROM instance_audit_log WHERE action = 'account.export' AND target = $1", "user:"+me.String()); n != 1 {
		t.Errorf("export not recorded: %d", n)
	}
}

func exportEntry(u uuid.UUID) access.AuditEntry {
	return access.AuditEntry{Actor: identity.UserID(u), Action: "account.export", Target: "user:" + u.String()}
}

// ageAudit moves the first n entries of the organization's log to 2000 and rebuilds the chain,
// as if they were written then.
func ageAudit(t *testing.T, f fixture, table, where string, n int) {
	t.Helper()
	fields := "r.org_id::text, r.seq::text, "
	if table == "instance_audit_log" {
		fields = "r.seq::text, "
	}
	ownerExec(t, f.db.OwnerDSN, fmt.Sprintf(`DO $$
DECLARE
    r      record;
    prev   bytea := (SELECT a.prev_hash FROM %[1]s a WHERE %[2]s ORDER BY a.seq LIMIT 1);
    new_at timestamptz;
BEGIN
    FOR r IN SELECT * FROM %[1]s a WHERE %[2]s ORDER BY seq LOOP
        new_at := CASE WHEN r.seq <= (SELECT min(seq) FROM %[1]s a WHERE %[2]s) + %[3]d - 1
                   THEN timestamptz '2000-01-01 00:00:00+00' + r.seq * interval '1 second' ELSE r.at END;
        UPDATE %[1]s u SET at = new_at, prev_hash = prev,
            hash = sha256(prev || convert_to(concat_ws(E'\x1f', %[4]s
                to_char(new_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
                COALESCE(r.actor_id::text, ''), r.action, r.target, r.details::text), 'UTF8'))
        WHERE u.id = r.id RETURNING u.hash INTO prev;
    END LOOP;
END $$`, table, where, n, fields))
}

func TestAccountStore_AuditRetentionKeepsTheChainVerifiable(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	db := open(t, f.appDSN)
	store, accessStore, instance := postgres.NewAccountStore(db), postgres.NewAccessStore(db), postgres.NewInstanceAuditStore(db)
	org := newAccountOrg(t, f)
	scope := scopeFor(t, org)
	for i := range 5 {
		if err := accessStore.AppendAudit(ctx, scope, nil, fmt.Sprintf("test.step%d", i+1), "x", nil); err != nil {
			t.Fatal(err)
		}
	}
	// A fresh instance log of four entries (agentca tests leave it the same way).
	ownerExec(t, f.db.OwnerDSN, "DELETE FROM instance_audit_log")
	u := identity.UserID(newAccount(t, f))
	for i := range 4 {
		if err := store.SetDisplayName(ctx, u, fmt.Sprintf("Name %d", i), access.AuditEntry{Actor: u, Action: "account.rename", Target: "user:x"}); err != nil {
			t.Fatal(err)
		}
	}
	where := fmt.Sprintf("a.org_id = '%s'", org)
	ageAudit(t, f, "audit_log", where, 3)
	ageAudit(t, f, "instance_audit_log", "true", 2)
	if err := accessStore.VerifyAuditChain(ctx, scope); err != nil {
		t.Fatalf("aged chain: %v", err)
	}

	n, err := store.PruneAudit(ctx, cutoff)
	if err != nil || n != 5 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	if err := accessStore.VerifyAuditChain(ctx, scope); err != nil {
		t.Errorf("org chain after the prune: %v", err)
	}
	if err := instance.Verify(ctx); err != nil {
		t.Errorf("instance chain after the prune: %v", err)
	}
	records, err := accessStore.ListAudit(ctx, scope, 0, 100)
	if err != nil || len(records) != 3 || records[0].Action != postgres.PruneAction || records[0].Details["through_seq"] != "3" || records[2].Action != "test.step4" {
		t.Fatalf("records = %+v, %v", records, err)
	}
	// Nothing older left: a second run drops nothing and adds no entry.
	if n, err := store.PruneAudit(ctx, cutoff); err != nil || n != 0 {
		t.Errorf("second run: %d, %v", n, err)
	}
	// The newest entry stays even if it is old: the chain goes on from it.
	ageAudit(t, f, "audit_log", where, 100)
	if n, err := store.PruneAudit(ctx, cutoff); err != nil || n != 2 {
		t.Errorf("all old: %d, %v", n, err)
	}
	if err := accessStore.VerifyAuditChain(ctx, scope); err != nil {
		t.Errorf("after pruning all but one: %v", err)
	}
	// A start cut by hand has no audit.prune entry: the chain is broken.
	ownerExec(t, f.db.OwnerDSN, "DELETE FROM audit_log WHERE org_id = $1 AND seq = (SELECT min(seq) FROM audit_log WHERE org_id = $1)", org)
	if err := accessStore.VerifyAuditChain(ctx, scope); !errors.Is(err, postgres.ErrAuditChainBroken) {
		t.Errorf("cut by hand: %v", err)
	}
	ownerExec(t, f.db.OwnerDSN, "DELETE FROM instance_audit_log") // leave a valid chain for other tests
}
