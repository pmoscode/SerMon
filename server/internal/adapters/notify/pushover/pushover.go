// Package pushover sends notifications through the Pushover API (KONZEPT §12.4, M2-13) as plain
// text with the priority from the severity. Application token and user key are secrets.
package pushover

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/pmoscode/SerMon/server/internal/adapters/notify/httppost"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
)

const api = "https://api.pushover.net"

// Limits of the API in characters.
const (
	maxTitle    = 250
	maxMessage  = 1024
	maxURL      = 512
	maxURLTitle = 100
	more        = "…"
)

var (
	key     = regexp.MustCompile(`^[A-Za-z0-9]{30}$`)
	devices = regexp.MustCompile(`^[A-Za-z0-9_-]{1,25}(,[A-Za-z0-9_-]{1,25})*$`)
)

// Notifier is the Pushover channel type.
type Notifier struct {
	client *safehttp.Client
	api    string
}

var _ notify.Notifier = (*Notifier)(nil)

// New returns the notifier; client is the SSRF-safe client of the server.
func New(client *safehttp.Client) *Notifier { return &Notifier{client: client, api: api} }

// Type implements notify.Notifier.
func (*Notifier) Type() notify.ChannelType { return "pushover" }

// Fields implements notify.Notifier.
func (*Notifier) Fields() []notify.Field {
	return []notify.Field{
		{Name: "token", Kind: notify.FieldText, Secret: true, Required: true, MaxLen: 30},
		{Name: "user", Kind: notify.FieldText, Secret: true, Required: true, MaxLen: 30},
		{Name: "device", Kind: notify.FieldText, MaxLen: 255},
	}
}

// Validate implements notify.Notifier.
func (*Notifier) Validate(c notify.Config) error {
	if !key.MatchString(c.Secrets["token"]) {
		return errors.New("token must be the API token of a Pushover application (30 letters and digits)")
	}
	if !key.MatchString(c.Secrets["user"]) {
		return errors.New("user must be a Pushover user or group key (30 letters and digits)")
	}
	if d, ok := c.Settings["device"]; ok && !devices.MatchString(d) {
		return errors.New("device takes device names (letters, digits, - and _, up to 25 each) separated by commas")
	}
	return nil
}

// priority from -2 (silent) to 2 (emergency): critical alerts are high and pass quiet hours;
// emergency, which repeats until confirmed in Pushover, is left out.
func priority(m notify.Message) int {
	switch m.Kind {
	case notify.Firing:
		if m.Severity == notify.Critical {
			return 1
		}
	case notify.Acknowledged, notify.FlappingStopped:
		return -1
	case notify.Resolved, notify.FlappingStarted, notify.Test:
	}
	return 0
}

func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + more
}

// message is the lines while they fit.
func message(m notify.Message) string {
	var b strings.Builder
	used := 0
	for _, l := range m.Lines {
		line := l.Label + ": " + l.Value
		sep := ""
		if used > 0 {
			sep = "\n"
		}
		n := utf8.RuneCountInString(sep + line)
		if used+n > maxMessage-utf8.RuneCountInString("\n"+more) {
			b.WriteString(sep + more)
			break
		}
		b.WriteString(sep + line)
		used += n
	}
	if b.Len() == 0 {
		return m.Title // the API refuses an empty message
	}
	return b.String()
}

// Send implements notify.Notifier.
func (n *Notifier) Send(ctx context.Context, c notify.Config, m notify.Message) error {
	// Plain text (no html=1), so the values need no escaping.
	form := url.Values{
		"token": {c.Secrets["token"]}, "user": {c.Secrets["user"]},
		"title": {cut(m.Title, maxTitle)}, "message": {message(m)},
		"priority": {strconv.Itoa(priority(m))}, "timestamp": {strconv.FormatInt(m.At.Unix(), 10)},
	}
	if d := c.Settings["device"]; d != "" {
		form.Set("device", d)
	}
	if m.Link != "" && len(m.Link) <= maxURL {
		form.Set("url", m.Link)
		form.Set("url_title", cut(notify.OpenInSerMon(m.Lang), maxURLTitle))
	}
	header := http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}
	return httppost.Post(ctx, n.client, n.api+"/1/messages.json", header, []byte(form.Encode())) //nolint:wrapcheck // classified for notify
}
