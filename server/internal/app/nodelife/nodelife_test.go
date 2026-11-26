package nodelife_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/nodelife"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	orgA   = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
	orgB   = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000b"))
	web    = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000aa"))
	db     = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000bb"))
	other  = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000cc"))
	webN   = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001"))
	dbN    = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000002"))
	otherN = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000003"))
	now    = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
)

type node struct {
	org         tenant.OrgID
	group       tenant.GroupID
	hostname    string
	deactivated *time.Time
	due         *time.Time
	revokedAt   *time.Time
	purged      bool
}

// store behaves like the Postgres store: scope checks, one audit entry per change, purge only
// of deactivated nodes that are due.
type store struct {
	nodes   map[identity.NodeID]*node
	audits  []access.AuditEntry
	purges  []identity.NodeID
	failDue error
}

func newStore() *store {
	return &store{nodes: map[identity.NodeID]*node{
		webN:   {org: orgA, group: web, hostname: "web-01"},
		dbN:    {org: orgA, group: db, hostname: "db-01"},
		otherN: {org: orgB, group: other, hostname: "web-01"},
	}}
}

func (s *store) State(_ context.Context, scope tenant.Scope, id identity.NodeID) (nodelife.State, error) {
	n, ok := s.nodes[id]
	if !ok || n.purged || n.org != scope.OrgID() || !scope.AllowsGroup(n.group) {
		return nodelife.State{}, access.ErrNotFound
	}
	return nodelife.State{Group: n.group, Hostname: n.hostname, DeactivatedAt: n.deactivated, DeletionDueAt: n.due}, nil
}

func (s *store) Deactivate(_ context.Context, org tenant.OrgID, id identity.NodeID, at time.Time, audit access.AuditEntry) error {
	n := s.nodes[id]
	if n.org != org {
		return errors.New("wrong org")
	}
	if n.deactivated == nil {
		n.deactivated = &at
	}
	n.revokedAt = &at
	s.audits = append(s.audits, audit)
	return nil
}

func (s *store) ScheduleDeletion(_ context.Context, org tenant.OrgID, id identity.NodeID, at, due time.Time, audit access.AuditEntry) error {
	n := s.nodes[id]
	if n.org != org {
		return errors.New("wrong org")
	}
	if n.deactivated == nil {
		n.deactivated = &at
		n.revokedAt = &at
	}
	n.due = &due
	s.audits = append(s.audits, audit)
	return nil
}

func (s *store) CancelDeletion(_ context.Context, org tenant.OrgID, id identity.NodeID, audit access.AuditEntry) error {
	n := s.nodes[id]
	if n.org != org {
		return errors.New("wrong org")
	}
	n.due = nil
	s.audits = append(s.audits, audit)
	return nil
}

func (s *store) DueForDeletion(_ context.Context, at time.Time, limit int) ([]nodelife.Due, error) {
	if s.failDue != nil {
		return nil, s.failDue
	}
	var out []nodelife.Due
	for id, n := range s.nodes {
		if !n.purged && n.deactivated != nil && n.due != nil && !n.due.After(at) && len(out) < limit {
			out = append(out, nodelife.Due{Org: n.org, Node: id})
		}
	}
	return out, nil
}

func (s *store) Purge(_ context.Context, org tenant.OrgID, id identity.NodeID, at time.Time) error {
	n, ok := s.nodes[id]
	if !ok || n.org != org || n.deactivated == nil || n.due == nil || n.due.After(at) {
		return errors.New("not deactivated and due")
	}
	n.purged = true
	s.purges = append(s.purges, id)
	return nil
}

type live struct{ ended []identity.NodeID }

func (l *live) Terminate(id identity.NodeID, _ error) { l.ended = append(l.ended, id) }

type eraser struct {
	erased []identity.NodeID
	fail   error
}

func (e *eraser) EraseNode(_ context.Context, _ tenant.OrgID, id identity.NodeID) error {
	if e.fail != nil {
		return e.fail
	}
	e.erased = append(e.erased, id)
	return nil
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func caller(stepUp time.Duration, org tenant.OrgID, bindings ...authz.Binding) access.Caller {
	return access.Caller{
		Principal: authz.Principal{User: identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000f1")), Org: org, Bindings: bindings},
		StepUpAt:  now.Add(-stepUp),
	}
}

var (
	adminWeb = caller(time.Minute, orgA, authz.Binding{Group: web, Role: role.Admin})
	adminAll = caller(time.Minute, orgA, authz.Binding{AllGroups: true, Role: role.Admin})
)

type fixture struct {
	store  *store
	live   *live
	eraser *eraser
	clock  *clock
	svc    *nodelife.Service
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	f := fixture{store: newStore(), live: &live{}, eraser: &eraser{}, clock: &clock{t: now}}
	svc, err := nodelife.NewService(f.store, authz.NewPolicy(), f.clock.now, nodelife.DefaultDeletionGrace,
		nodelife.WithLive(f.live), nodelife.WithLogs(f.eraser))
	if err != nil {
		t.Fatal(err)
	}
	f.svc = svc
	return f
}

// Rights (ADR 0041): admin on the node's group with a fresh step-up; a node of another group or
// organization is NOT_FOUND, its existence not revealed; nothing changes on a refusal.
func TestService_Rights(t *testing.T) {
	cases := map[string]struct {
		caller access.Caller
		node   identity.NodeID
		want   error
	}{
		"admin of the group":    {adminWeb, webN, nil},
		"admin of all groups":   {adminAll, dbN, nil},
		"stale step-up":         {caller(time.Hour, orgA, authz.Binding{Group: web, Role: role.Admin}), webN, access.ErrStepUpRequired},
		"operator":              {caller(time.Minute, orgA, authz.Binding{Group: web, Role: role.Operator}), webN, access.ErrForbidden},
		"viewer of all groups":  {caller(time.Minute, orgA, authz.Binding{AllGroups: true, Role: role.Viewer}), webN, access.ErrForbidden},
		"node of another group": {adminWeb, dbN, access.ErrNotFound},
		"node of another org":   {adminAll, otherN, access.ErrNotFound},
		"unknown node":          {adminWeb, identity.NodeID{9}, access.ErrNotFound},
	}
	ops := map[string]func(f fixture, c access.Caller, n identity.NodeID) error{
		"deactivate": func(f fixture, c access.Caller, n identity.NodeID) error {
			return f.svc.Deactivate(context.Background(), c, n)
		},
		"delete": func(f fixture, c access.Caller, n identity.NodeID) error {
			host := "web-01"
			if n == dbN {
				host = "db-01"
			}
			_, err := f.svc.Delete(context.Background(), c, n, host)
			return err
		},
	}
	for op, run := range ops {
		for name, tc := range cases {
			f := newFixture(t)
			err := run(f, tc.caller, tc.node)
			if !errors.Is(err, tc.want) {
				t.Errorf("%s, %s: %v, want %v", op, name, err, tc.want)
			}
			if tc.want != nil && (len(f.store.audits) != 0 || len(f.live.ended) != 0) {
				t.Errorf("%s, %s: refused but changed: audits %v, ended %v", op, name, f.store.audits, f.live.ended)
			}
		}
	}
}

// Deactivating revokes the certificates, ends the session, keeps the data and is audited once.
func TestService_DeactivateRevokesAndEndsTheSession(t *testing.T) {
	f := newFixture(t)
	if err := f.svc.Deactivate(context.Background(), adminWeb, webN); err != nil {
		t.Fatal(err)
	}
	n := f.store.nodes[webN]
	if n.deactivated == nil || n.revokedAt == nil || n.due != nil {
		t.Errorf("node after deactivation = %+v", n)
	}
	if len(f.live.ended) != 1 || f.live.ended[0] != webN {
		t.Errorf("sessions ended = %v, want web-01", f.live.ended)
	}
	if len(f.store.audits) != 1 || f.store.audits[0].Action != "node.deactivate" || f.store.audits[0].Target != "node:"+webN.String() ||
		f.store.audits[0].Details["hostname"] != "web-01" || f.store.audits[0].Details["group"] != web.String() {
		t.Errorf("audit = %+v", f.store.audits)
	}
	// Again: nothing new.
	if err := f.svc.Deactivate(context.Background(), adminWeb, webN); err != nil || len(f.store.audits) != 1 {
		t.Errorf("second deactivation: %v, audits %d", err, len(f.store.audits))
	}
}

// Deleting needs the host name, deactivates and sets the deadline after the grace; a second
// request keeps the first deadline; cancelling lifts it and the node stays deactivated.
func TestService_DeleteSchedulesAndCancelLiftsIt(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.svc.Delete(ctx, adminWeb, webN, "web-02"); !errors.Is(err, nodelife.ErrHostnameMismatch) {
		t.Errorf("wrong host name: %v", err)
	}
	if _, err := f.svc.Delete(ctx, adminWeb, webN, "WEB-01"); !errors.Is(err, nodelife.ErrHostnameMismatch) {
		t.Errorf("host name in other case: %v", err)
	}
	if len(f.store.audits) != 0 {
		t.Fatalf("refused but audited: %v", f.store.audits)
	}
	due, err := f.svc.Delete(ctx, adminWeb, webN, "  web-01 ")
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(nodelife.DefaultDeletionGrace); !due.Equal(want) {
		t.Errorf("due = %v, want %v", due, want)
	}
	n := f.store.nodes[webN]
	if n.deactivated == nil || n.revokedAt == nil || n.due == nil || !n.due.Equal(due) {
		t.Errorf("node = %+v", n)
	}
	if len(f.live.ended) != 1 {
		t.Errorf("sessions ended = %v", f.live.ended)
	}
	if a := f.store.audits; len(a) != 1 || a[0].Action != "node.schedule_deletion" || a[0].Details["deletion_due_at"] != due.Format(time.RFC3339) {
		t.Errorf("audit = %+v", a)
	}
	f.clock.t = now.Add(5 * time.Minute)
	again, err := f.svc.Delete(ctx, adminWeb, webN, "web-01")
	if err != nil || !again.Equal(due) || len(f.store.audits) != 1 {
		t.Errorf("second delete = %v, %v (audits %d), want the first deadline", again, err, len(f.store.audits))
	}

	if err := f.svc.CancelDeletion(ctx, caller(time.Minute, orgA, authz.Binding{Group: web, Role: role.Operator}), webN); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("operator cancels: %v", err)
	}
	if err := f.svc.CancelDeletion(ctx, adminWeb, webN); err != nil {
		t.Fatal(err)
	}
	if n.due != nil || n.deactivated == nil {
		t.Errorf("after cancel = %+v, want deactivated without deadline", n)
	}
	if a := f.store.audits; len(a) != 2 || a[1].Action != "node.cancel_deletion" {
		t.Errorf("audit = %+v", a)
	}
	if err := f.svc.CancelDeletion(ctx, adminWeb, webN); !errors.Is(err, nodelife.ErrNotScheduled) {
		t.Errorf("cancel without a deadline: %v", err)
	}
}

// The job erases the logs first and purges only what is due; a failing log store leaves the
// node due for the next run.
func TestService_MaintainPurgesDueNodesAfterTheirLogs(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.svc.Delete(ctx, adminAll, webN, "web-01"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Deactivate(ctx, adminAll, dbN); err != nil {
		t.Fatal(err)
	}
	f.clock.t = now.Add(nodelife.DefaultDeletionGrace - time.Second)
	if err := f.svc.Maintain(ctx); err != nil || len(f.store.purges) != 0 || len(f.eraser.erased) != 0 {
		t.Fatalf("before the deadline: %v, purged %v, erased %v", err, f.store.purges, f.eraser.erased)
	}
	f.clock.t = now.Add(nodelife.DefaultDeletionGrace)
	f.eraser.fail = errors.New("loki down")
	if err := f.svc.Maintain(ctx); err == nil || len(f.store.purges) != 0 {
		t.Fatalf("with Loki down: %v, purged %v", err, f.store.purges)
	}
	f.eraser.fail = nil
	if err := f.svc.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.eraser.erased) != 1 || len(f.store.purges) != 1 || f.store.purges[0] != webN {
		t.Errorf("erased %v, purged %v, want web-01 only (db-01 is only deactivated)", f.eraser.erased, f.store.purges)
	}
	if err := f.svc.Maintain(ctx); err != nil || len(f.store.purges) != 1 {
		t.Errorf("again: %v, purged %v", err, f.store.purges)
	}
	f.store.failDue = errors.New("db down")
	if err := f.svc.Maintain(ctx); err == nil {
		t.Error("a failing store went unreported")
	}
}

func TestNewService_RefusesAShortGrace(t *testing.T) {
	if _, err := nodelife.NewService(newStore(), authz.NewPolicy(), time.Now, nodelife.MinDeletionGrace-time.Second); !errors.Is(err, nodelife.ErrGraceTooShort) {
		t.Errorf("NewService = %v, want ErrGraceTooShort", err)
	}
	if _, err := nodelife.NewService(newStore(), authz.NewPolicy(), time.Now, nodelife.MinDeletionGrace); err != nil {
		t.Errorf("minimum grace: %v", err)
	}
}
