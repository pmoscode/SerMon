package fleet_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tag"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

type taggedNode struct {
	org   tenant.OrgID
	group tenant.GroupID
	tags  tag.Tags
}

// tagStore keeps tags per node and the audit entries it wrote, like the transaction of the
// Postgres store.
type tagStore struct {
	nodes  map[identity.NodeID]*taggedNode
	audits []access.AuditEntry
}

func (s *tagStore) SetTags(_ context.Context, scope tenant.Scope, node identity.NodeID, tags tag.Tags, audit func(tenant.GroupID, tag.Tags) access.AuditEntry) (bool, error) {
	n, ok := s.nodes[node]
	if !ok || n.org != scope.OrgID() || !scope.AllowsGroup(n.group) {
		return false, nil
	}
	s.audits = append(s.audits, audit(n.group, n.tags))
	n.tags = tags
	return true, nil
}

func tagStoreWithNodes() *tagStore {
	return &tagStore{nodes: map[identity.NodeID]*taggedNode{
		webN:   {org: orgA, group: web, tags: tag.Tags{"env": "staging"}},
		dbN:    {org: orgA, group: db},
		otherN: {org: orgB, group: other},
	}}
}

func callerWith(stepUp time.Duration, bindings ...authz.Binding) access.Caller {
	return access.Caller{Principal: authz.Principal{User: identity.UserID{1}, Org: orgA, Bindings: bindings}, StepUpAt: now.Add(-stepUp)}
}

// Tags (M2-29): admin on the node's group with a fresh step-up; everything else changes nothing.
// A node of another group or organization is NOT_FOUND, its existence is not revealed.
func TestTagger_SetTags(t *testing.T) {
	adminWeb := authz.Binding{Group: web, Role: role.Admin}
	prod := tag.Tags{"env": "prod", "role": "web"}
	cases := map[string]struct {
		caller access.Caller
		node   identity.NodeID
		tags   tag.Tags
		want   error
	}{
		"admin of the group":       {callerWith(time.Minute, adminWeb), webN, prod, nil},
		"admin of all groups":      {callerWith(time.Minute, authz.Binding{AllGroups: true, Role: role.Admin}), webN, prod, nil},
		"stale step-up":            {callerWith(time.Hour, adminWeb), webN, prod, access.ErrStepUpRequired},
		"operator":                 {callerWith(time.Minute, authz.Binding{Group: web, Role: role.Operator}), webN, prod, access.ErrForbidden},
		"node of another group":    {callerWith(time.Minute, adminWeb), dbN, prod, access.ErrNotFound},
		"node of another org":      {callerWith(time.Minute, authz.Binding{AllGroups: true, Role: role.Admin}), otherN, prod, access.ErrNotFound},
		"unknown node":             {callerWith(time.Minute, adminWeb), identity.NodeID{9}, prod, access.ErrNotFound},
		"invalid tags":             {callerWith(time.Minute, adminWeb), webN, tag.Tags{"Env": "prod"}, tag.ErrInvalid},
		"invalid before forbidden": {callerWith(time.Minute, authz.Binding{Group: web, Role: role.Viewer}), webN, tag.Tags{"env": ""}, tag.ErrInvalid},
	}
	for name, tc := range cases {
		store := tagStoreWithNodes()
		tagger := fleet.NewTagger(store, authz.NewPolicy(), func() time.Time { return now })
		err := tagger.SetTags(context.Background(), tc.caller, tc.node, tc.tags)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: SetTags = %v, want %v", name, err, tc.want)
			continue
		}
		if tc.want != nil {
			if store.nodes[webN].tags["env"] != "staging" || len(store.audits) != 0 {
				t.Errorf("%s: changed %v, audits %v", name, store.nodes[webN].tags, store.audits)
			}
			continue
		}
		if got := store.nodes[tc.node].tags; got.String() != "env=prod,role=web" {
			t.Errorf("%s: tags %v", name, got)
		}
		a := store.audits
		if len(a) != 1 || a[0].Action != "node.tags_set" || a[0].Target != "node:"+tc.node.String() ||
			a[0].Details["group"] != web.String() || a[0].Details["tags"] != "env=prod,role=web" || a[0].Details["previous"] != "env=staging" {
			t.Errorf("%s: audit %+v", name, a)
		}
	}
}

// Empty tags remove all of them.
func TestTagger_EmptyTagsClear(t *testing.T) {
	store := tagStoreWithNodes()
	tagger := fleet.NewTagger(store, authz.NewPolicy(), func() time.Time { return now })
	if err := tagger.SetTags(context.Background(), callerWith(time.Minute, authz.Binding{Group: web, Role: role.Admin}), webN, nil); err != nil {
		t.Fatal(err)
	}
	if len(store.nodes[webN].tags) != 0 || store.audits[0].Details["tags"] != "" {
		t.Errorf("tags %v, audit %+v", store.nodes[webN].tags, store.audits)
	}
}
