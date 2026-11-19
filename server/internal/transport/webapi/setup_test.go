package webapi_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/setup"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
	"github.com/pmoscode/SerMon/server/internal/platform/ratelimit"
	"github.com/pmoscode/SerMon/server/internal/transport/webapi"
)

// Test fixtures, not secrets.
const (
	setupPassword = "a very long passphrase 42" // betterleaks:allow
	weakPassword  = "short"                     // betterleaks:allow
)

type setupStore struct {
	completed bool
	claimErr  error
}

func (s *setupStore) Completed(context.Context) (bool, error)               { return s.completed, nil }
func (s *setupStore) IssueToken(context.Context, [32]byte, time.Time) error { return nil }
func (s *setupStore) Complete(context.Context, [32]byte, time.Time, setup.FirstAdmin) error {
	if s.claimErr != nil {
		return s.claimErr
	}
	s.completed = true
	return nil
}

type plainHasher struct{}

func (plainHasher) Hash(pw string) (string, error) { return "$argon2id$" + pw, nil }

func setupClient(t *testing.T, store *setupStore, limit int) webv1connect.SetupServiceClient {
	t.Helper()
	svc := setup.NewService(store, plainHasher{}, time.Now, uuid.New)
	mux := http.NewServeMux()
	webapi.Mount(mux, webapi.Config{AllowedOrigins: []string{"https://ui.example"}}, webapi.Handlers{
		Setup: webapi.NewSetupHandler(svc, ratelimit.New(limit, time.Minute, time.Now), slog.New(slog.DiscardHandler)),
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	hc := srv.Client()
	hc.Transport = originTransport{next: http.DefaultTransport, origin: "https://ui.example"}
	return webv1connect.NewSetupServiceClient(hc, srv.URL)
}

func setupRequest(token string) *connect.Request[webv1.CompleteSetupRequest] {
	return connect.NewRequest(&webv1.CompleteSetupRequest{
		Token: token, Email: "ann@example.org", DisplayName: "Ann", Password: setupPassword, OrganizationName: "Example",
	})
}

func TestSetupAPI_StatusAndCompletion(t *testing.T) {
	store := &setupStore{}
	c := setupClient(t, store, 10)
	ctx := context.Background()

	res, err := c.GetSetupStatus(ctx, connect.NewRequest(&webv1.GetSetupStatusRequest{}))
	if err != nil || !res.Msg.GetSetupRequired() {
		t.Fatalf("GetSetupStatus() = %v, %v; want required", res, err)
	}
	if _, err := c.CompleteSetup(ctx, setupRequest(strings.Repeat("A", 43))); err != nil {
		t.Fatalf("CompleteSetup() error = %v", err)
	}
	if res, _ := c.GetSetupStatus(ctx, connect.NewRequest(&webv1.GetSetupStatusRequest{})); res.Msg.GetSetupRequired() {
		t.Error("setup still required after completion")
	}
}

func TestSetupAPI_ErrorCodes(t *testing.T) {
	ctx := context.Background()
	c := setupClient(t, &setupStore{claimErr: setup.ErrInvalidToken}, 10)
	if _, err := c.CompleteSetup(ctx, setupRequest(strings.Repeat("A", 43))); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("invalid token: %v, want UNAUTHENTICATED", err)
	}
	weak := setupRequest(strings.Repeat("A", 43))
	weak.Msg.Password = weakPassword
	if _, err := c.CompleteSetup(ctx, weak); connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "at least") {
		t.Errorf("weak password: %v, want INVALID_ARGUMENT with the policy message", err)
	}
}

func TestSetupAPI_IsRateLimited(t *testing.T) {
	c := setupClient(t, &setupStore{claimErr: setup.ErrInvalidToken}, 3)
	ctx := context.Background()
	for range 3 {
		_, _ = c.CompleteSetup(ctx, setupRequest(strings.Repeat("B", 43)))
	}
	if _, err := c.CompleteSetup(ctx, setupRequest(strings.Repeat("B", 43))); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Errorf("error = %v, want RESOURCE_EXHAUSTED", err)
	}
}
