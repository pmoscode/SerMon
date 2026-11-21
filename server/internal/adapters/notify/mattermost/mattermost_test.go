package mattermost_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/pmoscode/SerMon/server/internal/adapters/notify/mattermost"
	"github.com/pmoscode/SerMon/server/internal/adapters/notify/notifytest"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
)

func config(url string) notify.Config { return notify.Config{Secrets: map[string]string{"url": url}} }

func TestMattermost_PostsAnAttachment(t *testing.T) {
	srv := notifytest.NewServer(t)
	srv.Answer = "ok"
	n := mattermost.New(notifytest.LocalClient(t))
	if err := n.Send(context.Background(), config(srv.URL+"/hooks/xyz"), notifytest.Security(notify.German)); err != nil {
		t.Fatal(err)
	}
	body, _ := srv.Last(t)
	notifytest.Golden(t, "security.de.json", notifytest.Indent(t, body))
	var p struct {
		Text        string `json:"text"`
		Attachments []struct {
			Color, Title, TitleLink string
			Fields                  []struct{ Title, Value string }
		} `json:"attachments"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	// Mattermost renders Markdown and notifies on @all/@channel/@here: both are defused.
	for _, f := range p.Attachments[0].Fields {
		if f.Title == "Benutzer" && (strings.Contains(f.Value, "@everyone") || strings.Contains(f.Value, "[click](") || strings.Contains(f.Value, "<!here>")) {
			t.Errorf("user value %q not defused", f.Value)
		}
	}
	if p.Attachments[0].Color != "#cf222e" || strings.ContainsAny(p.Text, "<>") {
		t.Errorf("payload %+v", p)
	}
	if err := n.Send(context.Background(), config(srv.URL), notifytest.Resolved(notify.English)); err != nil {
		t.Fatal(err)
	}
	body, _ = srv.Last(t)
	notifytest.Golden(t, "resolved.en.json", notifytest.Indent(t, body))
}

func TestMattermost_Validates(t *testing.T) {
	n := mattermost.New(notifytest.LocalClient(t))
	if err := n.Validate(config("https://chat.example/hooks/xyz")); err != nil {
		t.Errorf("valid: %v", err)
	}
	if f := n.Fields(); n.Type() != "mattermost" || len(f) != 1 || !f[0].Secret {
		t.Errorf("fields %+v", f)
	}
	srv := notifytest.NewServer(t)
	if err := mattermost.New(safehttp.New(safehttp.Options{})).Send(context.Background(), config(srv.URL), notifytest.Resolved(notify.English)); !errors.Is(err, safehttp.ErrBlocked) {
		t.Errorf("internal target without allowlist: %v", err)
	}
}
