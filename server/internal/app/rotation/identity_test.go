package rotation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/rotation"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

func (f *fakeNodes) Unlock(_ context.Context, _ tenant.OrgID, node identity.NodeID, a access.AuditEntry) (bool, error) {
	f.unlocked = append(f.unlocked, node)
	f.audits = append(f.audits, a)
	return f.locked, nil
}

func (f *fakeNodes) AllowTakeover(_ context.Context, _ tenant.OrgID, node identity.NodeID, until time.Time, a access.AuditEntry) error {
	f.takeover = append(f.takeover, node)
	f.until = until
	f.audits = append(f.audits, a)
	return nil
}

// Unlock and allowing a takeover (M2-23): operator on the node's group, a fresh step-up, audit;
// unknown or foreign nodes are NOT_FOUND.
func TestIdentity_UnlockAndTakeoverNeedOperatorAndStepUp(t *testing.T) {
	cases := map[string]struct {
		caller access.Caller
		nodes  []rotation.NodeRef
		want   error
	}{
		"operator":      {operatorOn(time.Minute, group1), []rotation.NodeRef{{Node: online, Group: group1}}, nil},
		"stale step-up": {operatorOn(time.Hour, group1), []rotation.NodeRef{{Node: online, Group: group1}}, access.ErrStepUpRequired},
		"unknown node":  {operatorOn(time.Minute, group1), nil, access.ErrNotFound},
		"viewer": {
			access.Caller{Principal: authz.Principal{User: ann, Org: org, Bindings: []authz.Binding{{AllGroups: true, Role: role.Viewer}}}, StepUpAt: now},
			[]rotation.NodeRef{{Node: online, Group: group1}},
			access.ErrForbidden,
		},
	}
	for name, tc := range cases {
		store := &fakeNodes{fakeStore: &fakeStore{}, nodes: tc.nodes, locked: true}
		svc := onDemand(store, &fakeLive{})
		err := svc.UnlockNode(context.Background(), tc.caller, online)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: unlock = %v, want %v", name, err, tc.want)
		}
		until, err := svc.AllowNodeTakeover(context.Background(), tc.caller, online)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: takeover = %v, want %v", name, err, tc.want)
		}
		if tc.want != nil {
			if len(store.unlocked)+len(store.takeover) != 0 {
				t.Errorf("%s: changed %v %v", name, store.unlocked, store.takeover)
			}
			continue
		}
		if !until.Equal(now.Add(rotation.TakeoverWindow)) || !store.until.Equal(until) || store.takeover[0] != online {
			t.Errorf("%s: takeover until %v (store %v)", name, until, store.until)
		}
		if len(store.audits) != 2 || store.audits[0].Action != "node.unlock" || store.audits[1].Action != "node.takeover_allow" ||
			store.audits[0].Actor != ann || store.audits[1].Details["until"] == "" || store.audits[0].Details["group"] != group1.String() {
			t.Errorf("%s: audits %+v", name, store.audits)
		}
	}
}

// Unlocking a node that is not locked changes nothing but is no error.
func TestIdentity_UnlockOfAnUnlockedNodeIsANoOp(t *testing.T) {
	store := &fakeNodes{fakeStore: &fakeStore{}, nodes: []rotation.NodeRef{{Node: online, Group: group1}}, locked: false}
	if err := onDemand(store, &fakeLive{}).UnlockNode(context.Background(), operatorOn(time.Minute, group1), online); err != nil {
		t.Fatal(err)
	}
}
