package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/auth"
	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/platform/ratelimit"
)

// fakeFactor accepts each listed code once.
type fakeFactor struct{ codes map[string]bool }

func (f *fakeFactor) Enabled(_ context.Context, user identity.UserID) (bool, error) {
	return user == alice, nil
}

func (f *fakeFactor) Verify(_ context.Context, _ identity.UserID, code string) (bool, error) {
	if f.codes[code] {
		delete(f.codes, code)
		return true, nil
	}
	return false, nil
}

func newEnvWithFactor(t *testing.T) env {
	t.Helper()
	e := newEnv(t, fast)
	factor := &fakeFactor{codes: map[string]bool{"111111": true, "222222": true, "333333": true}}
	clock := func() time.Time { return *e.now }
	e.svc = auth.NewService(auth.Deps{
		Credentials: e.creds, Sessions: session.NewManager(&memSessions{m: map[string]session.Session{}}, clock),
		Hasher: e.hash, IPLimiter: ratelimit.New(100, time.Minute, clock), Now: clock, SecondFactor: factor,
	})
	return e
}

func TestLogin_WithSecondFactorWaitsForTheCode(t *testing.T) {
	e := newEnvWithFactor(t)
	ctx := context.Background()

	res, err := e.svc.Login(ctx, "alice@example.org", alicePassword, "192.0.2.10")
	if err != nil || !res.SecondFactorRequired || !res.Session.SecondFactorPending || len(res.Organizations) != 0 {
		t.Fatalf("Login() = %+v, %v", res, err)
	}
	if _, err := e.svc.ConfirmPassword(ctx, res.Token, alicePassword, "192.0.2.10"); !errors.Is(err, session.ErrInvalid) {
		t.Errorf("a pending session is no session: %v", err)
	}
	if _, err := e.svc.VerifySecondFactor(ctx, res.Token, "999999", "192.0.2.10"); !errors.Is(err, auth.ErrWrongCode) {
		t.Errorf("wrong code: %v", err)
	}

	done, err := e.svc.VerifySecondFactor(ctx, res.Token, "111111", "192.0.2.10")
	if err != nil || done.Token == res.Token || done.Session.SecondFactorPending || len(done.Organizations) != 2 {
		t.Fatalf("VerifySecondFactor() = %+v, %v", done, err)
	}
	if c := e.creds.creds[alice]; c.FailedAttempts != 0 {
		t.Errorf("success resets the failures: %+v", c)
	}
	if _, err := e.svc.VerifySecondFactor(ctx, done.Token, "222222", "192.0.2.10"); !errors.Is(err, session.ErrNotPending) {
		t.Errorf("second verification: %v, want ErrNotPending", err)
	}
}

func TestLogin_PasswordAndCodeGuessesShareTheLockout(t *testing.T) {
	e := newEnvWithFactor(t)
	ctx := context.Background()

	// Someone who knows the password guesses codes, logging in again after every guess.
	for range 5 {
		res, err := e.svc.Login(ctx, "alice@example.org", alicePassword, "192.0.2.10")
		if err != nil {
			break
		}
		_, _ = e.svc.VerifySecondFactor(ctx, res.Token, "000000", "192.0.2.10")
	}

	if c := e.creds.creds[alice]; c.LockedUntil == nil || c.FailedAttempts < 5 {
		t.Fatalf("five wrong codes must lock the account although the password was right: %+v", c)
	}
	if _, err := e.svc.Login(ctx, "alice@example.org", alicePassword, "192.0.2.10"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("locked account: %v", err)
	}
}

func TestStepUp_UsersWithSecondFactorConfirmWithTheCode(t *testing.T) {
	e := newEnvWithFactor(t)
	ctx := context.Background()
	res, _ := e.svc.Login(ctx, "alice@example.org", alicePassword, "192.0.2.10")
	done, err := e.svc.VerifySecondFactor(ctx, res.Token, "111111", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	*e.now = e.now.Add(20 * time.Minute)

	if _, err := e.svc.ConfirmPassword(ctx, done.Token, alicePassword, "192.0.2.10"); !errors.Is(err, auth.ErrSecondFactorRequired) {
		t.Errorf("password step-up with 2FA: %v, want ErrSecondFactorRequired", err)
	}
	if _, err := e.svc.ConfirmSecondFactor(ctx, done.Token, "111111", "192.0.2.10"); !errors.Is(err, auth.ErrWrongCode) {
		t.Errorf("a used code: %v", err)
	}
	s, err := e.svc.ConfirmSecondFactor(ctx, done.Token, "222222", "192.0.2.10")
	if err != nil || !s.StepUpAt.Equal(*e.now) {
		t.Errorf("ConfirmSecondFactor() = %+v, %v", s, err)
	}
}

func TestConfirmSecondFactor_WithoutSecondFactorIsAWrongCode(t *testing.T) {
	e := newEnv(t, fast)
	token := loggedIn(t, e)

	if _, err := e.svc.ConfirmSecondFactor(context.Background(), token, "123456", "192.0.2.10"); !errors.Is(err, auth.ErrWrongCode) {
		t.Errorf("error = %v, want ErrWrongCode", err)
	}
}
