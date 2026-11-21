package discord_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/pmoscode/SerMon/server/internal/adapters/notify/discord"
	"github.com/pmoscode/SerMon/server/internal/adapters/notify/notifytest"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
)

func config(url string) notify.Config { return notify.Config{Secrets: map[string]string{"url": url}} }

type sent struct {
	AllowedMentions struct {
		Parse []string `json:"parse"`
	} `json:"allowed_mentions"`
	Embeds []struct {
		Title  string `json:"title"`
		URL    string `json:"url"`
		Color  int    `json:"color"`
		Fields []struct {
			Name, Value string
		} `json:"fields"`
	} `json:"embeds"`
}

func TestDiscord_PostsAnEmbedWithoutMentions(t *testing.T) {
	srv := notifytest.NewServer(t)
	srv.Status = http.StatusNoContent
	n := discord.New(notifytest.LocalClient(t))
	if err := n.Send(context.Background(), config(srv.URL), notifytest.Security(notify.German)); err != nil {
		t.Fatal(err)
	}
	body, _ := srv.Last(t)
	notifytest.Golden(t, "security.de.json", notifytest.Indent(t, body))
	var p sent
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	// Discord pings nobody (allowed_mentions), and the value renders as text, not as a link.
	if p.AllowedMentions.Parse == nil || len(p.AllowedMentions.Parse) != 0 || len(p.Embeds) != 1 || p.Embeds[0].Color != 0xCF222E ||
		p.Embeds[0].URL != "https://sermon.example/nodes/0193f5a0-0000-7000-8000-000000000001" {
		t.Errorf("payload %+v", p)
	}
	for _, f := range p.Embeds[0].Fields {
		if f.Name == "Benutzer" && (strings.Contains(f.Value, "[click](") || strings.Contains(f.Value, "@everyone") || !strings.Contains(f.Value, `\[click\]`)) {
			t.Errorf("user value %q not escaped", f.Value)
		}
	}
	if err := n.Send(context.Background(), config(srv.URL), notifytest.Resolved(notify.English)); err != nil {
		t.Fatal(err)
	}
	body, _ = srv.Last(t)
	notifytest.Golden(t, "resolved.en.json", notifytest.Indent(t, body))
}

func TestDiscord_ValidatesTheWebhookAndAnswers(t *testing.T) {
	n := discord.New(notifytest.LocalClient(t))
	for url, ok := range map[string]bool{
		"https://discord.com/api/webhooks/1/abc":        true,
		"https://discordapp.com/api/webhooks/1/abc":     true,
		"https://canary.discord.com/api/webhooks/1/abc": true,
		"https://discord.com.evil.example/api/webhooks": false,
		"https://discord.com/channels/1":                false,
		"http://discord.com/api/webhooks/1/abc":         false,
	} {
		if err := n.Validate(config(url)); (err == nil) != ok {
			t.Errorf("%s: %v", url, err)
		}
	}
	if f := n.Fields(); n.Type() != "discord" || len(f) != 1 || !f[0].Secret {
		t.Errorf("fields %+v", f)
	}
	srv := notifytest.NewServer(t)
	srv.Status = http.StatusTooManyRequests
	var rl *notify.RateLimitedError
	if err := n.Send(context.Background(), config(srv.URL), notifytest.Resolved(notify.English)); !errors.As(err, &rl) {
		t.Errorf("HTTP 429: %v", err)
	}
	if err := discord.New(safehttp.New(safehttp.Options{})).Send(context.Background(), config(srv.URL), notifytest.Resolved(notify.English)); !errors.Is(err, safehttp.ErrBlocked) {
		t.Errorf("internal target: %v", err)
	}
}

// Escaping can grow a value five-fold (every @ gets an escape and a zero-width space); the
// field still fits Discord's limit.
func TestDiscord_LongValuesFit(t *testing.T) {
	srv := notifytest.NewServer(t)
	m := notifytest.Security(notify.English)
	for i := range m.Lines {
		if m.Lines[i].Label == "User" {
			m.Lines[i].Value = strings.Repeat("@", 256)
		}
	}
	if err := discord.New(notifytest.LocalClient(t)).Send(context.Background(), config(srv.URL), m); err != nil {
		t.Fatal(err)
	}
	body, _ := srv.Last(t)
	var p sent
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	for _, f := range p.Embeds[0].Fields {
		if len(f.Value) > 1024 || f.Value == "" {
			t.Errorf("field %s has %d bytes", f.Name, len(f.Value))
		}
	}
}
