package postgres

import (
	"context"
	"fmt"
)

// SelfMonitorStore reads what the self-monitoring needs (M2-24): the superadmins' addresses (a
// definer function, nothing else of users) and the size of the database.
type SelfMonitorStore struct{ db *DB }

// NewSelfMonitorStore returns the store.
func NewSelfMonitorStore(db *DB) *SelfMonitorStore { return &SelfMonitorStore{db: db} }

// Superadmins returns the addresses of the superadmins.
func (s *SelfMonitorStore) Superadmins(ctx context.Context) ([]string, error) {
	emails, err := s.db.withoutTenant().SuperadminEmails(ctx)
	if err != nil {
		return nil, fmt.Errorf("superadmin addresses: %w", err)
	}
	return emails, nil
}

// DatabaseSize returns the size of the database in bytes.
func (s *SelfMonitorStore) DatabaseSize(ctx context.Context) (int64, error) {
	size, err := s.db.withoutTenant().DatabaseSize(ctx)
	if err != nil {
		return 0, fmt.Errorf("database size: %w", err)
	}
	return size, nil
}
