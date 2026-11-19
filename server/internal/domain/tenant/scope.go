// Package tenant defines the tenant scope every data access is bound to (KONZEPT §7, §18).
//
// A Scope names exactly one organization and the node groups the caller may access.
// Repositories require a valid Scope and enforce it in their queries; PostgreSQL row-level
// security enforces the organization a second time (ADR 0007).
package tenant

import (
	"errors"
	"slices"

	"github.com/google/uuid"
)

// ErrNoOrganization is returned when a Scope is created without an organization.
var ErrNoOrganization = errors.New("tenant scope requires an organization")

// OrgID identifies an organization (tenant).
type OrgID uuid.UUID

// String returns the canonical UUID representation.
func (id OrgID) String() string { return uuid.UUID(id).String() }

// GroupID identifies a node group within an organization.
type GroupID uuid.UUID

// String returns the canonical UUID representation.
func (id GroupID) String() string { return uuid.UUID(id).String() }

// Scope is the immutable access scope of one request. The zero value is invalid and grants nothing.
type Scope struct {
	org       OrgID
	groups    []GroupID
	allGroups bool
}

// ForAllGroups returns a scope for every node group of the organization (role binding "*").
func ForAllGroups(org OrgID) (Scope, error) {
	if uuid.UUID(org) == uuid.Nil {
		return Scope{}, ErrNoOrganization
	}
	return Scope{org: org, allGroups: true}, nil
}

// ForGroups returns a scope restricted to the given node groups of the organization.
// Without groups the scope is valid for organization-wide data but grants no node group.
func ForGroups(org OrgID, groups ...GroupID) (Scope, error) {
	if uuid.UUID(org) == uuid.Nil {
		return Scope{}, ErrNoOrganization
	}
	return Scope{org: org, groups: slices.Clone(groups)}, nil
}

// Valid reports whether the scope names an organization.
func (s Scope) Valid() bool { return uuid.UUID(s.org) != uuid.Nil }

// OrgID returns the organization of the scope.
func (s Scope) OrgID() OrgID { return s.org }

// AllGroups reports whether the scope covers every node group of the organization.
func (s Scope) AllGroups() bool { return s.Valid() && s.allGroups }

// Groups returns the explicitly granted node groups (empty for ForAllGroups; check AllGroups first).
func (s Scope) Groups() []GroupID { return slices.Clone(s.groups) }

// AllowsGroup reports whether the scope grants access to the node group.
func (s Scope) AllowsGroup(group GroupID) bool {
	if !s.Valid() {
		return false
	}
	return s.allGroups || slices.Contains(s.groups, group)
}
