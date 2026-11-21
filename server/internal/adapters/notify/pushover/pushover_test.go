package pushover_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pmoscode/SerMon/server/internal/adapters/notify/notifytest"
	"github.com/pmoscode/SerMon/server/internal/adapters/notify/pushover"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
)

const (
	appToken = "aFixture0123456789abcdefghijkl"
	userKey  = "uFixture0123456789abcdefghijkl"
)

func config() notify.Config {
	return notify.Config{Settings: map[string]string{}, Secrets: map[string]string{"token": appToken, "user": userKey}}
}

// form decodes a request and returns it as indented JSON for golden files.
func form(t *testing.T, body []byte) (url.Values, string) {
	t.Helper()
	v, err := url.ParseQuery(string(body))
	if err != nil {
		t.Fatal(err)
	}
	flat := map[string]string{}
	for k := range v {
		flat[k] = v.Get(k)
	}
	out, err := json.MarshalIndent(flat, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return v, string(out) + "\n"
}

func TestPushover_PostsAFormWithPlainText(t *testing.T) {
	srv := notifytest.NewServer(t)
	srv.Answer = `{"status":1,"request":"0193f5a0"}`
	n := pushover.NewWithAPI(notifytest.LocalClient(t), srv.URL)
	if err := n.Send(context.Background(), config(), notifytest.Security(notify.German)); err != nil {
		t.Fatal(err)
	}
	body, header := srv.Last(t)
	if path := srv.LastPath(t); path != "/1/messages.json" || header.Get("Content-Type") != "application/x-www-form-urlencoded" {
		t.Errorf("path %q, header %v", path, header)
	}
	v, golden := form(t, body)
	notifytest.Golden(t, "security.de.json", golden)
	// Plain text (no html=1): the hostile user name is shown as it is.
	if v.Get("token") != appToken || v.Get("user") != userKey || v.Has("html") || v.Has("device") || v.Get("priority") != "1" ||
		v.Get("timestamp") != "1795255200" || !strings.Contains(v.Get("message"), notifytest.Hostile) ||
		v.Get("url") != "https://sermon.example/nodes/0193f5a0-0000-7000-8000-000000000001" || v.Get("url_title") != "In SerMon öffnen" {
		t.Errorf("form %v", v)
	}
	c := config()
	c.Settings["device"] = "phone,tablet"
	if err := n.Send(context.Background(), c, notifytest.Resolved(notify.English)); err != nil {
		t.Fatal(err)
	}
	body, _ = srv.Last(t)
	v, golden = form(t, body)
	notifytest.Golden(t, "resolved.en.json", golden)
	if v.Get("device") != "phone,tablet" || v.Get("priority") != "0" {
		t.Errorf("form %v", v)
	}
}

func TestPushover_PriorityFromKindAndSeverity(t *testing.T) {
	srv := notifytest.NewServer(t)
	n := pushover.NewWithAPI(notifytest.LocalClient(t), srv.URL)
	for _, tc := range []struct {
		kind     notify.Kind
		severity notify.Severity
		priority string
	}{
		{notify.Firing, notify.Critical, "1"},
		{notify.Firing, notify.Warning, "0"},
		{notify.FlappingStarted, notify.Warning, "0"},
		{notify.Resolved, notify.Critical, "0"},
		{notify.Acknowledged, notify.Critical, "-1"},
		{notify.FlappingStopped, notify.Warning, "-1"},
	} {
		if err := n.Send(context.Background(), config(), notifytest.Message(tc.kind, tc.severity, notify.English)); err != nil {
			t.Fatal(err)
		}
		body, _ := srv.Last(t)
		if v, _ := form(t, body); v.Get("priority") != tc.priority {
			t.Errorf("%s %s: priority %s, want %s", tc.kind, tc.severity, v.Get("priority"), tc.priority)
		}
	}
}

// Pushover takes a title of 250 and a message of 1024 characters.
func TestPushover_LongMessagesFit(t *testing.T) {
	srv := notifytest.NewServer(t)
	if err := pushover.NewWithAPI(notifytest.LocalClient(t), srv.URL).Send(context.Background(), config(), notifytest.Long(notify.English)); err != nil {
		t.Fatal(err)
	}
	body, _ := srv.Last(t)
	v, _ := form(t, body)
	if n := utf8.RuneCountInString(v.Get("title")); n > 250 || !strings.HasSuffix(v.Get("title"), "…") {
		t.Errorf("title of %d characters", n)
	}
	if n := utf8.RuneCountInString(v.Get("message")); n > 1024 || !strings.HasSuffix(v.Get("message"), "…") || !strings.HasPrefix(v.Get("message"), "Rule: ") {
		t.Errorf("message of %d characters:\n%s", n, v.Get("message"))
	}
}

func TestPushover_Validates(t *testing.T) {
	n := pushover.New(notifytest.LocalClient(t))
	for _, tc := range []struct {
		token, user, device string
		ok                  bool
	}{
		{appToken, userKey, "", true},
		{appToken, userKey, "phone", true},
		{appToken, userKey, "phone,tablet_2,my-pc", true},
		{"short", userKey, "", false},
		{appToken, userKey + "x", "", false},
		{appToken, "uFixture0123456789abcdefghij-l", "", false},
		{appToken, userKey, "phone,", false},
		{appToken, userKey, "phone tablet", false},
		{appToken, userKey, "abcdefghijklmnopqrstuvwxyz", false},
	} {
		c := config()
		c.Secrets["token"], c.Secrets["user"] = tc.token, tc.user
		if tc.device != "" {
			c.Settings["device"] = tc.device
		}
		if err := n.Validate(c); (err == nil) != tc.ok {
			t.Errorf("%+v: %v", tc, err)
		}
	}
	f := n.Fields()
	if n.Type() != "pushover" || len(f) != 3 || !f[0].Secret || !f[1].Secret || f[2].Name != "device" || f[2].Secret || f[2].Required {
		t.Errorf("fields %+v", f)
	}
}

func TestPushover_ClassifiesErrors(t *testing.T) {
	srv := notifytest.NewServer(t)
	n := pushover.NewWithAPI(notifytest.LocalClient(t), srv.URL)
	srv.Status = http.StatusBadRequest
	srv.Answer = `{"user":"invalid","errors":["user identifier is not a valid user, group, or subscribed user key"],"status":0,"request":"0193f5a0"}`
	if err := n.Send(context.Background(), config(), notifytest.Resolved(notify.English)); !notify.IsPermanent(err) || !strings.Contains(err.Error(), "not a valid user") {
		t.Errorf("HTTP 400: %v", err)
	}
	srv.Status = http.StatusTooManyRequests
	var rl *notify.RateLimitedError
	if err := n.Send(context.Background(), config(), notifytest.Resolved(notify.English)); !errors.As(err, &rl) {
		t.Errorf("HTTP 429: %v", err)
	}
	if err := pushover.NewWithAPI(safehttp.New(safehttp.Options{}), srv.URL).Send(context.Background(), config(), notifytest.Resolved(notify.English)); !errors.Is(err, safehttp.ErrBlocked) {
		t.Errorf("internal target: %v", err)
	}
}
