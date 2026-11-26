package updates_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/updates"
	"github.com/pmoscode/SerMon/server/internal/domain/command"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

func cancelService(run command.Command) (*updates.Service, *manualStore) {
	m := &manualStore{}
	svc := updates.NewService(&store{}, authz.NewPolicy()).
		WithRefresher(&refresher{}, func() time.Time { return t0 }).
		WithManual(m, &policyStore{}, uuid.New).
		WithHistory(&history{run: run, refresh: command.Command{ID: uuid.New(), Node: node, Kind: command.Refresh}})
	return svc, m
}

// M4-31 (T97): a run the node hasn't started is cancelled by those who may start it, with a
// fresh step-up, in the audit log; checked in this order, so a stale step-up is asked for before
// the run's state is told.
func TestService_CancelsARunNotStarted(t *testing.T) {
	ctx := context.Background()
	pending := command.Command{ID: uuid.New(), Node: node, Kind: command.Reboot, State: command.Pending, CreatedAt: t0, ExpiresAt: t0.Add(time.Minute)}
	svc, m := cancelService(pending)
	if err := svc.CancelRun(ctx, fresh(caller(role.Operator, false)), pending.ID); err != nil {
		t.Fatal(err)
	}
	if a := m.audit[0]; len(m.cancelled) != 1 || a.Action != "node.run_cancelled" || a.Target != "node:"+node.String() ||
		a.Details["run"] != pending.ID.String() || a.Details["kind"] != "reboot" {
		t.Errorf("cancelled %v, audit %+v", m.cancelled, m.audit)
	}
	elsewhere := fresh(access.Caller{Principal: authz.Principal{User: user, Org: org, Bindings: []authz.Binding{
		{Group: tenant.GroupID(uuid.New()), Role: role.Admin},
	}}})
	for name, c := range map[string]struct {
		caller access.Caller
		id     uuid.UUID
		want   error
	}{
		"viewer":      {fresh(caller(role.Viewer, true)), pending.ID, access.ErrForbidden},
		"no step-up":  {caller(role.Admin, true), pending.ID, access.ErrStepUpRequired},
		"unknown run": {fresh(caller(role.Admin, true)), uuid.New(), access.ErrNotFound},
		"other group": {elsewhere, pending.ID, access.ErrNotFound},
	} {
		if err := svc.CancelRun(ctx, c.caller, c.id); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	running := pending
	running.State = command.Running
	svc, _ = cancelService(running)
	if err := svc.CancelRun(ctx, caller(role.Admin, true), running.ID); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("running, no step-up: %v", err)
	}
	if err := svc.CancelRun(ctx, fresh(caller(role.Admin, true)), running.ID); !errors.Is(err, updates.ErrNotCancellable) {
		t.Errorf("running: %v", err)
	}
	svc, m = cancelService(pending)
	m.refuse = true
	if err := svc.CancelRun(ctx, fresh(caller(role.Admin, true)), pending.ID); !errors.Is(err, updates.ErrNotCancellable) {
		t.Errorf("started meanwhile: %v", err)
	}
}

// M4-31: a running rollout is cancelled by those who may run updates in its group.
func TestService_CancelsARollout(t *testing.T) {
	ctx := context.Background()
	svc, m := cancelService(command.Command{})
	m.rollout = &updates.RolloutRun{ID: uuid.New(), Group: groupX, State: updates.RolloutRunning}
	if err := svc.CancelRollout(ctx, fresh(caller(role.Operator, false)), m.rollout.ID); err != nil {
		t.Fatal(err)
	}
	if a := m.audit[0]; len(m.cancelled) != 1 || a.Action != "updates.cancel" || a.Target != "rollout:"+m.rollout.ID.String() {
		t.Errorf("cancelled %v, audit %+v", m.cancelled, m.audit)
	}
	if err := svc.CancelRollout(ctx, fresh(caller(role.Viewer, true)), m.rollout.ID); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("viewer: %v", err)
	}
	if err := svc.CancelRollout(ctx, caller(role.Admin, true), m.rollout.ID); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("no step-up: %v", err)
	}
	if err := svc.CancelRollout(ctx, fresh(caller(role.Admin, true)), uuid.New()); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	m.rollout.State = updates.RolloutDone
	if err := svc.CancelRollout(ctx, fresh(caller(role.Admin, true)), m.rollout.ID); !errors.Is(err, updates.ErrRolloutEnded) {
		t.Errorf("ended: %v", err)
	}
	m.rollout.State, m.refuse = updates.RolloutRunning, true
	if err := svc.CancelRollout(ctx, fresh(caller(role.Admin, true)), m.rollout.ID); !errors.Is(err, updates.ErrRolloutEnded) {
		t.Errorf("ended meanwhile: %v", err)
	}
}

// M4-31 (T97): while the organization's updates are paused nothing starts by hand; reboots and
// self-updates by hand count against the limits of the organization and the instance, and a
// node that reboots already or runs in a rollout gets neither.
func TestService_RunsByHandRespectThePauseAndTheLimits(t *testing.T) {
	ctx := context.Background()
	operator := fresh(caller(role.Operator, false))
	try := func(m func(*manualStore)) map[string]error {
		svc, store := manualService()
		m(store)
		_, update := svc.StartUpdates(ctx, operator, []identity.NodeID{node}, command.ScopeAll, false)
		_, reboot := svc.RebootNodes(ctx, operator, []identity.NodeID{node}, "kernel")
		return map[string]error{"update": update, "reboot": reboot}
	}
	for name, got := range try(func(m *manualStore) { m.under.Paused = true }) {
		if !errors.Is(got, updates.ErrPaused) {
			t.Errorf("paused, %s: %v", name, got)
		}
	}
	for setup, want := range map[string]func(*manualStore){
		"organization full": func(m *manualStore) { m.under.Org = updates.MaxRunsPerOrg },
		"instance full":     func(m *manualStore) { m.under.Instance = updates.MaxRunsPerInstance },
	} {
		if got := try(want)["reboot"]; !errors.Is(got, updates.ErrTooManyRuns) {
			t.Errorf("%s: %v", setup, got)
		}
	}
	for setup, under := range map[string]updates.UnderWay{
		"rebooting":    {Rebooting: map[identity.NodeID]bool{node: true}},
		"in a rollout": {InRollout: map[identity.NodeID]bool{node: true}},
	} {
		if got := try(func(m *manualStore) { m.under = under })["reboot"]; !errors.Is(got, updates.ErrUnderWay) {
			t.Errorf("%s: %v", setup, got)
		}
	}
	if got := try(func(*manualStore) {}); got["update"] != nil || got["reboot"] != nil {
		t.Errorf("with room: %v", got)
	}
}

// M4-31: admins pause and resume the organization's updates with a fresh step-up, in the audit
// log; whoever views the policies sees since when.
func TestPolicyService_PausesTheUpdates(t *testing.T) {
	ctx := context.Background()
	store := &policyStore{}
	svc := updates.NewPolicyService(store, authz.NewPolicy(), func() time.Time { return t0 })
	since, err := svc.SetPaused(ctx, fresh(caller(role.Admin, true)), true)
	if err != nil || since == nil || !since.Equal(t0) || store.audit[0].Action != "updates.pause" {
		t.Fatalf("pause = %v, %v, %+v", since, err, store.audit)
	}
	if got, err := svc.PausedSince(ctx, caller(role.Viewer, false)); err != nil || got == nil || !got.Equal(t0) {
		t.Errorf("a viewer reads %v, %v", got, err)
	}
	if _, err := svc.SetPaused(ctx, fresh(caller(role.Operator, true)), false); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("operator: %v", err)
	}
	if _, err := svc.SetPaused(ctx, caller(role.Admin, true), false); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("no step-up: %v", err)
	}
	if since, err := svc.SetPaused(ctx, fresh(caller(role.Admin, true)), false); err != nil || since != nil || store.audit[1].Action != "updates.resume" {
		t.Errorf("resume = %v, %v, %+v", since, err, store.audit)
	}
}
