package webapi

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/liveview"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
)

// M3-28: all viewers of a snapshot send one message, built once.
func TestLiveView_ASnapshotIsBuiltOnceForAllViewers(t *testing.T) {
	h := NewLiveViewHandler(nil, nil, slog.New(slog.DiscardHandler))
	node, other := identity.NodeID(uuid.New()), identity.NodeID(uuid.New())
	snap := &liveview.Snapshot{Processes: []liveview.Process{{PID: 1, Name: "init"}}}
	first := h.message(node, liveview.Frame{Snapshot: snap})
	if again := h.message(node, liveview.Frame{Snapshot: snap}); again != first {
		t.Error("the same snapshot was built twice")
	}
	next := &liveview.Snapshot{Processes: []liveview.Process{{PID: 2, Name: "sshd"}}}
	if msg := h.message(node, liveview.Frame{Snapshot: next}); msg == first || msg.GetSnapshot().GetProcesses()[0].GetName() != "sshd" {
		t.Errorf("a new snapshot: %v", msg)
	}
	if msg := h.message(other, liveview.Frame{Snapshot: snap}); msg == first {
		t.Error("another node shares the message")
	}
	// M3-29: the copy without command lines and the full one alternate without rebuilding.
	full := &liveview.Snapshot{Processes: []liveview.Process{{PID: 3}}}
	hidden := &liveview.Snapshot{Processes: []liveview.Process{{PID: 3, CmdlineHidden: true}}, CmdlinesHidden: true}
	a, b := h.message(node, liveview.Frame{Snapshot: full}), h.message(node, liveview.Frame{Snapshot: hidden})
	if h.message(node, liveview.Frame{Snapshot: full}) != a || h.message(node, liveview.Frame{Snapshot: hidden}) != b || a == b {
		t.Error("full and hidden copies of one snapshot were built again")
	}
	if msg := h.message(node, liveview.Frame{Offline: true}); !msg.GetOffline() {
		t.Errorf("offline: %v", msg)
	}
}

// Every write of a live stream gets its own deadline; writing and flushing pass through.
func TestStreamingDeadline_WrapsTheLiveStreams(t *testing.T) {
	var got http.ResponseWriter
	h := streamingDeadline(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		got = w
		_, _ = w.Write([]byte("frame"))
		_ = http.NewResponseController(w).Flush()
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/sermon.web.v1.LiveViewService/WatchProcesses", nil))
	if _, ok := got.(*writeDeadline); !ok || rec.Body.String() != "frame" || !rec.Flushed {
		t.Errorf("writer %T, body %q, flushed %v", got, rec.Body.String(), rec.Flushed)
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/sermon.web.v1.AuthService/Login", nil))
	if _, ok := got.(*writeDeadline); ok {
		t.Error("a unary call got the stream's writer")
	}
}
