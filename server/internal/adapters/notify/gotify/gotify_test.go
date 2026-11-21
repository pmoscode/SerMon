package gotify_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/pmoscode/SerMon/server/internal/adapters/notify/gotify"
	"github.com/pmoscode/SerMon/server/internal/adapters/notify/notifytest"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
)

const token = "AFixture0123.-_"

func config(server string) notify.Config {
	return notify.Config{Settings: map[string]string{"server": server}, Secrets: map[string]string{"token": token}}
}

type sent struct {
	Title    string `json:"title"`
	Message  string `json:"message"`
	Priority int    `json:"priority"`
	Extras   struct {
		Display struct {
			ContentType string `json:"contentType"`
		} `json:"client::display"`
		Notification struct {
			Click struct {
				URL string `json:"url"`
			} `json:"click"`
		} `json:"client::notification"`
	} `json:"extras"`
}

func decode(t *testing.T, body []byte) sent {
	t.Helper()
	var p sent
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGotify_PostsPlainTextWithTheAppToken(t *testing.T) {
	srv := notifytest.NewServer(t)
	n := gotify.New(notifytest.LocalClient(t))
	// A server under a sub-path, with or without the slash.
	if err := n.Send(context.Background(), config(srv.URL+"/gotify/"), notifytest.Security(notify.German)); err != nil {
		t.Fatal(err)
	}
	body, header := srv.Last(t)
	if path := srv.LastPath(t); path != "/gotify/message" || header.Get("X-Gotify-Key") != token || header.Get("Content-Type") != "application/json" {
		t.Errorf("path %q, header %v", path, header)
	}
	notifytest.Golden(t, "security.de.json", notifytest.Indent(t, body))
	p := decode(t, body)
	// Plain text: the client shows the hostile user name as it is.
	if p.Priority != 8 || p.Extras.Display.ContentType != "text/plain" || !strings.Contains(p.Message, notifytest.Hostile) ||
		p.Extras.Notification.Click.URL != "https://sermon.example/nodes/0193f5a0-0000-7000-8000-000000000001" {
		t.Errorf("payload %+v", p)
	}
	if err := n.Send(context.Background(), config(srv.URL+"/gotify"), notify.Render(notify.TestNotification(notifytest.T0), notify.English, "https://sermon.example")); err != nil {
		t.Fatal(err)
	}
	body, _ = srv.Last(t)
	if path := srv.LastPath(t); path != "/gotify/message" {
		t.Errorf("path %q", path)
	}
	notifytest.Golden(t, "test.en.json", notifytest.Indent(t, body))
}

func TestGotify_PriorityFromKindAndSeverity(t *testing.T) {
	srv := notifytest.NewServer(t)
	n := gotify.New(notifytest.LocalClient(t))
	for _, tc := range []struct {
		kind     notify.Kind
		severity notify.Severity
		priority int
	}{
		{notify.Firing, notify.Critical, 8},
		{notify.Firing, notify.Warning, 5},
		{notify.FlappingStarted, notify.Warning, 5},
		{notify.Resolved, notify.Critical, 3},
		{notify.Acknowledged, notify.Critical, 2},
		{notify.FlappingStopped, notify.Warning, 2},
	} {
		if err := n.Send(context.Background(), config(srv.URL), notifytest.Message(tc.kind, tc.severity, notify.English)); err != nil {
			t.Fatal(err)
		}
		body, _ := srv.Last(t)
		if p := decode(t, body); p.Priority != tc.priority {
			t.Errorf("%s %s: priority %d, want %d", tc.kind, tc.severity, p.Priority, tc.priority)
		}
	}
}

func TestGotify_ValidatesAndClassifies(t *testing.T) {
	n := gotify.New(notifytest.LocalClient(t))
	for tok, ok := range map[string]bool{token: true, "AbCdEfGhIjKlMnO": true, "short": false, "with space0123": false, "tok/en0123456": false} {
		c := config("https://gotify.example")
		c.Secrets["token"] = tok
		if err := n.Validate(c); (err == nil) != ok {
			t.Errorf("token %q: %v", tok, err)
		}
	}
	if f := n.Fields(); n.Type() != "gotify" || len(f) != 2 || f[0].Name != "server" || f[0].Secret || f[0].Kind != notify.FieldURL || !f[1].Secret {
		t.Errorf("fields %+v", f)
	}
	srv := notifytest.NewServer(t)
	srv.Status = http.StatusUnauthorized
	srv.Answer = `{"error":"Unauthorized","errorCode":401,"errorDescription":"you need to provide a valid access token or user credentials to access this api"}`
	if err := n.Send(context.Background(), config(srv.URL), notifytest.Resolved(notify.English)); !notify.IsPermanent(err) || !strings.Contains(err.Error(), "valid access token") {
		t.Errorf("HTTP 401: %v", err)
	}
	if err := gotify.New(safehttp.New(safehttp.Options{})).Send(context.Background(), config(srv.URL), notifytest.Resolved(notify.English)); !errors.Is(err, safehttp.ErrBlocked) {
		t.Errorf("internal target: %v", err)
	}
}
