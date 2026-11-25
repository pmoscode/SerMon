//go:build integration

package webapi_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
)

// M4-23: a viewer reads the band of a node of its group and "what was unusual?"; ranges longer
// than 7 days or older than 30 are INVALID_ARGUMENT, an unknown node is NOT_FOUND.
func TestAnomalies_AViewerReadsTheBand(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ctx := context.Background()
	mia := login(t, s, "mia@example.org")
	now := time.Now()
	band, err := mia.anomalies.GetNodeAnomalies(ctx, connect.NewRequest(&webv1.GetNodeAnomaliesRequest{
		NodeId: nodeAl, From: timestamppb.New(now.Add(-time.Hour)), To: timestamppb.New(now),
	}))
	if err != nil || band.Msg.GetLearningSince() == nil || len(band.Msg.GetSeries()) != 0 {
		t.Fatalf("band = %v, %v", band, err)
	}
	if _, err := mia.anomalies.ListUnusual(ctx, connect.NewRequest(&webv1.ListUnusualRequest{
		From: timestamppb.New(now.Add(-time.Hour)), To: timestamppb.New(now), Limit: 5,
	})); err != nil {
		t.Errorf("unusual: %v", err)
	}
	for name, rng := range map[string][2]time.Time{
		"8 days": {now.Add(-8 * 24 * time.Hour), now}, "40 days ago": {now.Add(-40 * 24 * time.Hour), now.Add(-39 * 24 * time.Hour)}, "backwards": {now, now.Add(-time.Hour)},
	} {
		_, err := mia.anomalies.GetNodeAnomalies(ctx, connect.NewRequest(&webv1.GetNodeAnomaliesRequest{NodeId: nodeAl, From: timestamppb.New(rng[0]), To: timestamppb.New(rng[1])}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := mia.anomalies.GetNodeAnomalies(ctx, connect.NewRequest(&webv1.GetNodeAnomaliesRequest{
		NodeId: "0193f5a0-0000-7000-8000-00000000ffff", From: timestamppb.New(now.Add(-time.Hour)), To: timestamppb.New(now),
	})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("unknown node: %v", err)
	}
}
