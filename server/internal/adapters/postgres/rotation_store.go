package postgres

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/agentca"
	"github.com/pmoscode/SerMon/server/internal/app/rotation"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/secevent"
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
			Serial: next.Serial, OrgID: uuid.UUID(next.Org), NodeID: uuid.UUID(next.Node), AuthorityID: next.Authority,
			NotBefore: next.NotBefore, NotAfter: next.NotAfter,
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
		locked := secevent.Event{Node: node, OccurredAt: now, Kind: secevent.NodeLocked, Details: secevent.Details{Reason: cutReason(reason)}}
		if err := insertServerEvent(ctx, q, locked); err != nil {
			return err
		}
		return appendAudit(ctx, q, audit)
	})
}

// NoteNewAddress records a rotation from another address than the previous session (T4): the
// audit note and the security event in one transaction.
func (s *RotationStore) NoteNewAddress(ctx context.Context, org tenant.OrgID, node identity.NodeID, ip, previous string, now time.Time, audit access.AuditEntry) error {
	e := secevent.Event{Node: node, OccurredAt: now, Kind: secevent.NodeRotationNewIP, Details: secevent.Details{PreviousIP: previous}}
	if addr, err := netip.ParseAddr(ip); err == nil {
		e.Remote = &secevent.Remote{IP: addr.Unmap()}
	}
	return s.inOrg(ctx, org, func(q *sqlcdb.Queries) error {
		if err := insertServerEvent(ctx, q, e); err != nil {
			return err
		}
		return appendAudit(ctx, q, audit)
	})
}

// cutReason keeps a lock reason within the column (and the event field) on a character boundary.
func cutReason(reason string) string {
	if len(reason) <= secevent.MaxReason {
		return reason
	}
	cut := secevent.MaxReason
	for cut > 0 && !utf8.RuneStart(reason[cut]) {
		cut--
	}
	return reason[:cut]
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

var _ rotation.NodeStore = (*RotationStore)(nil)

// NodesForRotation returns the target's nodes within the scope's groups.
func (s *RotationStore) NodesForRotation(ctx context.Context, scope tenant.Scope, t rotation.Target) ([]rotation.NodeRef, error) {
	ids := make([]uuid.UUID, 0, len(t.Nodes))
	for _, n := range t.Nodes {
		ids = append(ids, uuid.UUID(n))
	}
	var group *uuid.UUID
	if t.Group != nil {
		g := uuid.UUID(*t.Group)
		group = &g
	}
	var refs []rotation.NodeRef
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		rows, err := q.NodesForRotation(ctx, sqlcdb.NodesForRotationParams{
			AllGroups: scope.AllGroups(), GroupIds: groupIDs(scope), ByIds: len(t.Nodes) > 0, NodeIds: ids, GroupID: group,
		})
		for _, r := range rows {
			refs = append(refs, rotation.NodeRef{Node: identity.NodeID(r.ID), Group: tenant.GroupID(r.GroupID)})
		}
		return wrap(err, "list nodes")
	})
	return refs, err
}

// RevokeAll revokes all certificates of the node now.
func (s *RotationStore) RevokeAll(ctx context.Context, org tenant.OrgID, node identity.NodeID, now time.Time, audit access.AuditEntry) error {
	return s.inOrg(ctx, org, func(q *sqlcdb.Queries) error {
		if err := q.RevokeNodeCertificates(ctx, sqlcdb.RevokeNodeCertificatesParams{At: &now, NodeID: uuid.UUID(node)}); err != nil {
			return fmt.Errorf("revoke certificates: %w", err)
		}
		return appendAudit(ctx, q, audit)
	})
}

// Unlock lifts the node's lock and writes the audit entry; false (and no audit) if it was not
// locked.
func (s *RotationStore) Unlock(ctx context.Context, org tenant.OrgID, node identity.NodeID, audit access.AuditEntry) (bool, error) {
	var unlocked bool
	err := s.inOrg(ctx, org, func(q *sqlcdb.Queries) error {
		lockedAt, err := q.NodeLockedAt(ctx, uuid.UUID(node))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("load node: %w", err)
		}
		if lockedAt != nil {
			if err := q.RestoreLockRevokedCertificate(ctx, sqlcdb.RestoreLockRevokedCertificateParams{NodeID: uuid.UUID(node), LockedAt: lockedAt}); err != nil {
				return fmt.Errorf("restore the certificate: %w", err)
			}
		}
		n, err := q.UnlockNode(ctx, uuid.UUID(node))
		if err != nil {
			return fmt.Errorf("unlock node: %w", err)
		}
		if unlocked = n == 1; !unlocked {
			return nil
		}
		return appendAudit(ctx, q, audit)
	})
	return unlocked, err
}

// AllowTakeover opens the node's takeover window and writes the audit entry.
func (s *RotationStore) AllowTakeover(ctx context.Context, org tenant.OrgID, node identity.NodeID, until time.Time, audit access.AuditEntry) error {
	return s.inOrg(ctx, org, func(q *sqlcdb.Queries) error {
		n, err := q.AllowNodeTakeover(ctx, sqlcdb.AllowNodeTakeoverParams{ID: uuid.UUID(node), Until: &until})
		if err != nil {
			return fmt.Errorf("allow takeover: %w", err)
		}
		if n != 1 {
			// The node was found by the use case: it is scheduled for deletion (ADR 0041).
			return rotation.ErrScheduledForDeletion
		}
		return appendAudit(ctx, q, audit)
	})
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
