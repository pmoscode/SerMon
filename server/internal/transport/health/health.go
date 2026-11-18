// Package health provides the liveness (/healthz) and readiness (/readyz) endpoints.
//
// Responses never contain error details – failing checks are logged server-side only.
package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"
)

// checkTimeout bounds a single readiness check.
const checkTimeout = 2 * time.Second

// Check is a named readiness probe, e.g. a database ping.
type Check struct {
	Name string
	Fn   func(ctx context.Context) error
}

// Handler serves the health endpoints.
type Handler struct {
	logger   *slog.Logger
	checks   []Check
	draining atomic.Bool
}

// New returns a Handler that runs the given readiness checks on every /readyz request.
func New(logger *slog.Logger, checks ...Check) *Handler {
	return &Handler{logger: logger, checks: checks}
}

// Register adds the endpoints to mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /readyz", h.readyz)
}

// SetDraining marks the server as shutting down; /readyz reports not ready from then on.
func (h *Handler) SetDraining() {
	h.draining.Store(true)
}

type response struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
}

func (h *Handler) healthz(w http.ResponseWriter, _ *http.Request) {
	h.write(w, http.StatusOK, response{Status: "ok"})
}

func (h *Handler) readyz(w http.ResponseWriter, r *http.Request) {
	if h.draining.Load() {
		h.write(w, http.StatusServiceUnavailable, response{Status: "draining"})
		return
	}

	results := make(map[string]string, len(h.checks))
	ready := true
	for _, check := range h.checks {
		if err := h.run(r.Context(), check); err != nil {
			h.logger.WarnContext(r.Context(), "readiness check failed", "check", check.Name, "error", err)
			results[check.Name] = "failing"
			ready = false
			continue
		}
		results[check.Name] = "ok"
	}

	if !ready {
		h.write(w, http.StatusServiceUnavailable, response{Status: "not_ready", Checks: results})
		return
	}
	h.write(w, http.StatusOK, response{Status: "ready", Checks: results})
}

func (h *Handler) run(ctx context.Context, check Check) error {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	return check.Fn(ctx)
}

func (h *Handler) write(w http.ResponseWriter, status int, body response) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		h.logger.Error("writing health response failed", "error", err)
	}
}
