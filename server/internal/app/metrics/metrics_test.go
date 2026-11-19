package metrics_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
)

var now = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

func TestChooseSource_FinestThatHoldsTheRangeAndFitsTheStep(t *testing.T) {
	tests := []struct {
		name string
		from time.Duration // before now
		step time.Duration
		want metrics.Source
	}{
		{"last 5 min, 1 s step", 5 * time.Minute, time.Second, metrics.SourceRing},
		{"last 15 min, zero step", 15 * time.Minute, 0, metrics.SourceRing},
		{"last 15 min, 10 s step", 15 * time.Minute, 10 * time.Second, metrics.SourceRaw},
		{"16 min ago, 1 s step", 16 * time.Minute, time.Second, metrics.SourceRaw},
		{"last hour, fine step", time.Hour, 10 * time.Second, metrics.SourceRaw},
		{"last hour, zero step", time.Hour, 0, metrics.SourceRaw},
		{"last day, 30 s step", 24 * time.Hour, 30 * time.Second, metrics.SourceRaw},
		{"last day, 5 min step", 24 * time.Hour, 5 * time.Minute, metrics.Source1m},
		{"25 h ago, fine step", 25 * time.Hour, 10 * time.Second, metrics.Source1m},
		{"last week", 7 * 24 * time.Hour, 5 * time.Minute, metrics.Source1m},
		{"last week, 1 h step", 7 * 24 * time.Hour, time.Hour, metrics.Source1h},
		{"two months", 60 * 24 * time.Hour, time.Minute, metrics.Source1h},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := metrics.ChooseSource(now.Add(-tc.from), tc.step, now); got != tc.want {
				t.Errorf("ChooseSource() = %v, want %v", got, tc.want)
			}
		})
	}
}

func query(from, to time.Duration, step time.Duration) metrics.Query {
	return metrics.Query{
		Metric: "sermon_cpu_utilization_ratio", Nodes: []identity.NodeID{identity.NodeID(uuid.New())},
		From: now.Add(-from), To: now.Add(-to), Step: step,
	}
}

func TestQueryNormalize_AlignsTheStepToTheSource(t *testing.T) {
	q, err := query(time.Hour, 0, 25*time.Second).Normalize(now)
	if err != nil || q.Source != metrics.SourceRaw || q.Step != 20*time.Second {
		t.Fatalf("Normalize() = %+v, %v; want raw with a 20 s step", q, err)
	}
	q, err = query(time.Hour, 0, 0).Normalize(now)
	if err != nil || q.Step != metrics.Window {
		t.Fatalf("Normalize() = %+v, %v; want the window as minimum step", q, err)
	}
	// Automatic step: 90 days in at most 2000 points → 1.08 h, rounded up to whole hours.
	q, err = query(90*24*time.Hour, 0, 0).Normalize(now)
	if err != nil || q.Source != metrics.Source1h || q.Step != 2*time.Hour {
		t.Fatalf("Normalize() = %+v, %v; want 1 h source with a 2 h step", q, err)
	}
	q, err = query(24*time.Hour, 0, 0).Normalize(now)
	if err != nil || q.Source != metrics.SourceRaw || q.Step != 50*time.Second {
		t.Fatalf("Normalize() = %+v, %v; want raw with a 50 s step (1728 points)", q, err)
	}
}

func TestQueryNormalize_RejectsInvalidQueriesAndTooManyPoints(t *testing.T) {
	invalid := map[string]metrics.Query{
		"no metric":      {Nodes: query(time.Hour, 0, 0).Nodes, From: now.Add(-time.Hour), To: now},
		"no nodes":       {Metric: "sermon_x", From: now.Add(-time.Hour), To: now},
		"empty range":    query(time.Hour, time.Hour, 0),
		"reversed range": query(0, time.Hour, 0),
		"negative step":  query(time.Hour, 0, -time.Second),
		"too many nodes": {Metric: "sermon_x", Nodes: make([]identity.NodeID, metrics.MaxNodes+1), From: now.Add(-time.Hour), To: now},
	}
	for name, q := range invalid {
		if _, err := q.Normalize(now); !errors.Is(err, metrics.ErrInvalidQuery) {
			t.Errorf("%s: Normalize() error = %v, want ErrInvalidQuery", name, err)
		}
	}
	// 24 h in 10 s steps are 8640 points.
	if _, err := query(24*time.Hour, 0, 10*time.Second).Normalize(now); !errors.Is(err, metrics.ErrTooManyPoints) {
		t.Errorf("Normalize() error = %v, want ErrTooManyPoints", err)
	}
	if _, err := query(24*time.Hour, 0, time.Minute).Normalize(now); err != nil {
		t.Errorf("24 h in 1 min steps (1440 points) must pass: %v", err)
	}
}

func TestLabelsKey_IsIndependentOfOrder(t *testing.T) {
	a := metrics.Labels{"cpu": "0", "mode": "user"}
	b := metrics.Labels{"mode": "user", "cpu": "0"}
	c := metrics.Labels{"cpu": "0", "mode": "system"}
	if a.Key() != b.Key() || a.Key() == c.Key() || (metrics.Labels{}).Key() != "" {
		t.Errorf("keys: %q %q %q", a.Key(), b.Key(), c.Key())
	}
}
