package webapi_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	agentv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1/agentv1connect"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
	"github.com/pmoscode/SerMon/server/internal/transport/webapi"
)

func limitedServer(t *testing.T) (*http.Client, string) {
	t.Helper()
	mux := http.NewServeMux()
	webapi.Mount(mux, webapi.Config{AllowedOrigins: []string{"https://ui.example"}}, webapi.Handlers{
		Auth: webapi.NewAuthHandler(nil, nil, nil, nil, slog.New(slog.DiscardHandler)),
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	hc := srv.Client()
	hc.Transport = originTransport{next: http.DefaultTransport, origin: "https://ui.example"}
	return hc, srv.URL
}

// Anonymous endpoints must not decode arbitrarily large (or highly compressed) messages: a small
// gzip body could otherwise expand to gigabytes before any validation or rate limit runs.
func TestMount_LimitsTheMessageSize(t *testing.T) {
	hc, url := limitedServer(t)
	ctx := context.Background()
	huge := strings.Repeat("a", webapi.MaxRequestBytes+1)

	for name, opts := range map[string][]connect.ClientOption{
		"plain": nil,
		"gzip":  {connect.WithSendGzip()},
	} {
		auth := webv1connect.NewAuthServiceClient(hc, url, opts...)
		_, err := auth.Login(ctx, connect.NewRequest(&webv1.LoginRequest{Email: "a@example.org", Password: huge}))
		if connect.CodeOf(err) != connect.CodeResourceExhausted {
			t.Errorf("%s: Login with %d bytes = %v, want RESOURCE_EXHAUSTED", name, len(huge), err)
		}
		enroll := agentv1connect.NewEnrollmentServiceClient(hc, url, opts...)
		_, err = enroll.Enroll(ctx, connect.NewRequest(&agentv1.EnrollRequest{Token: huge}))
		if connect.CodeOf(err) != connect.CodeResourceExhausted {
			t.Errorf("%s: Enroll with %d bytes = %v, want RESOURCE_EXHAUSTED", name, len(huge), err)
		}
	}

	// Normal requests still pass the limit (and then fail validation as before).
	auth := webv1connect.NewAuthServiceClient(hc, url)
	_, err := auth.Login(ctx, connect.NewRequest(&webv1.LoginRequest{Email: "a@example.org", Password: strings.Repeat("a", 2000)}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("Login with 2000 bytes = %v, want INVALID_ARGUMENT from validation", err)
	}
}
