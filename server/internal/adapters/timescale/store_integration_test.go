//go:build integration

package timescale_test

import (
	"context"
	"encoding/hex"
	"fmt"
	"reflect"
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
		exec("INSERT INTO organizations (id, name) VALUES ($1, 'Metrics ' || $1::uuid::text)", uuid.UUID(org))
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
		"SELECT compress_chunk(c, if_not_compressed => true) FROM show_chunks('metrics_1m') c",
		"SELECT compress_chunk(c, if_not_compressed => true) FROM show_chunks('metrics_1h') c",
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

// Series IDs are never reused; with integer they would run out after 2^31 registrations and stop
// the ingest of every tenant. The type can't be changed later once chunks are compressed.
func TestTimescaleStore_SeriesIDsAreBigint(t *testing.T) {
	e := setup(t)
	conn := owner(t, e)
	rows, err := conn.Query(context.Background(), `
		SELECT table_name || '.' || column_name, data_type FROM information_schema.columns
		WHERE (table_name, column_name) IN (('series', 'id'), ('metrics_raw', 'series_id'), ('metrics_1m', 'series_id'), ('metrics_1h', 'series_id'))`)
	if err != nil {
		t.Fatal(err)
	}
	types, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) ([2]string, error) {
		var c [2]string
		return c, r.Scan(&c[0], &c[1])
	})
	if err != nil || len(types) != 4 {
		t.Fatalf("columns = %v, %v", types, err)
	}
	for _, c := range types {
		if c[1] != "bigint" {
			t.Errorf("%s is %s, want bigint", c[0], c[1])
		}
	}
}

// The cache only saves lookups: it never holds more than its limit (series of deleted nodes or
// label churn would otherwise grow it forever), and writes stay correct after it starts over.
func TestTimescaleStore_BoundsTheSeriesCache(t *testing.T) {
	e := setup(t)
	store := timescale.New(e.db)
	timescale.SetCacheLimit(store, 3)
	f := &fixture{t: t, owner: owner(t, e), orgs: map[tenant.OrgID]bool{}, group: map[tenant.GroupID]bool{}}
	org := tenant.OrgID(uuid.New())
	node := f.Node(t, org, tenant.GroupID(uuid.New()))
	start := time.Now().Add(-10 * time.Minute).Truncate(time.Minute)
	write := func(devices ...string) {
		t.Helper()
		var aggregates []metrics.Aggregate
		for _, d := range devices {
			aggregates = append(aggregates, metrics.Aggregate{Node: node, Metric: "sermon_disk_reads_per_second", Labels: metrics.Labels{"device": d}, Start: start, Avg: 1, Min: 1, Max: 1})
		}
		if err := store.Write(context.Background(), org, aggregates, nil); err != nil {
			t.Fatal(err)
		}
	}

	write("a", "b", "c")
	write("d", "e")
	if n := store.CachedSeries(); n > 3 {
		t.Errorf("cache holds %d series, limit 3", n)
	}
	write("a", "d")
	series, err := store.NodeSeries(context.Background(), org, node, start.Add(-time.Hour))
	if err != nil || len(series) != 5 {
		t.Errorf("NodeSeries() = %d series, %v; want 5 (no duplicates after the cache started over)", len(series), err)
	}
}

// V1-21: the columnstore policies of the aggregates compress every chunk that ended before their
// limit (metrics_1m after 6 h, metrics_1h after 1 day) and nothing newer, and the readers return
// the same points from the compressed chunks.
func TestTimescaleStore_CompressesOldAggregateChunks(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	conn := owner(t, e)
	// The windows are older than the raw retention (24 h): it must not drop them before the
	// aggregates hold them.
	setJobScheduled(t, conn, "policy_retention", "metrics_raw", false)
	t.Cleanup(func() { setJobScheduled(t, owner(t, e), "policy_retention", "metrics_raw", true) })

	f := &fixture{t: t, owner: conn, orgs: map[tenant.OrgID]bool{}, group: map[tenant.GroupID]bool{}}
	org := tenant.OrgID(uuid.New())
	node := f.Node(t, org, tenant.GroupID(uuid.New()))
	store := timescale.New(e.db)
	newest := time.Now().Add(-2 * time.Hour).Truncate(time.Hour)
	const windows = 20 * 8 // every 3 h for 20 days, each alone in its minute and hour
	var aggregates []metrics.Aggregate
	for i := range windows {
		v := float64(i)
		for _, device := range []string{"sda", "sdb"} {
			aggregates = append(aggregates, metrics.Aggregate{
				Node: node, Metric: "sermon_disk_reads_per_second", Labels: metrics.Labels{"device": device},
				Start: newest.Add(-time.Duration(i) * 3 * time.Hour), Avg: v, Min: v - 1, Max: v + 2,
			})
		}
	}
	if err := store.Write(ctx, org, aggregates, nil); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		// Other tests compress every chunk by hand (Settle); start from what only the policies do.
		"SELECT decompress_chunk(c, if_compressed => true) FROM show_chunks('metrics_1m') c",
		"SELECT decompress_chunk(c, if_compressed => true) FROM show_chunks('metrics_1h') c",
		"CALL refresh_continuous_aggregate('metrics_1m', NULL, NULL)",
		"CALL refresh_continuous_aggregate('metrics_1h', NULL, NULL)",
	} {
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	scope, err := tenant.ForAllGroups(org)
	if err != nil {
		t.Fatal(err)
	}
	read := func(source metrics.Source) []metrics.Series {
		t.Helper()
		series, err := store.Query(ctx, scope, metrics.Query{
			Metric: "sermon_disk_reads_per_second", Nodes: []identity.NodeID{node},
			From: newest.Add(-21 * 24 * time.Hour), To: newest.Add(time.Hour), Step: source.Resolution(), Source: source,
		})
		if err != nil {
			t.Fatal(err)
		}
		return series
	}
	before := map[metrics.Source][]metrics.Series{metrics.Source1m: read(metrics.Source1m), metrics.Source1h: read(metrics.Source1h)}
	for source, series := range before {
		if len(series) != 2 || len(series[0].Points) != windows || len(series[1].Points) != windows {
			t.Fatalf("source %v before compression: %d series, want 2 with %d points each", source, len(series), windows)
		}
	}

	for view, limit := range map[string]time.Duration{"metrics_1m": 6 * time.Hour, "metrics_1h": 24 * time.Hour} {
		var job int
		var after float64
		if err := conn.QueryRow(ctx, `SELECT job_id, extract(epoch FROM (config->>'compress_after')::interval)
			FROM timescaledb_information.jobs WHERE proc_name = 'policy_compression' AND hypertable_name = $1`, view).Scan(&job, &after); err != nil {
			t.Fatalf("columnstore policy of %s: %v", view, err)
		}
		if time.Duration(after)*time.Second != limit {
			t.Errorf("%s compresses after %v s, want %s", view, after, limit)
		}
		if _, err := conn.Exec(ctx, fmt.Sprintf("CALL run_job(%d)", job)); err != nil {
			t.Fatalf("run the columnstore policy of %s: %v", view, err)
		}
		rows, err := conn.Query(ctx, `SELECT c.range_end, c.is_compressed FROM timescaledb_information.chunks c
			JOIN timescaledb_information.continuous_aggregates a
			  ON a.materialization_hypertable_schema = c.hypertable_schema AND a.materialization_hypertable_name = c.hypertable_name
			WHERE a.view_name = $1`, view)
		if err != nil {
			t.Fatal(err)
		}
		type chunk struct {
			end        time.Time
			compressed bool
		}
		chunks, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (chunk, error) {
			var c chunk
			return c, r.Scan(&c.end, &c.compressed)
		})
		if err != nil {
			t.Fatal(err)
		}
		cut := time.Now().Add(-limit)
		var compressed, open int
		for _, c := range chunks {
			if want := c.end.Before(cut); c.compressed != want {
				t.Errorf("%s: chunk ending %s compressed = %v, want %v (limit %s)", view, c.end, c.compressed, want, limit)
			}
			if c.compressed {
				compressed++
			} else {
				open++
			}
		}
		if compressed == 0 || open == 0 {
			t.Errorf("%s: %d compressed and %d open chunks, want both", view, compressed, open)
		}
	}

	// The segments follow the readers (series, then time); metrics_1h has chunks of 7 days, so a
	// segment holds a week of a series instead of ten hours.
	rows, err := conn.Query(ctx, `SELECT a.view_name, s.segmentby, s.orderby, extract(epoch FROM d.time_interval)
		FROM timescaledb_information.continuous_aggregates a
		JOIN timescaledb_information.hypertable_columnstore_settings s
		  ON s.hypertable = format('%I.%I', a.materialization_hypertable_schema, a.materialization_hypertable_name)::regclass
		JOIN timescaledb_information.dimensions d
		  ON d.hypertable_schema = a.materialization_hypertable_schema AND d.hypertable_name = a.materialization_hypertable_name
		WHERE a.view_name IN ('metrics_1m', 'metrics_1h') ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (string, error) {
		var view, segment, order string
		var chunk float64
		err := r.Scan(&view, &segment, &order, &chunk)
		return fmt.Sprintf("%s %s %s %s", view, segment, order, time.Duration(chunk)*time.Second), err
	})
	if want := []string{"metrics_1h series_id bucket 168h0m0s", "metrics_1m series_id bucket 10h0m0s"}; err != nil || !reflect.DeepEqual(settings, want) {
		t.Errorf("columnstore settings = %q, %v; want %q", settings, err, want)
	}

	for source, want := range before {
		if got := read(source); !reflect.DeepEqual(got, want) {
			t.Errorf("source %v: points changed after compression", source)
		}
	}
}

func setJobScheduled(t *testing.T, conn *pgx.Conn, proc, hypertable string, scheduled bool) {
	t.Helper()
	tag, err := conn.Exec(context.Background(), `SELECT alter_job(job_id, scheduled => $3) FROM timescaledb_information.jobs
		WHERE proc_name = $1 AND hypertable_name = $2`, proc, hypertable, scheduled)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("alter_job(%s of %s): %v, %d jobs", proc, hypertable, err, tag.RowsAffected())
	}
}
