// Command spike measures TimescaleDB against the acceptance criteria of ADR 0001 (plan M0-09).
//
// Throwaway spike code – results are documented in docs/adr/0001-*.md.
//
//	go run . -dsn <owner-dsn> schema
//	go run . -dsn <owner-dsn> seed      -nodes 50 -series 400 -orgs 5
//	go run . -dsn <owner-dsn> load      -duration 20m -container <name>
//	go run . -dsn <owner-dsn> compress
//	go run . -dsn <owner-dsn> backfill  -node-series 400 -hours 24 -long-series 10 -days 30
//	go run . -dsn <owner-dsn> queries
//	go run . -dsn <owner-dsn> rls
package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

var (
	dsn        = flag.String("dsn", "", "owner connection string")
	nodes      = flag.Int("nodes", 50, "number of nodes")
	perNode    = flag.Int("series", 400, "series per node")
	orgs       = flag.Int("orgs", 5, "number of organizations")
	duration   = flag.Duration("duration", 20*time.Minute, "load duration")
	container  = flag.String("container", "", "docker container to sample CPU/memory from")
	nodeSeries = flag.Int("node-series", 400, "backfill: series of one node for the 24 h chart")
	hours      = flag.Int("hours", 24, "backfill: hours of 1 s data for one node")
	longSeries = flag.Int("long-series", 10, "backfill: series with long history for the 30 d chart")
	days       = flag.Int("days", 30, "backfill: days of history for the long series")
)

func main() {
	flag.Parse()
	if *dsn == "" || flag.NArg() != 1 {
		log.Fatal("usage: spike -dsn <owner-dsn> schema|seed|load|compress|backfill|queries|rls")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	phases := map[string]func(context.Context, *pgxpool.Pool) error{
		"schema": schema, "seed": seed, "load": load, "compress": compress,
		"backfill": backfill, "queries": queries, "rls": rls,
	}
	phase, ok := phases[flag.Arg(0)]
	if !ok {
		log.Fatalf("unknown phase %q", flag.Arg(0))
	}
	if err := phase(ctx, pool); err != nil {
		log.Fatalf("%s: %v", flag.Arg(0), err)
	}
}

func schema(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, schemaSQL)
	return err
}

// seed creates nodes × series series rows, spread over orgs organizations.
func seed(ctx context.Context, pool *pgxpool.Pool) error {
	orgIDs := make([]uuid.UUID, *orgs)
	for i := range orgIDs {
		orgIDs[i] = uuid.New()
	}
	rows := make([][]any, 0, *nodes**perNode)
	for n := range *nodes {
		node := uuid.New()
		org := orgIDs[n%len(orgIDs)]
		for s := range *perNode {
			rows = append(rows, []any{org, node, fmt.Sprintf("sermon_metric_%03d", s%40), fmt.Sprintf(`{"dim": "%d"}`, s/40)})
		}
	}
	n, err := pool.CopyFrom(ctx, pgx.Identifier{"series"}, []string{"org_id", "node_id", "metric", "labels"}, pgx.CopyFromRows(rows))
	fmt.Printf("seeded %d series (%d nodes × %d, %d orgs)\n", n, *nodes, *perNode, *orgs)
	return err
}

func seriesIDs(ctx context.Context, pool *pgxpool.Pool) ([]int32, map[string][]int32, error) {
	rows, err := pool.Query(ctx, "SELECT id, node_id::text FROM series ORDER BY id")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var ids []int32
	byNode := map[string][]int32{}
	for rows.Next() {
		var id int32
		var node string
		if err := rows.Scan(&id, &node); err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
		byNode[node] = append(byNode[node], id)
	}
	return ids, byNode, rows.Err()
}

// load writes one sample per series every second (one COPY per second, like the server's
// ingest batching), runs dashboard queries concurrently and samples resource usage.
func load(ctx context.Context, pool *pgxpool.Pool) error {
	ids, byNode, err := seriesIDs(ctx, pool)
	if err != nil {
		return err
	}
	nodeList := make([]string, 0, len(byNode))
	for n := range byNode {
		nodeList = append(nodeList, n)
	}
	fmt.Printf("load: %d series, %d samples/s, %s\n", len(ids), len(ids), *duration)

	walStart := walLSN(ctx, pool)
	ctx, cancel := context.WithTimeout(ctx, *duration)
	defer cancel()

	var (
		mu          sync.Mutex
		lags        []time.Duration
		copyTimes   []time.Duration
		queryTimes  []time.Duration
		cpuSamples  []float64
		memSamples  []string
		wg          sync.WaitGroup
		writeErrors int
	)

	// Writer: ticks every second; lag = commit time − tick time (backlog shows up as growing lag).
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		value := make([]float64, len(ids))
		for {
			select {
			case <-ctx.Done():
				return
			case tick := <-ticker.C:
				rows := make([][]any, len(ids))
				for i, id := range ids {
					value[i] = math.Max(0, value[i]+rand.NormFloat64())
					rows[i] = []any{tick, id, value[i]}
				}
				start := time.Now()
				_, err := pool.CopyFrom(context.Background(), pgx.Identifier{"metrics_raw"}, []string{"ts", "series_id", "value"}, pgx.CopyFromRows(rows))
				done := time.Now()
				mu.Lock()
				if err != nil {
					writeErrors++
				}
				copyTimes = append(copyTimes, done.Sub(start))
				lags = append(lags, done.Sub(tick))
				mu.Unlock()
			}
		}
	}()

	// Readers: 5 dashboards, each refreshes a 1 h chart of 10 series of a random node every 2 s.
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case <-time.After(2 * time.Second):
				}
				series := byNode[nodeList[rand.IntN(len(nodeList))]][:10]
				start := time.Now()
				if err := chart(context.Background(), pool, "metrics_raw", "ts", "value", series, time.Hour, 10*time.Second); err == nil {
					mu.Lock()
					queryTimes = append(queryTimes, time.Since(start))
					mu.Unlock()
				}
			}
		}()
	}

	// Resource sampler.
	if *container != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
				}
				out, err := exec.Command("docker", "stats", "--no-stream", "--format", "{{.CPUPerc}};{{.MemUsage}}", *container).Output()
				if err != nil {
					continue
				}
				parts := strings.SplitN(strings.TrimSpace(string(out)), ";", 2)
				cpu, _ := strconv.ParseFloat(strings.TrimSuffix(parts[0], "%"), 64)
				mu.Lock()
				cpuSamples = append(cpuSamples, cpu)
				memSamples = append(memSamples, parts[len(parts)-1])
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	walBytes := walDiff(context.Background(), pool, walStart)
	elapsed := duration.Seconds()
	fmt.Printf("\n== load results (%s, %d samples/s)\n", *duration, len(ids))
	fmt.Printf("ticks: %d, write errors: %d\n", len(lags), writeErrors)
	fmt.Printf("ingest lag   p50 %s  p99 %s  max %s\n", pct(lags, 50), pct(lags, 99), pct(lags, 100))
	fmt.Printf("COPY time    p50 %s  p99 %s\n", pct(copyTimes, 50), pct(copyTimes, 99))
	fmt.Printf("1h chart (raw, 10 series, 10 s buckets) during load: p50 %s  p95 %s  (n=%d)\n", pct(queryTimes, 50), pct(queryTimes, 95), len(queryTimes))
	if len(cpuSamples) > 0 {
		fmt.Printf("container CPU: avg %.0f%%  max %.0f%%  (100%% = 1 core, limit 4 cores = 400%%)\n", avg(cpuSamples), maxOf(cpuSamples))
		fmt.Printf("container memory (last): %s\n", memSamples[len(memSamples)-1])
	}
	fmt.Printf("WAL: %s total, %s/h, extrapolated %s/day\n", bytesH(walBytes), bytesH(walBytes/elapsed*3600), bytesH(walBytes/elapsed*86400))
	return sizes(context.Background(), pool)
}

func chart(ctx context.Context, pool *pgxpool.Pool, table, tsCol, valCol string, series []int32, window, step time.Duration) error {
	q := fmt.Sprintf(`SELECT time_bucket($1::interval, %[2]s) AS b, series_id, avg(%[3]s)
		FROM %[1]s WHERE series_id = ANY($2) AND %[2]s > now() - $3::interval
		GROUP BY b, series_id ORDER BY b`, table, tsCol, valCol)
	rows, err := pool.Query(ctx, q, step, series, window)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
	}
	return rows.Err()
}

// compress converts all chunks except the newest into the columnstore and reports sizes.
func compress(ctx context.Context, pool *pgxpool.Pool) error {
	if err := sizes(ctx, pool); err != nil {
		return err
	}
	start := time.Now()
	_, err := pool.Exec(ctx, `
		SELECT count(compress_chunk(c, true))
		FROM show_chunks('metrics_raw', older_than => now() - INTERVAL '1 hour') c`)
	if err != nil {
		return err
	}
	fmt.Printf("compressed chunks older than 1 h in %s\n", time.Since(start).Round(time.Millisecond))
	return sizes(ctx, pool)
}

func sizes(ctx context.Context, pool *pgxpool.Pool) error {
	var rowsN int64
	var total, before, after *int64
	err := pool.QueryRow(ctx, `
		SELECT (SELECT approximate_row_count('metrics_raw')),
		       hypertable_size('metrics_raw'),
		       (SELECT sum(before_compression_total_bytes) FROM chunk_columnstore_stats('metrics_raw')),
		       (SELECT sum(after_compression_total_bytes) FROM chunk_columnstore_stats('metrics_raw'))`).Scan(&rowsN, &total, &before, &after)
	if err != nil {
		return err
	}
	fmt.Printf("metrics_raw: ~%d rows, total size %s", rowsN, bytesH(float64(deref(total))))
	if rowsN > 0 {
		fmt.Printf(" (%.1f bytes/sample overall)", float64(deref(total))/float64(rowsN))
	}
	if deref(before) > 0 {
		fmt.Printf("; columnstore chunks %s → %s (ratio %.1fx)", bytesH(float64(deref(before))), bytesH(float64(deref(after))), float64(deref(before))/float64(deref(after)))
	}
	fmt.Println()
	return nil
}

// backfill writes history for query tests: one node with 1 s data over `hours`, and a few
// series with 1 s data over `days` (for the 30 d chart from metrics_1m), then refreshes the
// continuous aggregates and compresses.
func backfill(ctx context.Context, pool *pgxpool.Pool) error {
	org, node := uuid.New(), uuid.New()
	var first int32
	err := pool.QueryRow(ctx, `
		INSERT INTO series (org_id, node_id, metric, labels)
		SELECT $1, $2, 'sermon_backfill_' || g, '{}' FROM generate_series(1, $3) g
		RETURNING id`, org, node, *nodeSeries).Scan(&first)
	if err != nil && !errors.Is(err, pgx.ErrTooManyRows) {
		return err
	}
	_, byNode, err := seriesIDs(ctx, pool)
	if err != nil {
		return err
	}
	nodeIDs := byNode[node.String()]
	longIDs := nodeIDs[:*longSeries]

	end := time.Now().Truncate(time.Hour)
	write := func(label string, ids []int32, from, to time.Time) error {
		start := time.Now()
		var total int64
		for chunkStart := from; chunkStart.Before(to); chunkStart = chunkStart.Add(time.Hour) {
			_, err := pool.Exec(ctx, `
				INSERT INTO metrics_raw (ts, series_id, value)
				SELECT t, s, 50 + 20 * sin(extract(epoch FROM t) / 600 + s) + random()
				FROM generate_series($1::timestamptz, $2::timestamptz - INTERVAL '1 second', INTERVAL '1 second') t,
				     unnest($3::int[]) s`, chunkStart, chunkStart.Add(time.Hour), ids)
			if err != nil {
				return err
			}
			total += int64(len(ids)) * 3600
		}
		fmt.Printf("backfill %s: %d rows in %s (%.0f rows/s)\n", label, total, time.Since(start).Round(time.Second), float64(total)/time.Since(start).Seconds())
		return nil
	}
	if err := write(fmt.Sprintf("1 node × %d series × %d h", len(nodeIDs), *hours), nodeIDs[*longSeries:], end.Add(-time.Duration(*hours)*time.Hour), end); err != nil {
		return err
	}
	if err := write(fmt.Sprintf("%d series × %d days", len(longIDs), *days), longIDs, end.Add(-time.Duration(*days)*24*time.Hour), end); err != nil {
		return err
	}

	for _, view := range []string{"metrics_1m", "metrics_1h"} {
		start := time.Now()
		if _, err := pool.Exec(ctx, fmt.Sprintf("CALL refresh_continuous_aggregate('%s', NULL, now() - INTERVAL '1 hour')", view)); err != nil {
			return err
		}
		fmt.Printf("refresh %s: %s\n", view, time.Since(start).Round(time.Second))
	}
	if err := compress(ctx, pool); err != nil {
		return err
	}
	fmt.Printf("backfill node: %s (series %d..%d)\n", node, nodeIDs[0], nodeIDs[len(nodeIDs)-1])
	return os.WriteFile("backfill-node.txt", []byte(node.String()), 0o600)
}

// queries measures chart latencies on the backfilled data (cold-ish and warm, 20 runs each).
func queries(ctx context.Context, pool *pgxpool.Pool) error {
	nodeBytes, err := os.ReadFile("backfill-node.txt")
	if err != nil {
		return fmt.Errorf("run backfill first: %w", err)
	}
	_, byNode, err := seriesIDs(ctx, pool)
	if err != nil {
		return err
	}
	ids := byNode[strings.TrimSpace(string(nodeBytes))]
	long, short := ids[:*longSeries], ids[*longSeries:*longSeries+10]

	type tc struct {
		name           string
		table, ts, val string
		series         []int32
		window, step   time.Duration
	}
	cases := []tc{
		{"1 h chart, raw (10 series, 10 s buckets)", "metrics_raw", "ts", "value", short, time.Hour, 10 * time.Second},
		{"24 h chart, raw (10 series, 1 min buckets)", "metrics_raw", "ts", "value", short, 24 * time.Hour, time.Minute},
		{"24 h chart, metrics_1m (10 series, 5 min buckets)", "metrics_1m", "bucket", "avg", short, 24 * time.Hour, 5 * time.Minute},
		{"30 d chart, metrics_1m (10 series, 1 h buckets)", "metrics_1m", "bucket", "avg", long, 30 * 24 * time.Hour, time.Hour},
		{"30 d chart, metrics_1h (10 series, 6 h buckets)", "metrics_1h", "bucket", "avg", long, 30 * 24 * time.Hour, 6 * time.Hour},
	}
	fmt.Println("== chart query latencies (20 runs each)")
	for _, c := range cases {
		var times []time.Duration
		for range 20 {
			start := time.Now()
			if err := chart(ctx, pool, c.table, c.ts, c.val, c.series, c.window, c.step); err != nil {
				return fmt.Errorf("%s: %w", c.name, err)
			}
			times = append(times, time.Since(start))
		}
		fmt.Printf("%-52s first %-8s p50 %-8s p95 %s\n", c.name, times[0].Round(time.Millisecond), pct(times, 50), pct(times, 95))
	}
	return nil
}

// rls checks whether row-level security works on the hypertable, its compressed chunks and the
// continuous aggregates for an unprivileged role.
func rls(ctx context.Context, pool *pgxpool.Pool) error {
	steps := []string{
		`DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'spike_app') THEN CREATE ROLE spike_app NOLOGIN; END IF; END $$`,
		`GRANT SELECT ON series, metrics_raw, metrics_1m, metrics_1h TO spike_app`,
		`CREATE OR REPLACE FUNCTION spike_org() RETURNS uuid LANGUAGE sql STABLE AS $$ SELECT NULLIF(current_setting('spike.org_id', true), '')::uuid $$`,
		`ALTER TABLE series ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE series FORCE ROW LEVEL SECURITY`,
		`DROP POLICY IF EXISTS tenant ON series`,
		`CREATE POLICY tenant ON series USING (org_id = spike_org())`,
	}
	for _, s := range steps {
		if _, err := pool.Exec(ctx, s); err != nil {
			return fmt.Errorf("%q: %w", s, err)
		}
	}
	report := func(name, sql string) {
		_, err := pool.Exec(ctx, sql)
		status := "ok"
		if err != nil {
			status = "FAILED: " + err.Error()
		}
		fmt.Printf("%-58s %s\n", name, status)
	}
	fmt.Println("== RLS on TimescaleDB objects")
	report("ENABLE RLS on hypertable metrics_raw", `ALTER TABLE metrics_raw ENABLE ROW LEVEL SECURITY`)
	report("policy on metrics_raw via series (EXISTS)", `DROP POLICY IF EXISTS tenant ON metrics_raw; CREATE POLICY tenant ON metrics_raw USING (EXISTS (SELECT 1 FROM series s WHERE s.id = series_id AND s.org_id = spike_org()))`)
	report("ENABLE RLS on continuous aggregate metrics_1m", `ALTER TABLE metrics_1m ENABLE ROW LEVEL SECURITY`)
	report("ENABLE RLS on materialized hypertable of metrics_1m", `DO $$ DECLARE t regclass; BEGIN
		SELECT format('%I.%I', materialization_hypertable_schema, materialization_hypertable_name)::regclass INTO t
		FROM timescaledb_information.continuous_aggregates WHERE view_name = 'metrics_1m';
		EXECUTE format('ALTER TABLE %s ENABLE ROW LEVEL SECURITY', t); END $$`)

	// What does the unprivileged role see?
	var org uuid.UUID
	if err := pool.QueryRow(ctx, "SELECT org_id FROM series ORDER BY id LIMIT 1").Scan(&org); err != nil {
		return err
	}
	count := func(name, tenant, sql string) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			fmt.Println(name, err)
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()
		_, _ = tx.Exec(ctx, "SET LOCAL ROLE spike_app")
		if tenant != "" {
			_, _ = tx.Exec(ctx, "SELECT set_config('spike.org_id', $1, true)", tenant)
		}
		var n int64
		start := time.Now()
		if err := tx.QueryRow(ctx, sql).Scan(&n); err != nil {
			fmt.Printf("%-58s error: %v\n", name, err)
			return
		}
		fmt.Printf("%-58s %d rows (%s)\n", name, n, time.Since(start).Round(time.Millisecond))
	}
	fmt.Println("== visibility for spike_app")
	count("series without tenant", "", "SELECT count(*) FROM series")
	count("series with tenant", org.String(), "SELECT count(*) FROM series")
	count("metrics_raw last 5 min without tenant", "", "SELECT count(*) FROM metrics_raw WHERE ts > now() - INTERVAL '5 minutes'")
	count("metrics_raw last 5 min with tenant", org.String(), "SELECT count(*) FROM metrics_raw WHERE ts > now() - INTERVAL '5 minutes'")
	count("metrics_raw compressed (>2 h old) without tenant", "", "SELECT count(*) FROM metrics_raw WHERE ts < now() - INTERVAL '2 hours'")
	count("metrics_1m without tenant", "", "SELECT count(*) FROM metrics_1m")
	count("metrics_1m with tenant", org.String(), "SELECT count(*) FROM metrics_1m")
	return nil
}

func walLSN(ctx context.Context, pool *pgxpool.Pool) string {
	var lsn string
	_ = pool.QueryRow(ctx, "SELECT pg_current_wal_lsn()::text").Scan(&lsn)
	return lsn
}

func walDiff(ctx context.Context, pool *pgxpool.Pool, start string) float64 {
	var diff float64
	_ = pool.QueryRow(ctx, "SELECT pg_wal_lsn_diff(pg_current_wal_lsn(), $1::pg_lsn)", start).Scan(&diff)
	return diff
}

func pct(d []time.Duration, p int) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	i := int(math.Ceil(float64(p)/100*float64(len(s)))) - 1
	return s[max(0, min(i, len(s)-1))].Round(time.Millisecond)
}

func avg(v []float64) float64 {
	var s float64
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func maxOf(v []float64) float64 {
	m := v[0]
	for _, x := range v {
		m = math.Max(m, x)
	}
	return m
}

func bytesH(b float64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	i := 0
	for b >= 1024 && i < len(units)-1 {
		b /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", b, units[i])
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
