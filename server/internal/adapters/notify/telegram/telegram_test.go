package telegram_test

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/pmoscode/SerMon/server/internal/adapters/notify/notifytest"
	"github.com/pmoscode/SerMon/server/internal/adapters/notify/telegram"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
)

const token = "123456789:AAFixtureFixtureFixtureFixture0123"

func config(chat string) notify.Config {
	return notify.Config{Settings: map[string]string{"chat": chat}, Secrets: map[string]string{"token": token}}
}

type sent struct {
	ChatID              string `json:"chat_id"`
	Thread              int    `json:"message_thread_id"`
	Text                string `json:"text"`
	ParseMode           string `json:"parse_mode"`
	DisableNotification bool   `json:"disable_notification"`
	LinkPreview         struct {
		IsDisabled bool `json:"is_disabled"`
	} `json:"link_preview_options"`
}

func decode(t *testing.T, body []byte) sent {
	t.Helper()
	var p sent
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTelegram_SendsHTMLWithEscapedValues(t *testing.T) {
	srv := notifytest.NewServer(t)
	srv.Answer = `{"ok":true,"result":{}}`
	n := telegram.NewWithAPI(notifytest.LocalClient(t), srv.URL)
	if err := n.Send(context.Background(), config("-1001234567890"), notifytest.Security(notify.German)); err != nil {
		t.Fatal(err)
	}
	body, header := srv.Last(t)
	if path := srv.LastPath(t); path != "/bot"+token+"/sendMessage" || header.Get("Content-Type") != "application/json" {
		t.Errorf("path %q, header %v", path, header)
	}
	notifytest.Golden(t, "security.de.json", notifytest.Indent(t, body))
	p := decode(t, body)
	if p.ChatID != "-1001234567890" || p.ParseMode != "HTML" || !p.LinkPreview.IsDisabled || p.DisableNotification || p.Thread != 0 {
		t.Errorf("payload %+v", p)
	}
	// The hostile user name stays text: no tags, no mention, the link at the end is the node's.
	for _, bad := range []string{"<script>", "<!here>", "@everyone"} {
		if strings.Contains(p.Text, bad) {
			t.Errorf("text contains %q:\n%s", bad, p.Text)
		}
	}
	if !strings.Contains(p.Text, "&lt;script&gt;") || !strings.HasSuffix(p.Text, "https://sermon.example/nodes/0193f5a0-0000-7000-8000-000000000001") {
		t.Errorf("text:\n%s", p.Text)
	}
	if err := n.Send(context.Background(), config("@sermon_ops"), notifytest.Resolved(notify.English)); err != nil {
		t.Fatal(err)
	}
	body, _ = srv.Last(t)
	notifytest.Golden(t, "resolved.en.json", notifytest.Indent(t, body))
}

func TestTelegram_QuietKindsAndTopics(t *testing.T) {
	srv := notifytest.NewServer(t)
	n := telegram.NewWithAPI(notifytest.LocalClient(t), srv.URL)
	c := config("-100123")
	c.Settings["thread"] = "42"
	for kind, quiet := range map[notify.Kind]bool{
		notify.Firing: false, notify.Resolved: false, notify.FlappingStarted: false,
		notify.Acknowledged: true, notify.FlappingStopped: true,
	} {
		if err := n.Send(context.Background(), c, notifytest.Message(kind, notify.Warning, notify.English)); err != nil {
			t.Fatal(err)
		}
		body, _ := srv.Last(t)
		if p := decode(t, body); p.DisableNotification != quiet || p.Thread != 42 {
			t.Errorf("%s: quiet %v, thread %d", kind, p.DisableNotification, p.Thread)
		}
	}
}

// Telegram allows 4096 characters (UTF-16 units of the text without markup).
func TestTelegram_LongMessagesFit(t *testing.T) {
	srv := notifytest.NewServer(t)
	if err := telegram.NewWithAPI(notifytest.LocalClient(t), srv.URL).Send(context.Background(), config("1"), notifytest.Long(notify.English)); err != nil {
		t.Fatal(err)
	}
	body, _ := srv.Last(t)
	text := decode(t, body).Text
	visible := html.UnescapeString(strings.NewReplacer("<b>", "", "</b>", "").Replace(text))
	if n := len(utf16.Encode([]rune(visible))); n > 4096 || !strings.Contains(visible, "…") || !strings.HasSuffix(visible, "/nodes/0193f5a0-0000-7000-8000-000000000001") {
		t.Errorf("%d UTF-16 units:\n%s", n, visible)
	}
	if strings.Count(text, "<b>") != 1 || strings.Count(text, "</b>") != 1 {
		t.Errorf("markup broken:\n%s", text)
	}
	// The title is cut to 256 characters, so it never eats the room of the values.
	title, _, _ := strings.Cut(visible, "\n")
	if n := len([]rune(title)); n > 256 || !strings.HasSuffix(title, "…") {
		t.Errorf("title of %d characters", n)
	}
}

func TestTelegram_ValidatesTokenAndChat(t *testing.T) {
	n := telegram.New(notifytest.LocalClient(t))
	for tok, ok := range map[string]bool{
		token:                                 true,
		"123456789:short":                     false,
		"abc:AAFixtureFixtureFixtureFixture0": false,
		"123456789AAFixtureFixtureFixture012": false,
		"123/456:AAFixtureFixtureFixture0123": false,
	} {
		c := config("1")
		c.Secrets["token"] = tok
		if err := n.Validate(c); (err == nil) != ok {
			t.Errorf("token %q: %v", tok, err)
		}
	}
	for chat, ok := range map[string]bool{
		"12345": true, "-1001234567890": true, "@sermon_ops": true,
		"@abc": false, "@1abcde": false, "12a": false, "-": false, "@with space": false,
	} {
		if err := n.Validate(config(chat)); (err == nil) != ok {
			t.Errorf("chat %q: %v", chat, err)
		}
	}
	f := n.Fields()
	if n.Type() != "telegram" || len(f) != 3 || f[0].Name != "token" || !f[0].Secret || f[1].Name != "chat" || f[1].Secret || f[2].Kind != notify.FieldNumber || f[2].Required {
		t.Errorf("fields %+v", f)
	}
}

func TestTelegram_ClassifiesErrors(t *testing.T) {
	srv := notifytest.NewServer(t)
	n := telegram.NewWithAPI(notifytest.LocalClient(t), srv.URL)
	srv.Status = http.StatusTooManyRequests
	srv.Answer = `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 17","parameters":{"retry_after":17}}`
	var rl *notify.RateLimitedError
	if err := n.Send(context.Background(), config("1"), notifytest.Resolved(notify.English)); !errors.As(err, &rl) || rl.RetryAfter != 17*time.Second {
		t.Errorf("HTTP 429: %v", err)
	}
	srv.Status = http.StatusBadRequest
	srv.Answer = `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`
	if err := n.Send(context.Background(), config("1"), notifytest.Resolved(notify.English)); !notify.IsPermanent(err) || !strings.Contains(err.Error(), "chat not found") {
		t.Errorf("HTTP 400: %v", err)
	}
	if err := telegram.NewWithAPI(safehttp.New(safehttp.Options{}), srv.URL).Send(context.Background(), config("1"), notifytest.Resolved(notify.English)); !errors.Is(err, safehttp.ErrBlocked) {
		t.Errorf("internal target: %v", err)
	}
}

// The token is part of the path: errors of the transport must not quote it.
func TestTelegram_ErrorsDoNotQuoteTheToken(t *testing.T) {
	srv := notifytest.NewServer(t)
	srv.Close()
	err := telegram.NewWithAPI(notifytest.LocalClient(t), srv.URL).Send(context.Background(), config("1"), notifytest.Resolved(notify.English))
	if err == nil || strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "/bot") {
		t.Errorf("error %v", err)
	}
}
