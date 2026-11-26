package webapi

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
)

// Labels come from the hosts (device and interface names): shown only as visible text (M2-28).
func TestLabelsView_KeepsHostTextVisible(t *testing.T) {
	got := labelsView(map[string]string{"device": "vda\u202e1", "mode": "user"})
	if got["device"] == "vda\u202e1" || got["mode"] != "user" {
		t.Errorf("labels = %q", got)
	}
}

// M4-31: mount points come from the hosts like labels: visible text only, on the node page and
// on the capacity page.
func TestFilesystemViews_KeepHostTextVisible(t *testing.T) {
	mount := "/data\u202e1"
	if got := filesystemView(fleet.Filesystem{Mountpoint: mount}).GetMountpoint(); got == mount {
		t.Errorf("node page: %q", got)
	}
	if got := fleetFilesystemView(fleet.NodeFilesystem{Filesystem: fleet.Filesystem{Mountpoint: mount}}).GetMountpoint(); got == mount {
		t.Errorf("capacity page: %q", got)
	}
	if got := filesystemView(fleet.Filesystem{Mountpoint: "/var/lib"}).GetMountpoint(); got != "/var/lib" {
		t.Errorf("a plain mount point changed: %q", got)
	}
}

// Review M4-30: the dashboards' reads end after their time with DEADLINE_EXCEEDED, not as an
// internal error.
func TestHeavyRead_EndsInTime(t *testing.T) {
	ctx, release, err := NewHeavyReads(4).admit(t.Context(), access.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > heavyReadTimeout {
		t.Fatalf("deadline %v, %v", deadline, ok)
	}
	err = fmt.Errorf("read: %w", context.DeadlineExceeded)
	for name, got := range map[string]error{
		"read":    readFailure(t.Context(), slog.New(slog.DiscardHandler), "x", err),
		"anomaly": (&AnomalyHandler{logger: slog.New(slog.DiscardHandler)}).failure(t.Context(), "x", err),
	} {
		if connect.CodeOf(got) != connect.CodeDeadlineExceeded {
			t.Errorf("%s: %v", name, got)
		}
	}
}
