package updates

import (
	"context"
	"fmt"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	"github.com/pmoscode/SerMon/server/internal/domain/update"
)

// Fleet is the overview: every visible node with its status, and how many nodes have each status.
type Fleet struct {
	Nodes          []NodeUpdates
	Counts         map[update.Status]int
	RebootRequired int
}

// Service shows the reports of the nodes a caller may view (ViewNodes).
type Service struct {
	store  Store
	policy authz.Policy
}

// NewService returns the service.
func NewService(store Store, policy authz.Policy) *Service {
	return &Service{store: store, policy: policy}
}

func (s *Service) scope(c access.Caller) (tenant.Scope, error) {
	scope, err := s.policy.ScopeForAction(c.Principal, authz.ViewNodes)
	if err != nil || (!scope.AllGroups() && len(scope.Groups()) == 0) {
		return tenant.Scope{}, access.ErrForbidden
	}
	return scope, nil
}

// Node returns a node's report with its packages; access.ErrNotFound for a node the caller can't
// view (also of another organization), access.ErrForbidden without ViewNodes on any group.
func (s *Service) Node(ctx context.Context, c access.Caller, node identity.NodeID) (NodeUpdates, error) {
	scope, err := s.scope(c)
	if err != nil {
		return NodeUpdates{}, err
	}
	n, ok, err := s.store.Node(ctx, scope, node)
	if err != nil {
		return NodeUpdates{}, fmt.Errorf("read node updates: %w", err)
	}
	if !ok {
		return NodeUpdates{}, access.ErrNotFound
	}
	return n, nil
}

// Fleet returns the visible nodes with their status, without packages.
func (s *Service) Fleet(ctx context.Context, c access.Caller) (Fleet, error) {
	scope, err := s.scope(c)
	if err != nil {
		return Fleet{}, err
	}
	nodes, err := s.store.Fleet(ctx, scope)
	if err != nil {
		return Fleet{}, fmt.Errorf("read fleet updates: %w", err)
	}
	fleet := Fleet{Nodes: nodes, Counts: map[update.Status]int{}}
	for _, n := range nodes {
		fleet.Counts[n.Status()]++
		if n.Report != nil && n.Report.RebootRequired {
			fleet.RebootRequired++
		}
	}
	return fleet, nil
}
