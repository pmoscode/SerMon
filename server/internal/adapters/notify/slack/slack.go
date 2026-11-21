// Package slack posts notifications to a Slack incoming webhook as Block Kit (KONZEPT §12.4,
// M2-12). Values from the hosts go only into plain_text objects, which Slack shows literally:
// no mentions, links or formatting. The webhook URL carries its token and is a secret.
package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/pmoscode/SerMon/server/internal/adapters/notify/httppost"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
)

// Limits of Block Kit.
const (
	maxHeader = 150
	maxField  = 2000
	maxFields = 10
)

// Notifier is the Slack channel type.
type Notifier struct{ client *safehttp.Client }

var _ notify.Notifier = (*Notifier)(nil)

// New returns the notifier; client is the SSRF-safe client of the server.
func New(client *safehttp.Client) *Notifier { return &Notifier{client: client} }

// Type implements notify.Notifier.
func (*Notifier) Type() notify.ChannelType { return "slack" }

// Fields implements notify.Notifier.
func (*Notifier) Fields() []notify.Field {
	return []notify.Field{{Name: "url", Kind: notify.FieldURL, Secret: true, Required: true}}
}

// Validate implements notify.Notifier: Slack's webhooks live on hooks.slack.com.
func (*Notifier) Validate(c notify.Config) error {
	if !strings.HasPrefix(c.Secrets["url"], "https://hooks.slack.com/") {
		return errors.New("url must be a Slack webhook (https://hooks.slack.com/…)")
	}
	return nil
}

type text struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Emoji *bool  `json:"emoji,omitempty"`
}

type block struct {
	Type     string    `json:"type"`
	Text     *text     `json:"text,omitempty"`
	Fields   []text    `json:"fields,omitempty"`
	Elements []element `json:"elements,omitempty"`
}

type element struct {
	Type string `json:"type"`
	Text text   `json:"text"`
	URL  string `json:"url"`
}

type attachment struct {
	Color  string  `json:"color"`
	Blocks []block `json:"blocks"`
}

type payload struct {
	Text        string       `json:"text"` // fallback (mrkdwn): notifications, screen readers
	Attachments []attachment `json:"attachments"`
}

var colors = map[notify.Tone]string{
	notify.ToneCritical: "#cf222e", notify.ToneWarning: "#d4a72c", notify.ToneGood: "#1a7f37", notify.ToneNeutral: "#8c959f",
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func plain(s string, n int) text {
	no := false
	return text{Type: "plain_text", Text: truncate(s, n), Emoji: &no}
}

func payloadOf(m notify.Message) payload {
	blocks := []block{{Type: "header", Text: ptr(plain(m.Title, maxHeader))}}
	var fields []text
	for _, l := range m.Lines {
		fields = append(fields, plain(l.Label+": "+l.Value, maxField))
	}
	for chunk := range chunks(fields, maxFields) {
		blocks = append(blocks, block{Type: "section", Fields: chunk})
	}
	if m.Link != "" {
		blocks = append(blocks, block{Type: "actions", Elements: []element{{Type: "button", Text: plain(notify.OpenInSerMon(m.Lang), 75), URL: m.Link}}})
	}
	return payload{Text: notify.EscapeSlack(m.Title), Attachments: []attachment{{Color: colors[m.Tone()], Blocks: blocks}}}
}

func ptr[T any](v T) *T { return &v }

func chunks[T any](s []T, n int) func(func([]T) bool) {
	return func(yield func([]T) bool) {
		for len(s) > 0 {
			k := min(n, len(s))
			if !yield(s[:k]) {
				return
			}
			s = s[k:]
		}
	}
}

// Send implements notify.Notifier.
func (n *Notifier) Send(ctx context.Context, c notify.Config, m notify.Message) error {
	body, err := json.Marshal(payloadOf(m))
	if err != nil {
		return notify.Permanent(fmt.Errorf("encode message: %w", err))
	}
	return httppost.Post(ctx, n.client, c.Secrets["url"], http.Header{"Content-Type": {"application/json"}}, body) //nolint:wrapcheck // classified for notify
}
