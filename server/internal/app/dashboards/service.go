// Package dashboards keeps the custom dashboards of an organization (KONZEPT §16, ADR 0040).
// Members see their own and the shared ones; the owner changes them, shared ones also an admin of
// all groups. The service stores documents only: the widgets read through the existing readers
// with the viewer's rights. Saving checks every node and group the document names against what
// the caller may view.
package dashboards

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/dashboard"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// MaxPerOrganization bounds the dashboards of an organization.
const MaxPerOrganization = 200

// WritesPerMinute bounds a member's writes of dashboards (each one is an audit entry, M5-33).
const WritesPerMinute = 60

var (
	// ErrTooMany means the organization has MaxPerOrganization dashboards.
	ErrTooMany = fmt.Errorf("at most %d dashboards per organization", MaxPerOrganization)
	// ErrConflict means the dashboard changed since the revision the change is based on.
	ErrConflict = errors.New("the dashboard was changed meanwhile, load it again")
	// ErrUnknownReference means the document names a node or group the caller can't view: of
	// another group, another organization or nobody (the same answer for all, NOT_FOUND).
	ErrUnknownReference = errors.New("not found")
	// ErrTooManyWrites means the member changed dashboards WritesPerMinute times within a minute.
	ErrTooManyWrites = errors.New("too many changes of dashboards, wait a moment")
)

// Limiter allows a number of calls per key and window (ratelimit.Limiter).
type Limiter interface{ Allow(key string) bool }

// ID identifies a dashboard.
type ID uuid.UUID

// String returns the UUID.
func (id ID) String() string { return uuid.UUID(id).String() }

// Dashboard is a stored dashboard.
type Dashboard struct {
	ID        ID
	Owner     identity.UserID
	OwnerName string
	Shared    bool
	Revision  int64
	Spec      dashboard.Spec
	CreatedAt time.Time
	UpdatedAt time.Time
	// CanEdit tells whether the caller may change, share and delete it (set by the service).
	CanEdit bool
}

// Found are the nodes and groups of a list the scope sees.
type Found struct {
	Nodes  map[identity.NodeID]bool
	Groups map[tenant.GroupID]bool
}

// Store keeps the dashboards. List, Get, Change and Delete only see the user's own and the
// shared dashboards of the scope's organization (access.ErrNotFound otherwise).
type Store interface {
	List(ctx context.Context, scope tenant.Scope, user identity.UserID) ([]Dashboard, error)
	Get(ctx context.Context, scope tenant.Scope, user identity.UserID, id ID) (Dashboard, error)
	// Create stores d with the audit entry; ErrTooMany at MaxPerOrganization.
	Create(ctx context.Context, scope tenant.Scope, d Dashboard, audit access.AuditEntry) error
	// Change locks the dashboard, stores what change returns and its audit entry in one
	// transaction; an error of change is returned as is.
	Change(ctx context.Context, scope tenant.Scope, user identity.UserID, id ID,
		change func(Dashboard) (Dashboard, access.AuditEntry, error)) (Dashboard, error)
	// Delete locks the dashboard and deletes it if check allows, with its audit entry.
	Delete(ctx context.Context, scope tenant.Scope, user identity.UserID, id ID,
		check func(Dashboard) (access.AuditEntry, error)) error
	// Visible returns which of the nodes and groups are in the scope's groups.
	Visible(ctx context.Context, scope tenant.Scope, nodes []identity.NodeID, groups []tenant.GroupID) (Found, error)
}

// Service keeps the dashboards.
type Service struct {
	store  Store
	policy authz.Policy
	writes Limiter
	now    func() time.Time
	newID  func() uuid.UUID
}

// NewService returns the service; writes limits each member's writes (WritesPerMinute).
func NewService(store Store, policy authz.Policy, writes Limiter, now func() time.Time, newID func() uuid.UUID) *Service {
	return &Service{store: store, policy: policy, writes: writes, now: now, newID: newID}
}

// authorize lets members with a role on any group use dashboards; the scope is the whole
// organization (dashboards belong to it, not to a group).
func (s *Service) authorize(c access.Caller) (tenant.Scope, error) {
	p := c.Principal
	if !s.policy.Allowed(p, authz.UseDashboards, authz.InOrg(p.Org)) {
		return tenant.Scope{}, access.ErrForbidden
	}
	scope, err := tenant.ForAllGroups(p.Org)
	if err != nil {
		return tenant.Scope{}, access.ErrForbidden
	}
	return scope, nil
}

// authorizeWrite authorizes as authorize and counts the write against the member's limit.
func (s *Service) authorizeWrite(c access.Caller) (tenant.Scope, error) {
	scope, err := s.authorize(c)
	if err != nil {
		return tenant.Scope{}, err
	}
	if !s.writes.Allow(c.Principal.Org.String() + "/" + c.Principal.User.String()) {
		return tenant.Scope{}, ErrTooManyWrites
	}
	return scope, nil
}

func (s *Service) canEdit(c access.Caller, d Dashboard) bool {
	return d.Owner == c.Principal.User ||
		(d.Shared && s.policy.Allowed(c.Principal, authz.ManageDashboards, authz.InOrg(c.Principal.Org)))
}

func (s *Service) withRights(c access.Caller, d Dashboard) Dashboard {
	d.CanEdit = s.canEdit(c, d)
	return d
}

// List returns the caller's and the shared dashboards.
func (s *Service) List(ctx context.Context, c access.Caller) ([]Dashboard, error) {
	scope, err := s.authorize(c)
	if err != nil {
		return nil, err
	}
	list, err := s.store.List(ctx, scope, c.Principal.User)
	if err != nil {
		return nil, fmt.Errorf("list dashboards: %w", err)
	}
	for i := range list {
		list[i] = s.withRights(c, list[i])
	}
	return list, nil
}

// Get returns a dashboard the caller sees.
func (s *Service) Get(ctx context.Context, c access.Caller, id ID) (Dashboard, error) {
	scope, err := s.authorize(c)
	if err != nil {
		return Dashboard{}, err
	}
	d, err := s.store.Get(ctx, scope, c.Principal.User, id)
	if err != nil {
		return Dashboard{}, fmt.Errorf("get dashboard: %w", err)
	}
	return s.withRights(c, d), nil
}

// Create adds a private dashboard of the caller.
func (s *Service) Create(ctx context.Context, c access.Caller, spec dashboard.Spec) (Dashboard, error) {
	return s.create(ctx, c, spec, "dashboard.create")
}

// Import adds a private dashboard of the caller from an imported document.
func (s *Service) Import(ctx context.Context, c access.Caller, spec dashboard.Spec) (Dashboard, error) {
	return s.create(ctx, c, spec, "dashboard.import")
}

func (s *Service) create(ctx context.Context, c access.Caller, spec dashboard.Spec, action string) (Dashboard, error) {
	scope, err := s.authorizeWrite(c)
	if err != nil {
		return Dashboard{}, err
	}
	if err := s.check(ctx, c, &spec); err != nil {
		return Dashboard{}, err
	}
	at := s.now()
	d := Dashboard{ID: ID(s.newID()), Owner: c.Principal.User, Revision: 1, Spec: spec, CreatedAt: at, UpdatedAt: at}
	if err := s.store.Create(ctx, scope, d, audit(c, action, d)); err != nil {
		return Dashboard{}, fmt.Errorf("create dashboard: %w", err)
	}
	return s.withRights(c, d), nil
}

// Update replaces the document of a dashboard the caller may edit, based on revision.
func (s *Service) Update(ctx context.Context, c access.Caller, id ID, revision int64, spec dashboard.Spec) (Dashboard, error) {
	scope, err := s.authorizeWrite(c)
	if err != nil {
		return Dashboard{}, err
	}
	if err := s.check(ctx, c, &spec); err != nil {
		return Dashboard{}, err
	}
	d, err := s.store.Change(ctx, scope, c.Principal.User, id, func(d Dashboard) (Dashboard, access.AuditEntry, error) {
		if !s.canEdit(c, d) {
			return Dashboard{}, access.AuditEntry{}, access.ErrForbidden
		}
		if d.Revision != revision {
			return Dashboard{}, access.AuditEntry{}, ErrConflict
		}
		d.Spec, d.Revision, d.UpdatedAt = spec, d.Revision+1, s.now()
		return d, audit(c, "dashboard.update", d), nil
	})
	if err != nil {
		return Dashboard{}, fmt.Errorf("update dashboard: %w", err)
	}
	return s.withRights(c, d), nil
}

// SetShared shares a dashboard the caller may edit with the organization, or makes it private.
func (s *Service) SetShared(ctx context.Context, c access.Caller, id ID, shared bool) (Dashboard, error) {
	scope, err := s.authorizeWrite(c)
	if err != nil {
		return Dashboard{}, err
	}
	d, err := s.store.Change(ctx, scope, c.Principal.User, id, func(d Dashboard) (Dashboard, access.AuditEntry, error) {
		if !s.canEdit(c, d) {
			return Dashboard{}, access.AuditEntry{}, access.ErrForbidden
		}
		d.Shared, d.Revision, d.UpdatedAt = shared, d.Revision+1, s.now()
		e := audit(c, "dashboard.share", d)
		e.Details["shared"] = strconv.FormatBool(shared)
		return d, e, nil
	})
	if err != nil {
		return Dashboard{}, fmt.Errorf("share dashboard: %w", err)
	}
	return s.withRights(c, d), nil
}

// Delete removes a dashboard the caller may edit.
func (s *Service) Delete(ctx context.Context, c access.Caller, id ID) error {
	scope, err := s.authorizeWrite(c)
	if err != nil {
		return err
	}
	err = s.store.Delete(ctx, scope, c.Principal.User, id, func(d Dashboard) (access.AuditEntry, error) {
		if !s.canEdit(c, d) {
			return access.AuditEntry{}, access.ErrForbidden
		}
		return audit(c, "dashboard.delete", d), nil
	})
	if err != nil {
		return fmt.Errorf("delete dashboard: %w", err)
	}
	return nil
}

// check validates the document and that the caller views every node and group it names. A
// foreign and an unknown ID look the same.
func (s *Service) check(ctx context.Context, c access.Caller, spec *dashboard.Spec) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	scope, err := s.policy.ScopeForAction(c.Principal, authz.ViewNodes)
	if err != nil {
		return access.ErrForbidden
	}
	nodes, groups := spec.References()
	if len(nodes) == 0 && len(groups) == 0 {
		return nil
	}
	found, err := s.store.Visible(ctx, scope, nodes, groups)
	if err != nil {
		return fmt.Errorf("check references: %w", err)
	}
	for _, w := range spec.Widgets {
		n, g := w.References()
		for _, id := range n {
			if !found.Nodes[id] {
				return fmt.Errorf("%w: widget %q: unknown node", ErrUnknownReference, w.ID)
			}
		}
		for _, id := range g {
			if !found.Groups[id] {
				return fmt.Errorf("%w: widget %q: unknown group", ErrUnknownReference, w.ID)
			}
		}
	}
	for _, v := range spec.Variables {
		known := v.Default == uuid.Nil ||
			(v.Kind == dashboard.VariableNode && found.Nodes[identity.NodeID(v.Default)]) ||
			(v.Kind == dashboard.VariableGroup && found.Groups[tenant.GroupID(v.Default)])
		if !known {
			return fmt.Errorf("%w: variable %q: unknown default", ErrUnknownReference, v.Name)
		}
	}
	return nil
}

func audit(c access.Caller, action string, d Dashboard) access.AuditEntry {
	return access.AuditEntry{
		Actor: c.Principal.User, Action: action, Target: "dashboard:" + d.ID.String(),
		Details: map[string]string{"title": d.Spec.Title, "revision": strconv.FormatInt(d.Revision, 10)},
	}
}
