package webapi

import (
	"context"
	"log/slog"
	"testing"

	"connectrpc.com/connect"

	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
)

type limiterFunc func(string) bool

func (f limiterFunc) Allow(key string) bool { return f(key) }

// The display's address is limited before its token is even looked at; without the header of
// the kiosk scheme nothing runs, and the KioskService has no streams.
func TestKioskAuthenticator_LimitsAndRejectsBeforeTheHandler(t *testing.T) {
	called := false
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		called = true
		return nil, nil
	}
	deny := limiterFunc(func(string) bool { return false })
	allow := limiterFunc(func(string) bool { return true })
	req := connect.NewRequest(&webv1.GetKioskDashboardRequest{})
	req.Header().Set("Authorization", KioskAuthorization+"smk_x")

	a := NewKioskAuthenticator(nil, deny, allow, slog.New(slog.DiscardHandler))
	if _, err := a.WrapUnary(next)(context.Background(), req); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Errorf("limited address: %v", err)
	}
	a = NewKioskAuthenticator(nil, allow, allow, slog.New(slog.DiscardHandler))
	for _, header := range []string{"", "Bearer smk_x", "kiosk smk_x"} {
		r := connect.NewRequest(&webv1.GetKioskDashboardRequest{})
		r.Header().Set("Authorization", header)
		if _, err := a.WrapUnary(next)(context.Background(), r); connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Errorf("header %q: %v", header, err)
		}
	}
	if err := a.WrapStreamingHandler(nil)(context.Background(), nil); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("stream: %v", err)
	}
	if called {
		t.Error("the handler must not run")
	}
	if _, err := displayFrom(context.Background()); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("no display in the context: %v", err)
	}
}
