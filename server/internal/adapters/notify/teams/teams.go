// Package teams posts notifications to Microsoft Teams as an Adaptive Card through a workflow
// ("When a Teams webhook request is received", KONZEPT §12.4, M2-12). Adaptive Cards render
// Markdown in TextBlock and FactSet, so values from the hosts go only into TextRun elements of a
// RichTextBlock, which are plain text. The workflow URL carries its signature and is a secret.
package teams

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/pmoscode/SerMon/server/internal/adapters/notify/httppost"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
)

// Notifier is the Microsoft Teams channel type.
type Notifier struct{ client *safehttp.Client }

var _ notify.Notifier = (*Notifier)(nil)

// New returns the notifier; client is the SSRF-safe client of the server.
func New(client *safehttp.Client) *Notifier { return &Notifier{client: client} }

// Type implements notify.Notifier.
func (*Notifier) Type() notify.ChannelType { return "teams" }

// Fields implements notify.Notifier.
func (*Notifier) Fields() []notify.Field {
	return []notify.Field{{Name: "url", Kind: notify.FieldURL, Secret: true, Required: true}}
}

// Validate implements notify.Notifier: workflow URLs vary by tenant and region, but use https.
func (*Notifier) Validate(c notify.Config) error {
	if !strings.HasPrefix(c.Secrets["url"], "https://") {
		return errors.New("url must be the https URL of the Teams workflow")
	}
	return nil
}

type run struct {
	Type   string `json:"type"`
	Text   string `json:"text"`
	Weight string `json:"weight,omitempty"`
	Size   string `json:"size,omitempty"`
	Color  string `json:"color,omitempty"`
}

type element struct {
	Type    string `json:"type"`
	Inlines []run  `json:"inlines"`
	Spacing string `json:"spacing,omitempty"`
}

type action struct {
	Type  string `json:"type"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

type card struct {
	Schema  string         `json:"$schema"`
	Type    string         `json:"type"`
	Version string         `json:"version"`
	Body    []element      `json:"body"`
	Actions []action       `json:"actions,omitempty"`
	MSTeams map[string]any `json:"msteams"`
}

type attachment struct {
	ContentType string `json:"contentType"`
	Content     card   `json:"content"`
}

type payload struct {
	Type        string       `json:"type"`
	Attachments []attachment `json:"attachments"`
}

var colors = map[notify.Tone]string{
	notify.ToneCritical: "attention", notify.ToneWarning: "warning", notify.ToneGood: "good", notify.ToneNeutral: "default",
}

func payloadOf(m notify.Message) payload {
	body := []element{{Type: "RichTextBlock", Inlines: []run{{Type: "TextRun", Text: m.Title, Weight: "bolder", Size: "medium", Color: colors[m.Tone()]}}}}
	for _, l := range m.Lines {
		body = append(body, element{Type: "RichTextBlock", Spacing: "none", Inlines: []run{
			{Type: "TextRun", Text: l.Label + ": ", Weight: "bolder"}, {Type: "TextRun", Text: l.Value},
		}})
	}
	c := card{
		Schema: "http://adaptivecards.io/schemas/adaptive-card.json", Type: "AdaptiveCard", Version: "1.4",
		Body: body, MSTeams: map[string]any{"width": "Full"},
	}
	if m.Link != "" {
		c.Actions = []action{{Type: "Action.OpenUrl", Title: notify.OpenInSerMon(m.Lang), URL: m.Link}}
	}
	return payload{Type: "message", Attachments: []attachment{{ContentType: "application/vnd.microsoft.card.adaptive", Content: c}}}
}

// Send implements notify.Notifier.
func (n *Notifier) Send(ctx context.Context, c notify.Config, m notify.Message) error {
	body, err := json.Marshal(payloadOf(m))
	if err != nil {
		return notify.Permanent(fmt.Errorf("encode message: %w", err))
	}
	return httppost.Post(ctx, n.client, c.Secrets["url"], http.Header{"Content-Type": {"application/json"}}, body) //nolint:wrapcheck // classified for notify
}
