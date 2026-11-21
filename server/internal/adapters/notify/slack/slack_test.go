package slack_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/pmoscode/SerMon/server/internal/adapters/notify/notifytest"
	"github.com/pmoscode/SerMon/server/internal/adapters/notify/slack"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
)

func config(url string) notify.Config {
	return notify.Config{Secrets: map[string]string{"url": url}}
}

func TestSlack_PostsBlocks(t *testing.T) {
	srv := notifytest.NewServer(t)
	srv.Answer = "ok"
	n := slack.New(notifytest.LocalClient(t))
	if err := n.Send(context.Background(), config(srv.URL+"/services/T0/B0/x"), notifytest.Security(notify.German)); err != nil {
		t.Fatal(err)
	}
	body, header := srv.Last(t)
	if header.Get("Content-Type") != "application/json" {
		t.Errorf("content type %q", header.Get("Content-Type"))
	}
	notifytest.Golden(t, "security.de.json", notifytest.Indent(t, body))
	if err := n.Send(context.Background(), config(srv.URL), notifytest.Resolved(notify.English)); err != nil {
		t.Fatal(err)
	}
	body, _ = srv.Last(t)
	notifytest.Golden(t, "resolved.en.json", notifytest.Indent(t, body))
}

// Values from the hosts go only into plain_text (no mentions, links or formatting); the
// mrkdwn fallback is escaped.
func TestSlack_KeepsHostileValuesInert(t *testing.T) {
	srv := notifytest.NewServer(t)
	m := notifytest.Security(notify.English)
	m.Title += " <!channel>" // the title carries the host name
	if err := slack.New(notifytest.LocalClient(t)).Send(context.Background(), config(srv.URL), m); err != nil {
		t.Fatal(err)
	}
	body, _ := srv.Last(t)
	var p struct {
		Text        string `json:"text"`
		Attachments []struct {
			Blocks []json.RawMessage `json:"blocks"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(p.Text, "<>") || !strings.Contains(p.Text, "&lt;!channel&gt;") {
		t.Errorf("fallback text %q not escaped", p.Text)
	}
	var found bool
	walk(t, body, func(kind, text string) {
		if strings.Contains(text, notifytest.Hostile) {
			found = true
			if kind != "plain_text" {
				t.Errorf("the hostile value is in a %s text: %q", kind, text)
			}
		}
	})
	if !found {
		t.Error("the user name is missing")
	}
}

// walk calls f with the type and text of every text object.
func walk(t *testing.T, body []byte, f func(kind, text string)) {
	t.Helper()
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	var visit func(any)
	visit = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if kind, ok := x["type"].(string); ok {
				if text, ok := x["text"].(string); ok {
					f(kind, text)
				}
			}
			for _, child := range x {
				visit(child)
			}
		case []any:
			for _, child := range x {
				visit(child)
			}
		}
	}
	visit(v)
}

func TestSlack_ValidatesTheWebhookAndAnswers(t *testing.T) {
	n := slack.New(notifytest.LocalClient(t))
	for url, ok := range map[string]bool{
		"https://hooks.slack.com/services/T0/B0/xyz": true,
		"https://hooks.slack.com.evil.example/x":     false,
		"https://evil.example/hooks.slack.com/":      false,
		"http://hooks.slack.com/services/T0/B0/x":    false,
	} {
		if err := n.Validate(config(url)); (err == nil) != ok {
			t.Errorf("%s: %v", url, err)
		}
	}
	if f := n.Fields(); n.Type() != "slack" || len(f) != 1 || !f[0].Secret || f[0].Kind != notify.FieldURL {
		t.Errorf("fields %+v", f)
	}
	srv := notifytest.NewServer(t)
	srv.Status, srv.Answer = http.StatusNotFound, "no_service"
	err := n.Send(context.Background(), config(srv.URL), notifytest.Resolved(notify.English))
	if !notify.IsPermanent(err) || !strings.Contains(err.Error(), "no_service") {
		t.Errorf("HTTP 404: %v", err)
	}
	if err := slack.New(safehttp.New(safehttp.Options{})).Send(context.Background(), config(srv.URL), notifytest.Resolved(notify.English)); !errors.Is(err, safehttp.ErrBlocked) {
		t.Errorf("internal target: %v", err)
	}
}
