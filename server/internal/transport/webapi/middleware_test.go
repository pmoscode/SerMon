package webapi_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/pmoscode/SerMon/server/internal/transport/webapi"
)

func ok() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, webapi.ClientIPFrom(r.Context()))
	})
}

func request(method, origin, remote string, headers map[string]string) *http.Request {
	r := httptest.NewRequestWithContext(context.Background(), method, "/sermon.web.v1.AuthService/Login", strings.NewReader("{}"))
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	r.RemoteAddr = remote
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

func TestClientIP_UsesTheRemoteAddressByDefault(t *testing.T) {
	h := webapi.ClientIP(nil)(ok())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, request(http.MethodPost, "", "198.51.100.7:51234", map[string]string{"X-Forwarded-For": "203.0.113.1"}))

	if got := rec.Body.String(); got != "198.51.100.7" {
		t.Errorf("client IP = %q, want the remote address (X-Forwarded-For must be ignored without trusted proxy)", got)
	}
}

func TestClientIP_HonorsForwardedForOnlyFromTrustedProxies(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	h := webapi.ClientIP(trusted)(ok())
	cases := map[string]struct {
		remote, xff, want string
	}{
		"behind trusted proxy":                  {"10.0.0.2:443", "203.0.113.9", "203.0.113.9"},
		"spoofed entry left of the real client": {"10.0.0.2:443", "1.2.3.4, 203.0.113.9", "203.0.113.9"},
		"chain of trusted proxies":              {"10.0.0.2:443", "203.0.113.9, 10.0.0.5", "203.0.113.9"},
		"untrusted remote ignores header":       {"192.0.2.50:443", "203.0.113.9", "192.0.2.50"},
		"garbage header falls back":             {"10.0.0.2:443", "not-an-ip", "10.0.0.2"},
		"IPv6 client":                           {"10.0.0.2:443", "2001:db8::1", "2001:db8::1"},
	}
	for name, tc := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, request(http.MethodPost, "", tc.remote, map[string]string{"X-Forwarded-For": tc.xff}))
		if got := rec.Body.String(); got != tc.want {
			t.Errorf("%s: client IP = %q, want %q", name, got, tc.want)
		}
	}
}

func TestRequireOrigin(t *testing.T) {
	h := webapi.RequireOrigin([]string{"https://sermon.example.com"})(ok())
	cases := []struct {
		name, method, origin string
		want                 int
	}{
		{"same origin POST", http.MethodPost, "https://sermon.example.com", http.StatusOK},
		{"foreign origin POST", http.MethodPost, "https://evil.example", http.StatusForbidden},
		{"missing origin POST", http.MethodPost, "", http.StatusForbidden},
		{"origin with path is not an origin", http.MethodPost, "https://sermon.example.com/x", http.StatusForbidden},
		{"scheme matters", http.MethodPost, "http://sermon.example.com", http.StatusForbidden},
		{"null origin", http.MethodPost, "null", http.StatusForbidden},
		{"GET without origin", http.MethodGet, "", http.StatusOK},
		{"GET with foreign origin", http.MethodGet, "https://evil.example", http.StatusForbidden},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, request(tc.method, tc.origin, "192.0.2.1:1", nil))
		if rec.Code != tc.want {
			t.Errorf("%s: status = %d, want %d", tc.name, rec.Code, tc.want)
		}
		if tc.want == http.StatusForbidden && !strings.Contains(rec.Body.String(), `"code":"permission_denied"`) {
			t.Errorf("%s: body %q is not a Connect error", tc.name, rec.Body.String())
		}
	}
}

func TestRequireOrigin_WithoutAllowedOriginsRejectsEverythingStateChanging(t *testing.T) {
	h := webapi.RequireOrigin(nil)(ok())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, request(http.MethodPost, "https://sermon.example.com", "192.0.2.1:1", nil))

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (fail closed when no origin is configured)", rec.Code)
	}
}

func TestAPIHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	webapi.APIHeaders(ok()).ServeHTTP(rec, request(http.MethodPost, "", "192.0.2.1:1", nil))

	want := map[string]string{
		"Cache-Control":           "no-store",
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Referrer-Policy":         "same-origin",
		"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
	}
	for name, value := range want {
		if got := rec.Header().Get(name); got != value {
			t.Errorf("%s = %q, want %q", name, got, value)
		}
	}
}
