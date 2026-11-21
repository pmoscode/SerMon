// Package email sends notifications by mail over the server's SMTP connection (KONZEPT §12.4,
// M2-11): text with an HTML alternative, one mail per recipient (recipients don't see each
// other). The mail server is the operator's (SERMON_SMTP_*); the channel names only recipients.
package email

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/mail"
	"net/textproto"
	"strings"

	"github.com/pmoscode/SerMon/server/internal/adapters/smtp"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
)

// MaxRecipients bounds the recipients of a channel.
const MaxRecipients = 10

// Sender sends one mail (smtp.Mailer).
type Sender interface {
	Send(ctx context.Context, m smtp.Message) error
}

// Notifier is the e-mail channel type.
type Notifier struct{ sender Sender }

var _ notify.Notifier = (*Notifier)(nil)

// New returns the notifier.
func New(sender Sender) *Notifier { return &Notifier{sender: sender} }

// Type implements notify.Notifier.
func (*Notifier) Type() notify.ChannelType { return "email" }

// Fields implements notify.Notifier.
func (*Notifier) Fields() []notify.Field {
	return []notify.Field{{Name: "to", Kind: notify.FieldText, Required: true, MaxLen: 2048}}
}

// recipients parses "a@example.org, b@example.org": plain addresses, no names, no duplicates.
func recipients(list string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, item := range strings.Split(list, ",") {
		item = strings.TrimSpace(item)
		addr, err := mail.ParseAddress(item)
		if err != nil || addr.Address != item || addr.Name != "" {
			return nil, errors.New("to takes plain addresses separated by commas")
		}
		key := strings.ToLower(item)
		if seen[key] {
			return nil, errors.New("to names an address twice")
		}
		seen[key] = true
		out = append(out, item)
	}
	if len(out) > MaxRecipients {
		return nil, fmt.Errorf("to takes at most %d addresses", MaxRecipients)
	}
	return out, nil
}

// Validate implements notify.Notifier.
func (*Notifier) Validate(c notify.Config) error {
	_, err := recipients(c.Settings["to"])
	return err
}

// page is the HTML of a notification. html/template escapes every value for its context and
// accepts only safe URLs for the link.
var page = template.Must(template.New("mail").Parse(`<!doctype html>
<html lang="{{.Lang}}"><head><meta charset="utf-8"><title>{{.Title}}</title></head>
<body style="font-family:sans-serif;color:#1f2328">
<h2 style="font-size:16px;margin:0 0 12px;color:{{.Color}}">{{.Title}}</h2>
<table style="border-collapse:collapse;font-size:14px">
{{- range .Lines}}
<tr><td style="padding:2px 12px 2px 0;color:#59636e">{{.Label}}</td><td style="padding:2px 0">{{.Value}}</td></tr>
{{- end}}
</table>
<p style="font-size:14px"><a href="{{.Link}}">{{.Open}}</a></p>
<p style="font-size:12px;color:#59636e">{{.Footer}}</p>
</body></html>
`))

var footer = notify.Text{
	De: "Diese Nachricht hat SerMon automatisch geschickt. Welche Alerts hier ankommen, legen die Admins der Organisation fest.",
	En: "SerMon sent this message automatically. The admins of the organization decide which alerts arrive here.",
}

func color(m notify.Message) string {
	switch {
	case m.Kind == notify.Firing && m.Severity == notify.Critical:
		return "#cf222e"
	case m.Kind == notify.Firing:
		return "#9a6700"
	case m.Kind == notify.Resolved:
		return "#1a7f37"
	}
	return "#1f2328"
}

func render(m notify.Message) (string, string, error) {
	var b bytes.Buffer
	err := page.Execute(&b, struct {
		Lang, Title, Color, Link, Open, Footer string
		Lines                                  []notify.Line
	}{string(m.Lang), m.Title, color(m), m.Link, notify.OpenInSerMon(m.Lang), footer.In(m.Lang), m.Lines})
	if err != nil {
		return "", "", fmt.Errorf("render mail: %w", err)
	}
	return m.PlainText() + "\n-- \n" + footer.In(m.Lang) + "\n", b.String(), nil
}

// Send implements notify.Notifier.
func (n *Notifier) Send(ctx context.Context, c notify.Config, m notify.Message) error {
	to, err := recipients(c.Settings["to"])
	if err != nil {
		return notify.Permanent(err)
	}
	text, html, err := render(m)
	if err != nil {
		return notify.Permanent(err)
	}
	for _, addr := range to {
		if err := n.sender.Send(ctx, smtp.Message{To: addr, Subject: "[SerMon] " + m.Title, Text: text, HTML: html}); err != nil {
			return classify(err)
		}
	}
	return nil
}

// classify marks what the mail server rejects for good (5xx) and malformed mails as permanent.
func classify(err error) error {
	var reply *textproto.Error
	if errors.Is(err, smtp.ErrInvalidMessage) || (errors.As(err, &reply) && reply.Code >= 500) {
		return notify.Permanent(err)
	}
	return err
}
