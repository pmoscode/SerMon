package anomalies_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/anomalies"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	groupX = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000c1"))
	userU  = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000d1"))
)

func viewer() access.Caller {
	return access.Caller{Principal: authz.Principal{User: userU, Org: orgA, Bindings: []authz.Binding{{Group: groupX, Role: role.Viewer}}}}
}

// reader serves node1 (visible) with two series; node2 is outside the scope.
type reader struct {
	scope   tenant.Scope
	minutes []anomalies.Notable
	tops    []anomalies.Top
	nodes   []identity.NodeID
	limit   int
}

func (r *reader) Learning(_ context.Context, scope tenant.Scope, node identity.NodeID) (time.Time, bool, error) {
	r.scope = scope
	if node != node1 {
		return time.Time{}, false, nil
	}
	return now0.Add(-50 * time.Hour), true, nil
}

func (r *reader) ScopedSeries(_ context.Context, _ tenant.Scope, names []string, nodes []identity.NodeID, _ int) ([]anomalies.SeriesRef, error) {
	r.nodes = nodes
	if len(names) != 19 {
		return nil, errors.New("not the candidates")
	}
	return []anomalies.SeriesRef{
		{ID: 1, Node: node1, Metric: "sermon_cpu_utilization_ratio", Labels: map[string]string{"mode": "user"}},
		{ID: 2, Node: node1, Metric: "sermon_memory_available_bytes"},
		{ID: 3, Node: node1, Metric: "sermon_disk_busy_ratio", Labels: map[string]string{"device": "vda"}},
	}, nil
}

func (r *reader) Minutes(context.Context, tenant.Scope, []int64, time.Time, time.Time, int) ([]anomalies.Notable, error) {
	return r.minutes, nil
}

func (r *reader) Top(_ context.Context, _ tenant.Scope, _ []int64, _, _ time.Time, limit int) ([]anomalies.Top, error) {
	r.limit = limit
	return r.tops, nil
}

func minute(i int) time.Time { return now0.Add(-time.Hour + time.Duration(i)*time.Minute) }

// M4-23: the band merges anomalous minutes into stretches; series without notable minutes are
// left out; the learning phase counts in days.
func TestService_NodeBandAndLearningDay(t *testing.T) {
	r := &reader{minutes: []anomalies.Notable{
		{Series: 1, T: minute(1), Score: 5, Anomalous: true},
		{Series: 1, T: minute(2), Score: 7, Anomalous: true},
		{Series: 1, T: minute(3), Score: 3.5},
		{Series: 1, T: minute(5), Score: 6, Anomalous: true},
		{Series: 2, T: minute(4), Score: 3.2},
	}}
	svc := anomalies.NewService(r, authz.NewPolicy(), func() time.Time { return now0 })
	got, err := svc.Node(context.Background(), viewer(), node1, now0.Add(-time.Hour), now0)
	if err != nil {
		t.Fatal(err)
	}
	if got.LearningDay != 3 || len(got.Series) != 2 {
		t.Fatalf("node = %+v", got)
	}
	cpu := got.Series[0]
	want := []anomalies.Interval{{From: minute(1), To: minute(3)}, {From: minute(5), To: minute(6)}}
	if cpu.ID != 1 || cpu.MaxScore != 7 || len(cpu.Intervals) != 2 || cpu.Intervals[0] != want[0] || cpu.Intervals[1] != want[1] {
		t.Errorf("cpu = %+v", cpu)
	}
	if mem := got.Series[1]; mem.ID != 2 || mem.MaxScore != 3.2 || len(mem.Intervals) != 0 {
		t.Errorf("memory = %+v", mem)
	}
	if r.scope.AllGroups() || !r.scope.AllowsGroup(groupX) || r.scope.OrgID() != orgA {
		t.Errorf("scope = %+v", r.scope)
	}
}

func TestService_RightsRangesAndVisibility(t *testing.T) {
	r := &reader{}
	svc := anomalies.NewService(r, authz.NewPolicy(), func() time.Time { return now0 })
	ctx := context.Background()
	if _, err := svc.Node(ctx, viewer(), node2, now0.Add(-time.Hour), now0); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("another node: %v", err)
	}
	if _, err := svc.Node(ctx, access.Caller{Principal: authz.Principal{User: userU, Org: orgA}}, node1, now0.Add(-time.Hour), now0); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("without a role: %v", err)
	}
	for name, rng := range map[string][2]time.Time{
		"empty": {now0, now0}, "backwards": {now0, now0.Add(-time.Hour)},
		"too long": {now0.Add(-8 * 24 * time.Hour), now0}, "too old": {now0.Add(-32 * 24 * time.Hour), now0.Add(-31 * 24 * time.Hour)},
	} {
		if _, err := svc.Node(ctx, viewer(), node1, rng[0], rng[1]); !errors.Is(err, anomalies.ErrInvalidRange) {
			t.Errorf("%s: %v", name, err)
		}
		if _, err := svc.Unusual(ctx, viewer(), nil, rng[0], rng[1], 10); !errors.Is(err, anomalies.ErrInvalidRange) {
			t.Errorf("unusual %s: %v", name, err)
		}
	}
	other := node2
	if _, err := svc.Unusual(ctx, viewer(), &other, now0.Add(-time.Hour), now0, 10); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("unusual of another node: %v", err)
	}
	if got, err := svc.Node(ctx, viewer(), node1, now0.Add(-time.Hour), now0); err != nil || got.LearningDay != 3 || len(got.Series) != 0 {
		t.Errorf("without minutes: %+v, %v", got, err)
	}
}

// "What was unusual?": the highest scores first, the limit bounded; for one node or the scope.
func TestService_Unusual(t *testing.T) {
	r := &reader{tops: []anomalies.Top{{Series: 3, MaxScore: 4}, {Series: 1, MaxScore: 9, Anomalous: 12}, {Series: 99, MaxScore: 20}}}
	svc := anomalies.NewService(r, authz.NewPolicy(), func() time.Time { return now0 })
	ctx := context.Background()
	got, err := svc.Unusual(ctx, viewer(), nil, now0.Add(-time.Hour), now0, 500)
	if err != nil || len(got) != 2 || got[0].ID != 1 || got[0].Anomalous != 12 || got[1].ID != 3 || got[0].Metric != "sermon_cpu_utilization_ratio" {
		t.Fatalf("unusual = %+v, %v", got, err)
	}
	if r.limit != anomalies.MaxUnusual || r.nodes != nil {
		t.Errorf("limit %d, nodes %v", r.limit, r.nodes)
	}
	n := node1
	if _, err := svc.Unusual(ctx, viewer(), &n, now0.Add(-time.Hour), now0, 0); err != nil || r.limit != 1 || len(r.nodes) != 1 {
		t.Errorf("one node: limit %d, nodes %v, %v", r.limit, r.nodes, err)
	}
}
