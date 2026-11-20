package instanceaudit_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/instanceaudit"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

type fakeStore struct{ limit int }

func (f *fakeStore) List(_ context.Context, _ int64, limit int) ([]access.AuditRecord, error) {
	f.limit = limit
	return []access.AuditRecord{{Seq: 1, Action: "pki.rollover_started"}}, nil
}

func TestList_OnlyForTheSuperadminWithBoundedPages(t *testing.T) {
	store := &fakeStore{}
	svc := instanceaudit.NewService(store, authz.NewPolicy())
	user := identity.UserID(uuid.New())
	super := access.Caller{Principal: authz.Principal{User: user, Superadmin: true}}
	admin := access.Caller{Principal: authz.Principal{
		User: user, Org: tenant.OrgID(uuid.New()), Bindings: []authz.Binding{{AllGroups: true, Role: role.Admin}},
	}}

	if records, err := svc.List(context.Background(), super, 0, 0); err != nil || len(records) != 1 || store.limit != instanceaudit.DefaultPageSize {
		t.Errorf("superadmin: %v, %v, limit %d", records, err, store.limit)
	}
	if _, _ = svc.List(context.Background(), super, 0, 10_000); store.limit != instanceaudit.MaxPageSize {
		t.Errorf("limit %d, want %d", store.limit, instanceaudit.MaxPageSize)
	}
	if _, err := svc.List(context.Background(), admin, 0, 0); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("org admin: %v, want ErrForbidden", err)
	}
}
