package routing

import (
	"context"
	"fmt"
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

// ServiceDeps are the collaborators of Service.
type ServiceDeps struct {
	Store    PolicyStore
	Channels ChannelLister
	Groups   GroupChecker
	Policy   authz.Policy
	Now      func() time.Time
	NewID    func() uuid.UUID
}

// Service manages the notification policies of an organization (admins with ManageChannels,
// like the channels they route to).
type Service struct{ d ServiceDeps }

// NewService returns the service.
func NewService(d ServiceDeps) *Service { return &Service{d: d} }

func (s *Service) authorize(c access.Caller, stepUp bool) (tenant.Scope, error) {
	p := c.Principal
	if !s.d.Policy.Allowed(p, authz.ManageChannels, authz.InOrg(p.Org)) {
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
