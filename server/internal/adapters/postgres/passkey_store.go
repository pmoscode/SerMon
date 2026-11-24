package postgres

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/auth/passkey"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
)

// PasskeyStore keeps passkeys and their ceremonies (M3-23, migration 00045).
type PasskeyStore struct{ db *DB }

var _ passkey.Store = (*PasskeyStore)(nil)

// NewPasskeyStore returns the store.
func NewPasskeyStore(db *DB) *PasskeyStore { return &PasskeyStore{db: db} }

func passkeyOf(r sqlcdb.WebauthnCredential) (passkey.Passkey, error) {
	p := passkey.Passkey{ID: r.ID, User: identity.UserID(r.UserID), Name: r.Name, CreatedAt: r.CreatedAt}
	if err := json.Unmarshal(r.Credential, &p.Credential); err != nil {
		return passkey.Passkey{}, fmt.Errorf("decode passkey: %w", err)
	}
	if r.LastUsedAt != nil {
		p.LastUsedAt = *r.LastUsedAt
	}
	return p, nil
}

// List implements passkey.Store.
func (s *PasskeyStore) List(ctx context.Context, user identity.UserID) ([]passkey.Passkey, error) {
	rows, err := s.db.withoutTenant().ListPasskeys(ctx, uuid.UUID(user))
	if err != nil {
		return nil, fmt.Errorf("list passkeys: %w", err)
	}
	out := make([]passkey.Passkey, 0, len(rows))
	for _, r := range rows {
		p, err := passkeyOf(r)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// Find implements passkey.Store.
func (s *PasskeyStore) Find(ctx context.Context, id []byte) (passkey.Passkey, bool, error) {
	r, err := s.db.withoutTenant().FindPasskey(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return passkey.Passkey{}, false, nil
	}
	if err != nil {
		return passkey.Passkey{}, false, fmt.Errorf("find passkey: %w", err)
	}
	p, err := passkeyOf(r)
	return p, err == nil, err
}

// Add implements passkey.Store.
func (s *PasskeyStore) Add(ctx context.Context, p passkey.Passkey, limit int) error {
	cred, err := json.Marshal(p.Credential)
	if err != nil {
		return fmt.Errorf("encode passkey: %w", err)
	}
	return s.db.instanceTx(ctx, func(q *sqlcdb.Queries) error {
		user := uuid.UUID(p.User)
		if err := q.LockUserPasskeys(ctx, user); err != nil {
			return fmt.Errorf("lock passkeys: %w", err)
		}
		n, err := q.CountPasskeys(ctx, user)
		if err != nil {
			return fmt.Errorf("count passkeys: %w", err)
		}
		if n >= int64(limit) {
			return passkey.ErrTooMany
		}
		added, err := q.InsertPasskey(ctx, sqlcdb.InsertPasskeyParams{ID: p.ID, UserID: user, Name: p.Name, Credential: cred, CreatedAt: p.CreatedAt})
		if err != nil {
			return fmt.Errorf("insert passkey: %w", err)
		}
		if added == 0 {
			return passkey.ErrFailed // the credential exists (of this user or another)
		}
		return appendInstanceAudit(ctx, q, &p.User, "passkey.added", "user:"+p.User.String(),
			map[string]any{"name": p.Name, "credential": base64.RawURLEncoding.EncodeToString(p.ID)})
	})
}

// Used implements passkey.Store.
func (s *PasskeyStore) Used(ctx context.Context, id []byte, c webauthn.Credential, at time.Time) error {
	cred, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode passkey: %w", err)
	}
	return wrap(s.db.withoutTenant().PasskeyUsed(ctx, sqlcdb.PasskeyUsedParams{ID: id, Credential: cred, At: &at}), "passkey used")
}

// Rename implements passkey.Store.
func (s *PasskeyStore) Rename(ctx context.Context, user identity.UserID, id []byte, name string) error {
	n, err := s.db.withoutTenant().RenamePasskey(ctx, sqlcdb.RenamePasskeyParams{ID: id, UserID: uuid.UUID(user), Name: name})
	if err != nil {
		return fmt.Errorf("rename passkey: %w", err)
	}
	if n == 0 {
		return passkey.ErrNotFound
	}
	return nil
}

// Delete implements passkey.Store.
func (s *PasskeyStore) Delete(ctx context.Context, user identity.UserID, id []byte) error {
	return s.db.instanceTx(ctx, func(q *sqlcdb.Queries) error {
		name, err := q.DeletePasskey(ctx, sqlcdb.DeletePasskeyParams{ID: id, UserID: uuid.UUID(user)})
		if errors.Is(err, pgx.ErrNoRows) {
			return passkey.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("delete passkey: %w", err)
		}
		return appendInstanceAudit(ctx, q, &user, "passkey.deleted", "user:"+user.String(),
			map[string]any{"name": name, "credential": base64.RawURLEncoding.EncodeToString(id)})
	})
}

// InsertCeremony implements passkey.Store.
func (s *PasskeyStore) InsertCeremony(ctx context.Context, c passkey.Ceremony) error {
	data, err := json.Marshal(c.Data)
	if err != nil {
		return fmt.Errorf("encode ceremony: %w", err)
	}
	p := sqlcdb.InsertWebAuthnCeremonyParams{
		IDHash: c.IDHash, BindingHash: text(c.BindingHash), Purpose: int16(c.Purpose), SessionID: text(c.SessionID), //nolint:gosec // G115: 1..4
		Name: c.Name, Data: data, CreatedAt: c.CreatedAt, ExpiresAt: c.ExpiresAt,
	}
	if !c.User.IsZero() {
		u := uuid.UUID(c.User)
		p.UserID = &u
	}
	return wrap(s.db.withoutTenant().InsertWebAuthnCeremony(ctx, p), "insert webauthn ceremony")
}

// TakeCeremony implements passkey.Store.
func (s *PasskeyStore) TakeCeremony(ctx context.Context, idHash string, now time.Time) (passkey.Ceremony, bool, error) {
	r, err := s.db.withoutTenant().TakeWebAuthnCeremony(ctx, idHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return passkey.Ceremony{}, false, nil
	}
	if err != nil {
		return passkey.Ceremony{}, false, fmt.Errorf("take webauthn ceremony: %w", err)
	}
	c := passkey.Ceremony{
		IDHash: idHash, BindingHash: r.BindingHash.String, Purpose: passkey.Purpose(r.Purpose), SessionID: r.SessionID.String,
		Name: r.Name, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt,
	}
	if r.UserID != nil {
		c.User = identity.UserID(*r.UserID)
	}
	if err := json.Unmarshal(r.Data, &c.Data); err != nil {
		return passkey.Ceremony{}, false, fmt.Errorf("decode ceremony: %w", err)
	}
	return c, now.Before(c.ExpiresAt), nil
}

// DeleteExpiredCeremonies implements passkey.Store.
func (s *PasskeyStore) DeleteExpiredCeremonies(ctx context.Context, now time.Time) error {
	return wrap(s.db.withoutTenant().DeleteExpiredWebAuthnCeremonies(ctx, now), "delete expired webauthn ceremonies")
}

// CloneSuspected implements passkey.Store: an entry in the instance audit log.
func (s *PasskeyStore) CloneSuspected(ctx context.Context, user identity.UserID, id []byte) error {
	return s.db.instanceTx(ctx, func(q *sqlcdb.Queries) error {
		return appendInstanceAudit(ctx, q, nil, "passkey.clone_suspected", "user:"+user.String(),
			map[string]any{"credential": base64.RawURLEncoding.EncodeToString(id)})
	})
}
