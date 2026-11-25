package updates

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// PolicyStore keeps the update policies of the groups.
type PolicyStore interface {
	// Policy returns the group's policy (the default without one); false if the group isn't in
	// the scope.
	Policy(ctx context.Context, scope tenant.Scope, group tenant.GroupID) (Policy, bool, error)
	// Policies returns the policies of all groups of the scope (defaults included), by group name.
	Policies(ctx context.Context, scope tenant.Scope) ([]NamedPolicy, error)
	// PutPolicy stores p (nil: deletes the group's policy) with the audit entry in one
	// transaction; access.ErrNotFound for an unknown group.
	PutPolicy(ctx context.Context, scope tenant.Scope, group tenant.GroupID, p *Policy, audit access.AuditEntry) error
}

// NamedPolicy is a policy with the name of its group.
type NamedPolicy struct {
	Policy
	GroupName string
}

// PolicyService reads the update policies for the callers with ViewNodes on the group and changes
// them for admins with a fresh step-up (KONZEPT §14.2, M4-10).
type PolicyService struct {
	store  PolicyStore
	policy authz.Policy
	now    func() time.Time
}

// NewPolicyService returns the service.
func NewPolicyService(store PolicyStore, policy authz.Policy, now func() time.Time) *PolicyService {
	return &PolicyService{store: store, policy: policy, now: now}
}

func (s *PolicyService) viewScope(c access.Caller) (tenant.Scope, error) {
	scope, err := s.policy.ScopeForAction(c.Principal, authz.ViewNodes)
	if err != nil || (!scope.AllGroups() && len(scope.Groups()) == 0) {
		return tenant.Scope{}, access.ErrForbidden
	}
	return scope, nil
}

// Get returns the group's policy; access.ErrNotFound for a group the caller can't view.
func (s *PolicyService) Get(ctx context.Context, c access.Caller, group tenant.GroupID) (Policy, error) {
	scope, err := s.viewScope(c)
	if err != nil {
		return Policy{}, err
	}
	p, ok, err := s.store.Policy(ctx, scope, group)
	if err != nil {
		return Policy{}, fmt.Errorf("read update policy: %w", err)
	}
	if !ok {
		return Policy{}, access.ErrNotFound
	}
	return p, nil
}

// List returns the policies of the groups the caller can view.
func (s *PolicyService) List(ctx context.Context, c access.Caller) ([]NamedPolicy, error) {
	scope, err := s.viewScope(c)
	if err != nil {
		return nil, err
	}
	out, err := s.store.Policies(ctx, scope)
	if err != nil {
		return nil, fmt.Errorf("list update policies: %w", err)
	}
	return out, nil
}

// Set replaces the group's policy (manage_policies, step-up); ErrInvalidPolicy if it is malformed.
func (s *PolicyService) Set(ctx context.Context, c access.Caller, p Policy) (Policy, error) {
	scope, err := access.Authorize(s.policy, c, authz.ManagePolicies, true, s.now())
	if err != nil {
		return Policy{}, err //nolint:wrapcheck // access errors map to codes
	}
	if err := p.Validate(); err != nil {
		return Policy{}, err
	}
	p.UpdatedAt, p.UpdatedBy = s.now(), c.Principal.User
	details := map[string]string{
		"mode": p.Mode.String(), "auto_reboot": strconv.FormatBool(p.AutoReboot),
		"max_parallel": strconv.Itoa(p.Rollout.MaxParallel), "stop_on_failure": strconv.FormatBool(p.Rollout.StopOnFailure),
		"health_check": duration(p.Rollout.HealthCheck),
	}
	if w := p.Window; w != nil {
		details["window"] = fmt.Sprintf("%v %s+%s %s", w.Days, duration(w.Start), duration(w.Duration), w.TimeZone)
	}
	audit := access.AuditEntry{Actor: c.Principal.User, Action: "update_policy.update", Target: "group:" + p.Group.String(), Details: details}
	if err := s.store.PutPolicy(ctx, scope, p.Group, &p, audit); err != nil {
		return Policy{}, fmt.Errorf("set update policy: %w", err)
	}
	return p, nil
}

// Import reads the policy of the group from YAML and sets it like Set.
func (s *PolicyService) Import(ctx context.Context, c access.Caller, group tenant.GroupID, data []byte) (Policy, error) {
	if _, err := access.Authorize(s.policy, c, authz.ManagePolicies, true, s.now()); err != nil {
		return Policy{}, err //nolint:wrapcheck // access errors map to codes; checked before parsing
	}
	p, err := ParsePolicy(group, data)
	if err != nil {
		return Policy{}, err
	}
	return s.Set(ctx, c, p)
}

// Reset returns the group to the default (report only).
func (s *PolicyService) Reset(ctx context.Context, c access.Caller, group tenant.GroupID) error {
	scope, err := access.Authorize(s.policy, c, authz.ManagePolicies, true, s.now())
	if err != nil {
		return err //nolint:wrapcheck // access errors map to codes
	}
	audit := access.AuditEntry{Actor: c.Principal.User, Action: "update_policy.reset", Target: "group:" + group.String()}
	if err := s.store.PutPolicy(ctx, scope, group, nil, audit); err != nil {
		return fmt.Errorf("reset update policy: %w", err)
	}
	return nil
}
