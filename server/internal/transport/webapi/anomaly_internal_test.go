package webapi

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"
)

// Labels come from the hosts (device and interface names): shown only as visible text (M2-28).
func TestLabelsView_KeepsHostTextVisible(t *testing.T) {
	got := labelsView(map[string]string{"device": "vda\u202e1", "mode": "user"})
	if got["device"] == "vda\u202e1" || got["mode"] != "user" {
		t.Errorf("labels = %q", got)
	}
}

// Review M4-30: the dashboards' reads end after their time with DEADLINE_EXCEEDED, not as an
// internal error.
func TestHeavyRead_EndsInTime(t *testing.T) {
	ctx, cancel := heavyRead(t.Context())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > heavyReadTimeout {
		t.Fatalf("deadline %v, %v", deadline, ok)
	}
	err := fmt.Errorf("read: %w", context.DeadlineExceeded)
	for name, got := range map[string]error{
		"read":    readFailure(t.Context(), slog.New(slog.DiscardHandler), "x", err),
		"anomaly": (&AnomalyHandler{logger: slog.New(slog.DiscardHandler)}).failure(t.Context(), "x", err),
	} {
		if connect.CodeOf(got) != connect.CodeDeadlineExceeded {
			t.Errorf("%s: %v", name, got)
		}
	}
}
