//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/backup"
	"github.com/pmoscode/SerMon/server/internal/platform/backupfile"
)

// backupRole creates sermon_backup as deploy/compose/initdb/010-roles.sh does (V1-05): read-only
// and BYPASSRLS, the only way to read every organization in one snapshot.
func backupRole(t *testing.T, f fixture, database string) string {
	t.Helper()
	ownerExec(t, f.db.OwnerDSN, `DO $$ BEGIN
		IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'sermon_backup') THEN
			CREATE ROLE sermon_backup LOGIN PASSWORD 'backup-test' NOSUPERUSER NOCREATEDB NOCREATEROLE BYPASSRLS IN ROLE pg_read_all_data;
		END IF; END $$`)
	return strings.Replace(strings.Replace(f.db.OwnerDSN, "/sermon?", "/"+database+"?", 1), "sermon:sermon-test@", "sermon_backup:backup-test@", 1)
}

// emptyDatabase creates a database owned by the non-superuser owner, without migrations.
func emptyDatabase(t *testing.T, f fixture, name string) string {
	t.Helper()
	nonSuperuserDatabase(t, f, name+"_init") // creates the role once
	ownerExec(t, f.db.OwnerDSN, "CREATE DATABASE "+name+" OWNER sermon_owner_nonsuper")
	return strings.Replace(strings.Replace(f.db.OwnerDSN, "/sermon?", "/"+name+"?", 1),
		"sermon:sermon-test@", "sermon_owner_nonsuper:owner-test@", 1)
}

// seedTwoOrganizations writes rows of two organizations into tables with FORCE ROW LEVEL
// SECURITY, an audit log (hash chain by trigger) and an excluded table.
func seedTwoOrganizations(t *testing.T, superDSN string) {
	t.Helper()
	for _, org := range []string{orgA.String(), orgB.String()} {
		// As the superuser: row-level security doesn't apply.
		ownerExec(t, superDSN, `INSERT INTO organizations (id, name) VALUES ($1, $2)`, org, "Org "+org)
		ownerExec(t, superDSN, `INSERT INTO node_groups (id, org_id, name) VALUES (gen_random_uuid(), $1, 'servers')`, org)
		ownerExec(t, superDSN, `INSERT INTO audit_log (org_id, action, target) VALUES ($1, 'test.one', 'x'), ($1, 'test.two', 'y')`, org)
		ownerExec(t, superDSN, `INSERT INTO security_events (org_id, node_id, occurred_at, kind, user_name)
			VALUES ($1, gen_random_uuid(), '2026-11-20T10:00:00Z', 'ssh.login.failed', 'alice')`, org)
	}
	ownerExec(t, superDSN, `INSERT INTO users (id, email, display_name) VALUES (gen_random_uuid(), 'alice@example.org', 'Alice')`)
	ownerExec(t, superDSN, `INSERT INTO sessions (id, user_id, created_at, last_seen_at, step_up_at)
		SELECT repeat('a', 64), id, now(), now(), now() FROM users`)
}

func superDSNOf(f fixture, database string) string {
	return strings.Replace(f.db.OwnerDSN, "/sermon?", "/"+database+"?", 1)
}

func countTableRows(t *testing.T, superDSN, table string) int {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, superDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var n int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize()).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func query(t *testing.T, superDSN, sql string) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, superDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var s string
	if err := conn.QueryRow(ctx, sql).Scan(&s); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return s
}

// memorySource copies the source into memory as the backup file would carry it.
type copied struct {
	info   backup.Info
	tables []backupfile.Table
	data   map[string]string
	rows   map[string]int64
	seqs   []backupfile.Sequence
}

func snapshot(t *testing.T, dsn string) copied {
	t.Helper()
	ctx := context.Background()
	src, err := postgres.OpenBackupSource(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close(ctx)
	c := copied{data: map[string]string{}, rows: map[string]int64{}}
	if c.info, err = src.Info(ctx); err != nil {
		t.Fatal(err)
	}
	if c.tables, err = src.Tables(ctx); err != nil {
		t.Fatal(err)
	}
	for _, tb := range c.tables {
		var buf bytes.Buffer
		n, err := src.CopyOut(ctx, tb, &buf)
		if err != nil {
			t.Fatalf("CopyOut(%s) = %v", tb.Name, err)
		}
		c.data[tb.Name], c.rows[tb.Name] = buf.String(), n
	}
	if c.seqs, err = src.Sequences(ctx); err != nil {
		t.Fatal(err)
	}
	return c
}

func load(ctx context.Context, target *postgres.RestoreTarget, c copied) error {
	if err := target.Prepare(ctx, c.info.Migrations); err != nil {
		return err
	}
	err := target.Load(ctx, func(l backup.Loader) error {
		for _, tb := range c.tables {
			n, err := l.CopyIn(ctx, tb, strings.NewReader(c.data[tb.Name]))
			if err != nil {
				return err
			}
			if n != c.rows[tb.Name] {
				return errors.New("row count of " + tb.Name)
			}
		}
		l.Restored(backupfile.Header{ID: "0193f5a0000070008000000000000042"})
		return l.SetSequences(ctx, c.seqs)
	})
	if err != nil {
		return err
	}
	return target.Finish(ctx)
}

// V1-05: the snapshot as sermon_backup sees every organization of the tables with FORCE ROW
// LEVEL SECURITY, orders the tables by their foreign keys and leaves out the excluded ones; the
// restore as the non-superuser owner puts back exactly the same rows, keeps the hash chain of the
// audit log, the sequences and FORCE on every table.
func TestBackup_RoundTripBetweenTwoDatabases(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	nonSuperuserDatabase(t, f, "sermon_backup_src")
	seedTwoOrganizations(t, superDSNOf(f, "sermon_backup_src"))
	c := snapshot(t, backupRole(t, f, "sermon_backup_src"))

	names := make([]string, 0, len(c.tables))
	for _, tb := range c.tables {
		names = append(names, tb.Name)
	}
	for name := range postgres.BackupExclusions() {
		if slices.Contains(names, name) {
			t.Errorf("excluded table %s is in the backup", name)
		}
	}
	// The counts of the log lines go with the logs in Loki, which the backup doesn't hold (V1-24).
	for _, name := range []string{"metrics_raw", "log_counts_1m", "log_counts_1h", "log_count_marks"} {
		if slices.Contains(names, name) {
			t.Errorf("table %s is in the backup", name)
		}
	}
	for _, want := range []string{"organizations", "users", "audit_log", "security_events", "pki_authorities", "module_secrets", "kiosk_tokens", "dashboards", "instance_setup", "series"} {
		if !slices.Contains(names, want) {
			t.Errorf("table %s is missing from the backup", want)
		}
	}
	if slices.Index(names, "organizations") > slices.Index(names, "memberships") || slices.Index(names, "users") > slices.Index(names, "memberships") {
		t.Errorf("tables not in the order of their foreign keys: %v", names)
	}
	if c.rows["organizations"] != 2 || c.rows["audit_log"] != 4 || c.rows["security_events"] != 2 {
		t.Errorf("rows = %v: the snapshot must see every organization", c.rows)
	}
	if len(c.info.Migrations) < 60 || c.info.Postgres == "" || c.info.TimescaleDB == "" {
		t.Errorf("info = %+v", c.info)
	}

	dstOwner := emptyDatabase(t, f, "sermon_backup_dst")
	if err := load(ctx, postgres.NewRestoreTarget(dstOwner), c); err != nil {
		t.Fatalf("restore: %v", err)
	}
	dst := superDSNOf(f, "sermon_backup_dst")
	if got := query(t, dst, "SELECT target FROM instance_audit_log WHERE action = 'instance.restored'"); got != "backup:0193f5a0000070008000000000000042" {
		t.Errorf("the restore is not in the instance's audit log: %q", got)
	}
	for _, tb := range c.tables {
		want := c.rows[tb.Name]
		if tb.Name == "instance_audit_log" {
			want++ // the restore itself
		}
		if n := countTableRows(t, dst, tb.Name); int64(n) != want {
			t.Errorf("%s: %d rows after the restore, want %d", tb.Name, n, want)
		}
	}
	if n := countTableRows(t, dst, "sessions"); n != 0 {
		t.Errorf("%d sessions restored, want none", n)
	}
	chain := "SELECT string_agg(encode(hash, 'hex'), ',' ORDER BY org_id, seq) FROM audit_log"
	if query(t, dst, chain) != query(t, superDSNOf(f, "sermon_backup_src"), chain) {
		t.Error("the hash chain of the audit log changed: the trigger ran during the restore")
	}
	// The sequences go on after the restored rows.
	ownerExec(t, dst, `INSERT INTO audit_log (org_id, action, target) VALUES ($1, 'test.three', 'z')`, orgA)
	if got := query(t, dst, "SELECT (max(id) > 4)::text FROM audit_log"); got != "true" {
		t.Error("the identity of audit_log starts again: sequences not restored")
	}
	if got := query(t, dst, "SELECT seq::text FROM audit_log WHERE action = 'test.three'"); got != "3" {
		t.Errorf("the chain goes on at seq %s, want 3 (trigger enabled again)", got)
	}
	unforced := query(t, dst, `SELECT coalesce(string_agg(relname, ','), '') FROM pg_class
		WHERE relnamespace = 'public'::regnamespace AND relrowsecurity AND NOT relforcerowsecurity`)
	if unforced != "" {
		t.Errorf("tables without FORCE ROW LEVEL SECURITY after the restore: %s", unforced)
	}
	disabled := query(t, dst, `SELECT coalesce(string_agg(tgname, ','), '') FROM pg_trigger WHERE NOT tgisinternal AND tgenabled = 'D'`)
	if disabled != "" {
		t.Errorf("disabled triggers after the restore: %s", disabled)
	}

	// A second restore into the same database is refused.
	if err := load(ctx, postgres.NewRestoreTarget(dstOwner), c); !errors.Is(err, backup.ErrNotEmpty) {
		t.Errorf("restore into a database with data = %v, want ErrNotEmpty", err)
	}
}

// A role subject to row-level security gets an error, never a backup without the tenants' rows.
func TestBackupSource_RefusesARoleSubjectToRowLevelSecurity(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	src, err := postgres.OpenBackupSource(ctx, f.appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close(ctx)
	tables, err := src.Tables(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tb := range tables {
		if tb.Name != "organizations" {
			continue
		}
		if _, err := src.CopyOut(ctx, tb, io.Discard); err == nil {
			t.Error("CopyOut(organizations) as sermon_app must fail")
		}
	}
}

// The excluded tables exist: a renamed table would silently enter the backup.
func TestBackupExclusions_NameExistingTables(t *testing.T) {
	f := setup(t)
	for name := range postgres.BackupExclusions() {
		if query(t, f.db.OwnerDSN, "SELECT coalesce(to_regclass('public."+name+"')::text, '')") == "" {
			t.Errorf("excluded table %s does not exist", name)
		}
	}
}

// A database with data, or one migrated past the backup, takes no restore; a refused restore
// changes nothing.
func TestRestoreTarget_Prepare(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if err := postgres.NewRestoreTarget(f.db.OwnerDSN).Prepare(ctx, []int64{1, 2, 3}); !errors.Is(err, backup.ErrSchema) {
		t.Errorf("Prepare() on a database migrated past the backup = %v, want ErrSchema", err)
	}
	all, err := postgres.MigrationVersions()
	if err != nil {
		t.Fatal(err)
	}
	if err := postgres.NewRestoreTarget(f.db.OwnerDSN).Prepare(ctx, all); !errors.Is(err, backup.ErrNotEmpty) {
		t.Errorf("Prepare() on a database with organizations = %v, want ErrNotEmpty", err)
	}
	fresh := emptyDatabase(t, f, "sermon_backup_prepare")
	if err := postgres.NewRestoreTarget(fresh).Prepare(ctx, append(slices.Clone(all), all[len(all)-1]+1000)); !errors.Is(err, backup.ErrSchema) {
		t.Errorf("Prepare() with a migration this server lacks = %v, want ErrSchema", err)
	}
	if got := query(t, superDSNOf(f, "sermon_backup_prepare"), "SELECT coalesce(to_regclass('public.goose_db_version')::text, '')"); got != "" {
		t.Error("a refused Prepare migrated the database")
	}
	if err := postgres.NewRestoreTarget(fresh).Prepare(ctx, all[:20]); err != nil {
		t.Fatalf("Prepare() on a new database = %v", err)
	}
	if got := query(t, superDSNOf(f, "sermon_backup_prepare"), "SELECT max(version_id)::text FROM goose_db_version"); got != strconv.FormatInt(all[19], 10) {
		t.Errorf("migrated to %s, want %d (the backup's last)", got, all[19])
	}
}
