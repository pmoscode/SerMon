package notify

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
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
	"note":             {"Hinweis", "Note"},
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
}

// fieldOrder is the order of event fields in a message; others follow by name.
var fieldOrder = []string{
	"ts", "user", "ip", "port", "method", "invalid_user", "key_type", "key_fingerprint", "tty", "target", "command",
	"uid", "pid", "process", "previous_boot_id", "uptime_before",
}

func word(key string, l Lang) string {
	if t, ok := words[key]; ok {
		return t.In(l)
	}
	return key
}

// oneLine keeps a value on its line: control characters (line breaks, tabs) become spaces.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

// Render renders a notification in a language; baseURL is the origin of the UI.
func Render(n Notification, l Lang, baseURL string) Message {
	base := strings.TrimRight(baseURL, "/")
	m := Message{Lang: l, Kind: n.Kind, Severity: n.Severity, At: n.At, Link: base + "/"}
	if n.Kind == Test {
		m.Title = word("test", l)
		m.Lines = []Line{{word("at", l), formatTime(n.At)}}
		return m
	}
	status := string(n.Kind)
	if n.Kind == Firing {
		status = string(n.Severity)
	}
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
		m.Lines = append(m.Lines, Line{word(name, l), oneLine(n.Fields[name])})
	}
	return m
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
	case UnitCount:
		return number(v, 0)
	}
	return number(v, 3)
}
