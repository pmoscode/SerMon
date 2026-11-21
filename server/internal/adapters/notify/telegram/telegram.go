// Package telegram sends notifications through a Telegram bot (KONZEPT §12.4, M2-13) with
// sendMessage in HTML mode: the title is bold, values from the hosts are HTML-escaped and their
// mentions defused. The bot token is a secret; the chat is a setting.
package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/pmoscode/SerMon/server/internal/adapters/notify/httppost"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
)

const (
	api = "https://api.telegram.org"
	// maxText is the limit of sendMessage in UTF-16 units of the text without markup.
	maxText  = 4096
	maxTitle = 256
	more     = "…"
)

var (
	botToken = regexp.MustCompile(`^[0-9]{1,20}:[A-Za-z0-9_-]{30,100}$`)
	chatID   = regexp.MustCompile(`^(-?[0-9]{1,20}|@[A-Za-z][A-Za-z0-9_]{4,31})$`)
)

// Notifier is the Telegram channel type.
type Notifier struct {
	client *safehttp.Client
	api    string
}

var _ notify.Notifier = (*Notifier)(nil)

// New returns the notifier; client is the SSRF-safe client of the server.
func New(client *safehttp.Client) *Notifier { return &Notifier{client: client, api: api} }

// Type implements notify.Notifier.
func (*Notifier) Type() notify.ChannelType { return "telegram" }

// Fields implements notify.Notifier.
func (*Notifier) Fields() []notify.Field {
	return []notify.Field{
		{Name: "token", Kind: notify.FieldText, Secret: true, Required: true, MaxLen: 128},
		{Name: "chat", Kind: notify.FieldText, Required: true, MaxLen: 64},
		{Name: "thread", Kind: notify.FieldNumber},
	}
}

// Validate implements notify.Notifier.
func (*Notifier) Validate(c notify.Config) error {
	if !botToken.MatchString(c.Secrets["token"]) {
		return errors.New("token must be a bot token from @BotFather (<digits>:<key>)")
	}
	if !chatID.MatchString(c.Settings["chat"]) {
		return errors.New("chat must be a numeric chat ID or a public @channelname")
	}
	return nil
}

type linkPreview struct {
	IsDisabled bool `json:"is_disabled"`
}

type message struct {
	ChatID              string      `json:"chat_id"`
	Thread              int         `json:"message_thread_id,omitempty"`
	Text                string      `json:"text"`
	ParseMode           string      `json:"parse_mode"`
	DisableNotification bool        `json:"disable_notification"`
	LinkPreview         linkPreview `json:"link_preview_options"`
}

// safe is a value as Telegram shows it: mentions defused. escape adds the HTML escaping.
func safe(s string) string   { return notify.DefuseMentions(s) }
func escape(s string) string { return notify.EscapeHTML(s) }

func visible(s string) int { return len(utf16.Encode([]rune(s))) }

func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + more
}

// text is the message: the bold title, the lines while they fit, and the link to the node as
// plain text (an inline button would reject the link of a UI on localhost).
func text(m notify.Message) string {
	title, link := safe(cut(m.Title, maxTitle)), safe(m.Link)
	budget := maxText - visible(title) - visible("\n\n") - visible("\n\n"+link)
	var lines []string
	used := 0
	for _, l := range m.Lines {
		line := safe(l.Label + ": " + l.Value)
		if used+visible("\n"+line) > budget-visible("\n"+more) {
			lines = append(lines, more)
			break
		}
		lines, used = append(lines, line), used+visible("\n"+line)
	}
	var b strings.Builder
	b.WriteString("<b>" + escape(title) + "</b>\n")
	for _, l := range lines {
		b.WriteString("\n" + escape(l))
	}
	b.WriteString("\n\n" + escape(link))
	return b.String()
}

// quiet kinds arrive without a sound (like ntfy's low priority).
func quiet(k notify.Kind) bool { return k == notify.Acknowledged || k == notify.FlappingStopped }

// retryAfter reads parameters.retry_after of an error answer.
func retryAfter(answer []byte) string {
	var a struct {
		Parameters struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if json.Unmarshal(answer, &a) != nil || a.Parameters.RetryAfter <= 0 {
		return ""
	}
	return strconv.Itoa(a.Parameters.RetryAfter)
}

// Send implements notify.Notifier.
func (n *Notifier) Send(ctx context.Context, c notify.Config, m notify.Message) error {
	msg := message{
		ChatID: c.Settings["chat"], Text: text(m), ParseMode: "HTML",
		DisableNotification: quiet(m.Kind), LinkPreview: linkPreview{IsDisabled: true},
	}
	if t := c.Settings["thread"]; t != "" {
		thread, err := strconv.Atoi(t)
		if err != nil {
			return notify.Permanent(errors.New("thread is not a number"))
		}
		msg.Thread = thread
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return notify.Permanent(fmt.Errorf("encode message: %w", err))
	}
	// The token is part of the path; safehttp's errors name only method and host.
	url := n.api + "/bot" + c.Secrets["token"] + "/sendMessage"
	return httppost.PostWith(ctx, n.client, url, http.Header{"Content-Type": {"application/json"}}, body, //nolint:wrapcheck // classified for notify
		httppost.Options{RetryAfter: retryAfter})
}
