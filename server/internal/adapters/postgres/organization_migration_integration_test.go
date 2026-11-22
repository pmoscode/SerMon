//go:build integration

package postgres_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
)

// Names of organizations become unique regardless of case (M2-32, migration 00035): existing
// duplicates keep the oldest as it is and get a suffix; afterwards the index refuses new ones.
func TestMigration35_DeduplicatesOrganizationNames(t *testing.T) {
	f := setup(t)
	ownerExec(t, f.db.OwnerDSN, "CREATE DATABASE sermon_migration_35")
	dsn := strings.Replace(f.db.OwnerDSN, "/sermon?", "/sermon_migration_35?", 1)
	ctx := context.Background()
	if err := postgres.MigrateTo(ctx, dsn, 34); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	orgs := []struct{ id, name, created string }{
		{"0193f5a0-0000-7000-8000-000000035001", "Acme", "2026-01-01"},
		{"0193f5a0-0000-7000-8000-000000035002", "ACME", "2026-02-01"},
		{"0193f5a0-0000-7000-8000-000000035003", "acme", "2026-03-01"},
		{"0193f5a0-0000-7000-8000-000000035004", "Other", "2026-01-01"},
	}
	for _, o := range orgs {
		for _, q := range []string{
			"SELECT set_config('sermon.org_id', '" + o.id + "', false)",
			"INSERT INTO organizations (id, name, created_at) VALUES ('" + o.id + "', '" + o.name + "', '" + o.created + "')",
		} {
			if _, err := conn.Exec(ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
	}
	if err := postgres.Migrate(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{orgs[0].id: "Acme", orgs[1].id: "ACME (2)", orgs[2].id: "acme (3)", orgs[3].id: "Other"}
	for id, name := range want {
		var got string
		for _, q := range []string{"SELECT set_config('sermon.org_id', '" + id + "', false)"} {
			if _, err := conn.Exec(ctx, q); err != nil {
				t.Fatal(err)
			}
		}
		if err := conn.QueryRow(ctx, "SELECT name FROM organizations WHERE id = $1", id).Scan(&got); err != nil || got != name {
			t.Errorf("%s: name %q, %v; want %q", id, got, err, name)
		}
	}
	if _, err := conn.Exec(ctx, "SELECT set_config('sermon.org_id', '0193f5a0-0000-7000-8000-000000035005', false)"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "INSERT INTO organizations (id, name) VALUES ('0193f5a0-0000-7000-8000-000000035005', 'oTHER')"); err == nil {
		t.Error("a duplicate name in another case was stored")
	}
}
