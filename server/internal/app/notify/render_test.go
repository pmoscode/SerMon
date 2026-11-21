package notify_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
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
	n.Hostname = "web-01\r\nX: y"
	m := notify.Render(n, notify.German, base)
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
