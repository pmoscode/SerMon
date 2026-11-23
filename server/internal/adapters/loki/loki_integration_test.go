//go:build integration

package loki_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/loki"
	"github.com/pmoscode/SerMon/server/internal/adapters/loki/lokitest"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/logql"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Against the Loki of the production compose file (ADR 0031): an organization reads its own
// lines with their metadata, another one nothing; without a tenant Loki refuses.
func TestLoki_KeepsOrganizationsApart(t *testing.T) {
	base := lokitest.Shared(t)
	c := loki.New(base)
	ctx := context.Background()
	if err := c.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	orgA, orgB := tenant.OrgID(uuid.New()), tenant.OrgID(uuid.New())
	nodeA := identity.NodeID(uuid.New())
	now := time.Now().Truncate(time.Microsecond)
	err := c.Push(ctx, orgA, []loki.Stream{{
		Labels: loki.Labels{Node: nodeA, Source: "journal", Unit: "nginx.service", Severity: "error"},
		Entries: []loki.Entry{
			{At: now.Add(-2 * time.Second), Line: "upstream timed out", Metadata: map[string]string{"pid": "812"}},
			{At: now.Add(-time.Second), Line: "recovered"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	q, _ := logql.Select([]identity.NodeID{nodeA})
	query := loki.QueryRange{Query: q, Start: now.Add(-time.Minute), End: now.Add(time.Second), Limit: 10, Direction: loki.Backward}

	var lines []loki.Line
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if lines, err = c.Query(ctx, orgA, query); err == nil && len(lines) == 2 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil || len(lines) != 2 {
		t.Fatalf("Query(A) = %+v, %v", lines, err)
	}
	if lines[0].Line != "recovered" || lines[1].Line != "upstream timed out" || lines[1].Metadata["pid"] != "812" ||
		!lines[1].At.Equal(now.Add(-2*time.Second)) {
		t.Errorf("lines = %+v (newest first, with metadata)", lines)
	}
	got := lines[0].Labels
	if got["node_id"] != nodeA.String() || got["unit"] != "nginx.service" || got["severity"] != "error" || got["source"] != "journal" {
		t.Errorf("labels = %v", got)
	}

	if other, err := c.Query(ctx, orgB, query); err != nil || len(other) != 0 {
		t.Errorf("Query(B) = %+v, %v; want nothing of A", other, err)
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base.String()+"/loki/api/v1/push",
		strings.NewReader(`{"streams":[{"stream":{"node_id":"x"},"values":[["1","forged"]]}]}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("push without tenant: %d, want 401 (auth_enabled)", res.StatusCode)
	}
}

// Loki's limits answer as errors of the adapter: an entry older than 7 days is refused for good.
func TestLoki_LimitsAreErrors(t *testing.T) {
	c := loki.New(lokitest.Shared(t))
	org := tenant.OrgID(uuid.New())
	old := []loki.Stream{{
		Labels:  loki.Labels{Node: identity.NodeID(uuid.New()), Source: "journal"},
		Entries: []loki.Entry{{At: time.Now().Add(-8 * 24 * time.Hour), Line: "from last week"}},
	}}
	if err := c.Push(context.Background(), org, old); !errors.Is(err, loki.ErrRejected) {
		t.Errorf("an entry older than 7 days: %v, want ErrRejected", err)
	}
}
