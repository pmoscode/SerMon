// Package notify delivers alert notifications to the channels of an organization (KONZEPT §12.4,
// M2-10): channel configuration with sealed credentials, templates in German and English, a
// delivery log with retries and backoff, and "send test". The channel types (e-mail, webhook,
// chat and push services) implement Notifier; every request they make goes through safehttp.
package notify

import (
	"encoding/json"
	"time"

	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
)

// Kind is what a notification reports.
type Kind string

// Kinds of notifications (from the transitions of an alert instance).
const (
	Firing          Kind = "firing"
	Resolved        Kind = "resolved"
	Acknowledged    Kind = "acknowledged"
	FlappingStarted Kind = "flapping_started"
	FlappingStopped Kind = "flapping_stopped"
	Test            Kind = "test"
	// KindDigest combines the notifications of one round for a channel (Digest).
	KindDigest Kind = "digest"
)

// Reason says why a firing alert is notified although it did not change.
type Reason string

// Reasons of notifications without a change (routing, M2-14).
const (
	ReasonRepeat     Reason = "repeat"     // still firing and not acknowledged
	ReasonEscalation Reason = "escalation" // not acknowledged in time, to more channels
	ReasonUnmuted    Reason = "unmuted"    // a silence or maintenance window held it back and ended
)

// Severity is the level of the alert.
type Severity string

// Severities.
const (
	Warning  Severity = "warning"
	Critical Severity = "critical"
)

// Unit is how the value of a rule reads.
type Unit string

// Units of values (ratios are 0..1).
const (
	UnitRatio     Unit = "ratio"
	UnitSeconds   Unit = "seconds"
	UnitHours     Unit = "hours"
	UnitBytes     Unit = "bytes"
	UnitCount     Unit = "count"
	UnitPerSecond Unit = "per_second"
	UnitPerMinute Unit = "per_minute"
	// UnitBytesPerSecond is a throughput.
	UnitBytesPerSecond Unit = "bytes_per_second"
)

// Lang is the language of a channel.
type Lang string

// Languages.
const (
	German  Lang = "de"
	English Lang = "en"
)

// Text is a text in both languages.
type Text struct {
	De string `json:"de"`
	En string `json:"en"`
}

// In returns the text in a language.
func (t Text) In(l Lang) string {
	if l == German {
		return t.De
	}
	return t.En
}

// Notification is what happened to an alert, as stored with each delivery. Hostname, dimension
// and fields come from the monitored hosts and are untrusted.
type Notification struct {
	Kind      Kind            `json:"kind"`
	Severity  Severity        `json:"severity,omitempty"`
	Rule      alert.RuleID    `json:"rule,omitempty"`
	RuleName  Text            `json:"rule_name"`
	Node      identity.NodeID `json:"node"`
	Hostname  string          `json:"hostname,omitempty"`
	Dimension string          `json:"dimension,omitempty"` // labels key (mountpoint=/var)
	Value     float64         `json:"value"`
	Unit      Unit            `json:"unit,omitempty"`
	Fields    alert.Fields    `json:"fields,omitempty"`
	At        time.Time       `json:"at"`
	AckBy     string          `json:"ack_by,omitempty"` // display name
	// Reason and FiringSeconds explain a reminder or an escalation.
	Reason        Reason `json:"reason,omitempty"`
	FiringSeconds int64  `json:"firing_seconds,omitempty"`
	// Items and More make up a digest (KindDigest).
	Items []Notification `json:"items,omitempty"`
	More  int            `json:"more,omitempty"`
}

// Bounds of a digest; the rest is counted in More. MaxDigestBytes keeps the stored notification
// below the delivery log's limit (16 KiB, migration 00024), also for rules with long names.
const (
	MaxDigestItems = 20
	MaxDigestBytes = 12 << 10
)

// Digest combines notifications into one: the first MaxDigestItems without their fields (the
// delivery log keeps a digest small; the details are in SerMon) and within MaxDigestBytes, the
// others counted in more. A digest of one node names it.
func Digest(at time.Time, items []Notification, more int) Notification {
	d := digest(at, items, more)
	for len(d.Items) > 1 {
		data, err := json.Marshal(d)
		if err == nil && len(data) <= MaxDigestBytes {
			break
		}
		d = digest(at, d.Items[:len(d.Items)-1], d.More+1)
	}
	return d
}

func digest(at time.Time, items []Notification, more int) Notification {
	d := Notification{Kind: KindDigest, At: at, More: more}
	if len(items) > MaxDigestItems {
		d.More += len(items) - MaxDigestItems
		items = items[:MaxDigestItems]
	}
	for i, it := range items {
		it.Fields, it.Items, it.More = nil, nil, 0
		d.Items = append(d.Items, it)
		if i == 0 {
			d.Node, d.Hostname = it.Node, it.Hostname
		} else if it.Node != d.Node {
			d.Node, d.Hostname = identity.NodeID{}, ""
		}
	}
	return d
}

// TestNotification is what "send test" delivers.
func TestNotification(at time.Time) Notification {
	return Notification{Kind: Test, At: at, RuleName: Text{De: "Testnachricht", En: "Test message"}}
}
