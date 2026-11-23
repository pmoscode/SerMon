package logs

import (
	"strconv"
	"testing"
	"time"
)

// The window forgets what it sent once it falls out: its memory stays bounded by the window.
func TestWindow_ForgetsWhatFallsOut(t *testing.T) {
	w := window{seen: map[string]time.Time{}}
	t0 := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	for round := range 100 {
		now := t0.Add(time.Duration(round) * TailInterval)
		stored := []StoredLine{{At: now, Line: "line " + strconv.Itoa(round)}}
		if lines, _ := w.fresh(stored, now.Add(-TailLookback), nil); len(lines) != 1 {
			t.Fatalf("round %d: %d lines", round, len(lines))
		}
	}
	if limit := int(TailLookback/TailInterval) + 1; len(w.seen) > limit {
		t.Errorf("remembers %d lines, want at most %d", len(w.seen), limit)
	}
}
