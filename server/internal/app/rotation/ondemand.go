package rotation

import (
	"context"
	"fmt"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Target selects the nodes of an on-demand rotation (exactly one field is set).
type Target struct {
	Nodes        []identity.NodeID
	Group        *tenant.GroupID
	Organization bool
}

// NodeRef is a node with its group.
type NodeRef struct {
	Node  identity.NodeID
	Group tenant.GroupID
}

// Result counts the rotated nodes.
type Result struct {
	Online  int // asked to rotate over their stream
	Offline int // certificates revoked, re-enrollment needed
}

// NodeStore finds nodes and revokes their certificates.
type NodeStore interface {
	// NodesForRotation returns the target's nodes within the scope's groups.
	NodesForRotation(ctx context.Context, scope tenant.Scope, t Target) ([]NodeRef, error)
	// RevokeAll revokes all certificates of the node now and writes the audit entry.
	RevokeAll(ctx context.Context, org tenant.OrgID, node identity.NodeID, now time.Time, audit access.AuditEntry) error
}

// Live reaches connected agents (agentapi.Sessions).
type Live interface {
	// RequestRotation asks a connected agent to rotate now; onTimeout runs if it does not answer.
	// It reports false if the node is not connected.
	RequestRotation(org tenant.OrgID, node identity.NodeID, onTimeout func()) bool
	// Terminate ends the node's session.
	Terminate(node identity.NodeID)
}

type onDemand struct {
	nodes  NodeStore
	live   Live
	policy authz.Policy
}

// Option configures a Service.
type Option func(*Service)

// WithOnDemand enables on-demand rotation (web API).
func WithOnDemand(nodes NodeStore, live Live, policy authz.Policy) Option {
	return func(s *Service) { s.onDemand = &onDemand{nodes: nodes, live: live, policy: policy} }
}

// RotateOnDemand rotates the keys of the target's nodes now (KONZEPT §6): connected agents get a
// new certificate over their stream and the old one is revoked at once; offline nodes have all
// certificates revoked. Needs operator on the groups ("*" for the whole organization) and a
// recent re-authentication.
func (s *Service) RotateOnDemand(ctx context.Context, c access.Caller, t Target) (Result, error) {
	if s.onDemand == nil {
		return Result{}, fmt.Errorf("on-demand rotation is not configured")
	}
	scope, err := authz.ScopeFor(c.Principal, role.Operator)
	switch {
	case err != nil:
		return Result{}, access.ErrForbidden
	case t.Organization && !scope.AllGroups(), t.Group != nil && !scope.AllowsGroup(*t.Group):
		return Result{}, access.ErrForbidden
	case s.now().Sub(c.StepUpAt) > access.StepUpMaxAge:
		return Result{}, access.ErrStepUpRequired
	}
	nodes, err := s.onDemand.nodes.NodesForRotation(ctx, scope, t)
	if err != nil {
		return Result{}, fmt.Errorf("find nodes: %w", err)
	}
	if len(t.Nodes) > 0 && len(nodes) != len(t.Nodes) {
		return Result{}, access.ErrNotFound // unknown, foreign or not allowed: not distinguished
	}
	var res Result
	org := c.Principal.Org
	for _, n := range nodes {
		node := n.Node
		audit := func(mode string) access.AuditEntry {
			return access.AuditEntry{
				Actor: c.Principal.User, Action: "certificate.rotate_on_demand", Target: "node:" + node.String(),
				Details: map[string]string{"mode": mode, "group": n.Group.String()},
			}
		}
		timeout := func() {
			// The agent did not rotate in time: revoke everything (re-enrollment needed).
			ctx := context.WithoutCancel(ctx)
			if err := s.onDemand.nodes.RevokeAll(ctx, org, node, s.now(), access.AuditEntry{
				Action: "certificate.rotate_timeout", Target: "node:" + node.String(),
			}); err == nil {
				s.onDemand.live.Terminate(node)
			}
		}
		if s.onDemand.live.RequestRotation(org, node, timeout) {
			if err := s.store.Note(ctx, org, audit("online")); err != nil {
				return res, fmt.Errorf("audit rotation: %w", err)
			}
			res.Online++
			continue
		}
		if err := s.onDemand.nodes.RevokeAll(ctx, org, node, s.now(), audit("offline")); err != nil {
			return res, fmt.Errorf("revoke certificates of %s: %w", node, err)
		}
		res.Offline++
	}
	return res, nil
}
