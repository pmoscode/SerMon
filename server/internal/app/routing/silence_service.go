package routing

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Retention of ended silences: the list shows them for a day, the table keeps them 30 days.
const (
	EndedSilencesShown = 24 * time.Hour
	EndedSilencesKept  = 30 * 24 * time.Hour
)

// SilenceStore keeps the silences of an organization. Silence and ExpireSilence return
// access.ErrNotFound outside the scope (ExpireSilence also once it ended).
type SilenceStore interface {
	Silences(ctx context.Context, scope tenant.Scope, since time.Time) ([]Silence, error)
	Silence(ctx context.Context, scope tenant.Scope, id SilenceID) (Silence, error)
	CreateSilence(ctx context.Context, scope tenant.Scope, s Silence, purgeBefore time.Time, audit access.AuditEntry) error
	ExpireSilence(ctx context.Context, scope tenant.Scope, id SilenceID, at time.Time, by identity.UserID, audit access.AuditEntry) error
}

// SilenceDeps are the collaborators of SilenceService.
type SilenceDeps struct {
	Store  SilenceStore
	Nodes  fleet.Directory
	Policy authz.Policy
	Now    func() time.Time
	NewID  func() uuid.UUID
}

// SilenceService lets operators mute the alerts of their groups for a while (SilenceAlerts,
// group-scoped); a silence for all groups needs the right on all groups.
type SilenceService struct{ d SilenceDeps }

// NewSilenceService returns the service.
func NewSilenceService(d SilenceDeps) *SilenceService { return &SilenceService{d: d} }

// allowed reports whether scope covers the silence's groups (all groups for none).
func allowed(scope tenant.Scope, groups []tenant.GroupID) bool {
	if len(groups) == 0 {
		return scope.AllGroups()
	}
	return !slices.ContainsFunc(groups, func(g tenant.GroupID) bool { return !scope.AllowsGroup(g) })
}

// silenceScope is where the caller may silence; ErrForbidden if nowhere.
func (s *SilenceService) silenceScope(c access.Caller) (tenant.Scope, error) {
	scope, err := s.d.Policy.ScopeForAction(c.Principal, authz.SilenceAlerts)
	if err != nil || (!scope.AllGroups() && len(scope.Groups()) == 0) {
		return tenant.Scope{}, access.ErrForbidden
	}
	return scope, nil
}

func orgScope(org tenant.OrgID) (tenant.Scope, error) {
	scope, err := tenant.ForAllGroups(org)
	if err != nil {
		return tenant.Scope{}, access.ErrForbidden
	}
	return scope, nil
}

// List returns the silences that have not ended or ended within a day, as far as the caller
// sees their groups (ViewNodes); silences for all groups name no nodes and are shown to every
// member.
func (s *SilenceService) List(ctx context.Context, c access.Caller) ([]Silence, error) {
	view, err := s.d.Policy.ScopeForAction(c.Principal, authz.ViewNodes)
	if err != nil {
		return nil, access.ErrForbidden
	}
	org, err := orgScope(c.Principal.Org)
	if err != nil {
		return nil, err
	}
	list, err := s.d.Store.Silences(ctx, org, s.d.Now().Add(-EndedSilencesShown))
	if err != nil {
		return nil, fmt.Errorf("list silences: %w", err)
	}
	return slices.DeleteFunc(list, func(x Silence) bool {
		return len(x.Match.Groups) > 0 && !allowed(view, x.Match.Groups)
	}), nil
}

// Create adds a silence (step-up required): within the caller's groups, with nodes of those
// groups, at most MaxOpenSilences that have not ended. A zero start means now.
func (s *SilenceService) Create(ctx context.Context, c access.Caller, x Silence) (Silence, error) {
	may, err := s.silenceScope(c)
	if err != nil {
		return Silence{}, err
	}
	now := s.d.Now()
	// Silences can hide security alerts: a hijacked session must not mute them.
	if now.Sub(c.StepUpAt) > access.StepUpMaxAge {
		return Silence{}, access.ErrStepUpRequired
	}
	if !allowed(may, x.Match.Groups) {
		return Silence{}, access.ErrForbidden
	}
	if x.StartsAt.IsZero() {
		x.StartsAt = now
	}
	if err := x.Validate(now); err != nil {
		return Silence{}, err
	}
	org, err := orgScope(c.Principal.Org)
	if err != nil {
		return Silence{}, err
	}
	if err := s.checkTargets(ctx, org, x); err != nil {
		return Silence{}, err
	}
	open, err := s.d.Store.Silences(ctx, org, now)
	if err != nil {
		return Silence{}, fmt.Errorf("list silences: %w", err)
	}
	if len(open) >= MaxOpenSilences {
		return Silence{}, invalidSilence("an organization has at most %d silences that have not ended", MaxOpenSilences)
	}
	x.ID, x.CreatedAt, x.CreatedBy, x.ExpiredBy = SilenceID(s.d.NewID()), now, c.Principal.User, identity.UserID{}
	audit := access.AuditEntry{
		Actor: c.Principal.User, Action: "alert_silence.create", Target: "alert_silence:" + x.ID.String(),
		Details: map[string]string{"starts": x.StartsAt.UTC().Format(time.RFC3339), "ends": x.EndsAt.UTC().Format(time.RFC3339)},
	}
	if err := s.d.Store.CreateSilence(ctx, org, x, now.Add(-EndedSilencesKept), audit); err != nil {
		return Silence{}, fmt.Errorf("store silence: %w", err)
	}
	return x, nil
}

// checkTargets checks that the groups exist and the nodes belong to them.
func (s *SilenceService) checkTargets(ctx context.Context, org tenant.Scope, x Silence) error {
	for _, g := range x.Match.Groups {
		ok, err := s.d.Nodes.HasGroup(ctx, org, g)
		if err != nil {
			return fmt.Errorf("check group: %w", err)
		}
		if !ok {
			return invalidSilence("group %s does not exist", uuid.UUID(g))
		}
	}
	if len(x.Nodes) == 0 {
		return nil
	}
	in, err := tenant.ForGroups(org.OrgID(), x.Match.Groups...)
	if err != nil {
		return fmt.Errorf("scope: %w", err)
	}
	nodes, err := s.d.Nodes.List(ctx, in)
	if err != nil {
		return fmt.Errorf("list nodes: %w", err)
	}
	for _, n := range x.Nodes {
		if !slices.ContainsFunc(nodes, func(f fleet.Node) bool { return f.ID == n }) {
			return invalidSilence("node %s is not in the silence's groups", n)
		}
	}
	return nil
}

// Expire ends a silence now (no step-up: it only makes alerts audible again); access.ErrNotFound
// if it does not exist or ended already.
func (s *SilenceService) Expire(ctx context.Context, c access.Caller, id SilenceID) error {
	may, err := s.silenceScope(c)
	if err != nil {
		return err
	}
	org, err := orgScope(c.Principal.Org)
	if err != nil {
		return err
	}
	x, err := s.d.Store.Silence(ctx, org, id)
	if err != nil {
		return fmt.Errorf("load silence: %w", err)
	}
	if !allowed(may, x.Match.Groups) {
		return access.ErrForbidden
	}
	audit := access.AuditEntry{Actor: c.Principal.User, Action: "alert_silence.expire", Target: "alert_silence:" + id.String()}
	if err := s.d.Store.ExpireSilence(ctx, org, id, s.d.Now(), c.Principal.User, audit); err != nil {
		return fmt.Errorf("expire silence: %w", err)
	}
	return nil
}
