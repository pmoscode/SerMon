package webhook_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/notify/webhook"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
)

var update = flag.Bool("update", false, "rewrite the golden files")

var t0 = time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)

const secret = "whsec-fixture-0123456789abcdef" // fixture, not a secret

func local(t *testing.T) *safehttp.Client {
	t.Helper()
	p, err := safehttp.NewPolicy([]netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")})
	if err != nil {
		t.Fatal(err)
	}
	return safehttp.New(safehttp.Options{Policy: p})
}

func message(t *testing.T) notify.Message {
	t.Helper()
	n := notify.Notification{
		Kind: notify.Firing, Severity: notify.Warning, Rule: "security.ssh.bruteforce",
		RuleName: notify.Text{De: "SSH-Brute-Force", En: "SSH brute force"},
		Node:     identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001")), Hostname: "web-01",
		Dimension: "ip=192.0.2.7\x1f", Value: 12, Unit: notify.UnitCount, At: t0,
		Fields: alert.Fields{"user": "<script>\"root\"</script>", "ip": "192.0.2.7", "port": "50022"},
	}
	return notify.Render(n, notify.English, "https://sermon.example")
}

type received struct {
	mu      sync.Mutex
	header  http.Header
	body    []byte
	path    string
	answers []int
}

func (r *received) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.header, r.path = req.Header.Clone(), req.URL.Path
		r.body, _ = io.ReadAll(req.Body)
		code := http.StatusOK
		if len(r.answers) > 0 {
			code, r.answers = r.answers[0], r.answers[1:]
		}
		w.WriteHeader(code)
	}
}

func config(url string) notify.Config {
	return notify.Config{Settings: map[string]string{"url": url}, Secrets: map[string]string{"secret": secret}}
}

func TestWebhook_PostsSignedJSON(t *testing.T) {
	r := &received{}
	srv := httptest.NewServer(r.handler())
	defer srv.Close()
	n := webhook.New(local(t), func() time.Time { return t0.Add(3 * time.Second) })
	if err := n.Send(context.Background(), config(srv.URL+"/sermon"), message(t)); err != nil {
		t.Fatal(err)
	}
	if r.path != "/sermon" || r.header.Get("Content-Type") != "application/json" || r.header.Get("X-SerMon-Event") != "firing" ||
		!strings.HasPrefix(r.header.Get("User-Agent"), "SerMon") {
		t.Errorf("request %s, headers %v", r.path, r.header)
	}
	// The receiver checks the signature: HMAC-SHA256 over "<t>.<body>" with the shared secret.
	sig := r.header.Get("X-SerMon-Signature")
	parts := strings.Split(sig, ",")
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "t=") || !strings.HasPrefix(parts[1], "v1=") {
		t.Fatalf("signature header %q", sig)
	}
	ts := strings.TrimPrefix(parts[0], "t=")
	if want := strconv.FormatInt(t0.Add(3*time.Second).Unix(), 10); ts != want {
		t.Errorf("timestamp %s, want %s", ts, want)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(r.body)
	if got := strings.TrimPrefix(parts[1], "v1="); !hmac.Equal([]byte(got), []byte(hex.EncodeToString(mac.Sum(nil)))) {
		t.Errorf("signature %s does not verify", got)
	}
	golden(t, "firing.json", string(r.body))
}

func TestWebhook_TestMessage(t *testing.T) {
	r := &received{}
	srv := httptest.NewServer(r.handler())
	defer srv.Close()
	m := notify.Render(notify.TestNotification(t0), notify.German, "https://sermon.example")
	if err := webhook.New(local(t), time.Now).Send(context.Background(), config(srv.URL), m); err != nil {
		t.Fatal(err)
	}
	golden(t, "test.json", string(r.body))
}

func TestWebhook_AnswersAndTargets(t *testing.T) {
	r := &received{answers: []int{http.StatusInternalServerError, http.StatusGone}}
	srv := httptest.NewServer(r.handler())
	defer srv.Close()
	n := webhook.New(local(t), time.Now)
	if err := n.Send(context.Background(), config(srv.URL), message(t)); err == nil || notify.IsPermanent(err) {
		t.Errorf("HTTP 500: %v, want a retry", err)
	}
	if err := n.Send(context.Background(), config(srv.URL), message(t)); !notify.IsPermanent(err) {
		t.Errorf("HTTP 410: %v, want permanent", err)
	}
	// Without an allowlist the loopback target is refused.
	err := webhook.New(safehttp.New(safehttp.Options{}), time.Now).Send(context.Background(), config(srv.URL), message(t))
	if !errors.Is(err, safehttp.ErrBlocked) {
		t.Errorf("internal target: %v", err)
	}
}

func TestWebhook_Fields(t *testing.T) {
	n := webhook.New(local(t), time.Now)
	fields := n.Fields()
	if n.Type() != "webhook" || len(fields) != 2 || fields[0].Name != "url" || fields[0].Kind != notify.FieldURL || !fields[1].Secret || !fields[1].Required {
		t.Errorf("fields %+v", fields)
	}
	if err := n.Validate(notify.Config{Secrets: map[string]string{"secret": "short"}}); err == nil {
		t.Error("a short secret accepted")
	}
	if err := n.Validate(config("https://hooks.example/")); err != nil {
		t.Errorf("valid config: %v", err)
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

// A digest keeps its kind for the receiver and lists its items as data; the severity is the
// highest of them.
func TestWebhook_Digest(t *testing.T) {
	r := &received{}
	srv := httptest.NewServer(r.handler())
	defer srv.Close()
	alert := message(t).Source
	reminder := alert
	reminder.Severity, reminder.Reason, reminder.FiringSeconds = notify.Critical, notify.ReasonRepeat, 7200
	m := notify.Render(notify.Digest(t0, []notify.Notification{alert, reminder}, 3), notify.English, "https://sermon.example")
	if err := webhook.New(local(t), time.Now).Send(context.Background(), config(srv.URL), m); err != nil {
		t.Fatal(err)
	}
	if r.header.Get("X-SerMon-Event") != "digest" {
		t.Errorf("event %q", r.header.Get("X-SerMon-Event"))
	}
	golden(t, "digest.json", string(r.body))
	if strings.Contains(string(r.body), "<script>") || strings.Contains(string(r.body), `"fields"`) {
		t.Errorf("digest items carry fields:\n%s", r.body)
	}
}
