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
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
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

// TrustedAuthorities loads all CAs that are not retired, oldest first (instance-level).
func (s *AgentCAStore) TrustedAuthorities(ctx context.Context) ([]agentca.StoredAuthority, error) {
	rows, err := s.db.withoutTenant().TrustedAuthorities(ctx)
	if err != nil {
		return nil, fmt.Errorf("load authorities: %w", err)
	}
	out := make([]agentca.StoredAuthority, 0, len(rows))
	for _, r := range rows {
		out = append(out, agentca.StoredAuthority{ID: r.ID, Issuing: r.Active, CreatedAt: r.CreatedAt, Record: agentca.AuthorityRecord{
			RootCert: r.RootCert, RootKeySealed: r.RootKeySealed, IntermediateCert: r.IntermediateCert, IntermediateKeySealed: r.IntermediateKeySealed,
		}})
	}
	return out, nil
}

// InsertAuthority stores the first CA; agentca.ErrAuthorityExists if one is already active.
func (s *AgentCAStore) InsertAuthority(ctx context.Context, r agentca.AuthorityRecord) error {
	err := s.db.instanceTx(ctx, func(q *sqlcdb.Queries) error {
		id, err := q.InsertAuthority(ctx, sqlcdb.InsertAuthorityParams{
			RootCert: r.RootCert, RootKeySealed: r.RootKeySealed,
			IntermediateCert: r.IntermediateCert, IntermediateKeySealed: r.IntermediateKeySealed,
		})
		if err != nil {
			return err //nolint:wrapcheck // classified below
		}
		return appendInstanceAudit(ctx, q, nil, "pki.authority_created", authorityTarget(id),
			map[string]any{"root_fingerprint": agentca.Fingerprint(r.RootCert)})
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

// StartRollover stores a new issuing CA; the previous one stays trusted.
func (s *AgentCAStore) StartRollover(ctx context.Context, r agentca.AuthorityRecord, by identity.UserID) error {
	return s.db.instanceTx(ctx, func(q *sqlcdb.Queries) error {
		if err := q.LockPKI(ctx); err != nil {
			return fmt.Errorf("lock pki: %w", err)
		}
		trusted, err := q.CountTrustedAuthorities(ctx)
		if err != nil {
			return fmt.Errorf("count authorities: %w", err)
		}
		if trusted > 1 {
			return agentca.ErrRolloverInProgress
		}
		if err := q.DeactivateIssuingAuthority(ctx); err != nil {
			return fmt.Errorf("deactivate issuing authority: %w", err)
		}
		starter := uuid.UUID(by)
		id, err := q.InsertIssuingAuthority(ctx, sqlcdb.InsertIssuingAuthorityParams{
			RootCert: r.RootCert, RootKeySealed: r.RootKeySealed, IntermediateCert: r.IntermediateCert,
			IntermediateKeySealed: r.IntermediateKeySealed, StartedBy: &starter,
		})
		if err != nil {
			return fmt.Errorf("insert issuing authority: %w", err)
		}
		return appendInstanceAudit(ctx, q, &by, "pki.rollover_started", authorityTarget(id),
			map[string]any{"root_fingerprint": agentca.Fingerprint(r.RootCert)})
	})
}

// Retire stops trusting a CA without valid certificates.
func (s *AgentCAStore) Retire(ctx context.Context, id int32, by identity.UserID, now time.Time) error {
	return s.db.instanceTx(ctx, func(q *sqlcdb.Queries) error {
		if err := q.LockPKI(ctx); err != nil {
			return fmt.Errorf("lock pki: %w", err)
		}
		row, err := q.AuthorityForRetirement(ctx, sqlcdb.AuthorityForRetirementParams{ID: id, At: now})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return agentca.ErrUnknownAuthority
		case err != nil:
			return fmt.Errorf("load authority: %w", err)
		case row.Active:
			return agentca.ErrIssuingAuthority
		case row.InUse > 0:
			return agentca.ErrAuthorityInUse
		}
		retirer := uuid.UUID(by)
		if err := q.RetireAuthority(ctx, sqlcdb.RetireAuthorityParams{ID: id, At: &now, RetiredBy: &retirer}); err != nil {
			return fmt.Errorf("retire authority: %w", err)
		}
		return appendInstanceAudit(ctx, q, &by, "pki.authority_retired", authorityTarget(id),
			map[string]any{"root_fingerprint": agentca.Fingerprint(row.RootCert)})
	})
}

// Authorities returns all CAs with their certificates in use.
func (s *AgentCAStore) Authorities(ctx context.Context, now time.Time) ([]agentca.AuthorityState, error) {
	rows, err := s.db.withoutTenant().AllAuthorities(ctx, now)
	if err != nil {
		return nil, fmt.Errorf("load authorities: %w", err)
	}
	out := make([]agentca.AuthorityState, 0, len(rows))
	for _, r := range rows {
		out = append(out, agentca.AuthorityState{
			ID: r.ID, Issuing: r.Active, CreatedAt: r.CreatedAt, RetiredAt: r.RetiredAt,
			RootFingerprint: agentca.Fingerprint(r.RootCert), CertificatesInUse: r.InUse,
		})
	}
	return out, nil
}

// RecordCertificate stores an issued certificate in the tenant.
func (s *AgentCAStore) RecordCertificate(ctx context.Context, scope tenant.Scope, c agentca.CertificateRecord) error {
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		return q.InsertAgentCertificate(ctx, sqlcdb.InsertAgentCertificateParams{
			Serial: c.Serial, OrgID: uuid.UUID(c.Org), NodeID: uuid.UUID(c.Node), AuthorityID: c.Authority,
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

// Usable runs the tenant-less revocation check (SECURITY DEFINER function, yes/no only) for the
// serial with the identity of the certificate.
func (s *AgentCAStore) Usable(ctx context.Context, serial string, org tenant.OrgID, node identity.NodeID, at time.Time) (bool, error) {
	usable, err := s.db.withoutTenant().AgentCertificateUsable(ctx, sqlcdb.AgentCertificateUsableParams{
		Serial: serial, OrgID: uuid.UUID(org), NodeID: uuid.UUID(node), At: at,
	})
	if err != nil {
		return false, fmt.Errorf("check certificate: %w", err)
	}
	return usable, nil
}
