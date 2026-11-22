//go:build integration

package postgres_test

import (
	"context"
	"crypto/sha256"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/pgtest"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// purgeOrg is an organization with rows in the tables the purge has to reach: groups, a node with
// a series, raw and aggregated metrics, a security event, an invitation, an audit entry and three
// members – one only here, one also elsewhere, one superadmin.
type purgeOrg struct {
	org, node, onlyHere, alsoElsewhere, super uuid.UUID
}

func seedPurgeOrg(t *testing.T, ownerDSN string, other uuid.UUID) purgeOrg {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	p := purgeOrg{org: uuid.New(), node: uuid.New(), onlyHere: uuid.New(), alsoElsewhere: uuid.New(), super: uuid.New()}
	group := uuid.New()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec("SELECT set_config('sermon.org_id', $1, false)", p.org.String())
	exec("INSERT INTO organizations (id, name) VALUES ($1, 'Purge ' || $1::uuid::text)", p.org)
	exec("INSERT INTO node_groups (id, org_id, name) VALUES ($1, $2, 'web')", group, p.org)
	exec(`INSERT INTO nodes (id, org_id, group_id, hostname, machine_id, os_id, os_version, kernel_release, architecture, cpu_count, memory_bytes)
	      VALUES ($1, $2, $3, 'host', $4, 'debian', '13', '6.12.0', 'amd64', 2, 1073741824)`, p.node, p.org, group, strings.ReplaceAll(p.node.String(), "-", ""))
	exec("INSERT INTO series (org_id, node_id, metric) VALUES ($1, $2, 'sermon_purge_test')", p.org, p.node)
	exec(`INSERT INTO metrics_raw (ts, series_id, avg, min, max)
	      SELECT now() - interval '5 minutes', id, 1, 1, 1 FROM series WHERE org_id = $1`, p.org)
	exec(`INSERT INTO security_events (org_id, node_id, occurred_at, kind, user_name) VALUES ($1, $2, now(), 'ssh.login.failed', 'root')`, p.org, p.node)
	hash := sha256.Sum256([]byte(p.org.String()))
	exec(`INSERT INTO invitations (id, org_id, email, role, token_hash, expires_at)
	      VALUES ($1, $2, $3, 'viewer', $4, now() + interval '1 day')`, uuid.New(), p.org, uuid.NewString()+"@example.org", hash[:])
	for _, u := range []uuid.UUID{p.onlyHere, p.alsoElsewhere, p.super} {
		exec("INSERT INTO users (id, email, display_name) VALUES ($1, $2, 'Purge')", u, u.String()+"@example.org")
		exec("INSERT INTO memberships (org_id, user_id) VALUES ($1, $2)", p.org, u)
	}
	if other != uuid.Nil {
		exec("INSERT INTO memberships (org_id, user_id) VALUES ($1, $2)", other, p.alsoElsewhere)
	}
	exec("INSERT INTO superadmins (user_id) VALUES ($1)", p.super)
	exec(`INSERT INTO audit_log (org_id, seq, action, target, prev_hash, hash) VALUES ($1, 0, 'group.create', 'group:x', '\x00', '\x00')`, p.org)
	exec("CALL refresh_continuous_aggregate('metrics_1m', now() - interval '1 hour', now())")
	return p
}

// orgRows counts the rows of every table with an org_id column per organization, and the
// metrics of the organization's series (they have no org_id).
func orgRows(t *testing.T, conn *pgx.Conn, org uuid.UUID, series []int64) map[string]int64 {
	t.Helper()
	ctx := context.Background()
	rows, err := conn.Query(ctx, `SELECT c.table_name FROM information_schema.columns c
		JOIN information_schema.tables t ON t.table_schema = c.table_schema AND t.table_name = c.table_name
		WHERE c.table_schema = 'public' AND c.column_name = 'org_id' AND t.table_type = 'BASE TABLE'
		  AND NOT c.table_name LIKE 'security_events\_%'`)
	if err != nil {
		t.Fatal(err)
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	tables = append(tables, "organizations")
	out := map[string]int64{}
	for _, table := range tables {
		column := "org_id"
		if table == "organizations" {
			column = "id"
		}
		var n int64
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize()+" WHERE "+column+" = $1", org).Scan(&n); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		out[table] = n
	}
	for _, table := range []string{"metrics_raw", "metrics_1m"} {
		var n int64
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE series_id = ANY($1)", series).Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[table] = n
	}
	return out
}

func seriesOf(t *testing.T, conn *pgx.Conn, org uuid.UUID) []int64 {
	t.Helper()
	rows, err := conn.Query(context.Background(), "SELECT id FROM series WHERE org_id = $1", org)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

// M2-33: org_purge removes everything of a locked organization that is due – in every table
// with org_id, its metrics included – and nothing of others; accounts only without another
// membership, superadmins keep theirs. It refuses organizations that are not locked and due.
func TestOrgPurge_RemovesEverythingOfTheOrganizationAndNothingElse(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	other := seedPurgeOrg(t, f.db.OwnerDSN, uuid.Nil)
	// The organization to delete shares one member with the other one.
	p := seedPurgeOrg(t, f.db.OwnerDSN, other.org)
	owner, err := pgx.Connect(ctx, pgtest.Shared(t).OwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close(ctx) }()
	series, otherSeries := seriesOf(t, owner, p.org), seriesOf(t, owner, other.org)
	before := orgRows(t, owner, p.org, series)
	if before["nodes"] != 1 || before["series"] != 1 || before["metrics_raw"] != 1 || before["metrics_1m"] != 1 ||
		before["security_events"] != 1 || before["audit_log"] != 1 || before["invitations"] != 1 || before["memberships"] != 3 {
		t.Fatalf("fixture incomplete: %v", before)
	}
	otherBefore := orgRows(t, owner, other.org, otherSeries)

	app, err := pgx.Connect(ctx, f.appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close(ctx) }()
	purge := func() error {
		var n int64
		return app.QueryRow(ctx, "SELECT org_purge($1, now())", p.org).Scan(&n)
	}
	if err := purge(); err == nil || !strings.Contains(err.Error(), "not locked and due") {
		t.Fatalf("purge of an active organization: %v", err)
	}
	ownerExec(t, f.db.OwnerDSN, "UPDATE organizations SET locked_at = now(), deletion_due_at = now() + interval '1 day' WHERE id = $1", p.org)
	if err := purge(); err == nil {
		t.Fatal("purge before the deadline succeeded")
	}
	ownerExec(t, f.db.OwnerDSN, "UPDATE organizations SET deletion_due_at = now() - interval '1 second' WHERE id = $1", p.org)
	var deletedUsers int64
	if err := app.QueryRow(ctx, "SELECT org_purge($1, now())", p.org).Scan(&deletedUsers); err != nil {
		t.Fatal(err)
	}
	if deletedUsers != 1 {
		t.Errorf("deleted %d accounts, want the one without another membership", deletedUsers)
	}
	for table, n := range orgRows(t, owner, p.org, series) {
		if n != 0 {
			t.Errorf("%s: %d rows of the deleted organization left", table, n)
		}
	}
	for table, n := range orgRows(t, owner, other.org, otherSeries) {
		if n != otherBefore[table] {
			t.Errorf("%s of the other organization: %d rows, had %d", table, n, otherBefore[table])
		}
	}
	var users []bool
	for _, u := range []uuid.UUID{p.onlyHere, p.alsoElsewhere, p.super} {
		var exists bool
		if err := owner.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)", u).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		users = append(users, exists)
	}
	if users[0] || !users[1] || !users[2] {
		t.Errorf("accounts left (only here, also elsewhere, superadmin) = %v, want false true true", users)
	}
}

// A lock refuses agents, enrollment tokens, invitations and the login into the organization at
// once; the certificate itself is not revoked (M2-33).
func TestOrganizationLock_RefusesAgentsTokensInvitationsAndLogins(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := seedPurgeOrg(t, f.db.OwnerDSN, uuid.Nil)
	serial := strings.ReplaceAll(uuid.NewString(), "-", "")
	token, invite := sha256.Sum256([]byte("token-"+serial)), sha256.Sum256([]byte("invite-"+serial))
	tokenHash, inviteHash := token[:], invite[:]
	group := uuid.New()
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO node_groups (id, org_id, name) VALUES ($1, $2, 'tokens')", group, p.org)
	ownerExec(t, f.db.OwnerDSN, `INSERT INTO enrollment_tokens (id, org_id, group_id, token_hash, max_uses, expires_at)
		VALUES ($1, $2, $3, $4, 5, now() + interval '1 day')`, uuid.New(), p.org, group, tokenHash)
	ownerExec(t, f.db.OwnerDSN, `INSERT INTO invitations (id, org_id, email, role, token_hash, expires_at)
		VALUES ($1, $2, $3, 'viewer', $4, now() + interval '1 day')`, uuid.New(), p.org, serial+"@example.org", inviteHash)
	var authority int32
	ownerQueryRowPurge(t, f.db.OwnerDSN, `INSERT INTO pki_authorities (root_cert, root_key_sealed, intermediate_cert, intermediate_key_sealed, active)
		VALUES ('\x00', '\x00', '\x00', '\x00', false) RETURNING id`, &authority)
	ownerExec(t, f.db.OwnerDSN, `INSERT INTO agent_certificates (serial, org_id, node_id, authority_id, not_before, not_after)
		VALUES ($1, $2, $3, $4, now() - interval '1 hour', now() + interval '1 day')`, serial, p.org, p.node, authority)

	app, err := pgx.Connect(ctx, f.appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close(ctx) }()
	pg, err := postgres.Open(ctx, f.appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	// The alert engine and the notification dispatcher skip a locked organization.
	evaluated := func() bool {
		t.Helper()
		orgs, err := postgres.NewNotificationStore(pg).Organizations(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return slices.Contains(orgs, tenant.OrgID(p.org))
	}
	state := func() (cert, token, invite bool, orgs int) {
		t.Helper()
		if err := app.QueryRow(ctx, "SELECT agent_certificate_usable($1, $2, $3, now())", serial, p.org, p.node).Scan(&cert); err != nil {
			t.Fatal(err)
		}
		if err := app.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM enrollment_token_by_hash($1, now()))", tokenHash).Scan(&token); err != nil {
			t.Fatal(err)
		}
		if err := app.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM invitation_by_token($1, now()))", inviteHash).Scan(&invite); err != nil {
			t.Fatal(err)
		}
		if err := app.QueryRow(ctx, "SELECT count(*) FROM auth_user_organizations($1) WHERE org_id = $2", p.onlyHere, p.org).Scan(&orgs); err != nil {
			t.Fatal(err)
		}
		return
	}
	if !evaluated() {
		t.Fatal("the active organization is not evaluated")
	}
	if cert, token, invite, orgs := state(); !cert || !token || !invite || orgs != 1 {
		t.Fatalf("before the lock: cert %v token %v invite %v orgs %d", cert, token, invite, orgs)
	}
	ownerExec(t, f.db.OwnerDSN, "UPDATE organizations SET locked_at = now() WHERE id = $1", p.org)
	if cert, token, invite, orgs := state(); cert || token || invite || orgs != 0 {
		t.Errorf("locked: cert %v token %v invite %v orgs %d, want all refused", cert, token, invite, orgs)
	}
	if evaluated() {
		t.Error("alerts of the locked organization are still evaluated")
	}
	var revoked bool
	ownerQueryRowPurge(t, f.db.OwnerDSN, "SELECT revoked_at IS NOT NULL FROM agent_certificates WHERE serial = '"+serial+"'", &revoked)
	if revoked {
		t.Error("the lock revoked the certificate")
	}
	ownerExec(t, f.db.OwnerDSN, "UPDATE organizations SET locked_at = NULL WHERE id = $1", p.org)
	if cert, token, invite, orgs := state(); !cert || !token || !invite || orgs != 1 {
		t.Errorf("unlocked: cert %v token %v invite %v orgs %d, want all admitted again", cert, token, invite, orgs)
	}
	// Deletion is only scheduled for a locked organization.
	ownerExecErr(t, f.db.OwnerDSN, "UPDATE organizations SET deletion_due_at = now() WHERE id = $1", p.org)
}

func ownerQueryRowPurge(t *testing.T, dsn, query string, dest ...any) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if err := conn.QueryRow(ctx, query).Scan(dest...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func ownerExecErr(t *testing.T, dsn, sql string, args ...any) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, sql, args...); err == nil {
		t.Errorf("%s: no error, want the check to refuse", sql)
	}
}
