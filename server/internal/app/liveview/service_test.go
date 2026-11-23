package liveview_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/app/liveview"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	web     = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000aa"))
	db      = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000ab"))
	dbNode  = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000003"))
	userID  = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000101"))
	errGone = errors.New("session ended")
)

// directory lists the nodes of a scope; moved moves the node into the group db.
type directory struct {
	mu    sync.Mutex
	moved bool
}

func (d *directory) List(_ context.Context, scope tenant.Scope) ([]fleet.Node, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	group := web
	if d.moved {
		group = db
	}
	var out []fleet.Node
	for _, n := range []fleet.Node{{ID: node, Group: group}, {ID: dbNode, Group: db}} {
		if scope.AllowsGroup(n.Group) {
			out = append(out, n)
		}
	}
	return out, nil
}

type settings struct {
	mu   sync.Mutex
	hide bool
}

func (s *settings) HideCommandLines(context.Context, tenant.Scope) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hide, nil
}

func viewer(r role.Role, group tenant.GroupID) access.Caller {
	return access.Caller{Principal: authz.Principal{User: userID, Org: org, Bindings: []authz.Binding{{Group: group, Role: r}}}}
}

type world struct {
	agents   *agents
	hub      *liveview.Hub
	dir      *directory
	settings *settings
	svc      *liveview.Service
}

func newWorld() *world {
	w := &world{agents: &agents{connected: true}, dir: &directory{}, settings: &settings{}}
	w.hub = liveview.NewHub(w.agents)
	w.svc = liveview.NewService(w.hub, w.dir, w.settings, authz.NewPolicy(), liveview.WithRecheck(10*time.Millisecond))
	return w
}

// watch runs Watch in the background and collects its frames.
func (w *world) watch(t *testing.T, c access.Caller, n identity.NodeID, recheck func() error) (func() []liveview.Frame, <-chan error, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	var mu sync.Mutex
	var frames []liveview.Frame
	done := make(chan error, 1)
	go func() {
		done <- w.svc.Watch(ctx, c, n, func(f liveview.Frame) error {
			mu.Lock()
			defer mu.Unlock()
			frames = append(frames, f)
			return nil
		}, recheck)
	}()
	return func() []liveview.Frame {
		mu.Lock()
		defer mu.Unlock()
		return append([]liveview.Frame(nil), frames...)
	}, done, cancel
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestWatch_ViewersOfTheGroupGetTheSnapshots(t *testing.T) {
	w := newWorld()
	frames, done, cancel := w.watch(t, viewer(role.Viewer, web), node, nil)
	waitFor(t, func() bool { s, _ := w.agents.counts(); return s == 1 })
	w.hub.Deliver(org, node, snapshot())
	waitFor(t, func() bool { return len(frames()) == 1 })
	if f := frames()[0]; f.Snapshot == nil || f.Snapshot.Processes[0].Name != "mysql" {
		t.Errorf("frame = %+v", f)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Watch() = %v", err)
	}
}

func TestWatch_ForeignNodesAndMissingRights(t *testing.T) {
	w := newWorld()
	if err := w.svc.Watch(t.Context(), viewer(role.Viewer, web), dbNode, func(liveview.Frame) error { return nil }, nil); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("a node of another group: %v", err)
	}
	if err := w.svc.Watch(t.Context(), access.Caller{Principal: authz.Principal{User: userID, Org: org}}, node, func(liveview.Frame) error { return nil }, nil); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("no binding: %v", err)
	}
	if starts, _ := w.agents.counts(); starts != 0 {
		t.Errorf("an agent was started for %d refused viewers", starts)
	}
}

func TestWatch_HidesCommandLinesByTheOrganizationsSwitch(t *testing.T) {
	w := newWorld()
	w.settings.hide = true
	frames, _, cancel := w.watch(t, viewer(role.Admin, web), node, nil)
	defer cancel()
	waitFor(t, func() bool { s, _ := w.agents.counts(); return s == 1 })
	if !w.agents.starts[0] {
		t.Error("the agent was not asked to hide")
	}
	w.hub.Deliver(org, node, snapshot())
	waitFor(t, func() bool { return len(frames()) == 1 })
	if p := frames()[0].Snapshot.Processes[0]; p.Cmdline != nil || !p.CmdlineHidden {
		t.Errorf("process = %+v", p)
	}
}

// Every recheck: the session, the node's visibility and the switch; a change ends the stream.
func TestWatch_RechecksAndEnds(t *testing.T) {
	w := newWorld()
	_, done, cancel := w.watch(t, viewer(role.Viewer, web), node, func() error { return errGone })
	defer cancel()
	if err := <-done; !errors.Is(err, errGone) {
		t.Errorf("failed session check: %v", err)
	}

	_, done, cancel = w.watch(t, viewer(role.Viewer, web), node, nil)
	defer cancel()
	waitFor(t, func() bool { s, _ := w.agents.counts(); return s >= 1 })
	w.dir.mu.Lock()
	w.dir.moved = true
	w.dir.mu.Unlock()
	if err := <-done; !errors.Is(err, access.ErrNotFound) {
		t.Errorf("node moved away: %v", err)
	}
	w.dir.mu.Lock()
	w.dir.moved = false
	w.dir.mu.Unlock()

	_, done, cancel = w.watch(t, viewer(role.Viewer, web), node, nil)
	defer cancel()
	time.Sleep(30 * time.Millisecond)
	w.settings.mu.Lock()
	w.settings.hide = true
	w.settings.mu.Unlock()
	if err := <-done; err != nil {
		t.Errorf("switch changed: %v, want a normal end (the client reconnects)", err)
	}
}

func TestWatch_EndsAfterARefusal(t *testing.T) {
	w := newWorld()
	frames, done, cancel := w.watch(t, viewer(role.Viewer, web), node, nil)
	defer cancel()
	waitFor(t, func() bool { s, _ := w.agents.counts(); return s == 1 })
	w.hub.Refused(org, node, "agent.toml forbids the live view")
	if err := <-done; err != nil {
		t.Errorf("Watch() = %v", err)
	}
	if f := frames(); len(f) != 1 || f[0].Refused == "" {
		t.Errorf("frames = %+v", f)
	}
}

func TestWatch_EndsAfterItsDuration(t *testing.T) {
	clock := t0
	var mu sync.Mutex
	w := newWorld()
	w.svc = liveview.NewService(w.hub, w.dir, w.settings, authz.NewPolicy(), liveview.WithRecheck(5*time.Millisecond),
		liveview.WithClock(func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }))
	_, done, cancel := w.watch(t, viewer(role.Viewer, web), node, nil)
	defer cancel()
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	clock = clock.Add(liveview.MaxDuration)
	mu.Unlock()
	if err := <-done; err != nil {
		t.Errorf("Watch() = %v, want a normal end", err)
	}
}
