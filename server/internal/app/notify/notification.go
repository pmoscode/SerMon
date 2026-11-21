// Package notify delivers alert notifications to the channels of an organization (KONZEPT §12.4,
// M2-10): channel configuration with sealed credentials, templates in German and English, a
// delivery log with retries and backoff, and "send test". The channel types (e-mail, webhook,
// chat and push services) implement Notifier; every request they make goes through safehttp.
package notify

import (
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
}

// TestNotification is what "send test" delivers.
func TestNotification(at time.Time) Notification {
	return Notification{Kind: Test, At: at, RuleName: Text{De: "Testnachricht", En: "Test message"}}
}
