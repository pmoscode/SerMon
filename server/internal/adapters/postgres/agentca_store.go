package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/agentca"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

const uniqueViolation = "23505"

// AgentCAStore implements agentca.Store.
type AgentCAStore struct {
	db *DB
}

var _ agentca.Store = (*AgentCAStore)(nil)

// NewAgentCAStore returns the PostgreSQL store of the agent CA.
func NewAgentCAStore(db *DB) *AgentCAStore { return &AgentCAStore{db: db} }

// ActiveAuthority loads the active CA (instance-level, no tenant).
func (s *AgentCAStore) ActiveAuthority(ctx context.Context) (agentca.AuthorityRecord, bool, error) {
	row, err := s.db.withoutTenant().ActiveAuthority(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return agentca.AuthorityRecord{}, false, nil
	}
	if err != nil {
		return agentca.AuthorityRecord{}, false, fmt.Errorf("load active authority: %w", err)
	}
	return agentca.AuthorityRecord{
		RootCert: row.RootCert, RootKeySealed: row.RootKeySealed,
		IntermediateCert: row.IntermediateCert, IntermediateKeySealed: row.IntermediateKeySealed,
	}, true, nil
}

// InsertAuthority stores a new active CA; agentca.ErrAuthorityExists if one is already active.
func (s *AgentCAStore) InsertAuthority(ctx context.Context, r agentca.AuthorityRecord) error {
	_, err := s.db.withoutTenant().InsertAuthority(ctx, sqlcdb.InsertAuthorityParams{
		RootCert: r.RootCert, RootKeySealed: r.RootKeySealed,
		IntermediateCert: r.IntermediateCert, IntermediateKeySealed: r.IntermediateKeySealed,
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return agentca.ErrAuthorityExists
	}
	if err != nil {
		return fmt.Errorf("insert authority: %w", err)
	}
	return nil
}

// RecordCertificate stores an issued certificate in the tenant.
func (s *AgentCAStore) RecordCertificate(ctx context.Context, scope tenant.Scope, c agentca.CertificateRecord) error {
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		return q.InsertAgentCertificate(ctx, sqlcdb.InsertAgentCertificateParams{
			Serial: c.Serial, OrgID: uuid.UUID(c.Org), NodeID: uuid.UUID(c.Node),
			NotBefore: c.NotBefore, NotAfter: c.NotAfter,
		})
	})
}

// Revoke marks the certificate as revoked; false if it is unknown in the tenant or already revoked.
func (s *AgentCAStore) Revoke(ctx context.Context, scope tenant.Scope, serial string, at time.Time) (bool, error) {
	var rows int64
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		var err error
		rows, err = q.RevokeAgentCertificate(ctx, sqlcdb.RevokeAgentCertificateParams{Serial: serial, RevokedAt: &at})
		if err != nil {
			return fmt.Errorf("revoke certificate: %w", err)
		}
		return nil
	})
	return rows > 0, err
}

// Usable runs the tenant-less revocation check (SECURITY DEFINER function, yes/no only).
func (s *AgentCAStore) Usable(ctx context.Context, serial string, at time.Time) (bool, error) {
	usable, err := s.db.withoutTenant().AgentCertificateUsable(ctx, sqlcdb.AgentCertificateUsableParams{Serial: serial, At: at})
	if err != nil {
		return false, fmt.Errorf("check certificate: %w", err)
	}
	return usable, nil
}
