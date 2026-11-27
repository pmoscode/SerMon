//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/loki"
	"github.com/pmoscode/SerMon/server/internal/adapters/loki/lokitest"
	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/ingest"
	"github.com/pmoscode/SerMon/server/internal/app/logs"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/logql"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// countOrg is an organization with two nodes for the log counts (V1-24).
type countOrg struct {
	org   tenant.OrgID
	a, b  identity.NodeID
	nodes []identity.NodeID
}

func seedCountOrg(t *testing.T, ownerDSN string) countOrg {
	t.Helper()
	c := countOrg{org: tenant.OrgID(uuid.New()), a: identity.NodeID(uuid.New()), b: identity.NodeID(uuid.New())}
	c.nodes = []identity.NodeID{c.a, c.b}
	group := uuid.New()
	ownerExec(t, ownerDSN, "INSERT INTO organizations (id, name) VALUES ($1, 'Counts ' || $1::uuid::text)", uuid.UUID(c.org))
	ownerExec(t, ownerDSN, "INSERT INTO node_groups (id, org_id, name) VALUES ($1, $2, 'web')", group, uuid.UUID(c.org))
	for _, n := range c.nodes {
		ownerExec(t, ownerDSN, `INSERT INTO nodes (id, org_id, group_id, hostname, machine_id, os_id, os_version, kernel_release, architecture, cpu_count, memory_bytes)
			VALUES ($1, $2, $3, $4, $5, 'debian', '13', '6.12.0', 'amd64', 2, 1073741824)`,
			uuid.UUID(n), uuid.UUID(c.org), group, "host-"+n.String()[:8], strings.ReplaceAll(n.String(), "-", ""))
	}
	return c
}

func count(seq uint64, minute time.Time, source ingest.LogSource, severity uint8, lines uint64) ingest.LogCount {
	return ingest.LogCount{Seq: seq, Minute: minute, Source: source, Severity: severity, Lines: lines}
}

// totals sums the series per severity and point.
func totals(series []logs.CountSeries) map[string]float64 {
	out := map[string]float64{}
	for _, s := range series {
		for _, p := range s.Points {
			out[fmt.Sprintf("%s@%s", s.Labels["severity"], p.At.UTC().Format(time.RFC3339))] += p.Value
		}
	}
	return out
}

// The counts per step: a bucket goes to the point at or after it (Loki's (t − step, t]), the
// hourly table answers steps of whole hours with the same numbers; sources and severities
// filter, times off the minute are rounded up by the database.
func TestLogCounts_PerStepFromTheMinutesOrTheHours(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := seedCountOrg(t, f.db.OwnerDSN)
	store := postgres.NewLogCountStore(open(t, f.appDSN))
	day := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
	at := func(h, m int) time.Time { return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute) }
	if err := store.CountLogs(ctx, c.org, c.a, []ingest.LogCount{
		count(1, at(10, 0), ingest.LogSourceJournal, 4, 3),                   // 09:59–10:00
		count(1, at(10, 1), ingest.LogSourceJournal, 4, 2),                   // 10:00–10:01
		count(1, at(10, 1).Add(-30*time.Second), ingest.LogSourceFile, 7, 5), // rounded up to 10:01
		count(2, at(11, 0), ingest.LogSourceContainer, 5, 1),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CountLogs(ctx, c.org, c.b, []ingest.LogCount{count(1, at(10, 1), ingest.LogSourceJournal, 4, 10)}); err != nil {
		t.Fatal(err)
	}
	read := func(q logs.CountQuery) map[string]float64 {
		t.Helper()
		series, err := store.Counts(ctx, c.org, q)
		if err != nil {
			t.Fatal(err)
		}
		return totals(series)
	}
	key := func(sev string, at time.Time) string { return sev + "@" + at.Format(time.RFC3339) }
	got := read(logs.CountQuery{Nodes: c.nodes, After: at(9, 0), Until: at(12, 0), Step: time.Minute})
	want := map[string]float64{key("error", at(10, 0)): 3, key("error", at(10, 1)): 12, key("info", at(10, 1)): 5, key("warning", at(11, 0)): 1}
	if !maps.Equal(got, want) {
		t.Errorf("per minute = %v, want %v", got, want)
	}
	got = read(logs.CountQuery{Nodes: []identity.NodeID{c.a}, After: at(9, 0), Until: at(12, 0), Step: time.Hour})
	want = map[string]float64{key("error", at(10, 0)): 3, key("error", at(11, 0)): 2, key("info", at(11, 0)): 5, key("warning", at(11, 0)): 1}
	if !maps.Equal(got, want) {
		t.Errorf("per hour = %v, want %v", got, want)
	}
	got = read(logs.CountQuery{Nodes: []identity.NodeID{c.a}, After: at(8, 0), Until: at(12, 0), Step: 4 * time.Hour})
	want = map[string]float64{key("error", at(12, 0)): 5, key("info", at(12, 0)): 5, key("warning", at(12, 0)): 1}
	if !maps.Equal(got, want) {
		t.Errorf("4 hours = %v, want %v", got, want)
	}
	// After is exclusive, Until inclusive.
	got = read(logs.CountQuery{Nodes: []identity.NodeID{c.a}, After: at(10, 0), Until: at(10, 1), Step: time.Minute})
	if want = (map[string]float64{key("error", at(10, 1)): 2, key("info", at(10, 1)): 5}); !maps.Equal(got, want) {
		t.Errorf("(10:00, 10:01] = %v, want %v", got, want)
	}
	got = read(logs.CountQuery{
		Nodes: c.nodes, After: at(9, 0), Until: at(12, 0), Step: 7 * time.Minute,
		Sources: []logql.Source{logql.SourceJournal, logql.SourceContainer}, Severities: []logql.Severity{logql.SeverityError},
	})
	// 10:00 and 10:01 lie in one 7-minute step since the epoch.
	point := time.Unix((at(10, 0).Unix()/420+1)*420, 0).UTC()
	if point.Before(at(10, 1)) {
		t.Fatalf("10:00 and 10:01 in different steps of 7 minutes: %v", point)
	}
	if want = (map[string]float64{key("error", point): 15}); !maps.Equal(got, want) {
		t.Errorf("filtered, 7 minutes = %v, want %v", got, want)
	}
	got = read(logs.CountQuery{Nodes: c.nodes, After: at(9, 0), Until: at(12, 0), Step: time.Hour, Sources: []logql.Source{logql.SourceFile}})
	if want = (map[string]float64{key("info", at(11, 0)): 5}); !maps.Equal(got, want) {
		t.Errorf("files only = %v, want %v", got, want)
	}
	if _, err := store.Counts(ctx, c.org, logs.CountQuery{Nodes: c.nodes, After: at(9, 0), Until: at(12, 0), Step: 90 * time.Second}); err == nil {
		t.Error("a step of 90 s was accepted")
	}
}

// A batch resent after a lost acknowledgement is counted once; an older one is skipped, a newer
// one counted, also into a compressed chunk (lines from an agent's buffer days later).
func TestLogCounts_AResendIsCountedOnceAlsoIntoCompressedChunks(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := seedCountOrg(t, f.db.OwnerDSN)
	store := postgres.NewLogCountStore(open(t, f.appDSN))
	minute := time.Now().UTC().Truncate(time.Hour).Add(-3 * 24 * time.Hour)
	add := func(seq uint64) {
		t.Helper()
		if err := store.CountLogs(ctx, c.org, c.a, []ingest.LogCount{count(seq, minute, ingest.LogSourceJournal, 7, 2)}); err != nil {
			t.Fatal(err)
		}
	}
	add(5)
	add(5)
	add(4)
	ownerExec(t, f.db.OwnerDSN, "SELECT count(compress_chunk(c, recompress => true)) FROM show_chunks('log_counts_1m') c")
	ownerExec(t, f.db.OwnerDSN, "SELECT count(compress_chunk(c, recompress => true)) FROM show_chunks('log_counts_1h') c")
	add(6)
	for _, step := range []time.Duration{time.Minute, time.Hour} {
		series, err := store.Counts(ctx, c.org, logs.CountQuery{Nodes: c.nodes, After: minute.Add(-time.Hour), Until: minute, Step: step})
		if err != nil {
			t.Fatal(err)
		}
		if got := totals(series); len(got) != 1 || got["info@"+minute.Format(time.RFC3339)] != 4 {
			t.Errorf("step %v: %v, want 4 lines (seq 5 and 6)", step, got)
		}
	}
}

// Tenant isolation: an organization neither counts for a node of another nor reads its counts;
// without a tenant the functions refuse or return nothing, and sermon_app can't reach the tables.
func TestLogCounts_StayInTheirOrganization(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	mine, theirs := seedCountOrg(t, f.db.OwnerDSN), seedCountOrg(t, f.db.OwnerDSN)
	store := postgres.NewLogCountStore(open(t, f.appDSN))
	minute := time.Now().UTC().Truncate(time.Minute)
	if err := store.CountLogs(ctx, theirs.org, theirs.a, []ingest.LogCount{count(1, minute, ingest.LogSourceJournal, 4, 9)}); err != nil {
		t.Fatal(err)
	}
	if err := store.CountLogs(ctx, mine.org, theirs.a, []ingest.LogCount{count(2, minute, ingest.LogSourceJournal, 4, 1)}); err == nil {
		t.Error("counted for a node of another organization")
	}
	q := logs.CountQuery{Nodes: append([]identity.NodeID{mine.a}, theirs.nodes...), After: minute.Add(-time.Hour), Until: minute, Step: time.Minute}
	if series, err := store.Counts(ctx, mine.org, q); err != nil || len(series) != 0 {
		t.Errorf("read the counts of another organization: %v, %v", series, err)
	}
	if series, err := store.Counts(ctx, theirs.org, q); err != nil || totals(series)["error@"+minute.Format(time.RFC3339)] != 9 {
		t.Errorf("own counts: %v, %v", series, err)
	}
	if n, err := store.ExpireLogCounts(ctx, mine.org, minute.Add(time.Hour)); err != nil || n != 0 {
		t.Errorf("expired %d of another organization, %v", n, err)
	}

	app, err := pgx.Connect(ctx, f.appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close(ctx) }()
	if _, err := app.Exec(ctx, "SELECT log_counts_add($1, '{3}', ARRAY[now()], '{1}', '{4}', '{1}')", uuid.UUID(theirs.a)); err == nil {
		t.Error("log_counts_add without a tenant")
	}
	if _, err := app.Exec(ctx, "SELECT log_counts_expire(now())"); err == nil {
		t.Error("log_counts_expire without a tenant")
	}
	var n int
	if err := app.QueryRow(ctx, "SELECT count(*) FROM log_counts_range($1, now() - interval '1 day', now() + interval '1 hour', 60, '{}', '{}')",
		[]uuid.UUID{uuid.UUID(theirs.a)}).Scan(&n); err != nil || n != 0 {
		t.Errorf("log_counts_range without a tenant: %d rows, %v", n, err)
	}
	for _, sql := range []string{
		"SELECT count(*) FROM log_count_marks", "INSERT INTO log_count_marks (org_id, node_id, seq) VALUES ($1, $2, 99)",
		"UPDATE log_counts_1m SET lines = 1", "DELETE FROM log_counts_1h",
	} {
		var args []any
		if strings.Contains(sql, "$1") {
			args = []any{uuid.UUID(theirs.org), uuid.UUID(theirs.a)}
		}
		if _, err := app.Exec(ctx, sql, args...); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s: %v, want permission denied", sql, err)
		}
	}
}

// The retention of an organization (M5-30) deletes its counts up to the boundary in both tables,
// nothing younger and nothing of others.
func TestLogCounts_ExpireDeletesTheOrganizationsOlderCounts(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	mine, theirs := seedCountOrg(t, f.db.OwnerDSN), seedCountOrg(t, f.db.OwnerDSN)
	store := postgres.NewLogCountStore(open(t, f.appDSN))
	now := time.Now().UTC().Truncate(time.Hour)
	old, young := now.Add(-3*24*time.Hour), now.Add(-time.Hour)
	for _, o := range []countOrg{mine, theirs} {
		if err := store.CountLogs(ctx, o.org, o.a, []ingest.LogCount{
			count(1, old, ingest.LogSourceJournal, 4, 1), count(1, young, ingest.LogSourceJournal, 4, 2),
		}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := store.ExpireLogCounts(ctx, mine.org, now.Add(-2*24*time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("expired %d rows, %v; want the old one in each table", n, err)
	}
	for _, step := range []time.Duration{time.Minute, time.Hour} {
		q := logs.CountQuery{Nodes: []identity.NodeID{mine.a, theirs.a}, After: old.Add(-time.Hour), Until: now, Step: step}
		got, _ := store.Counts(ctx, mine.org, q)
		if tt := totals(got); len(tt) != 1 || tt["error@"+young.Format(time.RFC3339)] != 2 {
			t.Errorf("step %v, mine: %v", step, tt)
		}
		got, _ = store.Counts(ctx, theirs.org, q)
		if tt := totals(got); len(tt) != 2 {
			t.Errorf("step %v, theirs: %v", step, tt)
		}
	}
}

// Both tables keep the counts as long as Loki keeps the lines and compress them after a day.
func TestLogCounts_PoliciesFollowLoki(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, f.db.OwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	rows, err := conn.Query(ctx, `SELECT hypertable_name || ':' || proc_name || ':' || coalesce(config->>'drop_after', config->>'compress_after')
		FROM timescaledb_information.jobs WHERE hypertable_name LIKE 'log_counts_%' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"log_counts_1h:policy_compression:1 day", "log_counts_1h:policy_retention:14 days",
		"log_counts_1m:policy_compression:1 day", "log_counts_1m:policy_retention:14 days",
	}
	if !slices.Equal(got, want) {
		t.Errorf("jobs = %v, want %v", got, want)
	}
}

// V1-24: the counts give the same numbers as Loki's count_over_time for the same lines, filters
// and points – lines on the boundaries of minutes and hours included.
func TestLogCounts_EqualLokisCountsOfTheSameLines(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := seedCountOrg(t, f.db.OwnerDSN)
	counts := postgres.NewLogCountStore(open(t, f.appDSN))
	client := loki.New(lokitest.Shared(t))
	writer, reader := loki.NewWriter(client), loki.NewReader(client)

	// Within the last 2 hours: Loki asks its ingesters (where fresh lines still are) only for
	// queries within query_ingesters_within (3 h); older lines would be flushed in production.
	now := time.Now().UTC()
	hour := now.Truncate(time.Hour)
	start, end := hour.Add(-time.Hour), now.Add(-time.Minute)
	sources := []ingest.LogSource{ingest.LogSourceJournal, ingest.LogSourceFile, ingest.LogSourceContainer}
	var lines int
	for i, node := range c.nodes {
		var entries []ingest.LogEntry
		for at, k := start, 0; at.Before(end); k++ {
			entries = append(entries, ingest.LogEntry{
				At: at, Source: sources[(k+i)%3], Unit: "u" + fmt.Sprint(k%4), Severity: uint8((k * 7) % 10), Message: fmt.Sprintf("line %d", k),
			})
			// Steps of 17 s and 43 s, now and then exactly on a minute or hour.
			at = at.Add(time.Duration(17+26*(k%2)) * time.Second)
			if k%11 == 0 {
				at = at.Truncate(time.Minute).Add(time.Minute)
			}
		}
		entries = append(entries, ingest.LogEntry{At: end, Source: ingest.LogSourceJournal, Severity: 4, Message: "last"})
		// On the hour and just after it, in the order Loki takes them.
		entries = append(entries, ingest.LogEntry{At: hour, Source: ingest.LogSourceFile, Severity: 4, Message: "on the hour"},
			ingest.LogEntry{At: hour.Add(time.Nanosecond), Source: ingest.LogSourceFile, Severity: 4, Message: "just after"})
		slices.SortFunc(entries, func(a, b ingest.LogEntry) int { return a.At.Compare(b.At) })
		if err := writer.Write(ctx, c.org, node, entries); err != nil {
			t.Fatal(err)
		}
		if err := counts.CountLogs(ctx, c.org, node, ingest.Counts(1, entries)); err != nil {
			t.Fatal(err)
		}
		lines += len(entries)
	}
	cases := []struct {
		step   time.Duration
		filter logql.Filter
	}{
		{time.Minute, logql.Filter{}},
		{2 * time.Minute, logql.Filter{}},
		{7 * time.Minute, logql.Filter{Sources: []logql.Source{logql.SourceFile, logql.SourceContainer}}},
		{15 * time.Minute, logql.Filter{Severities: logql.Severities[:5]}},
		{time.Hour, logql.Filter{}},
		{2 * time.Hour, logql.Filter{Sources: []logql.Source{logql.SourceJournal}, Severities: []logql.Severity{logql.SeverityError, logql.SeverityUnknown}}},
	}
	for _, tc := range cases {
		// Multiples of the step since the epoch, around the lines.
		seconds := int64(tc.step / time.Second)
		after := time.Unix((start.Unix()/seconds-1)*seconds, 0).UTC()
		until := time.Unix((end.Unix()/seconds+1)*seconds, 0).UTC()
		tc.filter.Nodes = c.nodes
		q, err := logql.CountBySeverity(tc.filter, tc.step)
		if err != nil {
			t.Fatal(err)
		}
		var fromLoki map[string]float64
		deadline := time.Now().Add(30 * time.Second)
		for {
			// As the log service asks: from a point early, which is dropped (Loki 3.7 sometimes
			// leaves out lines of its first point).
			series, err := reader.Counts(ctx, c.org, q, after, until, tc.step)
			if err != nil {
				t.Fatal(err)
			}
			for i := range series {
				series[i].Points = slices.DeleteFunc(series[i].Points, func(p logs.CountPoint) bool { return !p.At.After(after) })
			}
			fromLoki = totals(series)
			if tc.filter.Sources != nil || tc.filter.Severities != nil || sum(fromLoki) == float64(lines) || time.Now().After(deadline) {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		series, err := counts.Counts(ctx, c.org, logs.CountQuery{
			Nodes: c.nodes, Sources: tc.filter.Sources, Severities: tc.filter.Severities, After: after, Until: until, Step: tc.step,
		})
		if err != nil {
			t.Fatal(err)
		}
		fromCounts := totals(series)
		if len(fromLoki) == 0 || !maps.Equal(fromLoki, fromCounts) {
			t.Errorf("step %v, filter %+v: Loki %d points (%v lines), counts %d points (%v lines)\nLoki   %v\ncounts %v",
				tc.step, tc.filter, len(fromLoki), sum(fromLoki), len(fromCounts), sum(fromCounts), fromLoki, fromCounts)
		}
	}
}

func sum(m map[string]float64) float64 {
	var s float64
	for _, v := range m {
		s += v
	}
	return s
}
