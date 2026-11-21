// Package gotify sends notifications to a self-hosted Gotify server (KONZEPT §12.4, M2-13) as
// plain text with the priority from the severity. The app token is a secret.
package gotify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/pmoscode/SerMon/server/internal/adapters/notify/httppost"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
)

// appToken is what Gotify issues for applications (15 characters today), with room for change.
var appToken = regexp.MustCompile(`^[A-Za-z0-9._-]{8,128}$`)

// Notifier is the Gotify channel type.
type Notifier struct{ client *safehttp.Client }

var _ notify.Notifier = (*Notifier)(nil)

// New returns the notifier; client is the SSRF-safe client of the server.
func New(client *safehttp.Client) *Notifier { return &Notifier{client: client} }

// Type implements notify.Notifier.
func (*Notifier) Type() notify.ChannelType { return "gotify" }

// Fields implements notify.Notifier.
func (*Notifier) Fields() []notify.Field {
	return []notify.Field{
		{Name: "server", Kind: notify.FieldURL, Required: true},
		{Name: "token", Kind: notify.FieldText, Secret: true, Required: true, MaxLen: 128},
	}
}

// Validate implements notify.Notifier.
func (*Notifier) Validate(c notify.Config) error {
	if !appToken.MatchString(c.Secrets["token"]) {
		return errors.New("token must be the token of a Gotify application")
	}
	return nil
}

// priority on Gotify's scale of 0 to 10: the Android app alerts loudly from 8, with sound from 4.
func priority(m notify.Message) int {
	switch m.Kind {
	case notify.Firing:
		if m.Severity == notify.Critical {
			return 8
		}
		return 5
	case notify.FlappingStarted:
		return 5
	case notify.Resolved:
		return 3
	case notify.Acknowledged, notify.FlappingStopped:
		return 2
	case notify.Test:
	}
	return 5
}

type display struct {
	ContentType string `json:"contentType"`
}

type click struct {
	URL string `json:"url"`
}

type clientNotification struct {
	Click click `json:"click"`
}

type extras struct {
	Display      display            `json:"client::display"`
	Notification clientNotification `json:"client::notification"`
}

type message struct {
	Title    string `json:"title"`
	Message  string `json:"message"`
	Priority int    `json:"priority"`
	Extras   extras `json:"extras"`
}

// Send implements notify.Notifier.
func (n *Notifier) Send(ctx context.Context, c notify.Config, m notify.Message) error {
	lines := make([]string, len(m.Lines))
	for i, l := range m.Lines {
		lines[i] = l.Label + ": " + l.Value
	}
	// Plain text: Gotify renders Markdown only for text/markdown, so the values need no escaping.
	body, err := json.Marshal(message{
		Title: m.Title, Message: strings.Join(lines, "\n"), Priority: priority(m),
		Extras: extras{Display: display{ContentType: "text/plain"}, Notification: clientNotification{Click: click{URL: m.Link}}},
	})
	if err != nil {
		return notify.Permanent(fmt.Errorf("encode message: %w", err))
	}
	header := http.Header{"Content-Type": {"application/json"}, "X-Gotify-Key": {c.Secrets["token"]}}
	return httppost.Post(ctx, n.client, strings.TrimRight(c.Settings["server"], "/")+"/message", header, body) //nolint:wrapcheck // classified for notify
}
