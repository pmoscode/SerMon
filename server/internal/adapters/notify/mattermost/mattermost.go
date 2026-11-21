// Package mattermost posts notifications to a Mattermost incoming webhook as a message
// attachment (KONZEPT §12.4, M2-12). Mattermost renders Markdown and notifies on @all, @channel
// and @here: values from the hosts are escaped and their mentions defused. Mattermost runs on
// premises, so any URL is allowed (an internal one needs the operator's allowlist).
package mattermost

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/pmoscode/SerMon/server/internal/adapters/notify/httppost"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
)

// Notifier is the Mattermost channel type.
type Notifier struct{ client *safehttp.Client }

var _ notify.Notifier = (*Notifier)(nil)

// New returns the notifier; client is the SSRF-safe client of the server.
func New(client *safehttp.Client) *Notifier { return &Notifier{client: client} }

// Type implements notify.Notifier.
func (*Notifier) Type() notify.ChannelType { return "mattermost" }

// Fields implements notify.Notifier.
func (*Notifier) Fields() []notify.Field {
	return []notify.Field{{Name: "url", Kind: notify.FieldURL, Secret: true, Required: true}}
}

// Validate implements notify.Notifier (the URL was checked as a field).
func (*Notifier) Validate(notify.Config) error { return nil }

type field struct {
	Title string `json:"title"`
	Value string `json:"value"`
	Short bool   `json:"short"`
}

type attachment struct {
	Fallback  string  `json:"fallback"`
	Color     string  `json:"color"`
	Title     string  `json:"title"`
	TitleLink string  `json:"title_link,omitempty"`
	Fields    []field `json:"fields"`
	Footer    string  `json:"footer"`
}

type payload struct {
	Text        string       `json:"text"`
	Attachments []attachment `json:"attachments"`
}

var colors = map[notify.Tone]string{
	notify.ToneCritical: "#cf222e", notify.ToneWarning: "#d4a72c", notify.ToneGood: "#1a7f37", notify.ToneNeutral: "#8c959f",
}

func safe(s string) string { return notify.DefuseMentions(notify.EscapeMarkdown(s)) }

func payloadOf(m notify.Message) payload {
	title := safe(m.Title)
	a := attachment{Fallback: title, Color: colors[m.Tone()], Title: title, TitleLink: m.Link, Footer: "SerMon", Fields: []field{}}
	for _, l := range m.Lines {
		a.Fields = append(a.Fields, field{Title: safe(l.Label), Value: safe(l.Value), Short: true})
	}
	return payload{Text: title, Attachments: []attachment{a}}
}

// Send implements notify.Notifier.
func (n *Notifier) Send(ctx context.Context, c notify.Config, m notify.Message) error {
	body, err := json.Marshal(payloadOf(m))
	if err != nil {
		return notify.Permanent(fmt.Errorf("encode message: %w", err))
	}
	return httppost.Post(ctx, n.client, c.Secrets["url"], http.Header{"Content-Type": {"application/json"}}, body) //nolint:wrapcheck // classified for notify
}
