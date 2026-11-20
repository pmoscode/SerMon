package metric_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/pmoscode/SerMon/server/internal/domain/metric"
)

// The rules of proto/sermon/agent/v1/agent.proto (Sample) and of the agent's label filters.
var (
	namePattern  = regexp.MustCompile(`^sermon_[a-z0-9_]{1,120}$`)
	labelPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	units        = map[metric.Unit]bool{
		metric.UnitRatioPPM: true, metric.UnitBytes: true, metric.UnitBytesPerSecond: true, metric.UnitBitsPerSecond: true,
		metric.UnitPerSecond: true, metric.UnitPagesPerSecond: true, metric.UnitCount: true, metric.UnitSeconds: true,
		metric.UnitTimestamp: true, metric.UnitCelsius: true, metric.UnitLoad: true, metric.UnitBoolean: true,
	}
	aggregations = map[metric.Aggregation]bool{metric.Avg: true, metric.Max: true, metric.Last: true}
)

func TestCatalog_EveryDefinitionIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range metric.All() {
		if !namePattern.MatchString(d.Name) || seen[d.Name] {
			t.Errorf("%s: invalid or duplicate name", d.Name)
		}
		seen[d.Name] = true
		if !units[d.Unit] || !aggregations[d.Aggregation] || d.Area == "" {
			t.Errorf("%s: unit %q, aggregation %q, area %q", d.Name, d.Unit, d.Aggregation, d.Area)
		}
		if len(d.Labels) > 8 {
			t.Errorf("%s: %d labels, the protocol allows 8", d.Name, len(d.Labels))
		}
		for _, label := range d.Labels {
			if !labelPattern.MatchString(label) {
				t.Errorf("%s: invalid label %q", d.Name, label)
			}
		}
		if d.DisplayKey() == "" || d.HelpKey() != "metrics/"+d.Name {
			t.Errorf("%s: keys %q %q", d.Name, d.DisplayKey(), d.HelpKey())
		}
	}
	if len(seen) < 70 {
		t.Errorf("catalog has %d metrics, expected the base metrics of KONZEPT §8.1", len(seen))
	}
}

func TestCatalog_LookupAndLabels(t *testing.T) {
	d, ok := metric.Lookup("sermon_pressure_stall_ratio")
	if !ok || d.Unit != metric.UnitRatioPPM || d.DisplayKey() != "metric_pressure_stall_ratio" {
		t.Fatalf("Lookup() = %+v, %v", d, ok)
	}
	if !d.AcceptsLabels(map[string]string{"kind": "some", "resource": "io"}) {
		t.Error("exact labels rejected")
	}
	for _, labels := range []map[string]string{
		{}, {"kind": "some"}, {"kind": "some", "resource": "io", "pid": "1"}, {"kind": "some", "other": "io"},
	} {
		if d.AcceptsLabels(labels) {
			t.Errorf("accepted %v", labels)
		}
	}
	if _, ok := metric.Lookup("sermon_unknown"); ok {
		t.Error("unknown metric found")
	}
}

// Every metric of the catalog has a help text in German and English (KONZEPT §17, M1-31).
func TestCatalog_EveryMetricHasHelpTexts(t *testing.T) {
	for _, d := range metric.All() {
		for _, lang := range []string{"de", "en"} {
			path := filepath.Join("..", "..", "..", "..", "docs", "help", lang, d.HelpKey()+".md")
			if _, err := os.Stat(path); err != nil {
				t.Errorf("%s: missing %s", d.Name, path)
			}
		}
	}
}
