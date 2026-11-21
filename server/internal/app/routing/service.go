package routing

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// PolicyStore keeps the policies of an organization, ordered by name. Update and Delete return
// access.ErrNotFound for a policy outside the scope.
type PolicyStore interface {
	Policies(ctx context.Context, scope tenant.Scope) ([]Policy, error)
	CreatePolicy(ctx context.Context, scope tenant.Scope, p Policy, audit access.AuditEntry) error
	UpdatePolicy(ctx context.Context, scope tenant.Scope, p Policy, audit access.AuditEntry) error
	DeletePolicy(ctx context.Context, scope tenant.Scope, id PolicyID, audit access.AuditEntry) error
}

// ChannelLister lists the channels of an organization (notify.Store).
type ChannelLister interface {
	Channels(ctx context.Context, scope tenant.Scope) ([]notify.StoredChannel, error)
}

// GroupChecker tells whether a group exists (fleet.Directory).
type GroupChecker interface {
	HasGroup(ctx context.Context, scope tenant.Scope, group tenant.GroupID) (bool, error)
}

// WindowStore keeps the maintenance windows of an organization, ordered by name. Update and
// Delete return access.ErrNotFound for a window outside the scope.
type WindowStore interface {
	Windows(ctx context.Context, scope tenant.Scope) ([]MaintenanceWindow, error)
	CreateWindow(ctx context.Context, scope tenant.Scope, w MaintenanceWindow, audit access.AuditEntry) error
	UpdateWindow(ctx context.Context, scope tenant.Scope, w MaintenanceWindow, audit access.AuditEntry) error
	DeleteWindow(ctx context.Context, scope tenant.Scope, id WindowID, audit access.AuditEntry) error
}

// ServiceDeps are the collaborators of Service.
type ServiceDeps struct {
	Store    PolicyStore
	Windows  WindowStore
	Channels ChannelLister
	Groups   GroupChecker
	Policy   authz.Policy
	Now      func() time.Time
	NewID    func() uuid.UUID
}

// Service manages the notification policies and maintenance windows of an organization (admins
// with ManagePolicies).
type Service struct{ d ServiceDeps }

// NewService returns the service.
func NewService(d ServiceDeps) *Service { return &Service{d: d} }

func (s *Service) authorize(c access.Caller, stepUp bool) (tenant.Scope, error) {
	p := c.Principal
	if !s.d.Policy.Allowed(p, authz.ManagePolicies, authz.InOrg(p.Org)) {
		return tenant.Scope{}, access.ErrForbidden
	}
	// Policies decide who hears of security events: a hijacked session must not redirect them.
	if stepUp && s.d.Now().Sub(c.StepUpAt) > access.StepUpMaxAge {
		return tenant.Scope{}, access.ErrStepUpRequired
	}
	scope, err := tenant.ForAllGroups(p.Org)
	if err != nil {
		return tenant.Scope{}, access.ErrForbidden
	}
	return scope, nil
}

// List returns the organization's policies.
func (s *Service) List(ctx context.Context, c access.Caller) ([]Policy, error) {
	scope, err := s.authorize(c, false)
	if err != nil {
		return nil, err
	}
	list, err := s.d.Store.Policies(ctx, scope)
	if err != nil {
		return nil, fmt.Errorf("list policies: %w", err)
	}
	return list, nil
}

// check validates a policy and that its channels and groups belong to the organization.
func (s *Service) check(ctx context.Context, scope tenant.Scope, p *Policy) error {
	if err := p.Validate(); err != nil {
		return err
	}
	stored, err := s.d.Channels.Channels(ctx, scope)
	if err != nil {
		return fmt.Errorf("list channels: %w", err)
	}
	known := make(map[notify.ChannelID]bool, len(stored))
	for _, c := range stored {
		known[c.ID] = true
	}
	targets := p.Channels
	if p.Escalation != nil {
		targets = append(targets[:len(targets):len(targets)], p.Escalation.Channels...)
	}
	for _, c := range targets {
		if !known[c] {
			return invalid("channel %s does not exist", c)
		}
	}
	for _, g := range p.Match.Groups {
		ok, err := s.d.Groups.HasGroup(ctx, scope, g)
		if err != nil {
			return fmt.Errorf("check group: %w", err)
		}
		if !ok {
			return invalid("group %s does not exist", uuid.UUID(g))
		}
	}
	return nil
}

func audit(c access.Caller, action string, p Policy) access.AuditEntry {
	return access.AuditEntry{
		Actor: c.Principal.User, Action: "notification_policy." + action, Target: "notification_policy:" + p.ID.String(),
		Details: details(p),
	}
}

// Create adds a policy (step-up required).
func (s *Service) Create(ctx context.Context, c access.Caller, p Policy) (Policy, error) {
	scope, err := s.authorize(c, true)
	if err != nil {
		return Policy{}, err
	}
	if err := s.check(ctx, scope, &p); err != nil {
		return Policy{}, err
	}
	existing, err := s.d.Store.Policies(ctx, scope)
	if err != nil {
		return Policy{}, fmt.Errorf("list policies: %w", err)
	}
	if len(existing) >= MaxPolicies {
		return Policy{}, invalid("an organization has at most %d policies", MaxPolicies)
	}
	now := s.d.Now()
	p.ID, p.CreatedAt, p.UpdatedAt, p.CreatedBy = PolicyID(s.d.NewID()), now, now, c.Principal.User
	if err := s.d.Store.CreatePolicy(ctx, scope, p, audit(c, "create", p)); err != nil {
		return Policy{}, fmt.Errorf("store policy: %w", err)
	}
	return p, nil
}

// Update replaces a policy (step-up required); its ID and creation stay.
func (s *Service) Update(ctx context.Context, c access.Caller, id PolicyID, p Policy) (Policy, error) {
	scope, err := s.authorize(c, true)
	if err != nil {
		return Policy{}, err
	}
	list, err := s.d.Store.Policies(ctx, scope)
	if err != nil {
		return Policy{}, fmt.Errorf("list policies: %w", err)
	}
	var old *Policy
	for i := range list {
		if list[i].ID == id {
			old = &list[i]
		}
	}
	if old == nil {
		return Policy{}, access.ErrNotFound
	}
	if err := s.check(ctx, scope, &p); err != nil {
		return Policy{}, err
	}
	p.ID, p.CreatedAt, p.CreatedBy, p.UpdatedAt = id, old.CreatedAt, old.CreatedBy, s.d.Now()
	if err := s.d.Store.UpdatePolicy(ctx, scope, p, audit(c, "update", p)); err != nil {
		return Policy{}, fmt.Errorf("store policy: %w", err)
	}
	return p, nil
}

// Delete removes a policy (step-up required).
func (s *Service) Delete(ctx context.Context, c access.Caller, id PolicyID) error {
	scope, err := s.authorize(c, true)
	if err != nil {
		return err
	}
	if err := s.d.Store.DeletePolicy(ctx, scope, id, audit(c, "delete", Policy{ID: id})); err != nil {
		return fmt.Errorf("delete policy: %w", err)
	}
	return nil
}

func details(p Policy) map[string]string {
	if p.Name == "" {
		return nil
	}
	return map[string]string{"name": p.Name}
}

// ListWindows returns the organization's maintenance windows.
func (s *Service) ListWindows(ctx context.Context, c access.Caller) ([]MaintenanceWindow, error) {
	scope, err := s.authorize(c, false)
	if err != nil {
		return nil, err
	}
	list, err := s.d.Windows.Windows(ctx, scope)
	if err != nil {
		return nil, fmt.Errorf("list maintenance windows: %w", err)
	}
	return list, nil
}

func (s *Service) checkWindow(ctx context.Context, scope tenant.Scope, w *MaintenanceWindow) error {
	if err := w.Validate(); err != nil {
		return err
	}
	for _, g := range w.Groups {
		ok, err := s.d.Groups.HasGroup(ctx, scope, g)
		if err != nil {
			return fmt.Errorf("check group: %w", err)
		}
		if !ok {
			return invalidWindow("group %s does not exist", uuid.UUID(g))
		}
	}
	return nil
}

func windowAudit(c access.Caller, action string, w MaintenanceWindow) access.AuditEntry {
	a := access.AuditEntry{Actor: c.Principal.User, Action: "maintenance_window." + action, Target: "maintenance_window:" + w.ID.String()}
	if w.Name != "" {
		a.Details = map[string]string{"name": w.Name}
	}
	return a
}

// CreateWindow adds a maintenance window (step-up required).
func (s *Service) CreateWindow(ctx context.Context, c access.Caller, w MaintenanceWindow) (MaintenanceWindow, error) {
	scope, err := s.authorize(c, true)
	if err != nil {
		return MaintenanceWindow{}, err
	}
	if err := s.checkWindow(ctx, scope, &w); err != nil {
		return MaintenanceWindow{}, err
	}
	existing, err := s.d.Windows.Windows(ctx, scope)
	if err != nil {
		return MaintenanceWindow{}, fmt.Errorf("list maintenance windows: %w", err)
	}
	if len(existing) >= MaxWindows {
		return MaintenanceWindow{}, invalidWindow("an organization has at most %d maintenance windows", MaxWindows)
	}
	now := s.d.Now()
	w.ID, w.CreatedAt, w.UpdatedAt, w.CreatedBy = WindowID(s.d.NewID()), now, now, c.Principal.User
	if err := s.d.Windows.CreateWindow(ctx, scope, w, windowAudit(c, "create", w)); err != nil {
		return MaintenanceWindow{}, fmt.Errorf("store maintenance window: %w", err)
	}
	return w, nil
}

// UpdateWindow replaces a maintenance window (step-up required); its ID and creation stay.
func (s *Service) UpdateWindow(ctx context.Context, c access.Caller, id WindowID, w MaintenanceWindow) (MaintenanceWindow, error) {
	scope, err := s.authorize(c, true)
	if err != nil {
		return MaintenanceWindow{}, err
	}
	list, err := s.d.Windows.Windows(ctx, scope)
	if err != nil {
		return MaintenanceWindow{}, fmt.Errorf("list maintenance windows: %w", err)
	}
	i := slices.IndexFunc(list, func(x MaintenanceWindow) bool { return x.ID == id })
	if i < 0 {
		return MaintenanceWindow{}, access.ErrNotFound
	}
	if err := s.checkWindow(ctx, scope, &w); err != nil {
		return MaintenanceWindow{}, err
	}
	w.ID, w.CreatedAt, w.CreatedBy, w.UpdatedAt = id, list[i].CreatedAt, list[i].CreatedBy, s.d.Now()
	if err := s.d.Windows.UpdateWindow(ctx, scope, w, windowAudit(c, "update", w)); err != nil {
		return MaintenanceWindow{}, fmt.Errorf("store maintenance window: %w", err)
	}
	return w, nil
}

// DeleteWindow removes a maintenance window (step-up required).
func (s *Service) DeleteWindow(ctx context.Context, c access.Caller, id WindowID) error {
	scope, err := s.authorize(c, true)
	if err != nil {
		return err
	}
	if err := s.d.Windows.DeleteWindow(ctx, scope, id, windowAudit(c, "delete", MaintenanceWindow{ID: id})); err != nil {
		return fmt.Errorf("delete maintenance window: %w", err)
	}
	return nil
}
