// Package webhook delivers notifications as signed JSON POST requests (KONZEPT §12.4, M2-11).
//
// Signature: X-SerMon-Signature: t=<unix seconds>,v1=<hex HMAC-SHA256 of "<t>.<body>">, keyed with
// the channel's secret. The receiver recomputes it, compares in constant time and rejects old
// timestamps (replays); see docs/help/*/channels/webhook.md.
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/pmoscode/SerMon/server/internal/adapters/notify/httppost"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
)

// minSecret is the shortest signing secret (it must not be guessable).
const minSecret = 16

// Notifier is the webhook channel type.
type Notifier struct {
	client *safehttp.Client
	now    func() time.Time
}

var _ notify.Notifier = (*Notifier)(nil)

// New returns the notifier; client is the SSRF-safe client of the server.
func New(client *safehttp.Client, now func() time.Time) *Notifier {
	return &Notifier{client: client, now: now}
}

// Type implements notify.Notifier.
func (*Notifier) Type() notify.ChannelType { return "webhook" }

// Fields implements notify.Notifier.
func (*Notifier) Fields() []notify.Field {
	return []notify.Field{
		{Name: "url", Kind: notify.FieldURL, Required: true},
		{Name: "secret", Kind: notify.FieldText, Secret: true, Required: true, MaxLen: 256},
	}
}

// Validate implements notify.Notifier.
func (*Notifier) Validate(c notify.Config) error {
	if len(c.Secrets["secret"]) < minSecret {
		return fmt.Errorf("secret needs at least %d characters", minSecret)
	}
	return nil
}

// payload is the JSON body; version 1 changes only by adding fields.
type payload struct {
	Version  int             `json:"version"`
	Kind     notify.Kind     `json:"kind"`
	Severity notify.Severity `json:"severity,omitempty"`
	Test     bool            `json:"test"`
	Language notify.Lang     `json:"language"`
	Title    string          `json:"title"`
	Text     string          `json:"text"`
	Link     string          `json:"link"`
	At       time.Time       `json:"at"`
	*item
	// Items and More make up a digest (kind "digest").
	Items []digestItem `json:"items,omitempty"`
	More  int          `json:"more,omitempty"`
}

// digestItem is one notification of a digest.
type digestItem struct {
	Kind     notify.Kind     `json:"kind"`
	Severity notify.Severity `json:"severity,omitempty"`
	item
}

// item is the data of one alert notification.
type item struct {
	Rule           *ruleInfo         `json:"rule,omitempty"`
	Node           *nodeInfo         `json:"node,omitempty"`
	Dimension      map[string]string `json:"dimension,omitempty"`
	Value          *float64          `json:"value,omitempty"`
	Unit           notify.Unit       `json:"unit,omitempty"`
	ValueText      string            `json:"value_text,omitempty"`
	Fields         map[string]string `json:"fields,omitempty"`
	AcknowledgedBy string            `json:"acknowledged_by,omitempty"`
	Reason         notify.Reason     `json:"reason,omitempty"`
	FiringSeconds  int64             `json:"firing_seconds,omitempty"`
}

type ruleInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type nodeInfo struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname,omitempty"`
}

func itemOf(n notify.Notification, l notify.Lang) item {
	it := item{Rule: &ruleInfo{ID: string(n.Rule), Name: n.RuleName.In(l)}}
	if n.Node != (identity.NodeID{}) {
		it.Node = &nodeInfo{ID: n.Node.String(), Hostname: n.Hostname}
	}
	value := n.Value
	it.Dimension, it.Value, it.Unit, it.ValueText = notify.DimensionLabels(n.Dimension), &value, n.Unit, notify.FormatValue(n.Value, n.Unit, l)
	it.Fields, it.AcknowledgedBy, it.Reason, it.FiringSeconds = n.Fields, n.AckBy, n.Reason, n.FiringSeconds
	return it
}

func payloadOf(m notify.Message) payload {
	n := m.Source
	p := payload{
		Version: 1, Kind: m.Kind, Severity: m.Severity, Test: m.Kind == notify.Test, Language: m.Lang,
		Title: m.Title, Text: m.PlainText(), Link: m.Link, At: m.At.UTC(),
	}
	switch n.Kind {
	case notify.Test:
	case notify.KindDigest:
		p.Kind, p.More = notify.KindDigest, n.More
		for _, it := range n.Items {
			p.Items = append(p.Items, digestItem{Kind: it.Kind, Severity: it.Severity, item: itemOf(it, m.Lang)})
		}
	case notify.Firing, notify.Resolved, notify.Acknowledged, notify.FlappingStarted, notify.FlappingStopped:
		it := itemOf(n, m.Lang)
		p.item = &it
	}
	return p
}

// Send implements notify.Notifier.
func (w *Notifier) Send(ctx context.Context, c notify.Config, m notify.Message) error {
	p := payloadOf(m)
	body, err := json.Marshal(p)
	if err != nil {
		return notify.Permanent(fmt.Errorf("encode payload: %w", err))
	}
	secret := c.Secrets["secret"]
	if secret == "" {
		return notify.Permanent(errors.New("no signing secret"))
	}
	ts := strconv.FormatInt(w.now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	header := http.Header{
		"Content-Type":       {"application/json"},
		"X-Sermon-Event":     {string(p.Kind)},
		"X-Sermon-Signature": {"t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))},
	}
	return httppost.Post(ctx, w.client, c.Settings["url"], header, body) //nolint:wrapcheck // classified for notify
}
