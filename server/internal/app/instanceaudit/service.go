// Package instanceaudit reads the instance-wide audit log (M1-11b): actions outside any
// organization, such as the agent CA rollover. Only the superadmin may read it.
package instanceaudit

import (
	"context"
	"fmt"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
)

// Page sizes.
const (
	DefaultPageSize = 100
	MaxPageSize     = 500
)

// Store reads the log.
type Store interface {
	List(ctx context.Context, beforeSeq int64, limit int) ([]access.AuditRecord, error)
}

// Service authorizes reads of the log.
type Service struct {
	store  Store
	policy authz.Policy
}

// NewService returns the service.
func NewService(store Store, policy authz.Policy) *Service {
	return &Service{store: store, policy: policy}
}

// List returns entries older than beforeSeq (0 = newest), newest first.
func (s *Service) List(ctx context.Context, c access.Caller, beforeSeq int64, pageSize int) ([]access.AuditRecord, error) {
	if !s.policy.AllowedInstance(c.Principal, authz.ViewInstanceAudit) {
		return nil, access.ErrForbidden
	}
	if pageSize <= 0 {
		pageSize = DefaultPageSize
	}
	records, err := s.store.List(ctx, beforeSeq, min(pageSize, MaxPageSize))
	if err != nil {
		return nil, fmt.Errorf("instance audit: %w", err)
	}
	return records, nil
}
