package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/auth"
	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
)

func loggedIn(t *testing.T, e env) string {
	t.Helper()
	res, err := e.svc.Login(context.Background(), "alice@example.org", alicePassword, "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	return res.Token
}

func TestConfirmPassword_RenewsTheStepUp(t *testing.T) {
	e := newEnv(t, fast)
	token := loggedIn(t, e)
	*e.now = e.now.Add(20 * time.Minute)

	s, err := e.svc.ConfirmPassword(context.Background(), token, alicePassword, "192.0.2.10")
	if err != nil {
		t.Fatalf("ConfirmPassword() error = %v", err)
	}
	if !s.StepUpAt.Equal(*e.now) || s.User != alice || s.Org != orgA {
		t.Errorf("session = %+v, want step-up at %v", s, *e.now)
	}
}

func TestConfirmPassword_WrongPasswordCountsTowardsTheLockout(t *testing.T) {
	e := newEnv(t, fast)
	token := loggedIn(t, e)
	ctx := context.Background()

	for range 5 {
		if _, err := e.svc.ConfirmPassword(ctx, token, "wrong password here", "192.0.2.10"); !errors.Is(err, auth.ErrWrongPassword) {
			t.Fatalf("wrong password: error = %v, want ErrWrongPassword", err)
		}
	}

	if c := e.creds.creds[alice]; c.LockedUntil == nil {
		t.Fatalf("five wrong confirmations must lock the account: %+v", c)
	}
	if _, err := e.svc.ConfirmPassword(ctx, token, alicePassword, "192.0.2.10"); !errors.Is(err, auth.ErrWrongPassword) {
		t.Errorf("locked account: error = %v, want ErrWrongPassword", err)
	}
	if _, err := e.svc.Login(ctx, "alice@example.org", alicePassword, "192.0.2.10"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("the lock also applies to logins: %v", err)
	}
	*e.now = e.now.Add(time.Minute)
	if _, err := e.svc.ConfirmPassword(ctx, token, alicePassword, "192.0.2.10"); err != nil {
		t.Errorf("after the lock: %v", err)
	}
	if c := e.creds.creds[alice]; c.FailedAttempts != 0 {
		t.Errorf("a confirmation resets the failures: %+v", c)
	}
}

func TestConfirmPassword_RejectsInvalidSessionsAndOversizedInput(t *testing.T) {
	e := newEnv(t, fast)
	token := loggedIn(t, e)
	ctx := context.Background()

	if _, err := e.svc.ConfirmPassword(ctx, "unknown-token", alicePassword, "192.0.2.10"); !errors.Is(err, session.ErrInvalid) {
		t.Errorf("unknown session: error = %v, want session.ErrInvalid", err)
	}
	for _, pw := range []string{"", strings.Repeat("x", 1025)} {
		if _, err := e.svc.ConfirmPassword(ctx, token, pw, "192.0.2.10"); !errors.Is(err, auth.ErrWrongPassword) {
			t.Errorf("password of %d bytes: error = %v", len(pw), err)
		}
	}
	if c := e.creds.creds[alice]; c.FailedAttempts != 0 {
		t.Errorf("invalid input is no attempt: %+v", c)
	}
}

func TestConfirmPassword_RateLimitsPerClientIP(t *testing.T) {
	e := newEnv(t, fast)
	token := loggedIn(t, e)
	ctx := context.Background()
	for range 20 {
		_, _ = e.svc.ConfirmPassword(ctx, token, "wrong password here", "198.51.100.7")
		*e.now = e.now.Add(time.Second)
	}

	if _, err := e.svc.ConfirmPassword(ctx, token, alicePassword, "198.51.100.7"); !errors.Is(err, auth.ErrTooManyAttempts) {
		t.Errorf("error = %v, want ErrTooManyAttempts", err)
	}
}
