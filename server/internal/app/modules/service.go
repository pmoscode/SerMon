// Package modules manages the agent's modules in the server (KONZEPT §15, ADR 0039, M5-06):
// which run on the nodes of a group and with which configuration, and what the nodes report.
// Settings are for admins for all groups (manage_modules); changes need a recent step-up, are
// checked against the module's schema, audited and reach the connected agents at once (ADR
// 0030). What a node reports is for viewers of its group.
package modules

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/module"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// ErrTooMany means a group has the most modules the protocol carries.
var ErrTooMany = &module.InvalidError{Module: "group", Reason: fmt.Sprintf("a group has at most %d modules", module.MaxModules)}

// Group is a node group with its module settings.
type Group struct {
	ID      tenant.GroupID
	Name    string
	Modules []module.Setting
	// Available are the modules the agents of the group's nodes have built in.
	Available []string
}

// View is the module settings of the organization.
type View struct {
	Groups  []Group
	Version uint64
}

// Node is what a node has and reports.
type Node struct {
	ID    identity.NodeID
	Group tenant.GroupID
	// Builtin are the modules its agent has built in (Hello).
	Builtin []string
	// Vetoed: agent.toml forbids modules.
	Vetoed bool
	// Settings are the group's settings (all of them; the service keeps those built in).
	Settings []module.Setting
	// Health and Hints are the agent's newest reports; nil before the first.
	Health *module.HealthReport
	Hints  *module.Hints
}

// NodeHint is a suggestion for one node.
type NodeHint struct {
	Node     identity.NodeID
	Hostname string
	Group    tenant.GroupID
	Hint     module.Hint
}

// MaxHints bounds the suggestions listed for the fleet.
const MaxHints = 500

// Store keeps the settings. Every change raises the organization's configuration version and
// writes the audit entry in the same transaction.
type Store interface {
	View(ctx context.Context, scope tenant.Scope) (View, error)
	// Put stores the setting of a group's module; access.ErrNotFound for a group the scope does
	// not see, ErrTooMany for a new module beyond the bound.
	Put(ctx context.Context, scope tenant.Scope, group tenant.GroupID, s module.Setting, by identity.UserID, audit access.AuditEntry) error
	// Remove deletes it; access.ErrNotFound if the group or its setting does not exist.
	Remove(ctx context.Context, scope tenant.Scope, group tenant.GroupID, moduleID string, by identity.UserID, audit access.AuditEntry) error
	// Node returns a node of the scope's groups; access.ErrNotFound otherwise.
	Node(ctx context.Context, scope tenant.Scope, node identity.NodeID) (Node, error)
	// OpenHints returns the suggestions of the scope's nodes for modules their agent has built in
	// and their group doesn't run, at most limit.
	OpenHints(ctx context.Context, scope tenant.Scope, limit int) ([]NodeHint, error)
}

// Agents learn about a changed configuration (the connected agent sessions).
type Agents interface {
	ConfigChanged(org tenant.OrgID)
}

// Service is the use case.
type Service struct {
	store   Store
	catalog *module.Catalog
	policy  authz.Policy
	agents  Agents
	now     func() time.Time
}

// NewService returns the service.
func NewService(store Store, catalog *module.Catalog, policy authz.Policy, agents Agents, now func() time.Time) *Service {
	return &Service{store: store, catalog: catalog, policy: policy, agents: agents, now: now}
}

// viewScope is the groups whose nodes the caller sees; none is forbidden.
func (s *Service) viewScope(c access.Caller) (tenant.Scope, error) {
	scope, err := s.policy.ScopeForAction(c.Principal, authz.ViewNodes)
	if err != nil || (!scope.AllGroups() && len(scope.Groups()) == 0) {
		return tenant.Scope{}, access.ErrForbidden
	}
	return scope, nil
}

// Modules returns the modules the server knows (every member: the UI labels what nodes report).
func (s *Service) Modules(_ context.Context, c access.Caller) ([]module.Info, error) {
	if _, err := s.viewScope(c); err != nil {
		return nil, err
	}
	return s.catalog.All(), nil
}

// View returns the module settings of every group.
func (s *Service) View(ctx context.Context, c access.Caller) (View, error) {
	scope, err := access.Authorize(s.policy, c, authz.ManageModules, false, s.now())
	if err != nil {
		return View{}, err //nolint:wrapcheck // access errors are mapped by the transport
	}
	view, err := s.store.View(ctx, scope)
	if err != nil {
		return View{}, fmt.Errorf("module settings: %w", err)
	}
	return view, nil
}

// SetGroupModule sets one module of a group after checking it against the module's schema.
func (s *Service) SetGroupModule(ctx context.Context, c access.Caller, group tenant.GroupID, setting module.Setting) error {
	scope, err := access.Authorize(s.policy, c, authz.ManageModules, true, s.now())
	if err != nil {
		return err //nolint:wrapcheck // see View
	}
	// Credentials come with M5-05: none is set yet.
	checked, err := s.catalog.Check(setting, nil)
	if errors.Is(err, module.ErrUnknown) {
		return access.ErrNotFound
	}
	if err != nil {
		return err // the reason goes to the admin
	}
	audit := access.AuditEntry{
		Actor: c.Principal.User, Action: "module.update", Target: "group:" + group.String(),
		Details: map[string]string{
			"module":           checked.Module,
			"enabled":          strconv.FormatBool(checked.Enabled),
			"interval_seconds": strconv.Itoa(int(checked.Interval / time.Second)),
		},
	}
	if err := s.store.Put(ctx, scope, group, checked, c.Principal.User, audit); err != nil {
		return fmt.Errorf("set module: %w", err)
	}
	s.agents.ConfigChanged(c.Principal.Org)
	return nil
}

// RemoveGroupModule turns a module of a group off and forgets its configuration.
func (s *Service) RemoveGroupModule(ctx context.Context, c access.Caller, group tenant.GroupID, moduleID string) error {
	scope, err := access.Authorize(s.policy, c, authz.ManageModules, true, s.now())
	if err != nil {
		return err //nolint:wrapcheck // see View
	}
	if !module.ValidID(moduleID) {
		return access.ErrNotFound
	}
	audit := access.AuditEntry{
		Actor: c.Principal.User, Action: "module.remove", Target: "group:" + group.String(),
		Details: map[string]string{"module": moduleID},
	}
	if err := s.store.Remove(ctx, scope, group, moduleID, c.Principal.User, audit); err != nil {
		return fmt.Errorf("remove module: %w", err)
	}
	s.agents.ConfigChanged(c.Principal.Org)
	return nil
}

// Node returns the modules of a node: access.ErrNotFound for one outside the caller's groups.
// Settings of modules its agent doesn't have built in are left out (it would not run them).
func (s *Service) Node(ctx context.Context, c access.Caller, node identity.NodeID) (Node, error) {
	scope, err := s.viewScope(c)
	if err != nil {
		return Node{}, err
	}
	n, err := s.store.Node(ctx, scope, node)
	if err != nil {
		return Node{}, fmt.Errorf("node modules: %w", err)
	}
	n.Settings = module.ForAgent(n.Settings, n.Builtin)
	return n, nil
}

// Hints returns the open suggestions of the caller's nodes.
func (s *Service) Hints(ctx context.Context, c access.Caller) ([]NodeHint, error) {
	scope, err := s.viewScope(c)
	if err != nil {
		return nil, err
	}
	hints, err := s.store.OpenHints(ctx, scope, MaxHints)
	if err != nil {
		return nil, fmt.Errorf("module hints: %w", err)
	}
	return hints, nil
}
