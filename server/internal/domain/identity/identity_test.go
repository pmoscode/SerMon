package identity_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
)

func TestIDs(t *testing.T) {
	u := uuid.MustParse("0193f5a0-0000-7000-8000-000000000001")
	if got := identity.UserID(u).String(); got != u.String() {
		t.Errorf("UserID.String() = %q", got)
	}
	if got := identity.NodeID(u).String(); got != u.String() {
		t.Errorf("NodeID.String() = %q", got)
	}
	if identity.UserID(u).IsZero() || identity.NodeID(u).IsZero() {
		t.Error("set IDs must not be zero")
	}
	if !(identity.UserID{}).IsZero() || !(identity.NodeID{}).IsZero() {
		t.Error("zero IDs must report IsZero")
	}
}
