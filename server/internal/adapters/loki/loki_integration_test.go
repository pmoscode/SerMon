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
	"github.com/pmoscode/SerMon/server/internal/app/ingest"
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
	if len(got) != 4 {
		t.Errorf("labels = %v, want only node_id, source, unit, severity", got)
	}
	if len(lines[0].Metadata) != 0 || len(lines[1].Metadata) != 1 {
		t.Errorf("metadata = %v, %v; want only what SerMon sent", lines[0].Metadata, lines[1].Metadata)
	}
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

// A batch written twice (the agent resends after the server stored it but crashed before the
// acknowledgement) is in Loki once: identical entries of a stream are deduplicated (M3-07).
func TestWriter_AResendIsStoredOnce(t *testing.T) {
	c := loki.New(lokitest.Shared(t))
	w := loki.NewWriter(c)
	ctx := context.Background()
	org, node := tenant.OrgID(uuid.New()), identity.NodeID(uuid.New())
	now := time.Now().Truncate(time.Microsecond)
	pid := uint32(812)
	entries := []ingest.LogEntry{
		{At: now.Add(-2 * time.Second), Source: ingest.LogSourceJournal, Unit: "nginx.service", Severity: 4, Message: "upstream timed out", PID: pid},
		{At: now.Add(-time.Second), Source: ingest.LogSourceFile, Unit: "nginx", Message: "recovered", File: "/var/log/nginx/error.log"},
	}
	for range 2 {
		if err := w.Write(ctx, org, node, entries); err != nil {
			t.Fatal(err)
		}
	}
	q, _ := logql.Select([]identity.NodeID{node})
	query := loki.QueryRange{Query: q, Start: now.Add(-time.Minute), End: now.Add(time.Second), Limit: 10}
	var lines []loki.Line
	var err error
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if lines, err = c.Query(ctx, org, query); err == nil && len(lines) >= 2 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil || len(lines) != 2 {
		t.Fatalf("Query() = %d lines, %v; want each entry once: %+v", len(lines), err, lines)
	}
}

// The builder's queries (M3-08) run on the real Loki: every filter selects what it should, and
// hostile text is matched literally – it neither breaks the query nor widens it.
func TestLoki_TheBuildersQueriesSelectWhatTheySay(t *testing.T) {
	c := loki.New(lokitest.Shared(t))
	w := loki.NewWriter(c)
	ctx := context.Background()
	org := tenant.OrgID(uuid.New())
	node, other := identity.NodeID(uuid.New()), identity.NodeID(uuid.New())
	now := time.Now().Truncate(time.Microsecond)
	hostile := `say "hi" {node_id=~".+"} | \back`
	entry := func(ago time.Duration, src ingest.LogSource, unit string, sev uint8, msg string) ingest.LogEntry {
		return ingest.LogEntry{At: now.Add(-ago), Source: src, Unit: unit, Severity: sev, Message: msg}
	}
	if err := w.Write(ctx, org, node, []ingest.LogEntry{
		entry(5*time.Second, ingest.LogSourceJournal, "nginx.service", 4, "Upstream TIMED OUT"),
		entry(4*time.Second, ingest.LogSourceJournal, "cron.service", 7, "session opened"),
		entry(3*time.Second, ingest.LogSourceFile, "app", 5, hostile),
		entry(2*time.Second, ingest.LogSourceJournal, "nginx.service", 7, "upstream recovered"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.Write(ctx, org, other, []ingest.LogEntry{entry(time.Second, ingest.LogSourceJournal, "nginx.service", 4, "other node")}); err != nil {
		t.Fatal(err)
	}
	lines := func(f logql.Filter) []string {
		t.Helper()
		f.Nodes = []identity.NodeID{node}
		q, err := logql.Lines(f)
		if err != nil {
			t.Fatal(err)
		}
		var got []loki.Line
		deadline := time.Now().Add(30 * time.Second)
		for {
			got, err = c.Query(ctx, org, loki.QueryRange{Query: q, Start: now.Add(-time.Minute), End: now.Add(time.Second), Limit: 100, Direction: loki.Forward})
			if err != nil {
				t.Fatalf("%s: %v", q, err)
			}
			if len(got) > 0 || time.Now().After(deadline) {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		out := make([]string, 0, len(got))
		for _, l := range got {
			out = append(out, l.Line)
		}
		return out
	}
	// The first query waits until Loki has all lines of the node.
	if got := lines(logql.Filter{}); len(got) != 4 {
		t.Fatalf("all lines of the node = %v", got)
	}
	cases := map[string]struct {
		filter logql.Filter
		want   []string
	}{
		"text":          {logql.Filter{Text: "upstream"}, []string{"upstream recovered"}},
		"ignore case":   {logql.Filter{Text: "upstream", IgnoreCase: true}, []string{"Upstream TIMED OUT", "upstream recovered"}},
		"regex literal": {logql.Filter{Text: "TIMED.OUT", IgnoreCase: true}, nil},
		"hostile text":  {logql.Filter{Text: hostile}, []string{hostile}},
		"units":         {logql.Filter{Units: []string{"cron.service", "app"}}, []string{"session opened", hostile}},
		"unit literal":  {logql.Filter{Units: []string{"nginx.servic."}}, nil},
		"severity":      {logql.Filter{Severities: []logql.Severity{logql.SeverityError}}, []string{"Upstream TIMED OUT"}},
		"source":        {logql.Filter{Sources: []logql.Source{logql.SourceFile}}, []string{hostile}},
		"breakout":      {logql.Filter{Text: `"} or {node_id=~".+`}, nil},
	}
	for name, tc := range cases {
		f := tc.filter
		f.Nodes = []identity.NodeID{node}
		q, _ := logql.Lines(f)
		got, err := c.Query(ctx, org, loki.QueryRange{Query: q, Start: now.Add(-time.Minute), End: now.Add(time.Second), Limit: 100, Direction: loki.Forward})
		if err != nil {
			t.Errorf("%s: %s: %v", name, q, err)
			continue
		}
		texts := make([]string, 0, len(got))
		for _, l := range got {
			texts = append(texts, l.Line)
		}
		if strings.Join(texts, "\n") != strings.Join(tc.want, "\n") {
			t.Errorf("%s: %s → %q, want %q", name, q, texts, tc.want)
		}
	}
	count, _ := logql.CountBySeverity(logql.Filter{Nodes: []identity.NodeID{node}}, time.Minute)
	series, err := c.Counts(ctx, org, count, now.Add(-time.Minute), now.Add(time.Second), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	total := map[string]float64{}
	for _, s := range series {
		for _, p := range s.Points {
			total[s.Labels["severity"]] += p.Value
		}
	}
	if total["info"] != 2 || total["error"] != 1 || total["warning"] != 1 {
		t.Errorf("counts per severity = %v", total)
	}
}

// Deleting (M3-10, D29): the request is recorded for the organization's tenant only, a retry is
// no error. Loki filters the lines from queries once its querier reloads the requests (every 5
// min) and removes them in the compactor after the cancel period.
func TestLoki_RecordsTheDeletionOfAnOrganizationOrANode(t *testing.T) {
	c := loki.New(lokitest.Shared(t))
	ctx := context.Background()
	alpha, beta := tenant.OrgID(uuid.New()), tenant.OrgID(uuid.New())
	node := identity.NodeID(uuid.New())
	now := time.Now()
	if err := c.Delete(ctx, alpha, logql.MustSelect(node), now.Add(-30*24*time.Hour), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, alpha, logql.Everything(), time.Unix(0, 0), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, alpha, logql.Everything(), time.Unix(0, 0), now.Add(time.Minute)); err != nil {
		t.Errorf("a retry: %v", err)
	}
	requests, err := c.Deletions(ctx, alpha)
	if err != nil {
		t.Fatal(err)
	}
	queries := map[string]int{}
	for _, r := range requests {
		queries[r.Query]++
		if r.End.After(time.Now()) || r.Status == "" {
			t.Errorf("request = %+v", r)
		}
	}
	if queries[logql.MustSelect(node).String()] != 1 || queries[logql.Everything().String()] < 1 {
		t.Errorf("requests of Alpha = %+v", requests)
	}
	if other, err := c.Deletions(ctx, beta); err != nil || len(other) != 0 {
		t.Errorf("requests of Beta = %+v, %v", other, err)
	}
}

// M5-30: the retention of an organization asks the real Loki to delete its lines before its period,
// in its tenant only.
func TestLoki_EraseBeforeIsRecordedInTheTenant(t *testing.T) {
	c := loki.New(lokitest.Shared(t))
	ctx := context.Background()
	alpha, beta := tenant.OrgID(uuid.New()), tenant.OrgID(uuid.New())
	before := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	if err := loki.NewEraser(c).EraseBefore(ctx, alpha, before); err != nil {
		t.Fatal(err)
	}
	requests, err := c.Deletions(ctx, alpha)
	if err != nil || len(requests) != 1 || !requests[0].End.Equal(before) || requests[0].Query != logql.Everything().String() ||
		requests[0].Start.After(before.Add(-11*24*time.Hour)) {
		t.Errorf("requests of Alpha = %+v, %v", requests, err)
	}
	if other, err := c.Deletions(ctx, beta); err != nil || len(other) != 0 {
		t.Errorf("requests of Beta = %+v, %v", other, err)
	}
}

// The compactor runs a recorded request (here every few seconds instead of every 10 minutes).
func TestLoki_TheCompactorProcessesADeletion(t *testing.T) {
	c := loki.New(lokitest.Start(t, "-compactor.compaction-interval=2s", "-compactor.delete-request-cancel-period=1s"))
	w := loki.NewWriter(c)
	ctx := context.Background()
	org, node := tenant.OrgID(uuid.New()), identity.NodeID(uuid.New())
	if err := w.Write(ctx, org, node, []ingest.LogEntry{{At: time.Now().Add(-time.Minute), Source: ingest.LogSourceJournal, Unit: "app.service", Severity: 7, Message: "gone soon"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, org, logql.Everything(), time.Unix(0, 0), time.Now()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(90 * time.Second)
	for {
		requests, err := c.Deletions(ctx, org)
		if err != nil {
			t.Fatal(err)
		}
		if len(requests) == 1 && requests[0].Status == "processed" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("not processed: %+v", requests)
		}
		time.Sleep(time.Second)
	}
}
