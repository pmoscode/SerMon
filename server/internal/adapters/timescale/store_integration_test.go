//go:build integration

package timescale_test

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/pgtest"
	"github.com/pmoscode/SerMon/server/internal/adapters/timescale"
	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/app/metrics/metricstoretest"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

type env struct {
	ownerDSN string
	appDSN   string
	db       *postgres.DB
}

func setup(t *testing.T) env {
	t.Helper()
	ctx := context.Background()
	database := pgtest.Shared(t)
	if err := postgres.Migrate(ctx, database.OwnerDSN); err != nil {
		t.Fatal(err)
	}
	appDSN := pgtest.EnableAppRole(t, database)
	db, err := postgres.Open(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return env{ownerDSN: database.OwnerDSN, appDSN: appDSN, db: db}
}

func owner(t *testing.T, e env) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), e.ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

type fixture struct {
	t     *testing.T
	owner *pgx.Conn
	orgs  map[tenant.OrgID]bool
	group map[tenant.GroupID]bool
}

func (f *fixture) Node(t *testing.T, org tenant.OrgID, group tenant.GroupID) identity.NodeID {
	t.Helper()
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.owner.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec("SELECT set_config('sermon.org_id', $1, false)", org.String())
	if !f.orgs[org] {
		exec("INSERT INTO organizations (id, name) VALUES ($1, 'Metrics')", uuid.UUID(org))
		f.orgs[org] = true
	}
	if !f.group[group] {
		exec("INSERT INTO node_groups (id, org_id, name) VALUES ($1, $2, $3)", uuid.UUID(group), uuid.UUID(org), "g-"+group.String()[:8])
		f.group[group] = true
	}
	node := uuid.New()
	exec(`INSERT INTO nodes (id, org_id, group_id, hostname, machine_id, os_id, os_version, kernel_release, architecture, cpu_count, memory_bytes)
	      VALUES ($1, $2, $3, 'web-01', $4, 'debian', '13', '6.12.0', 'amd64', 2, 1073741824)`,
		node, uuid.UUID(org), uuid.UUID(group), hex.EncodeToString(node[:]))
	return identity.NodeID(node)
}

// Settle refreshes the continuous aggregates and compresses all chunks, so that the contract also
// covers the columnstore and the materialized aggregates (no RLS on either, spike M0-09).
func (f *fixture) Settle(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for _, sql := range []string{
		"CALL refresh_continuous_aggregate('metrics_1m', NULL, NULL)",
		"CALL refresh_continuous_aggregate('metrics_1h', NULL, NULL)",
		"SELECT compress_chunk(c, if_not_compressed => true) FROM show_chunks('metrics_raw') c",
	} {
		if _, err := f.owner.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
}

func TestTimescaleStore_Contract(t *testing.T) {
	e := setup(t)
	metricstoretest.Run(t, func(t *testing.T) (metrics.Store, metricstoretest.Fixture) {
		return timescale.New(e.db), &fixture{t: t, owner: owner(t, e), orgs: map[tenant.OrgID]bool{}, group: map[tenant.GroupID]bool{}}
	})
}

// The application role can write metrics but never read the metric tables directly; reading
// goes through the SECURITY DEFINER functions only.
func TestTimescaleStore_AppRoleCannotReadMetricTables(t *testing.T) {
	e := setup(t)
	conn, err := pgx.Connect(context.Background(), e.appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	for _, table := range []string{"metrics_raw", "metrics_1m", "metrics_1h"} {
		_, err := conn.Exec(context.Background(), "SELECT count(*) FROM "+table)
		if err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("SELECT on %s: %v, want permission denied", table, err)
		}
	}
}

// Second line of defence: even with the IDs of foreign series, the reader functions return
// nothing for another organization – and nothing without a tenant.
func TestTimescaleStore_ReaderFunctionsIgnoreForeignSeriesIDs(t *testing.T) {
	e := setup(t)
	f := &fixture{t: t, owner: owner(t, e), orgs: map[tenant.OrgID]bool{}, group: map[tenant.GroupID]bool{}}
	orgA, orgB := tenant.OrgID(uuid.New()), tenant.OrgID(uuid.New())
	nodeA := f.Node(t, orgA, tenant.GroupID(uuid.New()))
	f.Node(t, orgB, tenant.GroupID(uuid.New()))
	start := time.Now().Add(-10 * time.Minute).Truncate(time.Minute)
	store := timescale.New(e.db)
	if err := store.Write(context.Background(), orgA, []metrics.Aggregate{{Node: nodeA, Metric: "sermon_x", Start: start, Avg: 1, Min: 1, Max: 1}}, nil); err != nil {
		t.Fatal(err)
	}
	var ids []int32
	if err := f.owner.QueryRow(context.Background(), "SELECT array_agg(id) FROM series WHERE node_id = $1", uuid.UUID(nodeA)).Scan(&ids); err != nil {
		t.Fatal(err)
	}

	conn, err := pgx.Connect(context.Background(), e.appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	count := func(org string) int {
		t.Helper()
		tx, err := conn.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if org != "" {
			if _, err := tx.Exec(context.Background(), "SELECT set_config('sermon.org_id', $1, true)", org); err != nil {
				t.Fatal(err)
			}
		}
		var n int
		err = tx.QueryRow(context.Background(), "SELECT count(*) FROM metric_points_raw($1, $2, $3, '10 seconds')",
			ids, start.Add(-time.Hour), start.Add(time.Hour)).Scan(&n)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got := count(orgA.String()); got != 1 {
		t.Fatalf("own organization sees %d points, want 1", got)
	}
	if got := count(orgB.String()); got != 0 {
		t.Errorf("organization B sees %d points of A", got)
	}
	if got := count(""); got != 0 {
		t.Errorf("without tenant %d points are visible", got)
	}
}
