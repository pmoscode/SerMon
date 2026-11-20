package promtext_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pmoscode/SerMon/server/internal/platform/promtext"
)

func TestRegistry_WritesTheTextFormatSortedByName(t *testing.T) {
	r := promtext.NewRegistry()
	r.Gauge("sermon_server_live_streams", "Open live streams.", func() float64 { return 3 })
	r.Counter("sermon_server_ingest_batches_total", "Accepted batches.", func() float64 { return 42 })
	r.Info("sermon_server_build_info", "Version of the server.", map[string]string{"version": `1.0 "beta"\n`})

	var out strings.Builder
	if _, err := r.WriteTo(&out); err != nil {
		t.Fatal(err)
	}

	want := `# HELP sermon_server_build_info Version of the server.
# TYPE sermon_server_build_info gauge
sermon_server_build_info{version="1.0 \"beta\"\\n"} 1
# HELP sermon_server_ingest_batches_total Accepted batches.
# TYPE sermon_server_ingest_batches_total counter
sermon_server_ingest_batches_total 42
# HELP sermon_server_live_streams Open live streams.
# TYPE sermon_server_live_streams gauge
sermon_server_live_streams 3
`
	if out.String() != want {
		t.Errorf("output:\n%s\nwant:\n%s", out.String(), want)
	}
}

func TestRegistry_RejectsInvalidNames(t *testing.T) {
	for _, name := range []string{"", "9lives", "with-dash", "sermon server"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("name %q accepted", name)
				}
			}()
			promtext.NewRegistry().Gauge(name, "x", func() float64 { return 0 })
		}()
	}
}

func TestRegistry_FormatsSpecialValues(t *testing.T) {
	r := promtext.NewRegistry()
	r.Gauge("a", "Help with \\ and\nnewline.", func() float64 { return 0.25 })
	var out strings.Builder
	_, _ = r.WriteTo(&out)

	if !strings.Contains(out.String(), "# HELP a Help with \\\\ and\\nnewline.\n") || !strings.Contains(out.String(), "\na 0.25\n") {
		t.Errorf("output = %q", out.String())
	}
}

func TestHandler_ServesOnlyGetMetrics(t *testing.T) {
	r := promtext.NewRegistry()
	r.Gauge("a", "A.", func() float64 { return 1 })
	h := r.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/plain; version=0.0.4; charset=utf-8" ||
		rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(rec.Body.String(), "\na 1\n") {
		t.Errorf("GET /metrics = %d %v %q", rec.Code, rec.Header(), rec.Body.String())
	}
	for _, req := range []*http.Request{
		httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/metrics", nil),
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/other", nil),
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			t.Errorf("%s %s = %d", req.Method, req.URL.Path, rec.Code)
		}
	}
}
