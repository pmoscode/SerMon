package updates

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

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

// ErrTooSoon means a refresh was asked for the node within 5 minutes, or too many for the
// organization within a minute.
var ErrTooSoon = errors.New("refreshed a moment ago, try again later")

// Refresher asks a node's agent for fresh update information (M4-11).
type Refresher interface {
	// RequestRefresh enqueues the refresh; ErrTooSoon beyond the limits.
	RequestRefresh(ctx context.Context, org tenant.OrgID, node identity.NodeID, by identity.UserID, now time.Time) error
}

// Service shows the reports of the nodes a caller may view (ViewNodes) and asks for fresh ones
// (RunUpdates).
type Service struct {
	store     Store
	policy    authz.Policy
	refresher Refresher
	now       func() time.Time
	manual    ManualStore
	policies  PolicyStore
	newID     func() uuid.UUID
	history   History
}

// NewService returns the service.
func NewService(store Store, policy authz.Policy) *Service {
	return &Service{store: store, policy: policy, now: time.Now, newID: func() uuid.UUID { return uuid.Must(uuid.NewV7()) }}
}

// WithRefresher lets the service ask agents for fresh update information.
func (s *Service) WithRefresher(r Refresher, now func() time.Time) *Service {
	s.refresher, s.now = r, now
	return s
}

// Refresh asks the node's agent to list its updates with fresh sources (run_updates on its
// group): access.ErrNotFound for a node the caller can't view, access.ErrForbidden without the
// right, ErrTooSoon beyond the limits.
func (s *Service) Refresh(ctx context.Context, c access.Caller, node identity.NodeID) error {
	n, err := s.Node(ctx, c, node)
	if err != nil {
		return err
	}
	if !s.policy.Allowed(c.Principal, authz.RunUpdates, authz.InGroup(c.Principal.Org, n.Group)) {
		return access.ErrForbidden
	}
	if s.refresher == nil {
		return errors.New("no refresher")
	}
	if err := s.refresher.RequestRefresh(ctx, c.Principal.Org, node, c.Principal.User, s.now()); err != nil {
		return fmt.Errorf("request refresh: %w", err)
	}
	return nil
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
