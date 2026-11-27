package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// ErrAuditChainBroken means the audit log was modified outside the application.
var ErrAuditChainBroken = errors.New("audit log hash chain is broken")

// AccessStore loads role bindings and writes/verifies the audit log.
type AccessStore struct{ db *DB }

// NewAccessStore returns the PostgreSQL access store.
func NewAccessStore(db *DB) *AccessStore { return &AccessStore{db: db} }

// MembershipAndBindings reports whether the user is a member of the organization and returns the
// user's role bindings there (read inside the organization's tenant transaction).
func (s *AccessStore) MembershipAndBindings(ctx context.Context, org tenant.OrgID, user identity.UserID) (access.Membership, error) {
	scope, err := tenant.ForAllGroups(org)
	if err != nil {
		return access.Membership{}, err
	}
	var m access.Membership
	err = s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		var err error
		if m.Member, err = q.IsMember(ctx, uuid.UUID(user)); err != nil || !m.Member {
			return wrap(err, "check membership")
		}
		see, err := q.ViewersSeeLogs(ctx)
		if err != nil {
			return fmt.Errorf("viewer switch: %w", err)
		}
		m.ViewersWithoutLogs = !see
		rows, err := q.RoleBindingsOfUser(ctx, uuid.UUID(user))
		if err != nil {
			return fmt.Errorf("load role bindings: %w", err)
		}
		for _, r := range rows {
			parsed, err := role.Parse(r.Role)
			if err != nil {
				return fmt.Errorf("role binding %s: %w", r.ID, err)
			}
			b := authz.Binding{Role: parsed, AllGroups: r.GroupID == nil}
			if r.GroupID != nil {
				b.Group = tenant.GroupID(*r.GroupID)
			}
			m.Bindings = append(m.Bindings, b)
		}
		return nil
	})
	return m, err
}

// AppendAudit appends an entry to the organization's audit log (hash chain computed by the database).
func (s *AccessStore) AppendAudit(ctx context.Context, scope tenant.Scope, actor *identity.UserID, action, target string, details map[string]any) error {
	payload, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("encode audit details: %w", err)
	}
	var actorID *uuid.UUID
	if actor != nil {
		id := uuid.UUID(*actor)
		actorID = &id
	}
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		_, err := q.AppendAudit(ctx, sqlcdb.AppendAuditParams{ActorID: actorID, Action: action, Target: target, Details: payload})
		return wrap(err, "append audit entry")
	})
}

// VerifyAuditChain recomputes the hash chain of the organization's audit log.
func (s *AccessStore) VerifyAuditChain(ctx context.Context, scope tenant.Scope) error {
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		entries, err := q.AuditChain(ctx)
		if err != nil {
			return fmt.Errorf("load audit log: %w", err)
		}
		prev, ok := chainStart(orgLinks(entries))
		if !ok {
			return fmt.Errorf("%w at its start", ErrAuditChainBroken)
		}
		for i, e := range entries {
			if e.Seq != entries[0].Seq+int64(i) || !bytes.Equal(e.PrevHash, prev) || !bytes.Equal(e.Hash, auditHash(e)) {
				return fmt.Errorf("%w at entry %d", ErrAuditChainBroken, e.ID)
			}
			prev = e.Hash
		}
		return nil
	})
}

func orgLinks(entries []sqlcdb.AuditLog) []chainLink {
	links := make([]chainLink, len(entries))
	for i, e := range entries {
		links[i] = chainLink{seq: e.Seq, prev: e.PrevHash, action: e.Action, details: e.Details}
	}
	return links
}

// auditHash mirrors the trigger audit_log_chain (migration 00005).
func auditHash(e sqlcdb.AuditLog) []byte {
	actor := ""
	if e.ActorID != nil {
		actor = e.ActorID.String()
	}
	fields := strings.Join([]string{
		e.OrgID.String(), strconv.FormatInt(e.Seq, 10), e.At.UTC().Format("2006-01-02T15:04:05.000000Z"), actor, e.Action, e.Target, string(e.Details),
	}, "\x1f")
	sum := sha256.Sum256(append(bytes.Clone(e.PrevHash), fields...))
	return sum[:]
}

func wrap(err error, op string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", op, err)
}
