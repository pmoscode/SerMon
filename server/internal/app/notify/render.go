package notify

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/secevent"
	"github.com/pmoscode/SerMon/server/internal/domain/textsafe"
)

// Message is a rendered notification, independent of the channel: each channel turns it into
// its format and escapes the values for it (EscapeHTML, EscapeMarkdown, EscapeSlack).
type Message struct {
	Lang     Lang
	Kind     Kind
	Severity Severity
	Title    string
	Lines    []Line
	Link     string // deep link into the UI
	At       time.Time
	// Source is the notification itself, for channels that send data (webhook); its values are
	// as untrusted as those of the lines.
	Source Notification
	// ID names the delivery, the same on every attempt, so receivers can drop duplicates and
	// replays (webhook, M2-39); empty where there is none.
	ID string
}

// Line is a labelled value of a message.
type Line struct {
	Label, Value string
}

// PlainText is the message as text (e-mail, ntfy, golden files).
func (m Message) PlainText() string {
	var b strings.Builder
	b.WriteString(m.Title)
	b.WriteString("\n\n")
	for _, l := range m.Lines {
		b.WriteString(l.Label)
		b.WriteString(": ")
		b.WriteString(l.Value)
		b.WriteByte('\n')
	}
	if m.Link != "" {
		b.WriteByte('\n')
		b.WriteString(m.Link)
		b.WriteByte('\n')
	}
	return b.String()
}

var words = map[string]Text{
	"critical":         {"KRITISCH", "CRITICAL"},
	"warning":          {"WARNUNG", "WARNING"},
	"resolved":         {"BEHOBEN", "RESOLVED"},
	"acknowledged":     {"QUITTIERT", "ACKNOWLEDGED"},
	"flapping_started": {"FLATTERT", "FLAPPING"},
	"flapping_stopped": {"STABIL", "STABLE"},
	"test":             {"Test von SerMon: Der Kanal funktioniert.", "Test from SerMon: the channel works."},
	"rule":             {"Regel", "Rule"},
	"node":             {"Node", "Node"},
	"dimension":        {"Betrifft", "Affects"},
	"value":            {"Wert", "Value"},
	"at":               {"Zeitpunkt", "Time"},
	"ack_by":           {"Quittiert von", "Acknowledged by"},
	"flapping_note":    {"Benachrichtigungen werden gedrosselt.", "Notifications are throttled."},
	"open":             {"In SerMon öffnen", "Open in SerMon"},
	"note":             {"Hinweis", "Note"},
	"repeat":           {"Erinnerung: feuert seit %s und ist nicht quittiert.", "Reminder: firing for %s and not acknowledged."},
	"escalation":       {"Eskalation: seit %s nicht quittiert.", "Escalation: not acknowledged for %s."},
	"unmuted":          {"Stummschaltung beendet: feuert seit %s.", "Silence ended: firing for %s."},
	"repeat_short":     {"Erinnerung", "reminder"},
	"unmuted_short":    {"Stummschaltung beendet", "silence ended"},
	"escalation_short": {"Eskalation", "escalation"},
	"alerts":           {"%d Alerts", "%d alerts"},
	"on_nodes":         {"auf %d Nodes", "on %d nodes"},
	"more":             {"Weitere", "More"},
	"more_alerts":      {"%d weitere Alerts in SerMon", "%d more alerts in SerMon"},
	"ts":               {"Ereignis um", "Event at"},
	"user":             {"Benutzer", "User"},
	"ip":               {"Adresse", "Address"},
	"port":             {"Port", "Port"},
	"method":           {"Methode", "Method"},
	"invalid_user":     {"Unbekannter Benutzer", "Unknown user"},
	"key_type":         {"Schlüsseltyp", "Key type"},
	"key_fingerprint":  {"Schlüssel-Fingerabdruck", "Key fingerprint"},
	"tty":              {"Terminal", "Terminal"},
	"target":           {"Ziel-Benutzer", "Target user"},
	"command":          {"Befehl", "Command"},
	"uid":              {"UID", "UID"},
	"pid":              {"PID", "PID"},
	"process":          {"Prozess", "Process"},
	"previous_boot_id": {"Vorherige Boot-ID", "Previous boot ID"},
	"uptime_before":    {"Laufzeit davor", "Uptime before"},
	// ADR 0025: the source of a security alert, set only when it is unverified.
	"token":                   {"Token", "Token"},
	"reason":                  {"Grund", "Reason"},
	"previous_ip":             {"Vorherige Adresse", "Previous address"},
	secevent.FieldSource:      {"Quelle", "Source"},
	secevent.SourceUnverified: {"nicht verifiziert (Logdatei, jeder lokale Benutzer kann sie schreiben)", "unverified (log file any local user can write to)"},
}

// fieldOrder is the order of event fields in a message; others follow by name.
var fieldOrder = []string{
	secevent.FieldSource, "ts", "user", "ip", "port", "method", "invalid_user", "key_type", "key_fingerprint", "tty", "target", "command",
	"uid", "pid", "process", "previous_boot_id", "uptime_before", "token", "reason", "previous_ip",
}

// statusOf is the key of the status word: the severity while firing, else the kind.
func statusOf(n Notification) string {
	if n.Kind == Firing {
		return string(n.Severity)
	}
	return string(n.Kind)
}

// urgency orders the kinds of a digest's items: the most urgent gives the digest its kind.
func urgency(n Notification) int {
	switch n.Kind {
	case Firing:
		if n.Severity == Critical {
			return 6
		}
		return 5
	case FlappingStarted:
		return 4
	case Resolved:
		return 3
	case Acknowledged:
		return 2
	case FlappingStopped:
		return 1
	case Test, KindDigest:
	}
	return 0
}

// renderDigest writes one line per item: its status, rule, dimension, node (when the digest
// spans nodes) and value.
func renderDigest(m Message, d Notification, l Lang, base string) Message {
	nodes := map[identity.NodeID]bool{}
	var top Notification
	for _, it := range d.Items {
		nodes[it.Node] = true
		if urgency(it) > urgency(top) {
			top = it
		}
	}
	m.Kind, m.Severity = top.Kind, top.Severity
	count := len(d.Items) + d.More
	m.Title = word(statusOf(top), l) + ": " + fmt.Sprintf(word("alerts", l), count)
	if d.Hostname != "" {
		m.Title += " – " + oneLine(d.Hostname)
	} else if len(nodes) > 1 {
		m.Title += " " + fmt.Sprintf(word("on_nodes", l), len(nodes))
	}
	if d.Node != (identity.NodeID{}) {
		m.Link = base + "/nodes/" + d.Node.String()
	}
	for _, it := range d.Items {
		v := oneLine(it.RuleName.In(l))
		if dim := dimension(it.Dimension); dim != "" {
			v += " (" + dim + ")"
		}
		if d.Hostname == "" && it.Hostname != "" {
			v += " – " + oneLine(it.Hostname)
		}
		v += ": " + FormatValue(it.Value, it.Unit, l)
		if it.Reason != "" {
			v += " (" + word(string(it.Reason)+"_short", l) + ")"
		}
		m.Lines = append(m.Lines, Line{word(statusOf(it), l), v})
	}
	if d.More > 0 {
		m.Lines = append(m.Lines, Line{word("more", l), fmt.Sprintf(word("more_alerts", l), d.More)})
	}
	return m
}

// FormatDuration writes a duration in minutes, hours and days (at least 1 min).
func FormatDuration(d time.Duration) string {
	minutes := max(int64(d/time.Minute), 1)
	days, hours, mins := minutes/(24*60), minutes/60%24, minutes%60
	var parts []string
	if days > 0 {
		parts = append(parts, strconv.FormatInt(days, 10)+" d")
	}
	if hours > 0 {
		parts = append(parts, strconv.FormatInt(hours, 10)+" h")
	}
	if mins > 0 && days == 0 {
		parts = append(parts, strconv.FormatInt(mins, 10)+" min")
	}
	return strings.Join(parts, " ")
}

// OpenInSerMon is the text of a link or button to the deep link.
func OpenInSerMon(l Lang) string { return word("open", l) }

func word(key string, l Lang) string {
	if t, ok := words[key]; ok {
		return t.In(l)
	}
	return key
}

// oneLine keeps a value on its line: control characters (line breaks, tabs) become spaces.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r):
			return ' '
		case textsafe.Invisible(r): // bidi overrides, zero width, line separators (M2-28)
			return utf8.RuneError
		}
		return r
	}, s)
}

// Render renders a notification in a language; baseURL is the origin of the UI.
func Render(n Notification, l Lang, baseURL string) Message {
	base := strings.TrimRight(baseURL, "/")
	m := Message{Lang: l, Kind: n.Kind, Severity: n.Severity, At: n.At, Link: base + "/", Source: n}
	if n.Kind == KindDigest {
		return renderDigest(m, n, l, base)
	}
	if n.Kind == Test {
		m.Title = word("test", l)
		m.Lines = []Line{{word("at", l), formatTime(n.At)}}
		return m
	}
	status := statusOf(n)
	subject := oneLine(n.RuleName.In(l))
	if n.Hostname != "" {
		subject += " – " + oneLine(n.Hostname)
	}
	m.Title = word(status, l) + ": " + subject
	if n.Node != (identity.NodeID{}) {
		m.Link = base + "/nodes/" + n.Node.String()
	}
	m.Lines = append(m.Lines, Line{word("rule", l), string(n.Rule)})
	if n.Hostname != "" {
		m.Lines = append(m.Lines, Line{word("node", l), oneLine(n.Hostname)})
	}
	if d := dimension(n.Dimension); d != "" {
		m.Lines = append(m.Lines, Line{word("dimension", l), d})
	}
	m.Lines = append(m.Lines, Line{word("value", l), FormatValue(n.Value, n.Unit, l)}, Line{word("at", l), formatTime(n.At)})
	if n.Kind == Acknowledged && n.AckBy != "" {
		m.Lines = append(m.Lines, Line{word("ack_by", l), oneLine(n.AckBy)})
	}
	if n.Kind == FlappingStarted {
		m.Lines = append(m.Lines, Line{word("note", l), word("flapping_note", l)})
	}
	if n.Reason != "" {
		m.Lines = append(m.Lines, Line{word("note", l), fmt.Sprintf(word(string(n.Reason), l), FormatDuration(time.Duration(n.FiringSeconds)*time.Second))})
	}
	names := make([]string, 0, len(n.Fields))
	for name := range n.Fields {
		names = append(names, name)
	}
	slices.SortFunc(names, func(a, b string) int {
		ia, ib := slices.Index(fieldOrder, a), slices.Index(fieldOrder, b)
		switch {
		case ia >= 0 && ib >= 0:
			return ia - ib
		case ia >= 0:
			return -1
		case ib >= 0:
			return 1
		}
		return strings.Compare(a, b)
	})
	for _, name := range names {
		value := oneLine(n.Fields[name])
		if name == secevent.FieldSource && value == secevent.SourceUnverified {
			value = word(value, l)
		}
		m.Lines = append(m.Lines, Line{word(name, l), value})
	}
	return m
}

// DimensionLabels splits a labels key ("mountpoint=/var\x1f") into its labels; nil if empty.
func DimensionLabels(key string) map[string]string {
	var out map[string]string
	for _, p := range strings.Split(key, "\x1f") {
		name, value, ok := strings.Cut(p, "=")
		if !ok || name == "" {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[name] = value
	}
	return out
}

// dimension turns a labels key ("mountpoint=/var\x1f") into "mountpoint=/var".
func dimension(key string) string {
	var parts []string
	for _, p := range strings.Split(key, "\x1f") {
		if p != "" {
			parts = append(parts, oneLine(p))
		}
	}
	return strings.Join(parts, ", ")
}

func formatTime(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05 UTC") }

// FormatValue writes a value in its unit, with the decimal separator of the language.
func FormatValue(v float64, u Unit, l Lang) string {
	number := func(x float64, decimals int) string {
		scale := math.Pow10(decimals)
		s := strconv.FormatFloat(math.Round(x*scale)/scale, 'f', decimals, 64) // half away from zero
		if strings.Contains(s, ".") {
			s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
		}
		if l == German {
			s = strings.Replace(s, ".", ",", 1)
		}
		return s
	}
	switch u {
	case UnitRatio:
		return number(v*100, 1) + " %"
	case UnitSeconds:
		return number(v, 0) + " s"
	case UnitHours:
		return number(v, 1) + " h"
	case UnitBytes:
		units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
		i := 0
		for math.Abs(v) >= 1024 && i < len(units)-1 {
			v /= 1024
			i++
		}
		return number(v, 1) + " " + units[i]
	case UnitPerSecond:
		return number(v, 2) + "/s"
	case UnitPerMinute:
		return number(v, 2) + "/min"
	case UnitBytesPerSecond:
		return FormatValue(v, UnitBytes, l) + "/s"
	case UnitCount:
		return number(v, 0)
	}
	return number(v, 3)
}

// Tone is the colour a channel gives a message.
type Tone int

// Tones.
const (
	ToneNeutral Tone = iota
	ToneCritical
	ToneWarning
	ToneGood
)

// Tone returns the colour of the message: critical and warning while firing (flapping warns),
// good once resolved, neutral otherwise.
func (m Message) Tone() Tone {
	switch m.Kind {
	case Firing:
		if m.Severity == Critical {
			return ToneCritical
		}
		return ToneWarning
	case FlappingStarted:
		return ToneWarning
	case Resolved:
		return ToneGood
	case Acknowledged, FlappingStopped, Test, KindDigest:
	}
	return ToneNeutral
}
