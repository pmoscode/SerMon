// Package discord posts notifications to a Discord webhook as an embed (KONZEPT §12.4, M2-12).
// allowed_mentions is empty, so no message pings anybody; values from the hosts are Markdown-
// escaped. The webhook URL carries its token and is a secret.
package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pmoscode/SerMon/server/internal/adapters/notify/httppost"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
)

// Limits of an embed.
const (
	maxTitle  = 256
	maxName   = 256
	maxValue  = 1024
	maxFields = 25
)

var hosts = []string{"https://discord.com/api/webhooks/", "https://discordapp.com/api/webhooks/", "https://canary.discord.com/api/webhooks/", "https://ptb.discord.com/api/webhooks/"}

// Notifier is the Discord channel type.
type Notifier struct{ client *safehttp.Client }

var _ notify.Notifier = (*Notifier)(nil)

// New returns the notifier; client is the SSRF-safe client of the server.
func New(client *safehttp.Client) *Notifier { return &Notifier{client: client} }

// Type implements notify.Notifier.
func (*Notifier) Type() notify.ChannelType { return "discord" }

// Fields implements notify.Notifier.
func (*Notifier) Fields() []notify.Field {
	return []notify.Field{{Name: "url", Kind: notify.FieldURL, Secret: true, Required: true}}
}

// Validate implements notify.Notifier.
func (*Notifier) Validate(c notify.Config) error {
	for _, h := range hosts {
		if strings.HasPrefix(c.Secrets["url"], h) {
			return nil
		}
	}
	return errors.New("url must be a Discord webhook (https://discord.com/api/webhooks/…)")
}

type field struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

type embed struct {
	Title     string  `json:"title"`
	URL       string  `json:"url,omitempty"`
	Color     int     `json:"color"`
	Fields    []field `json:"fields"`
	Timestamp string  `json:"timestamp"`
	Footer    footer  `json:"footer"`
}

type footer struct {
	Text string `json:"text"`
}

type mentions struct {
	Parse []string `json:"parse"`
}

type payload struct {
	Embeds          []embed  `json:"embeds"`
	AllowedMentions mentions `json:"allowed_mentions"`
}

var colors = map[notify.Tone]int{
	notify.ToneCritical: 0xCF222E, notify.ToneWarning: 0xD4A72C, notify.ToneGood: 0x1A7F37, notify.ToneNeutral: 0x8C959F,
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

// escape makes a value text in Discord's Markdown, with mentions defused on top of
// allowed_mentions; a cut never splits an escape.
func escape(s string, n int) string {
	safe := func(s string) string { return notify.DefuseMentions(notify.EscapeMarkdown(s)) }
	e := safe(s)
	for cut := len(s); len(e) > n; cut /= 2 {
		e = safe(truncate(s, cut))
	}
	if e == "" {
		return "–"
	}
	return e
}

func payloadOf(m notify.Message) payload {
	e := embed{
		Title: escape(m.Title, maxTitle), URL: m.Link, Color: colors[m.Tone()],
		Timestamp: m.At.UTC().Format(time.RFC3339), Footer: footer{Text: "SerMon"}, Fields: []field{},
	}
	for i, l := range m.Lines {
		if i == maxFields {
			break
		}
		e.Fields = append(e.Fields, field{Name: escape(l.Label, maxName), Value: escape(l.Value, maxValue), Inline: true})
	}
	return payload{Embeds: []embed{e}, AllowedMentions: mentions{Parse: []string{}}}
}

// Send implements notify.Notifier.
func (n *Notifier) Send(ctx context.Context, c notify.Config, m notify.Message) error {
	body, err := json.Marshal(payloadOf(m))
	if err != nil {
		return notify.Permanent(fmt.Errorf("encode message: %w", err))
	}
	return httppost.Post(ctx, n.client, c.Secrets["url"], http.Header{"Content-Type": {"application/json"}}, body) //nolint:wrapcheck // classified for notify
}
