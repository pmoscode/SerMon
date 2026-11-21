package ntfy_test

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/notify/ntfy"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
)

var update = flag.Bool("update", false, "rewrite the golden files")

var t0 = time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)

func local(t *testing.T) *safehttp.Client {
	t.Helper()
	p, err := safehttp.NewPolicy([]netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")})
	if err != nil {
		t.Fatal(err)
	}
	return safehttp.New(safehttp.Options{Policy: p})
}

func alertMessage(kind notify.Kind, severity notify.Severity) notify.Message {
	return notify.Render(notify.Notification{
		Kind: kind, Severity: severity, Rule: "system.cpu.usage_high", RuleName: notify.Text{De: "Hohe CPU-Auslastung", En: "High CPU usage"},
		Node: identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001")), Hostname: "web-01", Value: 0.964, Unit: notify.UnitRatio, At: t0,
	}, notify.German, "https://sermon.example")
}

type publish struct {
	Topic    string   `json:"topic"`
	Title    string   `json:"title"`
	Message  string   `json:"message"`
	Priority int      `json:"priority"`
	Tags     []string `json:"tags"`
	Click    string   `json:"click"`
}

func server(t *testing.T, got *[]publish, auth *string, body *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/" || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var p publish
		if err := json.Unmarshal(raw, &p); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		*got = append(*got, p)
		*auth, *body = r.Header.Get("Authorization"), string(raw)
		_, _ = io.WriteString(w, `{"id":"abc"}`)
	}))
}

func config(server string) notify.Config {
	return notify.Config{Settings: map[string]string{"server": server}, Secrets: map[string]string{"topic": "sermon-ops-7f3a", "token": "tk_fixture0123456789"}}
}

func TestNtfy_PublishesWithPriorityFromSeverity(t *testing.T) {
	var got []publish
	var auth, body string
	srv := server(t, &got, &auth, &body)
	defer srv.Close()
	n := ntfy.New(local(t))
	cases := []struct {
		kind     notify.Kind
		severity notify.Severity
		priority int
		tag      string
	}{
		{notify.Firing, notify.Critical, 5, "rotating_light"},
		{notify.Firing, notify.Warning, 4, "warning"},
		{notify.Resolved, notify.Critical, 3, "white_check_mark"},
		{notify.Acknowledged, notify.Critical, 2, "eyes"},
		{notify.FlappingStarted, notify.Warning, 3, "repeat"},
		{notify.FlappingStopped, notify.Warning, 2, "repeat"},
	}
	for _, tc := range cases {
		if err := n.Send(context.Background(), config(srv.URL+"/"), alertMessage(tc.kind, tc.severity)); err != nil {
			t.Fatal(err)
		}
		p := got[len(got)-1]
		if p.Priority != tc.priority || len(p.Tags) != 1 || p.Tags[0] != tc.tag {
			t.Errorf("%s %s: priority %d tags %v, want %d %s", tc.kind, tc.severity, p.Priority, p.Tags, tc.priority, tc.tag)
		}
		if p.Topic != "sermon-ops-7f3a" || p.Click != "https://sermon.example/nodes/0193f5a0-0000-7000-8000-000000000001" || auth != "Bearer tk_fixture0123456789" {
			t.Errorf("topic %q click %q auth %q", p.Topic, p.Click, auth)
		}
	}
	if err := n.Send(context.Background(), config(srv.URL), alertMessage(notify.Firing, notify.Critical)); err != nil {
		t.Fatal(err)
	}
	golden(t, "firing.json", body)
}

func TestNtfy_WithoutTokenAndTest(t *testing.T) {
	var got []publish
	var auth, body string
	srv := server(t, &got, &auth, &body)
	defer srv.Close()
	c := config(srv.URL)
	delete(c.Secrets, "token")
	m := notify.Render(notify.TestNotification(t0), notify.English, "https://sermon.example")
	if err := ntfy.New(local(t)).Send(context.Background(), c, m); err != nil {
		t.Fatal(err)
	}
	if auth != "" || got[0].Priority != 3 {
		t.Errorf("auth %q, priority %d", auth, got[0].Priority)
	}
	golden(t, "test.json", body)
}

func TestNtfy_RefusesInternalTargetsAndChecksTheTopic(t *testing.T) {
	var got []publish
	var auth, body string
	srv := server(t, &got, &auth, &body)
	defer srv.Close()
	err := ntfy.New(safehttp.New(safehttp.Options{})).Send(context.Background(), config(srv.URL), alertMessage(notify.Firing, notify.Critical))
	if !errors.Is(err, safehttp.ErrBlocked) {
		t.Errorf("internal ntfy without allowlist: %v", err)
	}
	n := ntfy.New(local(t))
	for _, topic := range []string{"", "with space", "slash/topic", "ümlaut", string(make([]byte, 65))} {
		c := config("https://ntfy.example")
		c.Secrets["topic"] = topic
		if err := n.Validate(c); err == nil {
			t.Errorf("topic %q accepted", topic)
		}
	}
	if err := n.Validate(config("https://ntfy.example")); err != nil {
		t.Errorf("valid config: %v", err)
	}
	fields := n.Fields()
	if n.Type() != "ntfy" || len(fields) != 3 || fields[0].Name != "server" || !fields[1].Secret || !fields[2].Secret || fields[2].Required {
		t.Errorf("fields %+v", fields)
	}
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}
