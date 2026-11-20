package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
)

// InstanceAuditStore reads the instance-wide audit log (migration 00014).
type InstanceAuditStore struct{ db *DB }

// NewInstanceAuditStore returns the store.
func NewInstanceAuditStore(db *DB) *InstanceAuditStore { return &InstanceAuditStore{db: db} }

// appendInstanceAudit writes an entry in the caller's transaction, so it exists exactly when the
// action does. The database computes the hash chain.
func appendInstanceAudit(ctx context.Context, q *sqlcdb.Queries, actor *identity.UserID, action, target string, details map[string]any) error {
	payload, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("encode audit details: %w", err)
	}
	var actorID *uuid.UUID
	if actor != nil {
		id := uuid.UUID(*actor)
		actorID = &id
	}
	return wrap(q.AppendInstanceAudit(ctx, sqlcdb.AppendInstanceAuditParams{ActorID: actorID, Action: action, Target: target, Details: payload}),
		"append instance audit entry")
}

func authorityTarget(id int32) string { return "ca:" + strconv.FormatInt(int64(id), 10) }

// List returns entries older than beforeSeq (0 = newest), newest first.
func (s *InstanceAuditStore) List(ctx context.Context, beforeSeq int64, limit int) ([]access.AuditRecord, error) {
	rows, err := s.db.withoutTenant().ListInstanceAudit(ctx, sqlcdb.ListInstanceAuditParams{BeforeSeq: beforeSeq, MaxRows: int32(min(limit, 1000))}) //nolint:gosec // G115: bounded
	if err != nil {
		return nil, fmt.Errorf("list instance audit: %w", err)
	}
	out := make([]access.AuditRecord, 0, len(rows))
	for _, r := range rows {
		rec := access.AuditRecord{Seq: r.Seq, At: r.At, Action: r.Action, Target: r.Target, Details: detailStrings(r.Details)}
		if r.ActorID != nil {
			actor := identity.UserID(*r.ActorID)
			rec.Actor = &actor
		}
		out = append(out, rec)
	}
	return out, nil
}

// Verify recomputes the hash chain of the instance audit log.
func (s *InstanceAuditStore) Verify(ctx context.Context) error {
	entries, err := s.db.withoutTenant().InstanceAuditChain(ctx)
	if err != nil {
		return fmt.Errorf("load instance audit log: %w", err)
	}
	prev := []byte{}
	for i, e := range entries {
		if e.Seq != int64(i)+1 || !bytes.Equal(e.PrevHash, prev) || !bytes.Equal(e.Hash, instanceAuditHash(e)) {
			return fmt.Errorf("%w at instance entry %d", ErrAuditChainBroken, e.ID)
		}
		prev = e.Hash
	}
	return nil
}

// instanceAuditHash mirrors the trigger instance_audit_log_chain (migration 00014).
func instanceAuditHash(e sqlcdb.InstanceAuditLog) []byte {
	actor := ""
	if e.ActorID != nil {
		actor = e.ActorID.String()
	}
	fields := strings.Join([]string{
		strconv.FormatInt(e.Seq, 10), e.At.UTC().Format("2006-01-02T15:04:05.000000Z"), actor, e.Action, e.Target, string(e.Details),
	}, "\x1f")
	sum := sha256.Sum256(append(bytes.Clone(e.PrevHash), fields...))
	return sum[:]
}
