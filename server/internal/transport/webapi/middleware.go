// Package webapi serves the Connect API for the web UI (KONZEPT §18.3): origin check against
// CSRF, security headers, trusted client IP, session cookie handling.
package webapi

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

type ctxKey int

const clientIPKey ctxKey = iota

// ClientIP determines the client IP. X-Forwarded-For is honored only when the direct peer is a
// trusted proxy; the client is the right-most address that is not a trusted proxy (entries
// further left can be forged by the client).
func ClientIP(trusted []netip.Prefix) func(http.Handler) http.Handler {
	isTrusted := func(a netip.Addr) bool {
		return slices.ContainsFunc(trusted, func(p netip.Prefix) bool { return p.Contains(a) })
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := remoteAddr(r)
			if ip.IsValid() && isTrusted(ip) {
				// All header lines in order: some proxies add a line instead of appending.
				hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
				for i := len(hops) - 1; i >= 0; i-- {
					hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
					if err != nil {
						break
					}
					ip = hop.Unmap()
					if !isTrusted(ip) {
						break
					}
				}
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientIPKey, ip.String())))
		})
	}
}

// ClientIPFrom returns the client IP determined by ClientIP ("invalid IP" if unknown).
func ClientIPFrom(ctx context.Context) string {
	ip, _ := ctx.Value(clientIPKey).(string)
	return ip
}

func remoteAddr(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return ip.Unmap()
}

// RequireOrigin rejects cross-site requests (CSRF, login CSRF): state-changing requests must carry
// an Origin from the allow list; requests that carry any Origin must match it. Without allowed
// origins every state-changing request is rejected (fail closed).
func RequireOrigin(allowed []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			safe := r.Method == http.MethodGet || r.Method == http.MethodHead
			if (origin == "" && !safe) || (origin != "" && !slices.Contains(allowed, origin)) {
				writeConnectError(w, http.StatusForbidden, "permission_denied", "request origin not allowed")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// APIHeaders sets security headers for API responses (never cached, never framed, no content).
func APIHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// writeConnectError writes an error in the Connect protocol's JSON error format.
func writeConnectError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"code":"` + code + `","message":"` + message + `"}`))
}
