package logs_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/app/logs"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/logql"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	org    = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
	web    = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000aa"))
	db     = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000ab"))
	web01  = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001"))
	web02  = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000002"))
	db01   = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000003"))
	alien  = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000099"))
	now    = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	nodes  = []fleet.Node{{ID: web01, Group: web, Hostname: "web-01"}, {ID: web02, Group: web, Hostname: "web-02"}, {ID: db01, Group: db, Hostname: "db-01"}}
	userID = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000101"))
)

// fakeDirectory returns the nodes of the scope's groups.
type fakeDirectory struct{}

func (fakeDirectory) List(_ context.Context, scope tenant.Scope) ([]fleet.Node, error) {
	var out []fleet.Node
	for _, n := range nodes {
		if scope.AllowsGroup(n.Group) {
			out = append(out, n)
		}
	}
	return out, nil
}

type call struct {
	org     tenant.OrgID
	query   string
	from    time.Time
	to      time.Time
	limit   int
	forward bool
	step    time.Duration
}

type fakeStore struct {
	calls []call
	lines []logs.StoredLine
	err   error
	// tooLarge answers ErrTooLarge above this limit (M3-29).
	tooLarge int
}

func (f *fakeStore) Lines(_ context.Context, o tenant.OrgID, q logql.Query, from, to time.Time, limit int, forward bool) ([]logs.StoredLine, error) {
	f.calls = append(f.calls, call{org: o, query: q.String(), from: from, to: to, limit: limit, forward: forward})
	if f.err != nil {
		return nil, f.err
	}
	if f.tooLarge > 0 && limit > f.tooLarge {
		return nil, logs.ErrTooLarge
	}
	return f.lines[:min(len(f.lines), limit)], nil
}

func (f *fakeStore) Counts(_ context.Context, o tenant.OrgID, q logql.Query, from, to time.Time, step time.Duration) ([]logs.CountSeries, error) {
	f.calls = append(f.calls, call{org: o, query: q.String(), from: from, to: to, step: step})
	return []logs.CountSeries{
		{Labels: map[string]string{"severity": "error"}, Points: []logs.CountPoint{{At: from, Value: 3}}},
		{Labels: map[string]string{"severity": "weird"}, Points: []logs.CountPoint{{At: from, Value: 1}}},
	}, nil
}

func caller(bindings ...authz.Binding) access.Caller {
	return access.Caller{Principal: authz.Principal{User: userID, Org: org, Bindings: bindings}}
}

func viewerOf(group tenant.GroupID) access.Caller {
	return caller(authz.Binding{Group: group, Role: role.Viewer})
}

func admin() access.Caller { return caller(authz.Binding{AllGroups: true, Role: role.Admin}) }

func line(node identity.NodeID, message string) logs.StoredLine {
	return logs.StoredLine{
		Labels: map[string]string{"node_id": node.String(), "source": "journal", "unit": "nginx.service", "severity": "error"},
		At:     now.Add(-time.Minute), Line: message, Metadata: map[string]string{"pid": "812", "truncated": "true"},
	}
}

func service(store *fakeStore, opts ...authz.Option) *logs.Service {
	return logs.NewService(store, fakeDirectory{}, authz.NewPolicy(opts...))
}

func filter() logs.Filter { return logs.Filter{From: now.Add(-time.Hour), To: now} }

func TestQuery_ReadsTheVisibleNodesOnly(t *testing.T) {
	store := &fakeStore{lines: []logs.StoredLine{line(web01, "upstream timed out")}}
	page, err := service(store).Query(context.Background(), viewerOf(web), filter(), 10, false)
	if err != nil {
		t.Fatal(err)
	}
	c := store.calls[0]
	if c.org != org || !strings.Contains(c.query, web01.String()) || !strings.Contains(c.query, web02.String()) || strings.Contains(c.query, db01.String()) {
		t.Errorf("query %s for %v, want the nodes of the group web only", c.query, c.org)
	}
	if c.limit != 11 || c.forward || !c.from.Equal(now.Add(-time.Hour)) || !c.to.Equal(now) {
		t.Errorf("call = %+v (one more than the limit, newest first)", c)
	}
	l := page.Lines[0]
	if l.Node != web01 || l.Hostname != "web-01" || l.Severity != logql.SeverityError || l.Source != logql.SourceJournal ||
		l.Unit != "nginx.service" || l.Message != "upstream timed out" || !l.Truncated || l.Fields["pid"] != "812" || page.More {
		t.Errorf("line = %+v, more %v", l, page.More)
	}
	if _, ok := l.Fields["truncated"]; ok {
		t.Error("truncated is a flag, not a field")
	}
}

// Nodes the caller doesn't see are no error, they just yield nothing (like a list); without any
// visible node Loki is not even asked.
func TestQuery_ForeignNodesYieldNothing(t *testing.T) {
	store := &fakeStore{}
	f := filter()
	f.Nodes = []identity.NodeID{web01, db01, alien}
	if _, err := service(store).Query(context.Background(), viewerOf(web), f, 10, false); err != nil {
		t.Fatal(err)
	}
	if q := store.calls[0].query; !strings.Contains(q, web01.String()) || strings.Contains(q, db01.String()) || strings.Contains(q, alien.String()) {
		t.Errorf("query = %s", q)
	}
	f.Nodes = []identity.NodeID{alien}
	page, err := service(store).Query(context.Background(), viewerOf(web), f, 10, false)
	if err != nil || len(page.Lines) != 0 || len(store.calls) != 1 {
		t.Errorf("only foreign nodes: %+v, %v, %d calls", page, err, len(store.calls))
	}
	f.Nodes, f.Group = nil, &db
	if _, err := service(store).Query(context.Background(), viewerOf(web), f, 10, false); err != nil || len(store.calls) != 1 {
		t.Errorf("a foreign group: %v, %d calls", err, len(store.calls))
	}
}

func TestQuery_TheFiltersReachTheQuery(t *testing.T) {
	store := &fakeStore{}
	f := filter()
	f.Group = &web
	f.Sources = []logql.Source{logql.SourceFile}
	f.Units = []string{"nginx"}
	f.MinSeverity = logql.SeverityWarning
	f.Text, f.IgnoreCase = "timed out", true
	if _, err := service(store).Query(context.Background(), admin(), f, 0, true); err != nil {
		t.Fatal(err)
	}
	c := store.calls[0]
	for _, want := range []string{`source=~"file"`, `unit=~"nginx"`, `severity=~"alert|critical|emergency|error|warning"`, `|~ "(?i)timed out"`, web02.String()} {
		if !strings.Contains(c.query, want) {
			t.Errorf("query %s lacks %s", c.query, want)
		}
	}
	if strings.Contains(c.query, db01.String()) || c.limit != logs.DefaultLimit+1 || !c.forward {
		t.Errorf("call = %+v", c)
	}
}

func TestQuery_MoreThanTheLimitSaysSo(t *testing.T) {
	store := &fakeStore{lines: []logs.StoredLine{line(web01, "1"), line(web01, "2"), line(web01, "3")}}
	page, err := service(store).Query(context.Background(), admin(), filter(), 2, false)
	if err != nil || len(page.Lines) != 2 || !page.More {
		t.Errorf("page = %+v, %v", page, err)
	}
}

// M3-29: an answer too large for one read (long lines) comes with fewer lines and says there is
// more; it doesn't fail.
func TestQuery_ATooLargeAnswerComesWithFewerLines(t *testing.T) {
	var stored []logs.StoredLine
	for range 300 {
		stored = append(stored, line(web01, "long"))
	}
	store := &fakeStore{lines: stored, tooLarge: 100}
	page, err := service(store).Query(context.Background(), admin(), filter(), 200, false)
	if err != nil || len(page.Lines) == 0 || len(page.Lines) > 100 || !page.More {
		t.Fatalf("page = %d lines, more %v, %v", len(page.Lines), page.More, err)
	}
	store = &fakeStore{lines: stored, tooLarge: 1}
	if _, err := service(store).Query(context.Background(), admin(), filter(), 200, false); !errors.Is(err, logs.ErrTooBroad) {
		t.Errorf("even small answers too large: %v", err)
	}
}

// manyNodes is an organization with n nodes in the group web.
type manyNodes int

func (m manyNodes) List(context.Context, tenant.Scope) ([]fleet.Node, error) {
	out := make([]fleet.Node, int(m))
	for i := range out {
		out[i] = fleet.Node{ID: identity.NodeID(uuid.New()), Group: web, Hostname: "h"}
	}
	return out, nil
}

// M3-29: a query names its nodes; beyond MaxSelectedNodes the user narrows by group or node.
func TestQuery_BoundsTheSelectedNodes(t *testing.T) {
	store := &fakeStore{}
	big := logs.NewService(store, manyNodes(logs.MaxSelectedNodes+1), authz.NewPolicy())
	if _, err := big.Query(context.Background(), admin(), filter(), 10, false); !errors.Is(err, logs.ErrTooBroad) {
		t.Errorf("all of %d nodes: %v, want ErrTooBroad", logs.MaxSelectedNodes+1, err)
	}
	if _, err := big.Histogram(context.Background(), admin(), filter()); !errors.Is(err, logs.ErrTooBroad) {
		t.Errorf("histogram of all: %v", err)
	}
	fits := logs.NewService(store, manyNodes(logs.MaxSelectedNodes), authz.NewPolicy())
	if _, err := fits.Query(context.Background(), admin(), filter(), 10, false); err != nil {
		t.Errorf("%d nodes: %v", logs.MaxSelectedNodes, err)
	}
}

func TestQuery_BoundsAndRights(t *testing.T) {
	store := &fakeStore{}
	svc := service(store)
	bad := map[string]logs.Filter{
		"no range":    {},
		"reversed":    {From: now, To: now.Add(-time.Hour)},
		"too long":    {From: now.Add(-logs.MaxRange - time.Hour), To: now},
		"many nodes":  {From: now.Add(-time.Hour), To: now, Nodes: make([]identity.NodeID, logs.MaxNodes+1)},
		"bad level":   {From: now.Add(-time.Hour), To: now, MinSeverity: "loud"},
		"long text":   {From: now.Add(-time.Hour), To: now, Text: strings.Repeat("x", logql.MaxTextBytes+1)},
		"bad source":  {From: now.Add(-time.Hour), To: now, Sources: []logql.Source{"docker"}},
		"empty unit":  {From: now.Add(-time.Hour), To: now, Units: []string{""}},
		"unknown sev": {From: now.Add(-time.Hour), To: now, MinSeverity: logql.SeverityUnknown},
	}
	for name, f := range bad {
		if _, err := svc.Query(context.Background(), admin(), f, 10, false); !errors.Is(err, logs.ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	if _, err := svc.Query(context.Background(), admin(), filter(), logs.MaxLimit+1, false); !errors.Is(err, logs.ErrInvalid) {
		t.Errorf("limit: %v", err)
	}
	// Viewers of an organization that hides logs from them see nothing.
	hidden := service(store, authz.WithViewerLogsDisabled(org))
	if _, err := hidden.Query(context.Background(), viewerOf(web), filter(), 10, false); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("viewer with logs off: %v, want ErrForbidden", err)
	}
	if _, err := hidden.Query(context.Background(), caller(authz.Binding{Group: web, Role: role.Operator}), filter(), 10, false); err != nil {
		t.Errorf("operator with logs off for viewers: %v", err)
	}
	if _, err := svc.Query(context.Background(), caller(), filter(), 10, false); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("no binding: %v", err)
	}
	if len(store.calls) != 1 {
		t.Errorf("Loki was asked %d times, want only for the operator", len(store.calls))
	}
}

func TestQuery_WithoutAStore(t *testing.T) {
	svc := logs.NewService(nil, fakeDirectory{}, authz.NewPolicy())
	if _, err := svc.Query(context.Background(), admin(), filter(), 10, false); !errors.Is(err, logs.ErrUnavailable) {
		t.Errorf("no Loki configured: %v", err)
	}
	store := &fakeStore{err: logs.ErrUnavailable}
	if _, err := service(store).Query(context.Background(), admin(), filter(), 10, false); !errors.Is(err, logs.ErrUnavailable) {
		t.Errorf("Loki down: %v", err)
	}
}

func TestHistogram_CountsPerSeverityInAboutAHundredSteps(t *testing.T) {
	store := &fakeStore{}
	h, err := service(store).Histogram(context.Background(), viewerOf(web), filter())
	if err != nil {
		t.Fatal(err)
	}
	c := store.calls[0]
	if c.step != 36*time.Second || !strings.HasPrefix(c.query, "sum by (severity) (count_over_time(") || strings.Contains(c.query, db01.String()) {
		t.Errorf("call = %+v", c)
	}
	if h.Step != 36*time.Second || len(h.Series[logql.SeverityError]) != 1 || h.Series[logql.SeverityError][0].Value != 3 {
		t.Errorf("histogram = %+v", h)
	}
	if _, odd := h.Series["weird"]; odd || len(h.Series[logql.SeverityUnknown]) != 1 {
		t.Errorf("an unknown label value counts as unknown: %+v", h.Series)
	}
	short := filter()
	short.From = now.Add(-10 * time.Second)
	if _, err := service(store).Histogram(context.Background(), admin(), short); err != nil || store.calls[1].step != time.Second {
		t.Errorf("short range: step %v, %v", store.calls[1].step, err)
	}
}

func TestContext_LinesAroundAnEntryOfTheSameStream(t *testing.T) {
	store := &fakeStore{lines: []logs.StoredLine{line(web01, "x")}}
	at := now.Add(-time.Minute)
	got, err := service(store).Context(context.Background(), viewerOf(web), web01, at, logql.SourceJournal, "nginx.service")
	if err != nil {
		t.Fatal(err)
	}
	if len(store.calls) != 2 {
		t.Fatalf("calls = %+v", store.calls)
	}
	before, after := store.calls[0], store.calls[1]
	for _, c := range store.calls {
		if !strings.Contains(c.query, web01.String()) || strings.Contains(c.query, web02.String()) ||
			!strings.Contains(c.query, `unit=~"nginx\\.service"`) || !strings.Contains(c.query, `source=~"journal"`) {
			t.Errorf("query = %s", c.query)
		}
	}
	if before.forward || !before.to.Equal(at) || before.limit != logs.ContextLines ||
		!after.forward || !after.from.Equal(at) || after.limit != logs.ContextLines+1 {
		t.Errorf("before %+v, after %+v", before, after)
	}
	if len(got) != 2 {
		t.Errorf("lines = %+v", got)
	}
	if _, err := service(store).Context(context.Background(), viewerOf(web), db01, at, logql.SourceJournal, "x"); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("a node of another group: %v, want ErrNotFound", err)
	}
}
