package fleet

import (
	"context"
	"fmt"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tag"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// TagStore changes the tags of nodes.
type TagStore interface {
	// SetTags replaces the tags of the node if it lies within the scope's groups and writes the
	// audit entry that audit builds from its group and its previous tags, in one transaction.
	// false: no such node within the scope.
	SetTags(ctx context.Context, scope tenant.Scope, node identity.NodeID, tags tag.Tags,
		audit func(group tenant.GroupID, previous tag.Tags) access.AuditEntry) (bool, error)
}

// Tagger sets the free tags of nodes (M2-29). Tags steer rules, notification policies and
// silences, so only admins of the node's group set them, with a fresh step-up and an audit entry.
type Tagger struct {
	store  TagStore
	policy authz.Policy
	now    func() time.Time
}

// NewTagger returns the use case.
func NewTagger(store TagStore, policy authz.Policy, now func() time.Time) *Tagger {
	return &Tagger{store: store, policy: policy, now: now}
}

// SetTags replaces the node's tags (empty: none). Malformed tags are tag.ErrInvalid; a node
// outside the caller's admin groups is access.ErrNotFound, whether it exists or not.
func (t *Tagger) SetTags(ctx context.Context, c access.Caller, node identity.NodeID, tags tag.Tags) error {
	if err := tags.Validate(); err != nil {
		return err
	}
	scope, err := t.policy.ScopeForAction(c.Principal, authz.TagNodes)
	if err != nil || (!scope.AllGroups() && len(scope.Groups()) == 0) {
		return access.ErrForbidden
	}
	if t.now().Sub(c.StepUpAt) > access.StepUpMaxAge {
		return access.ErrStepUpRequired
	}
	if tags == nil {
		tags = tag.Tags{}
	}
	found, err := t.store.SetTags(ctx, scope, node, tags, func(group tenant.GroupID, previous tag.Tags) access.AuditEntry {
		return access.AuditEntry{
			Actor: c.Principal.User, Action: "node.tags_set", Target: "node:" + node.String(),
			Details: map[string]string{"group": group.String(), "tags": tags.String(), "previous": previous.String()},
		}
	})
	if err != nil {
		return fmt.Errorf("set tags: %w", err)
	}
	if !found {
		return access.ErrNotFound
	}
	return nil
}
