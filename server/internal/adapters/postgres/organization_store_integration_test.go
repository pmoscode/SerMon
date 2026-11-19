//go:build integration

package postgres_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/invitation"
	"github.com/pmoscode/SerMon/server/internal/app/organization"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

func TestOrganizationStore_CreateListRename(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	db := open(t, f.appDSN)
	s := postgres.NewOrganizationStore(db)
	existing := newMgmtOrg(t, f) // two members
	actor := identity.UserID(uuid.New())
	now := time.Now()
	org := organization.Organization{ID: tenant.OrgID(uuid.New()), Name: "Created " + uuid.NewString()[:6], CreatedAt: now}
	inv := invitation.Invitation{ID: invitation.ID(uuid.New()), Email: uniqueEmail(), Role: role.Admin, CreatedAt: now, ExpiresAt: now.Add(invitation.Lifetime)}
	hash := sha256.Sum256([]byte(uuid.NewString()))

	if err := s.Create(ctx, org, inv, hash, access.AuditEntry{Actor: actor, Action: "org.create", Target: "org:" + org.ID.String()}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	pending, found, err := postgres.NewInvitationStore(db).Lookup(ctx, hash, now)
	if err != nil || !found || pending.Org != org.ID || pending.Role != role.Admin || pending.Group != nil {
		t.Errorf("first admin invitation = %+v, %v, %v", pending, found, err)
	}

	orgs, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	members := map[tenant.OrgID]int64{}
	for _, o := range orgs {
		members[o.ID] = o.Members
	}
	if m, ok := members[org.ID]; !ok || m != 0 {
		t.Errorf("new organization listed with %d members (%v), want 0", m, ok)
	}
	if members[existing.scope.OrgID()] != 2 {
		t.Errorf("existing organization has %d members, want 2", members[existing.scope.OrgID()])
	}

	if err := s.Rename(ctx, org.ID, "Renamed", access.AuditEntry{Actor: actor, Action: "org.rename", Target: "org:x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Rename(ctx, tenant.OrgID(uuid.New()), "Ghost", access.AuditEntry{Actor: actor, Action: "org.rename", Target: "org:x"}); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("rename of an unknown organization: %v, want ErrNotFound", err)
	}
	if got := auditActions(t, postgres.NewAccessStore(db), scopeFor(t, uuid.UUID(org.ID))); !equalStrings(got, []string{"org.create", "org.rename"}) {
		t.Errorf("audit = %v", got)
	}
}
