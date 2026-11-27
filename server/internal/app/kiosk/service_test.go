package kiosk_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/alerts"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/dashboards"
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/app/kiosk"
	"github.com/pmoscode/SerMon/server/internal/app/logs"
	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/dashboard"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/logql"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	orgA   = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
	groupX = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000aa"))
	groupY = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000bb"))
	alice  = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001"))
	bob    = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000002"))
	web1   = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000a1"))
	db1    = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000a2"))
	app1   = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000b1"))
	board  = dashboards.ID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000d1"))
	now    = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
)

type stored struct {
	t    kiosk.Token
	hash [sha256.Size]byte
}

type fakeStore struct {
	tokens  []stored
	audits  []access.AuditEntry
	touched int
	// wrongHash makes the lookup return a token with another hash (a broken index).
	wrongHash bool
}

func (f *fakeStore) List(_ context.Context, _ tenant.Scope, d dashboards.ID) ([]kiosk.Token, error) {
	var out []kiosk.Token
	for _, s := range f.tokens {
		if s.t.Dashboard == d {
			out = append(out, s.t)
		}
	}
	return out, nil
}

func (f *fakeStore) Create(_ context.Context, _ tenant.Scope, t kiosk.Token, hash [sha256.Size]byte, audit access.AuditEntry) error {
	f.tokens = append(f.tokens, stored{t, hash})
	f.audits = append(f.audits, audit)
	return nil
}

func (f *fakeStore) Revoke(_ context.Context, _ tenant.Scope, id kiosk.TokenID, at time.Time, audit access.AuditEntry) error {
	for i := range f.tokens {
		if f.tokens[i].t.ID == id {
			f.tokens[i].t.RevokedAt = &at
			f.audits = append(f.audits, audit)
			return nil
		}
	}
	return access.ErrNotFound
}

func (f *fakeStore) Lookup(_ context.Context, hash [sha256.Size]byte, at time.Time) (kiosk.Found, bool, error) {
	for _, s := range f.tokens {
		if s.hash == hash && s.t.RevokedAt == nil && s.t.ExpiresAt.After(at) {
			found := kiosk.Found{Token: s.t.ID, Org: orgA, Dashboard: s.t.Dashboard, Creator: s.t.CreatedBy, Hash: s.hash}
			if f.wrongHash {
				found.Hash[0] ^= 1
			}
			return found, true, nil
		}
	}
	return kiosk.Found{}, false, nil
}

func (f *fakeStore) Touch(context.Context, tenant.Scope, kiosk.TokenID, time.Time) error {
	f.touched++
	return nil
}

type fakeDashboards struct{ d dashboards.Dashboard }

func (f *fakeDashboards) Get(_ context.Context, _ tenant.Scope, user identity.UserID, id dashboards.ID) (dashboards.Dashboard, error) {
	if id != f.d.ID || (!f.d.Shared && user != f.d.Owner) {
		return dashboards.Dashboard{}, access.ErrNotFound
	}
	return f.d, nil
}

// fakePrincipals returns the rights of each user (admin by default for alice); a user without
// rights left the organization.
type fakePrincipals struct {
	p      authz.Principal
	others map[identity.UserID]authz.Principal
	err    error
}

func (f *fakePrincipals) ForUser(_ context.Context, _ tenant.OrgID, user identity.UserID) (authz.Principal, error) {
	if f.err != nil {
		return authz.Principal{}, f.err
	}
	if user == f.p.User {
		return f.p, nil
	}
	if p, ok := f.others[user]; ok {
		return p, nil
	}
	return authz.Principal{}, access.ErrNoMembership
}

// fakeReaders behave like the readers of the UI: they answer only within the caller's rights.
type fakeReaders struct {
	queries []metrics.Query
	filters []logs.Filter
	callers []access.Caller
}

var fleetNodes = []fleet.NodeStatus{
	{Node: fleet.Node{ID: web1, Hostname: "web-01", Group: groupX, GroupName: "web"}},
	{Node: fleet.Node{ID: db1, Hostname: "db-01", Group: groupX, GroupName: "web"}},
	{Node: fleet.Node{ID: app1, Hostname: "app-01", Group: groupY, GroupName: "api"}},
}

func allowed(c access.Caller, action authz.Action, group tenant.GroupID) bool {
	return authz.NewPolicy().Allowed(c.Principal, action, authz.InGroup(orgA, group))
}

func groupOf(n identity.NodeID) tenant.GroupID {
	for _, s := range fleetNodes {
		if s.ID == n {
			return s.Group
		}
	}
	return tenant.GroupID{}
}

func (f *fakeReaders) Summary(_ context.Context, c access.Caller, _ *tenant.GroupID) (fleet.Summary, error) {
	f.callers = append(f.callers, c)
	var out fleet.Summary
	for _, n := range fleetNodes {
		if allowed(c, authz.ViewNodes, n.Group) {
			out.Nodes = append(out.Nodes, n)
		}
	}
	return out, nil
}

func (f *fakeReaders) Query(_ context.Context, c access.Caller, q metrics.Query) ([]metrics.Series, time.Duration, error) {
	f.queries = append(f.queries, q)
	for _, n := range q.Nodes {
		if !allowed(c, authz.ViewNodes, groupOf(n)) {
			return nil, 0, access.ErrNotFound
		}
	}
	return []metrics.Series{{Node: q.Nodes[0]}}, time.Minute, nil
}

func (f *fakeReaders) List(_ context.Context, c access.Caller) ([]alerts.Alert, error) {
	var out []alerts.Alert
	for _, n := range []identity.NodeID{web1, db1, app1} {
		if allowed(c, authz.ViewNodes, groupOf(n)) {
			out = append(out, alerts.Alert{Instance: alert.Instance{Key: alert.Key{Node: n}}})
		}
	}
	return out, nil
}

type fakeLogs struct{ r *fakeReaders }

func (f fakeLogs) Query(_ context.Context, c access.Caller, filter logs.Filter, limit int, _ bool) (logs.Page, error) {
	f.r.filters = append(f.r.filters, filter)
	if filter.Group != nil && !allowed(c, authz.ViewLogs, *filter.Group) {
		return logs.Page{}, access.ErrForbidden
	}
	for _, n := range filter.Nodes {
		if !allowed(c, authz.ViewLogs, groupOf(n)) {
			return logs.Page{}, access.ErrForbidden
		}
	}
	return logs.Page{More: limit == 50}, nil
}

// fakeAdmission counts the slots of the displays' reads.
type fakeAdmission struct {
	admitted, released, logs int
	refuse                   error
}

func (f *fakeAdmission) Admit(_ context.Context, _ kiosk.Display, logs bool) (func(), error) {
	if f.refuse != nil {
		return nil, f.refuse
	}
	f.admitted++
	if logs {
		f.logs++
	}
	return func() { f.released++ }, nil
}

type env struct {
	svc        *kiosk.Service
	store      *fakeStore
	dashboards *fakeDashboards
	principals *fakePrincipals
	readers    *fakeReaders
	admission  *fakeAdmission
}

var admin = authz.Principal{User: alice, Org: orgA, Bindings: []authz.Binding{{AllGroups: true, Role: role.Admin}}}

func spec() dashboard.Spec {
	return dashboard.Spec{
		Version: 1, Title: "Board", TimeRange: dashboard.Preset6h,
		Variables: []dashboard.Variable{{Name: "host", Kind: dashboard.VariableNode, Default: uuid.UUID(db1)}},
		Widgets: []dashboard.Widget{
			{ID: "cpu", Kind: dashboard.KindStat, Title: "CPU", Query: dashboard.Query{Metric: "sermon_cpu_utilization_ratio", Labels: map[string]string{"mode": "user"}, Target: dashboard.Target{Variable: "host"}}},
			{ID: "logs", Kind: dashboard.KindLogStream, Title: "Logs", Query: dashboard.Query{Target: dashboard.Target{Group: &groupX}, Text: "error", MinSeverity: 4}},
			{ID: "alerts", Kind: dashboard.KindAlertList, Title: "Alerts", Query: dashboard.Query{Target: dashboard.Target{Nodes: []identity.NodeID{web1}}}},
		},
	}
}

func newEnv() env {
	r := &fakeReaders{}
	e := env{
		store:      &fakeStore{},
		dashboards: &fakeDashboards{d: dashboards.Dashboard{ID: board, Owner: alice, Shared: true, Spec: spec()}},
		principals: &fakePrincipals{p: admin},
		readers:    r,
		admission:  &fakeAdmission{},
	}
	e.svc = kiosk.NewService(kiosk.Deps{
		Store: e.store, Dashboards: e.dashboards, Principals: e.principals, Admission: e.admission,
		Readers: kiosk.Readers{Fleet: r, Logs: fakeLogs{r}, Alerts: r},
		Policy:  authz.NewPolicy(), Now: func() time.Time { return now }, NewID: uuid.New,
	})
	return e
}

func fresh(p authz.Principal) access.Caller { return access.Caller{Principal: p, StepUpAt: now} }

func (e env) token(t *testing.T) string {
	t.Helper()
	_, secret, err := e.svc.Create(context.Background(), fresh(admin), board, "Hall", 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

func TestCreate_StoresOnlyTheHashOfA256BitSecret(t *testing.T) {
	e := newEnv()
	tok, secret, err := e.svc.Create(context.Background(), fresh(admin), board, "  Hall  ", 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, "smk_") || len(secret) != 4+43 {
		t.Errorf("secret %q: want smk_ and 43 characters of base64url (32 bytes)", secret)
	}
	if tok.Name != "Hall" || !tok.ExpiresAt.Equal(now.Add(30*24*time.Hour)) || tok.CreatedBy != alice {
		t.Errorf("token = %+v", tok)
	}
	if e.store.tokens[0].hash != sha256.Sum256([]byte(secret)) {
		t.Error("the store must get the SHA-256 of the secret")
	}
	a := e.store.audits[0]
	if a.Action != "kiosk_token.create" || strings.Contains(a.Target+a.Details["name"]+a.Details["dashboard"], secret) {
		t.Errorf("audit = %+v (never the secret)", a)
	}
	_, other, _ := e.svc.Create(context.Background(), fresh(admin), board, "Hall 2", 24*time.Hour)
	if other == secret {
		t.Error("every secret is new")
	}
}

func TestCreate_Rejects(t *testing.T) {
	viewer := authz.Principal{User: alice, Org: orgA, Bindings: []authz.Binding{{AllGroups: true, Role: role.Viewer}}}
	groupAdmin := authz.Principal{User: alice, Org: orgA, Bindings: []authz.Binding{{Group: groupX, Role: role.Admin}}}
	tests := []struct {
		name    string
		c       access.Caller
		private bool
		title   string
		ttl     time.Duration
		want    error
	}{
		{"a viewer", fresh(viewer), false, "Hall", 24 * time.Hour, access.ErrForbidden},
		{"an admin of one group", fresh(groupAdmin), false, "Hall", 24 * time.Hour, access.ErrForbidden},
		{"an aged step-up", access.Caller{Principal: admin, StepUpAt: now.Add(-16 * time.Minute)}, false, "Hall", 24 * time.Hour, access.ErrStepUpRequired},
		{"a private dashboard", fresh(admin), true, "Hall", 24 * time.Hour, kiosk.ErrNotShared},
		{"no name", fresh(admin), false, " ", 24 * time.Hour, kiosk.ErrInvalid},
		{"less than a day", fresh(admin), false, "Hall", time.Hour, kiosk.ErrInvalid},
		{"more than a year", fresh(admin), false, "Hall", 366 * 24 * time.Hour, kiosk.ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv()
			e.dashboards.d.Shared = !tt.private
			if _, _, err := e.svc.Create(context.Background(), tt.c, board, tt.title, tt.ttl); !errors.Is(err, tt.want) {
				t.Errorf("Create() = %v, want %v", err, tt.want)
			}
			if len(e.store.tokens) != 0 {
				t.Error("nothing must be stored")
			}
		})
	}
}

func TestRevoke_NeedsNoStepUpAndEndsTheToken(t *testing.T) {
	e := newEnv()
	secret := e.token(t)
	aged := access.Caller{Principal: admin, StepUpAt: now.Add(-time.Hour)}
	if err := e.svc.Revoke(context.Background(), aged, e.store.tokens[0].t.ID); err != nil {
		t.Fatal(err)
	}
	if a := e.store.audits[len(e.store.audits)-1]; a.Action != "kiosk_token.revoke" {
		t.Errorf("audit = %+v", a)
	}
	if _, err := e.svc.Authenticate(context.Background(), secret); !errors.Is(err, kiosk.ErrInvalidToken) {
		t.Errorf("revoked token: %v", err)
	}
	viewer := fresh(authz.Principal{User: alice, Org: orgA, Bindings: []authz.Binding{{AllGroups: true, Role: role.Viewer}}})
	if err := e.svc.Revoke(context.Background(), viewer, e.store.tokens[0].t.ID); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("a viewer revokes: %v", err)
	}
}

func TestAuthenticate(t *testing.T) {
	e := newEnv()
	secret := e.token(t)
	ctx := context.Background()
	d, err := e.svc.Authenticate(ctx, secret)
	if err != nil || d.Org != orgA || d.Dashboard != board || e.store.touched != 1 {
		t.Fatalf("Authenticate() = %+v, %v (touched %d)", d, err, e.store.touched)
	}
	for name, s := range map[string]string{
		"empty":           "",
		"other prefix":    "smx_" + secret[4:],
		"too short":       secret[:len(secret)-1],
		"not base64":      secret[:len(secret)-1] + "!",
		"another secret":  "smk_" + strings.Repeat("A", 43),
		"with whitespace": secret + " ",
	} {
		if _, err := e.svc.Authenticate(ctx, s); !errors.Is(err, kiosk.ErrInvalidToken) {
			t.Errorf("%s: %v, want ErrInvalidToken", name, err)
		}
	}
	// The creator's rights count now: no admin of all groups, no member – nothing.
	e.principals.p = authz.Principal{User: alice, Org: orgA, Bindings: []authz.Binding{{AllGroups: true, Role: role.Operator}}}
	if _, err := e.svc.Authenticate(ctx, secret); !errors.Is(err, kiosk.ErrInvalidToken) {
		t.Errorf("creator without admin: %v", err)
	}
	e.principals.err = access.ErrNoMembership
	if _, err := e.svc.Authenticate(ctx, secret); !errors.Is(err, kiosk.ErrInvalidToken) {
		t.Errorf("creator left: %v", err)
	}
}

func TestAuthenticate_ExpiredTokens(t *testing.T) {
	e := newEnv()
	secret := e.token(t)
	e.store.tokens[0].t.ExpiresAt = now
	if _, err := e.svc.Authenticate(context.Background(), secret); !errors.Is(err, kiosk.ErrInvalidToken) {
		t.Errorf("expired: %v", err)
	}
}

func TestWidget_ReadsTheStoredQueryWithTheCreatorsRights(t *testing.T) {
	e := newEnv()
	ctx := context.Background()
	d, err := e.svc.Authenticate(ctx, e.token(t))
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.Widget(ctx, d, "cpu")
	if err != nil {
		t.Fatal(err)
	}
	q := e.readers.queries[0]
	if q.Metric != "sermon_cpu_utilization_ratio" || q.Labels["mode"] != "user" || len(q.Nodes) != 1 || q.Nodes[0] != db1 {
		t.Errorf("query = %+v; want the stored metric, labels and the default of the variable", q)
	}
	if !q.To.Equal(now) || q.To.Sub(q.From) != 6*time.Hour || res.Unit != "ratio_ppm" || res.Hostnames[db1] != "db-01" {
		t.Errorf("range %v – %v, result %+v", q.From, q.To, res)
	}
	if c := e.readers.callers[0]; c.Principal.User != alice || !c.StepUpAt.IsZero() {
		t.Errorf("caller = %+v; want the creator without a step-up", c)
	}

	logsRes, err := e.svc.Widget(ctx, d, "logs")
	if err != nil || logsRes.Logs == nil || !logsRes.Logs.More {
		t.Fatalf("logs = %+v, %v", logsRes, err)
	}
	f := e.readers.filters[0]
	if f.Group == nil || *f.Group != groupX || f.Text != "error" || f.MinSeverity != logql.Severities[3] || len(f.Nodes) != 0 {
		t.Errorf("log filter = %+v", f)
	}
	alertsRes, err := e.svc.Widget(ctx, d, "alerts")
	if err != nil || len(alertsRes.Alerts) != 1 || alertsRes.Alerts[0].Key.Node != web1 {
		t.Errorf("alerts = %+v, %v; want only those of the widget's node", alertsRes.Alerts, err)
	}
	if _, err := e.svc.Widget(ctx, d, "nope"); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("unknown widget: %v", err)
	}
}

func TestWidget_EndsWhenTheDashboardIsNoLongerShared(t *testing.T) {
	e := newEnv()
	ctx := context.Background()
	d, err := e.svc.Authenticate(ctx, e.token(t))
	if err != nil {
		t.Fatal(err)
	}
	e.dashboards.d.Shared = false
	if _, err := e.svc.Dashboard(ctx, d); !errors.Is(err, kiosk.ErrInvalidToken) {
		t.Errorf("Dashboard() = %v", err)
	}
	if _, err := e.svc.Widget(ctx, d, "cpu"); !errors.Is(err, kiosk.ErrInvalidToken) {
		t.Errorf("Widget() = %v", err)
	}
}

// The hash of the found token is compared again (in constant time): the lookup is no proof.
func TestAuthenticate_ComparesTheHashOfTheFoundToken(t *testing.T) {
	e := newEnv()
	secret := e.token(t)
	e.store.wrongHash = true
	if _, err := e.svc.Authenticate(context.Background(), secret); !errors.Is(err, kiosk.ErrInvalidToken) {
		t.Errorf("Authenticate() = %v, want ErrInvalidToken", err)
	}
}

// A shared dashboard of a viewer, with a token of an admin: the owner may change it at any time,
// so the display reads with the rights of both together (M5-33): what the widgets resolve
// without a fixed target stays within the owner's groups, and the org's switch for logs holds.
func TestWidget_ReadsWithTheRightsOfCreatorAndOwnerTogether(t *testing.T) {
	e := newEnv()
	ctx := context.Background()
	viewerX := authz.Principal{User: bob, Org: orgA, Bindings: []authz.Binding{{Group: groupX, Role: role.Viewer}}}
	e.principals.others = map[identity.UserID]authz.Principal{bob: viewerX}
	e.dashboards.d.Owner = bob
	d, err := e.svc.Authenticate(ctx, e.token(t))
	if err != nil {
		t.Fatal(err)
	}
	// The owner edits the board after the token exists: no defaults, no targets.
	e.dashboards.d.Spec = dashboard.Spec{
		Version: 1, Title: "Board", TimeRange: dashboard.Preset6h,
		Variables: []dashboard.Variable{{Name: "host", Kind: dashboard.VariableNode}, {Name: "team", Kind: dashboard.VariableGroup}},
		Widgets: []dashboard.Widget{
			{ID: "cpu", Kind: dashboard.KindStat, Title: "CPU", Query: dashboard.Query{Metric: "sermon_cpu_utilization_ratio", Target: dashboard.Target{Variable: "host"}}},
			{ID: "team", Kind: dashboard.KindStat, Title: "Team", Query: dashboard.Query{Metric: "sermon_cpu_utilization_ratio", Target: dashboard.Target{Variable: "team"}}},
			{ID: "alerts", Kind: dashboard.KindAlertList, Title: "Alerts"},
			{ID: "logs", Kind: dashboard.KindLogStream, Title: "Logs", Query: dashboard.Query{Target: dashboard.Target{Nodes: []identity.NodeID{web1}}}},
			{ID: "foreign", Kind: dashboard.KindStat, Title: "Y", Query: dashboard.Query{Metric: "sermon_cpu_utilization_ratio", Target: dashboard.Target{Nodes: []identity.NodeID{app1}}}},
		},
	}
	cpu, err := e.svc.Widget(ctx, d, "cpu")
	if err != nil {
		t.Fatal(err)
	}
	if q := e.readers.queries[0]; len(q.Nodes) != 1 || q.Nodes[0] != db1 {
		t.Errorf("node variable without default = %v; want db-01, the first node the owner sees (not app-01 of group Y)", q.Nodes)
	}
	if _, ok := cpu.Hostnames[app1]; ok {
		t.Errorf("host names %v name a node of a group the owner can't see", cpu.Hostnames)
	}
	if _, err := e.svc.Widget(ctx, d, "team"); err != nil {
		t.Fatal(err)
	}
	if q := e.readers.queries[1]; len(q.Nodes) != 2 || groupOf(q.Nodes[0]) != groupX {
		t.Errorf("group variable without default = %v; want group X (not api, the first of the org)", q.Nodes)
	}
	list, err := e.svc.Widget(ctx, d, "alerts")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range list.Alerts {
		if a.Key.Node == app1 {
			t.Errorf("alert list without a target shows %v of group Y", a.Key.Node)
		}
	}
	if len(list.Alerts) != 2 {
		t.Errorf("alerts = %v; want the two of group X", list.Alerts)
	}
	if _, err := e.svc.Widget(ctx, d, "foreign"); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("a node of group Y: %v; want NOT_FOUND as for the owner", err)
	}
	for _, c := range e.readers.callers {
		if c.Principal.User != alice || !c.StepUpAt.IsZero() {
			t.Errorf("caller = %+v; want the creator's identity without a step-up", c)
		}
	}
	// Viewers see no logs while the organization says so; the admin's token doesn't change that.
	e.principals.p.ViewersWithoutLogs = true
	viewerX.ViewersWithoutLogs = true
	e.principals.others[bob] = viewerX
	if _, err := e.svc.Widget(ctx, d, "logs"); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("logs of a viewer's board with the switch off: %v, want forbidden", err)
	}
}

// When the owner left, the display reads nothing (the token stays for the admin to revoke).
func TestWidget_NothingOnceTheOwnerLeft(t *testing.T) {
	e := newEnv()
	ctx := context.Background()
	e.dashboards.d.Owner = bob
	e.principals.others = map[identity.UserID]authz.Principal{bob: {User: bob, Org: orgA, Bindings: []authz.Binding{{AllGroups: true, Role: role.Viewer}}}}
	d, err := e.svc.Authenticate(ctx, e.token(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Widget(ctx, d, "cpu"); err != nil {
		t.Fatalf("owner still a member: %v", err)
	}
	delete(e.principals.others, bob)
	calls := len(e.readers.callers)
	if _, err := e.svc.Widget(ctx, d, "cpu"); !errors.Is(err, kiosk.ErrInvalidToken) {
		t.Errorf("owner left: %v, want ErrInvalidToken", err)
	}
	if len(e.readers.callers) != calls {
		t.Error("nothing must be read once the owner left")
	}
}

// Every read of a display takes a slot (logs one of the log queries' too) and frees it; a full
// limit reads nothing (M5-33, T115).
func TestWidget_ReadsOnlyWithASlot(t *testing.T) {
	e := newEnv()
	ctx := context.Background()
	d, err := e.svc.Authenticate(ctx, e.token(t))
	if err != nil {
		t.Fatal(err)
	}
	if d.Creator != alice {
		t.Errorf("display creator = %v, want alice (the limits count per creator)", d.Creator)
	}
	for _, w := range []string{"cpu", "logs", "alerts"} {
		if _, err := e.svc.Widget(ctx, d, w); err != nil {
			t.Fatal(err)
		}
	}
	if a := e.admission; a.admitted != 3 || a.released != 3 || a.logs != 1 {
		t.Errorf("admission = %+v; want 3 slots, all freed, one for logs", a)
	}
	busy := errors.New("busy")
	e.admission.refuse = busy
	calls := len(e.readers.callers) + len(e.readers.filters)
	if _, err := e.svc.Widget(ctx, d, "logs"); !errors.Is(err, busy) {
		t.Errorf("full: %v, want the admission's error", err)
	}
	if len(e.readers.callers)+len(e.readers.filters) != calls {
		t.Error("nothing must be read without a slot")
	}
}

// A display shows the latest lines: its log queries span at most a day, whatever the board's range.
func TestWidget_LogsSpanAtMostADay(t *testing.T) {
	e := newEnv()
	ctx := context.Background()
	e.dashboards.d.Spec.TimeRange = dashboard.Preset7d
	d, err := e.svc.Authenticate(ctx, e.token(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Widget(ctx, d, "logs"); err != nil {
		t.Fatal(err)
	}
	if f := e.readers.filters[0]; !f.To.Equal(now) || f.To.Sub(f.From) != 24*time.Hour {
		t.Errorf("log span %v – %v, want the last 24 h", f.From, f.To)
	}
}

// V1-13 (review C I5): a display's refresh reads every widget; the fleet's summary is read once
// per token for SummaryFor and only while the rights it was read with hold – the creator's and
// the owner's rights are loaded with every call, a change takes effect at once.
func TestWidget_ReadsTheFleetOncePerTokenAndRights(t *testing.T) {
	e := newEnv()
	clock := now
	e.svc = kiosk.NewService(kiosk.Deps{
		Store: e.store, Dashboards: e.dashboards, Principals: e.principals, Admission: e.admission,
		Readers: kiosk.Readers{Fleet: e.readers, Logs: fakeLogs{e.readers}, Alerts: e.readers},
		Policy:  authz.NewPolicy(), Now: func() time.Time { return clock }, NewID: uuid.New,
	})
	ctx := context.Background()
	viewerX := authz.Principal{User: bob, Org: orgA, Bindings: []authz.Binding{{Group: groupX, Role: role.Viewer}}}
	e.principals.others = map[identity.UserID]authz.Principal{bob: viewerX}
	e.dashboards.d.Owner = bob
	d, err := e.svc.Authenticate(ctx, e.token(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"cpu", "alerts", "cpu", "alerts"} {
		res, err := e.svc.Widget(ctx, d, w)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Hostnames) != 2 {
			t.Errorf("%s: host names %v", w, res.Hostnames)
		}
	}
	if len(e.readers.callers) != 1 {
		t.Errorf("the fleet read %d times for one refresh, want once", len(e.readers.callers))
	}

	// The owner may now see all groups: read again at once, with the new rights.
	e.principals.others[bob] = authz.Principal{User: bob, Org: orgA, Bindings: []authz.Binding{{AllGroups: true, Role: role.Viewer}}}
	res, err := e.svc.Widget(ctx, d, "cpu")
	if err != nil {
		t.Fatal(err)
	}
	if len(e.readers.callers) != 2 || len(res.Hostnames) != 3 {
		t.Errorf("after the rights changed: %d reads, host names %v", len(e.readers.callers), res.Hostnames)
	}
	// And back: nothing of group Y from the read before.
	e.principals.others[bob] = viewerX
	res, err = e.svc.Widget(ctx, d, "cpu")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.Hostnames[app1]; ok || len(e.readers.callers) != 3 {
		t.Errorf("rights lowered: %d reads, host names %v", len(e.readers.callers), res.Hostnames)
	}

	// Another token reads on its own.
	other, err := e.svc.Authenticate(ctx, e.token(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Widget(ctx, other, "cpu"); err != nil || len(e.readers.callers) != 4 {
		t.Errorf("another token: %d reads, %v", len(e.readers.callers), err)
	}
	// After SummaryFor the fleet is read again (new nodes, other states).
	clock = clock.Add(kiosk.SummaryFor - time.Second)
	if _, err := e.svc.Widget(ctx, d, "cpu"); err != nil || len(e.readers.callers) != 4 {
		t.Errorf("within SummaryFor: %d reads, %v", len(e.readers.callers), err)
	}
	clock = clock.Add(time.Second)
	if _, err := e.svc.Widget(ctx, d, "cpu"); err != nil || len(e.readers.callers) != 5 {
		t.Errorf("after SummaryFor: %d reads, %v", len(e.readers.callers), err)
	}
	if kiosk.SummaryFor > 15*time.Second {
		t.Errorf("SummaryFor %v: shorter than half the refresh of a display (30 s)", kiosk.SummaryFor)
	}
}
