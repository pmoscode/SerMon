package health_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pmoscode/SerMon/server/internal/transport/health"
)

type response struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

func serve(t *testing.T, h *health.Handler, method, path string) (*httptest.ResponseRecorder, response) {
	t.Helper()
	mux := http.NewServeMux()
	h.Register(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), method, path, nil))

	var body response
	if rec.Code != http.StatusMethodNotAllowed {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("invalid JSON body %q: %v", rec.Body.String(), err)
		}
	}
	return rec, body
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func passing(context.Context) error { return nil }

func TestHealthz_AlwaysOK(t *testing.T) {
	failing := health.Check{Name: "db", Fn: func(context.Context) error { return errors.New("down") }}
	h := health.New(discardLogger(), failing)

	rec, body := serve(t, h, http.MethodGet, "/healthz")

	if rec.Code != http.StatusOK || body.Status != "ok" {
		t.Errorf("GET /healthz = %d %+v, want 200 ok", rec.Code, body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func TestReadyz_ReadyWhenAllChecksPass(t *testing.T) {
	h := health.New(discardLogger(), health.Check{Name: "db", Fn: passing})

	rec, body := serve(t, h, http.MethodGet, "/readyz")

	if rec.Code != http.StatusOK || body.Status != "ready" || body.Checks["db"] != "ok" {
		t.Errorf("GET /readyz = %d %+v, want 200 ready with db ok", rec.Code, body)
	}
}

func TestReadyz_NotReadyWhenACheckFails_WithoutLeakingDetails(t *testing.T) {
	secret := "dial tcp 10.0.0.5:5432: password authentication failed for user sermon"
	h := health.New(discardLogger(),
		health.Check{Name: "db", Fn: func(context.Context) error { return errors.New(secret) }},
		health.Check{Name: "loki", Fn: passing},
	)

	rec, body := serve(t, h, http.MethodGet, "/readyz")

	if rec.Code != http.StatusServiceUnavailable || body.Status != "not_ready" {
		t.Errorf("GET /readyz = %d %+v, want 503 not_ready", rec.Code, body)
	}
	if body.Checks["db"] != "failing" || body.Checks["loki"] != "ok" {
		t.Errorf("checks = %+v, want db failing and loki ok", body.Checks)
	}
	if strings.Contains(rec.Body.String(), "password") {
		t.Errorf("response leaks error details: %s", rec.Body.String())
	}
}

func TestReadyz_NotReadyWhileDraining(t *testing.T) {
	h := health.New(discardLogger(), health.Check{Name: "db", Fn: passing})
	h.SetDraining()

	rec, body := serve(t, h, http.MethodGet, "/readyz")

	if rec.Code != http.StatusServiceUnavailable || body.Status != "draining" {
		t.Errorf("GET /readyz = %d %+v, want 503 draining", rec.Code, body)
	}
}

func TestEndpoints_OnlyAllowGET(t *testing.T) {
	h := health.New(discardLogger())

	for _, path := range []string{"/healthz", "/readyz"} {
		rec, _ := serve(t, h, http.MethodPost, path)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d, want 405", path, rec.Code)
		}
	}
}
