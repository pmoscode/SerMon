package rotation_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/rotation"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	group1  = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000c1"))
	group2  = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000c2"))
	online  = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000d1"))
	offline = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000d2"))
	ann     = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001"))
)

type fakeNodes struct {
	*fakeStore
	nodes   []rotation.NodeRef
	scope   tenant.Scope
	revoked []identity.NodeID
	// M2-23
	locked   bool
	unlocked []identity.NodeID
	takeover []identity.NodeID
	until    time.Time
}

func (f *fakeNodes) NodesForRotation(_ context.Context, scope tenant.Scope, _ rotation.Target) ([]rotation.NodeRef, error) {
	f.scope = scope
	return f.nodes, nil
}

func (f *fakeNodes) RevokeAll(_ context.Context, _ tenant.OrgID, node identity.NodeID, _ time.Time, a access.AuditEntry) error {
	f.revoked = append(f.revoked, node)
	f.audits = append(f.audits, a)
	return nil
}

type fakeLive struct {
	online     map[identity.NodeID]bool
	requested  []identity.NodeID
	terminated []identity.NodeID
	timeout    func()
}

func (f *fakeLive) RequestRotation(_ tenant.OrgID, node identity.NodeID, onTimeout func()) bool {
	if !f.online[node] {
		return false
	}
	f.requested = append(f.requested, node)
	f.timeout = onTimeout
	return true
}

func (f *fakeLive) Terminate(node identity.NodeID, _ error) {
	f.terminated = append(f.terminated, node)
}

func onDemand(store *fakeNodes, live *fakeLive) *rotation.Service {
	return rotation.NewService(store, fakeSigner{}, func() time.Time { return now },
		rotation.WithOnDemand(store, live, authz.NewPolicy()))
}

func operatorOn(stepUp time.Duration, groups ...tenant.GroupID) access.Caller {
	var bindings []authz.Binding
	for _, g := range groups {
		bindings = append(bindings, authz.Binding{Group: g, Role: role.Operator})
	}
	return access.Caller{Principal: authz.Principal{User: ann, Org: org, Bindings: bindings}, StepUpAt: now.Add(-stepUp)}
}

func TestRotateOnDemand_AsksOnlineAgentsAndRevokesOfflineNodes(t *testing.T) {
	store := &fakeNodes{fakeStore: &fakeStore{}, nodes: []rotation.NodeRef{{Node: online, Group: group1}, {Node: offline, Group: group1}}}
	live := &fakeLive{online: map[identity.NodeID]bool{online: true}}

	res, err := onDemand(store, live).RotateOnDemand(context.Background(), operatorOn(time.Minute, group1), rotation.Target{Group: &group1})
	if err != nil {
		t.Fatal(err)
	}
	if res.Online != 1 || res.Offline != 1 || len(live.requested) != 1 || live.requested[0] != online {
		t.Errorf("result %+v, requested %v", res, live.requested)
	}
	if len(store.revoked) != 1 || store.revoked[0] != offline {
		t.Errorf("revoked = %v, want only the offline node", store.revoked)
	}
	if !store.scope.AllowsGroup(group1) || store.scope.AllowsGroup(group2) {
		t.Errorf("scope = %+v", store.scope)
	}
	actions := map[string]int{}
	for _, a := range store.audits {
		if a.Actor == ann {
			actions[a.Action]++
		}
	}
	if actions["certificate.rotate_on_demand"] != 2 {
		t.Errorf("audits = %+v", store.audits)
	}

	// An online agent that does not answer in time loses its certificates as well.
	live.timeout()
	if len(store.revoked) != 2 || store.revoked[1] != online {
		t.Errorf("after the timeout revoked = %v", store.revoked)
	}
}

func TestRotateOnDemand_NeedsOperatorRightsAndAStepUp(t *testing.T) {
	ctx := context.Background()
	cases := map[string]struct {
		caller access.Caller
		target rotation.Target
		want   error
	}{
		"stale step-up":      {operatorOn(time.Hour, group1), rotation.Target{Group: &group1}, access.ErrStepUpRequired},
		"other group":        {operatorOn(time.Minute, group2), rotation.Target{Group: &group1}, access.ErrForbidden},
		"organization w/o *": {operatorOn(time.Minute, group1), rotation.Target{Organization: true}, access.ErrForbidden},
		"viewer":             {access.Caller{Principal: authz.Principal{User: ann, Org: org, Bindings: []authz.Binding{{AllGroups: true, Role: role.Viewer}}}, StepUpAt: now}, rotation.Target{Organization: true}, access.ErrForbidden},
	}
	for name, tc := range cases {
		store := &fakeNodes{fakeStore: &fakeStore{}}
		if _, err := onDemand(store, &fakeLive{}).RotateOnDemand(ctx, tc.caller, tc.target); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
		if len(store.revoked) != 0 {
			t.Errorf("%s: nodes revoked", name)
		}
	}
}

func TestRotateOnDemand_UnknownNodesAreNotFound(t *testing.T) {
	store := &fakeNodes{fakeStore: &fakeStore{}, nodes: []rotation.NodeRef{{Node: offline, Group: group1}}}
	all := access.Caller{Principal: authz.Principal{User: ann, Org: org, Bindings: []authz.Binding{{AllGroups: true, Role: role.Admin}}}, StepUpAt: now}
	_, err := onDemand(store, &fakeLive{}).RotateOnDemand(context.Background(), all, rotation.Target{Nodes: []identity.NodeID{offline, online}})
	if !errors.Is(err, access.ErrNotFound) || len(store.revoked) != 0 {
		t.Errorf("error = %v, revoked %v; want ErrNotFound and nothing changed", err, store.revoked)
	}
}

// If a key may have been stolen, the agent connected right now could be the thief: asking it to
// rotate would hand it the new certificate (M1-38). RevokeOnly revokes everything and ends the
// session instead; the real hosts enroll again.
func TestRotateOnDemand_RevokeOnlyNeverHandsOutANewCertificate(t *testing.T) {
	store := &fakeNodes{fakeStore: &fakeStore{}, nodes: []rotation.NodeRef{{Node: online, Group: group1}, {Node: offline, Group: group1}}}
	live := &fakeLive{online: map[identity.NodeID]bool{online: true}}

	res, err := onDemand(store, live).RotateOnDemand(context.Background(), operatorOn(time.Minute, group1), rotation.Target{Group: &group1, RevokeOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(live.requested) != 0 {
		t.Errorf("asked %v to rotate, want nobody", live.requested)
	}
	if res.Revoked != 2 || res.Online != 0 || res.Offline != 0 || len(store.revoked) != 2 {
		t.Errorf("result %+v, revoked %v; want both nodes revoked", res, store.revoked)
	}
	if !slices.Contains(live.terminated, online) {
		t.Errorf("terminated %v, want the session of the connected node ended", live.terminated)
	}
	if len(store.audits) != 2 || store.audits[0].Details["mode"] != "revoke" {
		t.Errorf("audits = %+v, want mode revoke", store.audits)
	}
}
