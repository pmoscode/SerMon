//go:build integration

package webapi_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/liveview"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

type processStream = *connect.ServerStreamForClient[webv1.WatchProcessesResponse]

func watchProcesses(t *testing.T, c webv1connect.LiveViewServiceClient, node string) processStream {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	stream, err := c.WatchProcesses(ctx, connect.NewRequest(&webv1.WatchProcessesRequest{NodeId: node}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	return stream
}

func nextFrame(t *testing.T, s processStream) *webv1.WatchProcessesResponse {
	t.Helper()
	if !s.Receive() {
		t.Fatalf("stream ended: %v", s.Err())
	}
	return s.Msg()
}

// The live view over the real stack (KONZEPT §9, M3-14): viewers of the node's group watch it
// (offline without an agent, then the hub's snapshots); the organization's switch hides command
// lines and ends running streams; a logout ends them with UNAUTHENTICATED.
func TestLiveView_ViewersWatchAndTheSwitchHidesCommandLines(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ctx := context.Background()
	ann := login(t, s, "ann@example.org") // admin of Alpha
	mia := login(t, s, "mia@example.org") // viewer of Alpha's group
	org, node := tenant.OrgID(uuid.MustParse(orgAlpha)), identity.NodeID(uuid.MustParse(nodeAl))
	cmdline := "nginx: worker process"
	deliver := func() {
		s.hub.Deliver(org, node, liveview.Snapshot{
			TakenAt: time.Now(), Summary: liveview.Summary{CPUCount: 2},
			Processes: []liveview.Process{{PID: 812, Name: "nginx", State: "S", Cmdline: &cmdline}},
		})
	}

	streams := []processStream{watchProcesses(t, ann.liveView, nodeAl), watchProcesses(t, mia.liveView, nodeAl)}
	for _, st := range streams {
		if f := nextFrame(t, st); !f.GetOffline() {
			t.Fatalf("first frame without an agent = %v, want offline", f)
		}
	}
	deliver()
	for _, st := range streams {
		p := nextFrame(t, st).GetSnapshot().GetProcesses()
		if len(p) != 1 || p[0].GetCmdline() != cmdline || p[0].GetCmdlineHidden() {
			t.Errorf("processes = %v", p)
		}
	}

	if _, err := mia.logSettings.SetLiveViewCommandLines(ctx, connect.NewRequest(&webv1.SetLiveViewCommandLinesRequest{HideCommandLines: true})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("a viewer sets the switch: %v", err)
	}
	if _, err := ann.logSettings.SetLiveViewCommandLines(ctx, connect.NewRequest(&webv1.SetLiveViewCommandLinesRequest{HideCommandLines: true})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = ann.logSettings.SetLiveViewCommandLines(ctx, connect.NewRequest(&webv1.SetLiveViewCommandLinesRequest{}))
	})
	// The running streams end normally at their next check; the clients reconnect.
	for _, st := range streams {
		for st.Receive() {
		}
		if st.Err() != nil {
			t.Errorf("stream after the switch: %v, want a normal end", st.Err())
		}
	}
	settings, err := ann.logSettings.GetLogSettings(ctx, connect.NewRequest(&webv1.GetLogSettingsRequest{}))
	if err != nil || !settings.Msg.GetHideCommandLines() {
		t.Errorf("GetLogSettings() = %v, %v", settings, err)
	}

	again := watchProcesses(t, mia.liveView, nodeAl)
	nextFrame(t, again) // offline
	// The hub takes at most one snapshot per half interval of an agent (M3-29).
	time.Sleep(liveview.Interval / 2)
	deliver()
	p := nextFrame(t, again).GetSnapshot().GetProcesses()
	if len(p) != 1 || p[0].Cmdline != nil || !p[0].GetCmdlineHidden() {
		t.Errorf("processes with the switch on = %v", p)
	}

	if _, err := mia.auth.Logout(ctx, connect.NewRequest(&webv1.LogoutRequest{})); err != nil {
		t.Fatal(err)
	}
	for again.Receive() {
	}
	if connect.CodeOf(again.Err()) != connect.CodeUnauthenticated {
		t.Errorf("stream after the logout: %v, want UNAUTHENTICATED", again.Err())
	}
}
