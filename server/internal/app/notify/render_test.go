package notify_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/secevent"
)

var update = flag.Bool("update", false, "rewrite the golden files")

var (
	t0   = time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	web  = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001"))
	base = "https://sermon.example"
)

func cpuAlert(kind notify.Kind) notify.Notification {
	return notify.Notification{
		Kind: kind, Severity: notify.Critical, Rule: "system.cpu.usage_high",
		RuleName: notify.Text{De: "Hohe CPU-Auslastung", En: "High CPU usage"},
		Node:     web, Hostname: "web-01", Value: 0.964, Unit: notify.UnitRatio, At: t0,
	}
}

func bruteForce() notify.Notification {
	return notify.Notification{
		Kind: notify.Firing, Severity: notify.Warning, Rule: "security.ssh.bruteforce",
		RuleName: notify.Text{De: "SSH-Brute-Force", En: "SSH brute force"},
		Node:     web, Hostname: "web-01", Dimension: "ip=192.0.2.7\x1f", Value: 12, Unit: notify.UnitCount, At: t0,
		Fields: alert.Fields{"ts": "2026-11-21T09:59:58Z", "user": "root", "ip": "192.0.2.7", "port": "50022"},
	}
}

func reminder(r notify.Reason, firing time.Duration) notify.Notification {
	n := cpuAlert(notify.Firing)
	n.Reason, n.FiringSeconds = r, int64(firing/time.Second)
	return n
}

func diskAlert() notify.Notification {
	n := cpuAlert(notify.Firing)
	n.Severity, n.Rule, n.Dimension, n.Value, n.Unit = notify.Warning, "system.filesystem.full_soon", "mountpoint=/var\x1f", 5.25, notify.UnitHours
	n.RuleName = notify.Text{De: "Dateisystem bald voll", En: "File system full soon"}
	return n
}

// nodeDigest is two alerts of one node in one round.
func nodeDigest() notify.Notification {
	return notify.Digest(t0, []notify.Notification{diskAlert(), cpuAlert(notify.Firing)}, 0)
}

// fleetDigest is alerts of two nodes, one resolved, one a reminder, and two left out.
func fleetDigest() notify.Notification {
	db := cpuAlert(notify.Resolved)
	db.Node, db.Hostname = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000002")), "db-01"
	return notify.Digest(t0, []notify.Notification{db, reminder(notify.ReasonEscalation, 20*time.Minute), bruteForce()}, 2)
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

func TestRender_Golden(t *testing.T) {
	disk := cpuAlert(notify.Firing)
	disk.Severity, disk.Rule, disk.Dimension, disk.Value, disk.Unit = notify.Warning, "system.filesystem.full_soon", "mountpoint=/var\x1f", 5.25, notify.UnitHours
	disk.RuleName = notify.Text{De: "Dateisystem bald voll", En: "File system full soon"}
	acked := cpuAlert(notify.Acknowledged)
	acked.AckBy = "Alice"
	cases := map[string]notify.Notification{
		"firing":           cpuAlert(notify.Firing),
		"firing_dimension": disk,
		"resolved":         cpuAlert(notify.Resolved),
		"acknowledged":     acked,
		"flapping":         cpuAlert(notify.FlappingStarted),
		"flapping_stopped": cpuAlert(notify.FlappingStopped),
		"security":         bruteForce(),
		"test":             notify.TestNotification(t0),
		"repeat":           reminder(notify.ReasonRepeat, 2*time.Hour+5*time.Minute),
		"escalation":       reminder(notify.ReasonEscalation, 15*time.Minute),
		"unmuted":          reminder(notify.ReasonUnmuted, 70*time.Minute),
		"digest_node":      nodeDigest(),
		"digest_fleet":     fleetDigest(),
	}
	for name, n := range cases {
		for _, lang := range []notify.Lang{notify.German, notify.English} {
			golden(t, name+"."+string(lang), notify.Render(n, lang, base).PlainText())
		}
	}
}

func TestRender_LinksToTheNode(t *testing.T) {
	m := notify.Render(cpuAlert(notify.Firing), notify.English, base+"/")
	if m.Link != base+"/nodes/"+web.String() {
		t.Errorf("link = %q", m.Link)
	}
	if m := notify.Render(notify.TestNotification(t0), notify.English, base); m.Link != base+"/" {
		t.Errorf("test link = %q", m.Link)
	}
}

// Values come in the unit of the rule: ratios as percent, with the decimal separator of the
// language.
func TestRender_FormatsValuesByUnit(t *testing.T) {
	for _, tc := range []struct {
		unit   notify.Unit
		value  float64
		de, en string
	}{
		{notify.UnitRatio, 0.964, "96,4 %", "96.4 %"},
		{notify.UnitRatio, 0.9, "90 %", "90 %"},
		{notify.UnitSeconds, 90, "90 s", "90 s"},
		{notify.UnitHours, 5.25, "5,3 h", "5.3 h"},
		{notify.UnitBytes, 1.5 * (1 << 30), "1,5 GiB", "1.5 GiB"},
		{notify.UnitBytes, 512, "512 B", "512 B"},
		{notify.UnitCount, 12, "12", "12"},
		{notify.UnitPerSecond, 0.25, "0,25/s", "0.25/s"},
		{notify.UnitPerMinute, 3, "3/min", "3/min"},
		{notify.UnitBytesPerSecond, 2.5 * (1 << 20), "2,5 MiB/s", "2.5 MiB/s"},
		{"", 1234.5, "1234,5", "1234.5"},
	} {
		if got := notify.FormatValue(tc.value, tc.unit, notify.German); got != tc.de {
			t.Errorf("%v %s de = %q, want %q", tc.value, tc.unit, got, tc.de)
		}
		if got := notify.FormatValue(tc.value, tc.unit, notify.English); got != tc.en {
			t.Errorf("%v %s en = %q, want %q", tc.value, tc.unit, got, tc.en)
		}
	}
}

// Rendering keeps the values as they are (each channel escapes for its format) but never lets
// one value spill into another line.
func TestRender_KeepsUntrustedValuesOnTheirLine(t *testing.T) {
	n := bruteForce()
	n.Fields["user"] = "root\nRegel: gefälscht"
	n.Hostname = "web-01\r\nX: y\u202e10-bew"
	n.Fields["ip"] = "192.0.2.7\u2028Adresse: 198.51.100.1"
	m := notify.Render(n, notify.German, base)
	// Separators and bidi overrides (Cf, Zl, Zp) would forge lines or reorder text (M2-28).
	if strings.ContainsAny(m.PlainText(), "\u202e\u2028") {
		t.Errorf("invisible characters reach the channel:\n%q", m.PlainText())
	}
	for _, l := range m.Lines {
		if strings.ContainsAny(l.Value, "\r\n") {
			t.Errorf("line %q: %q spans lines", l.Label, l.Value)
		}
	}
	if strings.ContainsAny(m.Title, "\r\n") {
		t.Errorf("title %q spans lines", m.Title)
	}
	forged := 0
	for _, line := range strings.Split(m.PlainText(), "\n") {
		if strings.HasPrefix(line, "Regel:") {
			forged++
		}
	}
	if forged != 1 {
		t.Errorf("a value forged a line:\n%s", m.PlainText())
	}
}

// Structured channels (webhook) send the data itself, not only the text.
func TestRender_KeepsTheSource(t *testing.T) {
	n := bruteForce()
	if m := notify.Render(n, notify.English, base); m.Source.Rule != n.Rule || m.Source.Fields["ip"] != "192.0.2.7" || m.Source.Value != 12 {
		t.Errorf("source = %+v", m.Source)
	}
}

func TestDimensionLabels(t *testing.T) {
	got := notify.DimensionLabels("interface=eth0\x1fmountpoint=/var\x1f")
	if len(got) != 2 || got["interface"] != "eth0" || got["mountpoint"] != "/var" {
		t.Errorf("DimensionLabels = %v", got)
	}
	if got := notify.DimensionLabels(""); got != nil {
		t.Errorf("empty: %v", got)
	}
	if got := notify.DimensionLabels("user=a=b\x1f"); got["user"] != "a=b" {
		t.Errorf("value with '=': %v", got)
	}
}

func TestMessage_Tone(t *testing.T) {
	for _, tc := range []struct {
		kind     notify.Kind
		severity notify.Severity
		want     notify.Tone
	}{
		{notify.Firing, notify.Critical, notify.ToneCritical},
		{notify.Firing, notify.Warning, notify.ToneWarning},
		{notify.Resolved, notify.Critical, notify.ToneGood},
		{notify.Acknowledged, notify.Critical, notify.ToneNeutral},
		{notify.FlappingStarted, notify.Warning, notify.ToneWarning},
		{notify.FlappingStopped, notify.Warning, notify.ToneNeutral},
		{notify.Test, "", notify.ToneNeutral},
	} {
		if got := (notify.Message{Kind: tc.kind, Severity: tc.severity}).Tone(); got != tc.want {
			t.Errorf("%s %s: %v, want %v", tc.kind, tc.severity, got, tc.want)
		}
	}
}

// A digest reads like its most urgent item: channels take priority and colour from Kind and
// Severity.
func TestDigest_TakesKindAndSeverityOfTheMostUrgentItem(t *testing.T) {
	warning := diskAlert()
	resolved := cpuAlert(notify.Resolved)
	acked := cpuAlert(notify.Acknowledged)
	for _, tc := range []struct {
		items    []notify.Notification
		kind     notify.Kind
		severity notify.Severity
	}{
		{[]notify.Notification{resolved, warning, cpuAlert(notify.Firing)}, notify.Firing, notify.Critical},
		{[]notify.Notification{resolved, warning}, notify.Firing, notify.Warning},
		{[]notify.Notification{acked, cpuAlert(notify.FlappingStarted)}, notify.FlappingStarted, notify.Critical},
		{[]notify.Notification{acked, resolved}, notify.Resolved, notify.Critical},
		{[]notify.Notification{cpuAlert(notify.FlappingStopped), acked}, notify.Acknowledged, notify.Critical},
	} {
		m := notify.Render(notify.Digest(t0, tc.items, 0), notify.English, base)
		if m.Kind != tc.kind || m.Severity != tc.severity || m.Source.Kind != notify.KindDigest {
			t.Errorf("%v: kind %s severity %s, want %s %s", tc.items, m.Kind, m.Severity, tc.kind, tc.severity)
		}
	}
}

// Items of a digest carry no fields (user, address): the delivery log keeps the digest within
// its size, and the details are in SerMon.
func TestDigest_DropsFieldsAndLinksToOneNodeOnly(t *testing.T) {
	d := notify.Digest(t0, []notify.Notification{bruteForce(), cpuAlert(notify.Firing)}, 0)
	for _, it := range d.Items {
		if it.Fields != nil {
			t.Errorf("item %s keeps fields %v", it.Rule, it.Fields)
		}
	}
	if d.Node != web || d.Hostname != "web-01" {
		t.Errorf("digest of one node: node %v host %q", d.Node, d.Hostname)
	}
	if m := notify.Render(d, notify.English, base); m.Link != base+"/nodes/"+web.String() {
		t.Errorf("link %q", m.Link)
	}
	if m := notify.Render(fleetDigest(), notify.English, base); m.Link != base+"/" || fleetDigest().Hostname != "" {
		t.Errorf("fleet link %q", m.Link)
	}
}

// Rules of an organization may have long names: a digest stays within the delivery log's
// limit (16 KiB) and counts what does not fit.
func TestDigest_FitsTheDeliveryLog(t *testing.T) {
	long := cpuAlert(notify.Firing)
	long.RuleName = notify.Text{De: strings.Repeat("ä", 2000), En: strings.Repeat("x", 2000)}
	items := make([]notify.Notification, 20)
	for i := range items {
		items[i] = long
	}
	d := notify.Digest(t0, items, 3)
	data, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > notify.MaxDigestBytes || len(d.Items) == 0 || len(d.Items)+d.More != 23 {
		t.Errorf("%d bytes, %d items, %d more", len(data), len(d.Items), d.More)
	}
}

// A security alert from classic log files says so first, in words (ADR 0025).
func TestRender_MarksUnverifiedSources(t *testing.T) {
	n := bruteForce()
	n.Fields[secevent.FieldSource] = secevent.SourceUnverified
	for lang, want := range map[notify.Lang]notify.Line{
		notify.German:  {Label: "Quelle", Value: "nicht verifiziert (Logdatei, jeder lokale Benutzer kann sie schreiben)"},
		notify.English: {Label: "Source", Value: "unverified (log file any local user can write to)"},
	} {
		m := notify.Render(n, lang, base)
		i := slices.IndexFunc(m.Lines, func(l notify.Line) bool { return l.Label == want.Label })
		if i < 0 || m.Lines[i] != want {
			t.Fatalf("%v: lines %v, want %v", lang, m.Lines, want)
		}
		if ts := slices.IndexFunc(m.Lines, func(l notify.Line) bool { return l.Value == "2026-11-21T09:59:58Z" }); ts < i {
			t.Errorf("%v: the source comes after the event fields: %v", lang, m.Lines)
		}
	}
	// Any other value is shown as it is.
	n.Fields[secevent.FieldSource] = "journal"
	m := notify.Render(n, notify.English, base)
	if !slices.Contains(m.Lines, notify.Line{Label: "Source", Value: "journal"}) {
		t.Errorf("lines %v", m.Lines)
	}
}
