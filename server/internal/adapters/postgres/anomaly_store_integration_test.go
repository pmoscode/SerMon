//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/adapters/timescale"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/anomalies"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/domain/anomaly"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// history writes four weeks of one value a minute for the series of node until end: a day
// cycle with noise, plus the spike of spiked from spikeFrom on.
func history(t *testing.T, store *timescale.Store, org tenant.OrgID, node identity.NodeID, end time.Time, spiked string, spikeFrom time.Time) {
	t.Helper()
	ctx := context.Background()
	start := end.Add(-28 * 24 * time.Hour)
	var batch []metrics.Aggregate
	flush := func() {
		if err := store.Write(ctx, org, batch, nil); err != nil {
			t.Fatal(err)
		}
		batch = batch[:0]
	}
	for m := start; m.Before(end); m = m.Add(time.Minute) {
		day := float64(m.Hour()*60+m.Minute()) / 1440
		noise := float64((m.Minute()*7 + m.Hour()*3) % 11)
		for mode, level := range map[string]float64{"user": 200_000, "system": 50_000} {
			v := level + 30_000*math.Sin(2*math.Pi*day) + 1000*noise
			if mode == spiked && !m.Before(spikeFrom) {
				v += 600_000
			}
			batch = append(batch, metrics.Aggregate{Node: node, Metric: "sermon_cpu_utilization_ratio", Labels: metrics.Labels{"mode": mode}, Start: m, Avg: v, Min: v, Max: v})
		}
		if len(batch) >= 20_000 {
			flush()
		}
	}
	flush()
}

// M4-23: four weeks of history and a spike: the worker computes the week's baselines, warms up
// and stores the spike's minutes; the rate of its node rises; the readers find it in the band and
// on top of "what was unusual?". A learning node is quiet, another organization sees nothing, a
// second instance leaves the organization alone.
func TestAnomalies_FindASpikeInFourWeeksOfHistory(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	db := open(t, f.appDSN)
	org := uuid.New()
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO organizations (id, name) VALUES ($1, 'Anomalies ' || $1::uuid::text)", org)
	web, fresh := uuid.New(), uuid.New()
	createNode(t, f.db.OwnerDSN, org, web)
	createNode(t, f.db.OwnerDSN, org, fresh)
	end := time.Now().UTC().Truncate(time.Minute).Add(-time.Minute)
	ts := timescale.New(db)
	history(t, ts, tenant.OrgID(org), identity.NodeID(web), end, "system", end.Add(-12*time.Minute))
	history(t, ts, tenant.OrgID(org), identity.NodeID(fresh), end, "user", end.Add(-12*time.Minute))
	ownerExec(t, f.db.OwnerDSN, "UPDATE series SET created_at = now() - interval '60 days' WHERE org_id = $1", org)
	ownerExec(t, f.db.OwnerDSN, "UPDATE nodes SET learning_since = now() - interval '60 days' WHERE id = $1", web)
	ownerExec(t, f.db.OwnerDSN, "UPDATE nodes SET learning_since = now() - interval '2 days' WHERE id = $1", fresh)
	ownerExec(t, f.db.OwnerDSN, "CALL refresh_continuous_aggregate('metrics_1m', NULL, NULL)")

	store := postgres.NewAnomalyStore(db)
	now := end.Add(3 * time.Minute) // ready: everything up to end
	config := anomalies.DefaultConfig("instance-a", slog.New(slog.NewTextHandler(t.Output(), nil)))
	config.Now = func() time.Time { return now }
	config.Backfill, config.Budget, config.Output = 168, 10*time.Minute, 12*time.Minute
	a := anomalies.NewWorker(store, ts, config)
	started := time.Now()
	a.Round(ctx)
	t.Logf("first round in %v: %d baselines, %d scored, %d notable", time.Since(started).Round(time.Millisecond),
		a.Stats().Baselines.Load(), a.Stats().Scored.Load(), a.Stats().Notable.Load())
	if a.Stats().Errors.Load() != 0 || a.Stats().Baselines.Load() < 168 {
		t.Fatalf("stats = %+v", a.Stats())
	}

	viewer := access.Caller{Principal: authz.Principal{User: identity.UserID(uuid.New()), Org: tenant.OrgID(org), Bindings: []authz.Binding{{Role: role.Viewer, AllGroups: true}}}}
	svc := anomalies.NewService(store, authz.NewPolicy(), func() time.Time { return now })
	band, err := svc.Node(ctx, viewer, identity.NodeID(web), end.Add(-time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	if band.LearningDay != 0 || len(band.Series) != 1 || band.Series[0].Labels["mode"] != "system" || len(band.Series[0].Intervals) != 1 {
		t.Fatalf("band = %+v", band)
	}
	iv := band.Series[0].Intervals[0]
	if iv.From.Before(end.Add(-12*time.Minute)) || iv.From.After(end.Add(-9*time.Minute)) || !iv.To.Equal(end) {
		t.Errorf("interval = %v – %v, spike %v – %v", iv.From, iv.To, end.Add(-12*time.Minute), end)
	}
	unusual, err := svc.Unusual(ctx, viewer, nil, end.Add(-time.Hour), now, 10)
	if err != nil || len(unusual) != 1 || unusual[0].Node != identity.NodeID(web) || unusual[0].Anomalous < 8 || unusual[0].MaxScore < 4 {
		t.Fatalf("unusual = %+v, %v", unusual, err)
	}
	// The learning node: no minutes, rate 0; its day of the learning phase.
	learning, err := svc.Node(ctx, viewer, identity.NodeID(fresh), end.Add(-time.Hour), now)
	if err != nil || learning.LearningDay != 3 || len(learning.Series) != 0 {
		t.Errorf("learning = %+v, %v", learning, err)
	}
	rate := func(node uuid.UUID) float64 {
		var v float64
		queryOwner(t, f.db.OwnerDSN, `SELECT coalesce(max(r.max), -1) FROM metrics_raw r JOIN series s ON s.id = r.series_id
			WHERE s.node_id = $1 AND s.metric = 'sermon_anomaly_rate_ratio'`, []any{node}, &v)
		return v
	}
	if r := rate(web); r != 500_000 {
		t.Errorf("rate of web = %v, want one of two series", r)
	}
	if r := rate(fresh); r != 0 {
		t.Errorf("rate of the learning node = %v", r)
	}

	// A viewer of another group of the organization sees neither the node nor its series.
	elsewhere := access.Caller{Principal: authz.Principal{User: identity.UserID(uuid.New()), Org: tenant.OrgID(org), Bindings: []authz.Binding{{Group: tenant.GroupID(uuid.New()), Role: role.Viewer}}}}
	if _, err := svc.Node(ctx, elsewhere, identity.NodeID(web), end.Add(-time.Hour), now); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("a viewer of another group: %v", err)
	}
	if got, err := svc.Unusual(ctx, elsewhere, nil, end.Add(-time.Hour), now, 10); err != nil || len(got) != 0 {
		t.Errorf("a viewer of another group: %+v, %v", got, err)
	}
	// Another organization: neither the readers nor the functions show A's series.
	other := access.Caller{Principal: authz.Principal{User: identity.UserID(uuid.New()), Org: tenant.OrgID(orgB), Bindings: []authz.Binding{{Role: role.Viewer, AllGroups: true}}}}
	if _, err := svc.Node(ctx, other, identity.NodeID(web), end.Add(-time.Hour), now); err == nil {
		t.Error("org B reads A's node")
	}
	if got, err := svc.Unusual(ctx, other, nil, end.Add(-time.Hour), now, 10); err != nil || len(got) != 0 {
		t.Errorf("org B's unusual = %+v, %v", got, err)
	}
	var ids []int64
	if err := db.InTenant(ctx, scopeFor(t, org), func(q *sqlcdb.Queries) error {
		rows, err := q.ScopedAnomalySeries(ctx, sqlcdb.ScopedAnomalySeriesParams{Metrics: []string{"sermon_cpu_utilization_ratio"}, AllGroups: true, MaxSeries: 100})
		for _, r := range rows {
			ids = append(ids, r.ID)
		}
		return err
	}); err != nil || len(ids) != 4 {
		t.Fatalf("series of A = %v, %v", ids, err)
	}
	if got, err := store.Minutes(ctx, scopeFor(t, orgB), ids, end.Add(-time.Hour), now, 100); err != nil || len(got) != 0 {
		t.Errorf("org B reads A's minutes through the function: %d, %v", len(got), err)
	}
	if err := store.SaveNotable(ctx, tenant.OrgID(orgB), []anomalies.Notable{{Series: ids[0], T: end.Add(-30 * time.Minute), Score: 99, Anomalous: true}}); err != nil {
		t.Fatal(err)
	}
	var forged int
	queryOwner(t, f.db.OwnerDSN, "SELECT count(*) FROM anomaly_points WHERE series_id = $1 AND score = 99", []any{ids[0]}, &forged)
	if forged != 0 {
		t.Error("org B wrote a minute for A's series")
	}

	// A second instance leaves the organization to the first while its lease runs; the first
	// again: every minute once.
	bConfig := config
	bConfig.Holder = "instance-b"
	b := anomalies.NewWorker(store, ts, bConfig)
	b.Round(ctx)
	if b.Stats().Scored.Load() != 0 {
		t.Errorf("instance b scored %d", b.Stats().Scored.Load())
	}
	a.Round(ctx)
	var dup int
	queryOwner(t, f.db.OwnerDSN, `SELECT count(*) FROM (SELECT r.ts FROM metrics_raw r JOIN series s ON s.id = r.series_id
		WHERE s.node_id = $1 AND s.metric = 'sermon_anomaly_rate_ratio' GROUP BY r.ts HAVING count(*) > 1) d`, []any{web}, &dup)
	if dup != 0 {
		t.Errorf("%d minutes of the rate written twice", dup)
	}
}

// Review M4-30: the baselines of an hour replace its others – a series no longer scored leaves
// no old row behind that would make the hour look stale for good; an hour without any is missing.
func TestAnomalies_BaselinesReplaceTheHour(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	db := open(t, f.appDSN)
	org := uuid.New()
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO organizations (id, name) VALUES ($1, 'Baselines ' || $1::uuid::text)", org)
	node := uuid.New()
	createNode(t, f.db.OwnerDSN, org, node)
	at := time.Now().UTC().Truncate(time.Minute).Add(-time.Hour)
	var points []metrics.Aggregate
	for _, mode := range []string{"user", "system"} {
		points = append(points, metrics.Aggregate{Node: identity.NodeID(node), Metric: "sermon_cpu_utilization_ratio", Labels: metrics.Labels{"mode": mode}, Start: at, Avg: 1, Min: 1, Max: 1})
	}
	if err := timescale.New(db).Write(ctx, tenant.OrgID(org), points, nil); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	if err := db.InTenant(ctx, scopeFor(t, org), func(q *sqlcdb.Queries) error {
		rows, err := q.ScopedAnomalySeries(ctx, sqlcdb.ScopedAnomalySeriesParams{Metrics: []string{"sermon_cpu_utilization_ratio"}, AllGroups: true, MaxSeries: 100})
		for _, r := range rows {
			ids = append(ids, r.ID)
		}
		return err
	}); err != nil || len(ids) != 2 {
		t.Fatalf("series = %v, %v", ids, err)
	}
	store := postgres.NewAnomalyStore(db)
	b := anomaly.Baseline{Median: 1, MAD: 0.1, Low: 0.5, High: 1.5, N: 60}
	old, later := at.Add(-9*24*time.Hour), at
	if err := store.SaveBaselines(ctx, tenant.OrgID(org), 5, old, map[int64]anomaly.Baseline{ids[0]: b, ids[1]: b}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveBaselines(ctx, tenant.OrgID(org), 5, later, map[int64]anomaly.Baseline{ids[0]: b}); err != nil {
		t.Fatal(err)
	}
	ages, err := store.BaselineAges(ctx, tenant.OrgID(org))
	if err != nil || !ages[5].Equal(later) {
		t.Fatalf("age of the hour = %v, %v; the stopped series kept its row", ages[5], err)
	}
	loaded, err := store.Baselines(ctx, tenant.OrgID(org), 5)
	if _, kept := loaded[ids[1]]; err != nil || kept || len(loaded) != 1 {
		t.Errorf("baselines = %v, %v", loaded, err)
	}
	if err := store.SaveBaselines(ctx, tenant.OrgID(org), 5, later, nil); err != nil {
		t.Fatal(err)
	}
	if ages, _ := store.BaselineAges(ctx, tenant.OrgID(org)); !ages[5].IsZero() {
		t.Errorf("an hour without baselines has the age %v", ages[5])
	}
}
