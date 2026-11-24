//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/auth"
	"github.com/pmoscode/SerMon/server/internal/app/auth/passkey"
	"github.com/pmoscode/SerMon/server/internal/app/auth/passkey/passkeytest"
	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/platform/ratelimit"
)

type noLockout struct{}

func (noLockout) VerifyFactor(_ context.Context, _ identity.UserID, check func() (bool, error)) error {
	ok, err := check()
	if err == nil && !ok {
		return auth.ErrWrongCode
	}
	return err
}

// M3-23: passkeys and their ceremonies over PostgreSQL, with the record of go-webauthn as JSON.
func TestPasskeyStore_RegisterSignInDelete(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	db := open(t, f.appDSN)
	user := uuid.New()
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO users (id, email, display_name) VALUES ($1, $2, 'Pia')", user, user.String()+"@example.org")
	cfg, _ := passkey.Config([]string{"https://sermon.example.com"})
	wa, err := webauthn.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.NewManager(postgres.NewSessionStore(db), time.Now)
	store := postgres.NewPasskeyStore(db)
	svc := passkey.NewService(passkey.Deps{
		WebAuthn: wa, Store: store, Users: postgres.NewCredentialStore(db), Lockout: noLockout{}, Sessions: sessions,
		Limiter: ratelimit.New(1000, time.Minute, time.Now), Now: time.Now,
	})
	_, s, err := sessions.Create(ctx, identity.UserID(user), [16]byte{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.BeginRegistration(ctx, s, "Laptop")
	if err != nil {
		t.Fatal(err)
	}
	a := passkeytest.New("https://sermon.example.com")
	resp, _ := a.Register(b.Options)
	if _, err := svc.FinishRegistration(ctx, s, b.CeremonyID, resp); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.FinishRegistration(ctx, s, b.CeremonyID, resp); !errors.Is(err, passkey.ErrFailed) {
		t.Errorf("the ceremony again: %v", err)
	}
	// The same credential can't be added twice (directly in the store).
	keys, err := svc.List(ctx, identity.UserID(user))
	if err != nil || len(keys) != 1 || keys[0].Name != "Laptop" || keys[0].Credential.Authenticator.SignCount != 1 {
		t.Fatalf("List() = %+v, %v", keys, err)
	}
	if err := store.Add(ctx, keys[0], 10); !errors.Is(err, passkey.ErrFailed) {
		t.Errorf("duplicate: %v", err)
	}
	other := keys[0]
	other.ID = []byte(strings.Repeat("x", 32))
	if err := store.Add(ctx, other, 1); !errors.Is(err, passkey.ErrTooMany) {
		t.Errorf("beyond the limit: %v", err)
	}

	lb, err := svc.Begin(ctx, passkey.PurposeLogin, "", "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	resp, _ = a.Assert(lb.Options)
	res, err := svc.Finish(ctx, passkey.PurposeLogin, lb.CeremonyID, lb.Binding, "", resp, "192.0.2.1")
	if err != nil || res.Session.User != identity.UserID(user) {
		t.Fatalf("sign-in = %+v, %v", res, err)
	}
	keys, _ = svc.List(ctx, identity.UserID(user))
	if keys[0].LastUsedAt.IsZero() || keys[0].Credential.Authenticator.SignCount != 2 {
		t.Errorf("use not stored: %+v", keys[0])
	}
	if err := svc.Rename(ctx, identity.UserID(user), a.ID, "Work laptop"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Rename(ctx, identity.UserID(uuid.New()), a.ID, "Mine"); !errors.Is(err, passkey.ErrNotFound) {
		t.Errorf("rename by another: %v", err)
	}
	if err := svc.Delete(ctx, s, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, s, a.ID); !errors.Is(err, passkey.ErrNotFound) {
		t.Errorf("delete again: %v", err)
	}
	var audits int
	ownerQueryRow(t, f.db.OwnerDSN, `SELECT count(*) FROM instance_audit_log WHERE target = $1 AND action IN ('passkey.added', 'passkey.deleted')`,
		[]any{"user:" + user.String()}, &audits)
	if audits != 2 {
		t.Errorf("%d audit entries", audits)
	}
	// Expired ceremonies are cleaned up, and the database refuses inconsistent ones.
	if err := store.InsertCeremony(ctx, passkey.Ceremony{
		IDHash: strings.Repeat("a", 64), Purpose: passkey.PurposeLogin,
		CreatedAt: time.Now(), ExpiresAt: time.Now(),
	}); err == nil {
		t.Error("a sign-in ceremony without a browser binding was stored")
	}
	if err := store.DeleteExpiredCeremonies(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
}
