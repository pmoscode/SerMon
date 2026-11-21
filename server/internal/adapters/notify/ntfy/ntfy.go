// Package ntfy publishes notifications to an ntfy server (KONZEPT §12.4, M2-11) as JSON to its
// root URL, with the priority from the severity. On a public server (ntfy.sh) whoever knows the
// topic reads it: the topic is a secret like the access token.
package ntfy

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

var topicName = regexp.MustCompile(`^[-_A-Za-z0-9]{1,64}$`)

// Notifier is the ntfy channel type.
type Notifier struct{ client *safehttp.Client }

var _ notify.Notifier = (*Notifier)(nil)

// New returns the notifier; client is the SSRF-safe client of the server.
func New(client *safehttp.Client) *Notifier { return &Notifier{client: client} }

// Type implements notify.Notifier.
func (*Notifier) Type() notify.ChannelType { return "ntfy" }

// Fields implements notify.Notifier.
func (*Notifier) Fields() []notify.Field {
	return []notify.Field{
		{Name: "server", Kind: notify.FieldURL, Required: true},
		{Name: "topic", Kind: notify.FieldText, Secret: true, Required: true, MaxLen: 64},
		{Name: "token", Kind: notify.FieldText, Secret: true, MaxLen: 256},
	}
}

// Validate implements notify.Notifier.
func (*Notifier) Validate(c notify.Config) error {
	if !topicName.MatchString(c.Secrets["topic"]) {
		return errors.New("topic takes 1 to 64 letters, digits, - and _")
	}
	return nil
}

// priority and tag (an emoji shortcode of ntfy) per kind of notification.
func style(m notify.Message) (int, string) {
	switch m.Kind {
	case notify.Firing:
		if m.Severity == notify.Critical {
			return 5, "rotating_light"
		}
		return 4, "warning"
	case notify.Resolved:
		return 3, "white_check_mark"
	case notify.Acknowledged:
		return 2, "eyes"
	case notify.FlappingStarted:
		return 3, "repeat"
	case notify.FlappingStopped:
		return 2, "repeat"
	case notify.Test, notify.KindDigest: // a rendered digest has the kind of its most urgent item
	}
	return 3, "test_tube"
}

type publish struct {
	Topic    string   `json:"topic"`
	Title    string   `json:"title"`
	Message  string   `json:"message"`
	Priority int      `json:"priority"`
	Tags     []string `json:"tags"`
	Click    string   `json:"click,omitempty"`
}

// Send implements notify.Notifier.
func (n *Notifier) Send(ctx context.Context, c notify.Config, m notify.Message) error {
	priority, tag := style(m)
	lines := make([]string, len(m.Lines))
	for i, l := range m.Lines {
		lines[i] = l.Label + ": " + l.Value
	}
	// Plain text: ntfy renders Markdown only when asked to, so the values need no escaping.
	body, err := json.Marshal(publish{
		Topic: c.Secrets["topic"], Title: m.Title, Message: strings.Join(lines, "\n"), Priority: priority, Tags: []string{tag}, Click: m.Link,
	})
	if err != nil {
		return notify.Permanent(fmt.Errorf("encode message: %w", err))
	}
	header := http.Header{"Content-Type": {"application/json"}}
	if token := c.Secrets["token"]; token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	return httppost.Post(ctx, n.client, strings.TrimRight(c.Settings["server"], "/")+"/", header, body) //nolint:wrapcheck // classified for notify
}
