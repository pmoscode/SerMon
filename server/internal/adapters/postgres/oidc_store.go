package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/auth/oidc"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
)

// OIDCStore keeps the OIDC flows and identities (M3-20, migration 00043).
type OIDCStore struct{ db *DB }

var (
	_ oidc.FlowStore     = (*OIDCStore)(nil)
	_ oidc.IdentityStore = (*OIDCStore)(nil)
)

// NewOIDCStore returns the store.
func NewOIDCStore(db *DB) *OIDCStore { return &OIDCStore{db: db} }

// Insert implements oidc.FlowStore.
func (s *OIDCStore) Insert(ctx context.Context, f oidc.Flow) error {
	p := sqlcdb.InsertOIDCFlowParams{
		StateHash: f.StateHash, BindingHash: f.BindingHash, Nonce: f.Nonce, Verifier: f.Verifier, Intent: int16(f.Intent), //nolint:gosec // G115: 1..3
		SessionID: text(f.SessionID), NextPath: f.Next, CreatedAt: f.CreatedAt, ExpiresAt: f.ExpiresAt,
	}
	if !f.User.IsZero() {
		u := uuid.UUID(f.User)
		p.UserID = &u
	}
	return wrap(s.db.withoutTenant().InsertOIDCFlow(ctx, p), "insert oidc flow")
}

// Take implements oidc.FlowStore.
func (s *OIDCStore) Take(ctx context.Context, stateHash string, now time.Time) (oidc.Flow, bool, error) {
	r, err := s.db.withoutTenant().TakeOIDCFlow(ctx, stateHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return oidc.Flow{}, false, nil
	}
	if err != nil {
		return oidc.Flow{}, false, fmt.Errorf("take oidc flow: %w", err)
	}
	f := oidc.Flow{
		StateHash: stateHash, BindingHash: r.BindingHash, Nonce: r.Nonce, Verifier: r.Verifier, Intent: oidc.Intent(r.Intent),
		SessionID: r.SessionID.String, Next: r.NextPath, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt,
	}
	if r.UserID != nil {
		f.User = identity.UserID(*r.UserID)
	}
	return f, now.Before(f.ExpiresAt), nil
}

// DeleteExpired implements oidc.FlowStore.
func (s *OIDCStore) DeleteExpired(ctx context.Context, now time.Time) error {
	return wrap(s.db.withoutTenant().DeleteExpiredOIDCFlows(ctx, now), "delete expired oidc flows")
}

// Find implements oidc.IdentityStore.
func (s *OIDCStore) Find(ctx context.Context, issuer, subject string) (identity.UserID, bool, error) {
	id, err := s.db.withoutTenant().FindIdentity(ctx, sqlcdb.FindIdentityParams{Issuer: issuer, Subject: subject})
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.UserID{}, false, nil
	}
	if err != nil {
		return identity.UserID{}, false, fmt.Errorf("find identity: %w", err)
	}
	return identity.UserID(id), true, nil
}

// Link implements oidc.IdentityStore: the identity and its entry in the instance audit log.
func (s *OIDCStore) Link(ctx context.Context, id oidc.Identity) error {
	return s.db.instanceTx(ctx, func(q *sqlcdb.Queries) error {
		n, err := q.InsertIdentity(ctx, sqlcdb.InsertIdentityParams{Issuer: id.Issuer, Subject: id.Subject, UserID: uuid.UUID(id.User), Email: text(id.Email)})
		if err != nil {
			return wrap(err, "link identity")
		}
		if n == 0 {
			return oidc.ErrIdentityTaken
		}
		user := id.User
		return appendInstanceAudit(ctx, q, &user, "oidc.identity_linked", "user:"+id.User.String(),
			map[string]any{"issuer": id.Issuer, "subject": id.Subject})
	})
}

// Seen implements oidc.IdentityStore.
func (s *OIDCStore) Seen(ctx context.Context, issuer, subject, email string, at time.Time) error {
	return wrap(s.db.withoutTenant().IdentitySeen(ctx, sqlcdb.IdentitySeenParams{At: &at, Email: text(email), Issuer: issuer, Subject: subject}),
		"identity seen")
}
