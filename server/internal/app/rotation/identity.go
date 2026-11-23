package rotation

import (
	"context"
	"fmt"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
)

// TakeoverWindow is how long an allowed takeover stays open (M2-23).
const TakeoverWindow = time.Hour

// identityTarget checks the caller may perform action on the node and returns it with its group:
// operator on the node's group, a fresh step-up; unknown or foreign nodes are NOT_FOUND.
func (s *Service) identityTarget(ctx context.Context, c access.Caller, action authz.Action, node identity.NodeID) (NodeRef, error) {
	if s.onDemand == nil {
		return NodeRef{}, fmt.Errorf("node identity management is not configured")
	}
	scope, err := s.onDemand.policy.ScopeForAction(c.Principal, action)
	// No group at all: the right is missing. A node of another group is NOT_FOUND below.
	if err != nil || (!scope.AllGroups() && len(scope.Groups()) == 0) {
		return NodeRef{}, access.ErrForbidden
	}
	if s.now().Sub(c.StepUpAt) > access.StepUpMaxAge {
		return NodeRef{}, access.ErrStepUpRequired
	}
	refs, err := s.onDemand.nodes.NodesForRotation(ctx, scope, Target{Nodes: []identity.NodeID{node}})
	if err != nil {
		return NodeRef{}, fmt.Errorf("find node: %w", err)
	}
	if len(refs) != 1 || refs[0].Node != node {
		return NodeRef{}, access.ErrNotFound
	}
	return refs[0], nil
}

// UnlockNode lifts the lock a second session or a rotation from a new address set (M1-11). The
// node's current certificate, revoked by the lock, becomes usable again (M2-39; older ones stay
// revoked): if the key may have been stolen, revoke it instead.
func (s *Service) UnlockNode(ctx context.Context, c access.Caller, node identity.NodeID) error {
	ref, err := s.identityTarget(ctx, c, authz.UnlockNode, node)
	if err != nil {
		return err
	}
	_, err = s.onDemand.nodes.Unlock(ctx, c.Principal.Org, node, access.AuditEntry{
		Actor: c.Principal.User, Action: "node.unlock", Target: "node:" + node.String(),
		Details: map[string]string{"group": ref.Group.String()},
	})
	if err != nil {
		return fmt.Errorf("unlock node: %w", err)
	}
	return nil
}

// AllowNodeTakeover lets the next enrollment with the node's machine-id and a token of its group
// take the node over for TakeoverWindow (T5): the host was reinstalled or cloned on purpose. The
// node keeps its ID and history; its old certificates are revoked by the takeover.
func (s *Service) AllowNodeTakeover(ctx context.Context, c access.Caller, node identity.NodeID) (time.Time, error) {
	ref, err := s.identityTarget(ctx, c, authz.AllowNodeTakeover, node)
	if err != nil {
		return time.Time{}, err
	}
	until := s.now().Add(TakeoverWindow)
	err = s.onDemand.nodes.AllowTakeover(ctx, c.Principal.Org, node, until, access.AuditEntry{
		Actor: c.Principal.User, Action: "node.takeover_allow", Target: "node:" + node.String(),
		Details: map[string]string{"group": ref.Group.String(), "until": until.UTC().Format(time.RFC3339)},
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("allow takeover: %w", err)
	}
	return until, nil
}
