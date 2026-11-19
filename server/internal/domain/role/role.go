// Package role defines the roles of the authorization model (KONZEPT §7).
package role

import (
	"errors"
	"fmt"
)

// ErrUnknown is returned for role names that do not exist.
var ErrUnknown = errors.New("unknown role")

// Role is a role within a node group or all groups of an organization. The zero value is
// invalid and grants nothing.
type Role uint8

// Roles in ascending order of privilege.
const (
	Viewer Role = iota + 1
	Operator
	Admin
)

var names = map[Role]string{Viewer: "viewer", Operator: "operator", Admin: "admin"}

// Parse converts a role name ("viewer", "operator", "admin") into a Role.
func Parse(name string) (Role, error) {
	for r, n := range names {
		if n == name {
			return r, nil
		}
	}
	return 0, fmt.Errorf("%w: %q", ErrUnknown, name)
}

// Valid reports whether r is a known role.
func (r Role) Valid() bool { return r >= Viewer && r <= Admin }

// AtLeast reports whether r is a valid role with at least the privileges of minimum.
func (r Role) AtLeast(minimum Role) bool { return r.Valid() && minimum.Valid() && r >= minimum }

// String returns the role name.
func (r Role) String() string {
	if n, ok := names[r]; ok {
		return n
	}
	return "invalid"
}
