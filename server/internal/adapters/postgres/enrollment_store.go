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
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/agentca"
	"github.com/pmoscode/SerMon/server/internal/app/enrollment"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// EnrollmentTokenStore persists enrollment tokens (M1-08).
type EnrollmentTokenStore struct{ db *DB }

var _ enrollment.Store = (*EnrollmentTokenStore)(nil)

// NewEnrollmentTokenStore returns the PostgreSQL enrollment token store.
func NewEnrollmentTokenStore(db *DB) *EnrollmentTokenStore { return &EnrollmentTokenStore{db: db} }

// Create stores a token; a group of another organization is reported as access.ErrNotFound.
func (s *EnrollmentTokenStore) Create(ctx context.Context, scope tenant.Scope, t enrollment.Token, hash [32]byte, audit access.AuditEntry) error {
	var creator *uuid.UUID
	if !t.CreatedBy.IsZero() {
		id := uuid.UUID(t.CreatedBy)
		creator = &id
	}
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		err := q.InsertEnrollmentToken(ctx, sqlcdb.InsertEnrollmentTokenParams{
			ID: uuid.UUID(t.ID), GroupID: uuid.UUID(t.Group), TokenHash: hash[:], CreatedBy: creator,
			CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt, MaxUses: int32(t.MaxUses), //nolint:gosec // G115: 1..1000, checked by the service
		})
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return access.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("insert enrollment token: %w", err)
		}
		return appendAudit(ctx, q, audit)
	})
}

// List returns the open tokens of the scope's groups, newest first.
func (s *EnrollmentTokenStore) List(ctx context.Context, scope tenant.Scope) ([]enrollment.Token, error) {
	var tokens []enrollment.Token
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		rows, err := q.ListEnrollmentTokens(ctx, sqlcdb.ListEnrollmentTokensParams{
			Now: time.Now(), AllGroups: scope.AllGroups(), GroupIds: groupIDs(scope),
		})
		if err != nil {
			return fmt.Errorf("list enrollment tokens: %w", err)
		}
		for _, r := range rows {
			t := enrollment.Token{
				ID: enrollment.TokenID(r.ID), Group: tenant.GroupID(r.GroupID), CreatedAt: r.CreatedAt,
				ExpiresAt: r.ExpiresAt, MaxUses: int(r.MaxUses), Uses: int(r.Uses),
			}
			if r.CreatedBy != nil {
				t.CreatedBy = identity.UserID(*r.CreatedBy)
			}
			tokens = append(tokens, t)
		}
		return nil
	})
	return tokens, err
}

// Revoke marks a token of the scope's groups as revoked.
func (s *EnrollmentTokenStore) Revoke(ctx context.Context, scope tenant.Scope, id enrollment.TokenID, audit access.AuditEntry) error {
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		n, err := q.RevokeEnrollmentToken(ctx, sqlcdb.RevokeEnrollmentTokenParams{
			ID: uuid.UUID(id), AllGroups: scope.AllGroups(), GroupIds: groupIDs(scope),
		})
		if err != nil {
			return fmt.Errorf("revoke enrollment token: %w", err)
		}
		if n == 0 {
			return access.ErrNotFound
		}
		return appendAudit(ctx, q, audit)
	})
}

// Lookup finds a usable token by hash (SECURITY DEFINER, no tenant).
func (s *EnrollmentTokenStore) Lookup(ctx context.Context, hash [32]byte, now time.Time) (enrollment.Stored, bool, error) {
	r, err := s.db.withoutTenant().EnrollmentTokenByHash(ctx, sqlcdb.EnrollmentTokenByHashParams{TokenHash: hash[:], At: now})
	if errors.Is(err, pgx.ErrNoRows) {
		return enrollment.Stored{}, false, nil
	}
	if err != nil {
		return enrollment.Stored{}, false, fmt.Errorf("look up enrollment token: %w", err)
	}
	stored := enrollment.Stored{ID: enrollment.TokenID(r.ID), Org: tenant.OrgID(r.OrgID), Group: tenant.GroupID(r.GroupID)}
	copy(stored.Hash[:], r.TokenHash)
	return stored, true, nil
}

// NodeRegistry runs the enrollment transaction (enrollment.Registry).
type NodeRegistry struct{ db *DB }

var _ enrollment.Registry = (*NodeRegistry)(nil)

// NewNodeRegistry returns the PostgreSQL node registry.
func NewNodeRegistry(db *DB) *NodeRegistry { return &NodeRegistry{db: db} }

// Register uses the token once, creates the node, records its certificate and writes the audit
// entries – all in one transaction of the token's organization. A machine-id conflict rolls
// everything back, so the token stays usable.
func (r *NodeRegistry) Register(ctx context.Context, st enrollment.Stored, n enrollment.Node, cert agentca.CertificateRecord, now time.Time, audits []access.AuditEntry) error {
	scope, err := tenant.ForGroups(st.Org, st.Group)
	if err != nil {
		return enrollment.ErrInvalidToken
	}
	f := n.Facts
	return r.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		used, err := q.ConsumeEnrollmentToken(ctx, sqlcdb.ConsumeEnrollmentTokenParams{ID: uuid.UUID(st.ID), Now: now})
		if err != nil {
			return fmt.Errorf("use enrollment token: %w", err)
		}
		if used == 0 {
			return enrollment.ErrInvalidToken // revoked, expired or used up in the meantime
		}
		token := uuid.UUID(n.Token)
		err = q.InsertNode(ctx, sqlcdb.InsertNodeParams{
			ID: uuid.UUID(n.ID), GroupID: uuid.UUID(n.Group), Hostname: f.Hostname, MachineID: f.MachineID,
			OsID: f.OSID, OsVersion: f.OSVersion, KernelRelease: f.KernelRelease, Architecture: f.Architecture,
			CpuCount: int32(f.CPUCount), MemoryBytes: f.MemoryBytes, EnrolledWith: &token, //nolint:gosec // G115: 1..4096 (protovalidate)
		})
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "nodes_org_id_machine_id_key" {
			return enrollment.ErrMachineIDConflict
		}
		if err != nil {
			return fmt.Errorf("insert node: %w", err)
		}
		if err := q.InsertAgentCertificate(ctx, sqlcdb.InsertAgentCertificateParams{
			Serial: cert.Serial, OrgID: uuid.UUID(cert.Org), NodeID: uuid.UUID(cert.Node), NotBefore: cert.NotBefore, NotAfter: cert.NotAfter,
		}); err != nil {
			return fmt.Errorf("record certificate: %w", err)
		}
		for _, a := range audits {
			if err := appendAudit(ctx, q, a); err != nil {
				return err
			}
		}
		return nil
	})
}

func groupIDs(scope tenant.Scope) []uuid.UUID {
	ids := []uuid.UUID{}
	for _, g := range scope.Groups() {
		ids = append(ids, uuid.UUID(g))
	}
	return ids
}
