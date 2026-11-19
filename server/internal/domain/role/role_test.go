package role_test

import (
	"errors"
	"testing"

	"github.com/pmoscode/SerMon/server/internal/domain/role"
)

func TestParse_KnownRoles(t *testing.T) {
	for input, want := range map[string]role.Role{"viewer": role.Viewer, "operator": role.Operator, "admin": role.Admin} {
		got, err := role.Parse(input)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %v, %v; want %v", input, got, err, want)
		}
	}
}

func TestParse_RejectsUnknownRoles(t *testing.T) {
	for _, input := range []string{"", "Admin", "root", "superadmin", " viewer"} {
		if _, err := role.Parse(input); !errors.Is(err, role.ErrUnknown) {
			t.Errorf("Parse(%q) error = %v, want ErrUnknown", input, err)
		}
	}
}

func TestRole_OrderingAndValidity(t *testing.T) {
	if !role.Admin.AtLeast(role.Operator) || !role.Operator.AtLeast(role.Viewer) || !role.Viewer.AtLeast(role.Viewer) {
		t.Error("expected Admin ≥ Operator ≥ Viewer")
	}
	if role.Viewer.AtLeast(role.Operator) {
		t.Error("Viewer must not satisfy Operator")
	}
	var zero role.Role
	if zero.Valid() || zero.AtLeast(role.Viewer) {
		t.Error("zero Role is invalid and grants nothing")
	}
	if role.Role(99).Valid() || role.Role(99).AtLeast(role.Viewer) {
		t.Error("out-of-range Role is invalid and grants nothing")
	}
}

func TestRole_String(t *testing.T) {
	cases := map[role.Role]string{role.Viewer: "viewer", role.Operator: "operator", role.Admin: "admin", role.Role(0): "invalid"}
	for r, want := range cases {
		if got := r.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", r, got, want)
		}
	}
}
