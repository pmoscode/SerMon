//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/auth/oidc"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
)

func hex64(c byte) string { return strings.Repeat(string(c), 64) }

// M3-20: a flow is taken once and only before it expires.
func TestOIDCStore_FlowsAreTakenOnce(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	store := postgres.NewOIDCStore(open(t, f.appDSN))
	now := time.Now().UTC().Truncate(time.Microsecond)
	flow := oidc.Flow{
		StateHash: hex64('a'), BindingHash: hex64('b'), Nonce: strings.Repeat("n", 43), Verifier: strings.Repeat("v", 64),
		Intent: oidc.IntentLogin, Next: "/alerts", CreatedAt: now, ExpiresAt: now.Add(oidc.FlowLifetime),
	}
	if err := store.Insert(ctx, flow); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.Take(ctx, flow.StateHash, now)
	if err != nil || !ok || got.BindingHash != flow.BindingHash || got.Verifier != flow.Verifier || got.Next != "/alerts" ||
		got.Intent != oidc.IntentLogin || !got.User.IsZero() || !got.CreatedAt.Equal(now) {
		t.Fatalf("Take() = %+v, %v, %v", got, ok, err)
	}
	if _, ok, _ := store.Take(ctx, flow.StateHash, now); ok {
		t.Error("taken twice")
	}
	late := flow
	late.StateHash = hex64('c')
	if err := store.Insert(ctx, late); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Take(ctx, late.StateHash, late.ExpiresAt); ok {
		t.Error("an expired flow was taken")
	}
	if _, ok, _ := store.Take(ctx, late.StateHash, now); ok {
		t.Error("an expired flow is deleted when it is tried")
	}
	old := flow
	old.StateHash = hex64('d')
	if err := store.Insert(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteExpired(ctx, old.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Take(ctx, old.StateHash, now); ok {
		t.Error("DeleteExpired kept an expired flow")
	}
	// A login flow names no user; the database refuses one that does (and a link without one).
	bad := flow
	bad.StateHash, bad.Intent = hex64('e'), oidc.IntentLink
	if err := store.Insert(ctx, bad); err == nil {
		t.Error("a link flow without user and session was stored")
	}
}

func TestOIDCStore_IdentitiesByIssuerAndSubject(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	store := postgres.NewOIDCStore(open(t, f.appDSN))
	alice, bob := uuid.New(), uuid.New()
	for _, u := range []uuid.UUID{alice, bob} {
		ownerExec(t, f.db.OwnerDSN, "INSERT INTO users (id, email, display_name) VALUES ($1, $2, 'U')", u, u.String()+"@example.org")
	}
	iss := "https://idp-" + uuid.NewString() + ".example"
	if _, ok, err := store.Find(ctx, iss, "alice-sub"); err != nil || ok {
		t.Fatalf("Find() before = %v, %v", ok, err)
	}
	if err := store.Link(ctx, oidc.Identity{Issuer: iss, Subject: "alice-sub", User: identity.UserID(alice), Email: "a@example.org"}); err != nil {
		t.Fatal(err)
	}
	if u, ok, err := store.Find(ctx, iss, "alice-sub"); err != nil || !ok || u != identity.UserID(alice) {
		t.Errorf("Find() = %v, %v, %v", u, ok, err)
	}
	// The same subject at another issuer is another identity.
	if _, ok, _ := store.Find(ctx, iss+"/other", "alice-sub"); ok {
		t.Error("found at another issuer")
	}
	for name, id := range map[string]oidc.Identity{
		"taken by alice":        {Issuer: iss, Subject: "alice-sub", User: identity.UserID(bob)},
		"alice has one already": {Issuer: iss, Subject: "second", User: identity.UserID(alice)},
	} {
		if err := store.Link(ctx, id); !errors.Is(err, oidc.ErrIdentityTaken) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := store.Seen(ctx, iss, "alice-sub", "new@example.org", time.Now()); err != nil {
		t.Fatal(err)
	}
	var email string
	var audit int
	ownerQueryRow(t, f.db.OwnerDSN, "SELECT email FROM user_identities WHERE issuer = $1", []any{iss}, &email)
	ownerQueryRow(t, f.db.OwnerDSN, "SELECT count(*) FROM instance_audit_log WHERE action = 'oidc.identity_linked' AND details->>'issuer' = $1", []any{iss}, &audit)
	if email != "new@example.org" || audit != 1 {
		t.Errorf("email %q, %d audit entries", email, audit)
	}
	// The identity goes with the user.
	ownerExec(t, f.db.OwnerDSN, "DELETE FROM users WHERE id = $1", alice)
	if _, ok, _ := store.Find(ctx, iss, "alice-sub"); ok {
		t.Error("the identity of a deleted user")
	}
}
