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
