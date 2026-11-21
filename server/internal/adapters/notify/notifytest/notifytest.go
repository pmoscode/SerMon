// Package notifytest has helpers for the tests of the channel types: a safehttp client that may
// reach the loopback test servers, a server that records requests, a message with hostile values
// and golden files.
package notifytest

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// T0 is the time of the test messages.
var T0 = time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)

// Hostile is what attackers put into user names and log lines, aimed at chat formats: mentions,
// links, emphasis, code and markup.
const Hostile = "<!here> @everyone [click](https://evil.example) *bold* `code` <script>x</script>"

// LocalClient returns a safehttp client that may reach the loopback test servers.
func LocalClient(t *testing.T) *safehttp.Client {
	t.Helper()
	p, err := safehttp.NewPolicy([]netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("::1/128")})
	if err != nil {
		t.Fatal(err)
	}
	return safehttp.New(safehttp.Options{Policy: p})
}

// Security returns a brute-force alert whose user name is Hostile.
func Security(lang notify.Lang) notify.Message {
	return notify.Render(notify.Notification{
		Kind: notify.Firing, Severity: notify.Critical, Rule: "security.ssh.bruteforce",
		RuleName: notify.Text{De: "SSH-Brute-Force", En: "SSH brute force"},
		Node:     identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001")), Hostname: "web-01",
		Dimension: "ip=192.0.2.7\x1f", Value: 60, Unit: notify.UnitCount, At: T0,
		Fields: alert.Fields{"user": Hostile, "ip": "192.0.2.7", "port": "50022"},
	}, lang, "https://sermon.example")
}

// Resolved returns a resolved CPU alert.
func Resolved(lang notify.Lang) notify.Message {
	return notify.Render(notify.Notification{
		Kind: notify.Resolved, Severity: notify.Critical, Rule: "system.cpu.usage_high",
		RuleName: notify.Text{De: "Hohe CPU-Auslastung", En: "High CPU usage"},
		Node:     identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001")), Hostname: "web-01",
		Value: 0.42, Unit: notify.UnitRatio, At: T0,
	}, lang, "https://sermon.example")
}

// Server records the requests it gets and answers with Status (200 if 0).
type Server struct {
	*httptest.Server
	mu     sync.Mutex
	Status int
	Answer string
	bodies [][]byte
	header []http.Header
	paths  []string
}

// NewServer starts a recording server; it stops with the test.
func NewServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.bodies, s.header, s.paths = append(s.bodies, body), append(s.header, r.Header.Clone()), append(s.paths, r.URL.Path)
		status, answer := s.Status, s.Answer
		s.mu.Unlock()
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(s.Close)
	return s
}

// Last returns the last body and its headers.
func (s *Server) Last(t *testing.T) ([]byte, http.Header) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.bodies) == 0 {
		t.Fatal("the server got no request")
	}
	return s.bodies[len(s.bodies)-1], s.header[len(s.header)-1]
}

// LastPath returns the path of the last request.
func (s *Server) LastPath(t *testing.T) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.paths) == 0 {
		t.Fatal("the server got no request")
	}
	return s.paths[len(s.paths)-1]
}

// Message renders a CPU alert of the given kind and severity.
func Message(kind notify.Kind, severity notify.Severity, lang notify.Lang) notify.Message {
	return notify.Render(notify.Notification{
		Kind: kind, Severity: severity, Rule: "system.cpu.usage_high",
		RuleName: notify.Text{De: "Hohe CPU-Auslastung", En: "High CPU usage"},
		Node:     identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001")), Hostname: "web-01",
		Value: 0.964, Unit: notify.UnitRatio, At: T0, AckBy: "alice",
	}, lang, "https://sermon.example")
}

// Long returns the security alert with every value much longer than a service allows.
func Long(lang notify.Lang) notify.Message {
	m := Security(lang)
	m.Title += strings.Repeat("<ü>", 200)
	for range 20 {
		m.Lines = append(m.Lines, notify.Line{Label: "x", Value: strings.Repeat("<&@ü", 300)})
	}
	return m
}

// Indent formats JSON for golden files.
func Indent(t *testing.T, body []byte) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Indent(&b, body, "", "  "); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, body)
	}
	return b.String() + "\n"
}

// Golden compares got with testdata/<name> (rewritten with -update).
func Golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path) //nolint:gosec // G304: a golden file named by the test
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}
