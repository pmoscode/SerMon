//go:build integration

package webapi_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
)

func TestFleet_AViewerOfTheGroupSeesNodeFiguresHistoryAndLiveValues(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ctx := context.Background()
	org, group, node := tenant.OrgID(uuid.MustParse(orgAlpha)), tenant.GroupID(uuid.MustParse(groupAl)), identity.NodeID(uuid.MustParse(nodeAl))
	start := time.Now().Add(-30 * time.Minute).Truncate(metrics.Window)
	if err := s.metrics.Write(ctx, org, []metrics.Aggregate{
		{Node: node, Metric: "sermon_load_average", Labels: metrics.Labels{"period": "1m"}, Start: start, Avg: 0.5, Min: 0.25, Max: 1},
	}, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s.ring.Add(org, group, node, "sermon_memory_utilization_ratio", nil, now.Add(-2*time.Second), 420_000)
	s.ring.Add(org, group, node, "sermon_filesystem_size_bytes", metrics.Labels{"mountpoint": "/"}, now.Add(-2*time.Second), 1e9)
	mia := login(t, s, "mia@example.org")

	summary, err := mia.fleet.GetFleetSummary(ctx, connect.NewRequest(&webv1.GetFleetSummaryRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	var found *webv1.NodeSummary
	for _, n := range summary.Msg.GetNodes() {
		if n.GetNodeId() == nodeAl {
			found = n
		}
	}
	if found == nil || found.GetOnline() || found.GetFigures().GetMemoryUsedRatio() != 0.42 {
		t.Fatalf("summary = %v", summary.Msg)
	}

	node1, err := mia.node.GetNode(ctx, connect.NewRequest(&webv1.GetNodeRequest{NodeId: nodeAl}))
	if err != nil || node1.Msg.GetNode().GetHostname() != "alpha-01" || node1.Msg.GetNode().GetGroupId() != groupAl {
		t.Fatalf("GetNode() = %v, %v", node1, err)
	}
	filesystems, err := mia.node.ListFilesystems(ctx, connect.NewRequest(&webv1.ListFilesystemsRequest{NodeId: nodeAl}))
	if err != nil || len(filesystems.Msg.GetFilesystems()) != 1 || filesystems.Msg.GetFilesystems()[0].GetSizeBytes() != 1e9 {
		t.Fatalf("ListFilesystems() = %v, %v", filesystems, err)
	}

	history, err := mia.metrics.QueryMetrics(ctx, connect.NewRequest(&webv1.QueryMetricsRequest{
		Selector: &webv1.MetricSelector{Metric: "sermon_load_average", NodeIds: []string{nodeAl}, Labels: map[string]string{"period": "1m"}},
		From:     timestamppb.New(now.Add(-time.Hour)), To: timestamppb.New(now),
	}))
	if err != nil || len(history.Msg.GetSeries()) != 1 || history.Msg.GetSeries()[0].GetPoints()[0].GetMax() != 1 {
		t.Fatalf("QueryMetrics() = %v, %v", history, err)
	}
	if history.Msg.GetStep().AsDuration() != 10*time.Second {
		t.Errorf("step = %v, want 10 s", history.Msg.GetStep().AsDuration())
	}

	liveCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	stream, err := mia.metrics.WatchMetrics(liveCtx, connect.NewRequest(&webv1.WatchMetricsRequest{
		Selector: &webv1.MetricSelector{Metric: "sermon_memory_utilization_ratio", NodeIds: []string{nodeAl}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if !stream.Receive() || len(stream.Msg().GetSeries()) != 1 || stream.Msg().GetSeries()[0].GetPoints()[0].GetAvg() != 420_000 {
		t.Fatalf("first live message = %v, %v", stream.Msg(), stream.Err())
	}

	catalog, err := mia.metrics.ListMetrics(ctx, connect.NewRequest(&webv1.ListMetricsRequest{}))
	if err != nil || len(catalog.Msg.GetMetrics()) < 70 {
		t.Errorf("ListMetrics() = %d metrics, %v", len(catalog.Msg.GetMetrics()), err)
	}
}

// M4-26: the capacity planning shows the file systems with their forecast and the daily trends
// of the nodes a viewer may view – of another organization nothing, even with data.
func TestFleet_CapacityOfTheVisibleNodes(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ctx := context.Background()
	org, group, node := tenant.OrgID(uuid.MustParse(orgAlpha)), tenant.GroupID(uuid.MustParse(groupAl)), identity.NodeID(uuid.MustParse(nodeAl))
	now := time.Now()
	root := metrics.Labels{"mountpoint": "/"}
	s.ring.Add(org, group, node, "sermon_filesystem_size_bytes", root, now.Add(-2*time.Second), 100e9)
	s.ring.Add(org, group, node, "sermon_filesystem_avail_bytes", root, now.Add(-2*time.Second), 30e9)
	s.ring.Add(org, group, node, "sermon_filesystem_utilization_ratio", root, now.Add(-2*time.Second), 700_000)
	var history []metrics.Aggregate
	for hour := 20; hour >= 1; hour-- {
		at := now.Add(-time.Duration(hour) * time.Hour).Truncate(metrics.Window)
		v := 30e9 + float64(hour)*1e9
		history = append(history,
			metrics.Aggregate{Node: node, Metric: "sermon_filesystem_avail_bytes", Labels: root, Start: at, Avg: v, Min: v, Max: v},
			metrics.Aggregate{Node: node, Metric: "sermon_memory_utilization_ratio", Labels: metrics.Labels{}, Start: at, Avg: 500_000, Min: 500_000, Max: 500_000})
	}
	if err := s.metrics.Write(ctx, org, history, nil); err != nil {
		t.Fatal(err)
	}
	// The forecast reads 1-min values (24 h are just beyond the raw ones), the trends hourly ones.
	ownerSQL(t, "CALL refresh_continuous_aggregate('metrics_1m', NULL, NULL)", "CALL refresh_continuous_aggregate('metrics_1h', NULL, NULL)")
	mia := login(t, s, "mia@example.org")
	res, err := mia.capacity.GetCapacity(ctx, connect.NewRequest(&webv1.GetCapacityRequest{}))
	if err != nil || len(res.Msg.GetFilesystems()) != 1 {
		t.Fatalf("GetCapacity() = %v, %v", res, err)
	}
	fs := res.Msg.GetFilesystems()[0]
	if fs.GetNodeId() != nodeAl || fs.GetHostname() != "alpha-01" || fs.GetMountpoint() != "/" || fs.GetSizeBytes() != 100e9 || fs.GetFullAt() == nil {
		t.Errorf("file system = %v", fs)
	}
	if until := time.Until(fs.GetFullAt().AsTime()); until < 25*time.Hour || until > 35*time.Hour {
		t.Errorf("full in %v, want about 30 h", until)
	}
	if trends := res.Msg.GetTrends(); len(trends) != 1 || len(trends[0].GetMemory()) == 0 || trends[0].GetMemory()[0].GetValue() != 0.5 {
		t.Errorf("trends = %v", trends)
	}
	bea := login(t, s, "bea@example.org")
	if other, err := bea.capacity.GetCapacity(ctx, connect.NewRequest(&webv1.GetCapacityRequest{})); err != nil || len(other.Msg.GetFilesystems())+len(other.Msg.GetTrends()) != 0 {
		t.Errorf("org Beta sees %v, %v", other, err)
	}
}

// M4-27: the heatmap of a viewer's nodes – CPU busy per column, rounded; another organization
// sees none of them; ranges outside 10 minutes to 30 days are INVALID_ARGUMENT.
func TestFleet_HeatmapOfTheVisibleNodes(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ctx := context.Background()
	org, node := tenant.OrgID(uuid.MustParse(orgAlpha)), identity.NodeID(uuid.MustParse(nodeAl))
	to := time.Now().Truncate(time.Minute)
	from := to.Add(-20 * time.Minute)
	at := from.Add(5 * time.Minute)
	if err := s.metrics.Write(ctx, org, []metrics.Aggregate{
		{Node: node, Metric: "sermon_cpu_utilization_ratio", Labels: metrics.Labels{"mode": "idle"}, Start: at, Avg: 666_666, Min: 666_666, Max: 666_666},
	}, nil); err != nil {
		t.Fatal(err)
	}
	mia := login(t, s, "mia@example.org")
	res, err := mia.heatmap.GetHeatmap(ctx, connect.NewRequest(&webv1.GetHeatmapRequest{Kind: webv1.HeatmapKind_HEATMAP_KIND_CPU, From: timestamppb.New(from), To: timestamppb.New(to)}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Msg.GetColumns() != 120 || res.Msg.GetStep().AsDuration() != 10*time.Second {
		t.Fatalf("heatmap = %d columns of %v", res.Msg.GetColumns(), res.Msg.GetStep().AsDuration())
	}
	// Other tests of the package add nodes to the group: its node among them.
	var row *webv1.HeatmapRow
	for _, r := range res.Msg.GetRows() {
		if r.GetNodeId() == nodeAl {
			row = r
		}
	}
	if row == nil {
		t.Fatalf("no row of %s in %v", nodeAl, res.Msg.GetRows())
	}
	i := int(at.Sub(res.Msg.GetStart().AsTime()) / (10 * time.Second))
	if row.GetNodeId() != nodeAl || row.GetHostname() != "alpha-01" || len(row.GetValues()) != 120 || row.GetValues()[i] != 0.333 || row.GetValues()[i+1] != -1 {
		t.Errorf("row = %v (cell %d)", row, i)
	}
	bea := login(t, s, "bea@example.org")
	if other, err := bea.heatmap.GetHeatmap(ctx, connect.NewRequest(&webv1.GetHeatmapRequest{Kind: webv1.HeatmapKind_HEATMAP_KIND_CPU, From: timestamppb.New(from), To: timestamppb.New(to)})); err != nil || len(other.Msg.GetRows()) != 0 {
		t.Errorf("org Beta sees %v, %v", other, err)
	}
	for name, req := range map[string]*webv1.GetHeatmapRequest{
		"5 minutes": {Kind: webv1.HeatmapKind_HEATMAP_KIND_CPU, From: timestamppb.New(to.Add(-5 * time.Minute)), To: timestamppb.New(to)},
		"31 days":   {Kind: webv1.HeatmapKind_HEATMAP_KIND_CPU, From: timestamppb.New(to.Add(-31 * 24 * time.Hour)), To: timestamppb.New(to)},
		"no kind":   {From: timestamppb.New(from), To: timestamppb.New(to)},
		"unknown":   {Kind: 9, From: timestamppb.New(from), To: timestamppb.New(to)},
	} {
		if _, err := mia.heatmap.GetHeatmap(ctx, connect.NewRequest(req)); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
}
