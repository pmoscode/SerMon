package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/agentca"
	"github.com/pmoscode/SerMon/server/internal/app/rotation"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// RotationStore persists certificate rotations and node locks (M1-11).
type RotationStore struct{ db *DB }

var _ rotation.Store = (*RotationStore)(nil)

// NewRotationStore returns the PostgreSQL rotation store.
func NewRotationStore(db *DB) *RotationStore { return &RotationStore{db: db} }

// Rotate replaces the session's certificate (valid until revokeOldAt) with next.
func (s *RotationStore) Rotate(ctx context.Context, id rotation.Identity, next agentca.CertificateRecord, revokeOldAt time.Time, audit access.AuditEntry) error {
	return s.inOrg(ctx, id.Org, func(q *sqlcdb.Queries) error {
		n, err := q.SupersedeCertificate(ctx, sqlcdb.SupersedeCertificateParams{RevokeAt: &revokeOldAt, Serial: id.Serial, NodeID: uuid.UUID(id.Node)})
		if err != nil {
			return fmt.Errorf("replace certificate: %w", err)
		}
		if n == 0 {
			return rotation.ErrSuperseded
		}
		if err := q.InsertAgentCertificate(ctx, sqlcdb.InsertAgentCertificateParams{
			Serial: next.Serial, OrgID: uuid.UUID(next.Org), NodeID: uuid.UUID(next.Node), NotBefore: next.NotBefore, NotAfter: next.NotAfter,
		}); err != nil {
			return fmt.Errorf("record certificate: %w", err)
		}
		return appendAudit(ctx, q, audit)
	})
}

// Lock locks the node and revokes all its certificates now.
func (s *RotationStore) Lock(ctx context.Context, org tenant.OrgID, node identity.NodeID, reason string, now time.Time, audit access.AuditEntry) error {
	return s.inOrg(ctx, org, func(q *sqlcdb.Queries) error {
		if _, err := q.LockNode(ctx, sqlcdb.LockNodeParams{At: &now, Reason: pgtype.Text{String: reason, Valid: true}, ID: uuid.UUID(node)}); err != nil {
			return fmt.Errorf("lock node: %w", err)
		}
		if err := q.RevokeNodeCertificates(ctx, sqlcdb.RevokeNodeCertificatesParams{At: &now, NodeID: uuid.UUID(node)}); err != nil {
			return fmt.Errorf("revoke certificates: %w", err)
		}
		return appendAudit(ctx, q, audit)
	})
}

// RecordSession stores the session's source IP and returns the previous one.
func (s *RotationStore) RecordSession(ctx context.Context, org tenant.OrgID, node identity.NodeID, ip string) (string, error) {
	var previous string
	err := s.inOrg(ctx, org, func(q *sqlcdb.Queries) error {
		var err error
		previous, err = q.SwapNodeLastIP(ctx, sqlcdb.SwapNodeLastIPParams{Ip: pgtype.Text{String: ip, Valid: true}, NodeID: uuid.UUID(node)})
		return wrap(err, "record session ip")
	})
	return previous, err
}

// Note writes a security note to the organization's audit log.
func (s *RotationStore) Note(ctx context.Context, org tenant.OrgID, audit access.AuditEntry) error {
	return s.inOrg(ctx, org, func(q *sqlcdb.Queries) error { return appendAudit(ctx, q, audit) })
}

func (s *RotationStore) inOrg(ctx context.Context, org tenant.OrgID, fn func(*sqlcdb.Queries) error) error {
	scope, err := tenant.ForAllGroups(org)
	if err != nil {
		return fmt.Errorf("organization: %w", err)
	}
	return s.db.InTenant(ctx, scope, fn)
}
