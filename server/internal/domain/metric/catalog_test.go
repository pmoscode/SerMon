package metric_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

// ADR 0037: the candidates of the anomaly detection – CPU modes and the load of a minute only,
// a counter for the log rate; the server writes the rate itself.
func TestCatalog_AnomalyCandidates(t *testing.T) {
	count := 0
	for _, d := range metric.All() {
		if d.Anomaly == nil {
			continue
		}
		count++
		if d.Anomaly.Floor <= 0 || d.Server {
			t.Errorf("%s: %+v", d.Name, d.Anomaly)
		}
	}
	if candidates := metric.Candidates(); count != 19 || len(candidates) != count || candidates[0].Name != "sermon_cpu_utilization_ratio" {
		t.Errorf("%d candidates, %d in order", count, len(candidates))
	}
	cpu, _ := metric.Lookup("sermon_cpu_utilization_ratio")
	if !cpu.Anomaly.Scores(map[string]string{"mode": "iowait"}) || cpu.Anomaly.Scores(map[string]string{"mode": "idle"}) {
		t.Error("cpu modes")
	}
	disk, _ := metric.Lookup("sermon_disk_busy_ratio")
	if !disk.Anomaly.Scores(map[string]string{"device": "vda"}) {
		t.Error("every disk")
	}
	logs, _ := metric.Lookup("sermon_agent_logs_sent_entries")
	if !logs.Anomaly.Counter {
		t.Error("the log rate is a counter")
	}
	if rate, ok := metric.Lookup("sermon_anomaly_rate_ratio"); !ok || !rate.Server || rate.Anomaly != nil {
		t.Errorf("rate = %+v", rate)
	}
}

// ADR 0039: the modules' metrics come from their catalog files (agent/crates/sermon-modules/metrics,
// copied by just gen), named after the module, in the area of the modules.
func TestCatalog_ModuleMetrics(t *testing.T) {
	if errs := metric.ModuleErrors(); len(errs) > 0 {
		t.Fatalf("module catalogs: %v", errs)
	}
	for _, name := range []string{"sermon_simulate_value", "sermon_simulate_token"} {
		d, ok := metric.Lookup(name)
		if !ok || d.Module != "simulate" || d.Area != metric.AreaModule || len(d.Labels) != 0 || d.Server {
			t.Errorf("%s = %+v, %v", name, d, ok)
		}
	}
	for _, d := range metric.All() {
		if (d.Module != "") != (d.Area == metric.AreaModule) {
			t.Errorf("%s: module %q in area %q", d.Name, d.Module, d.Area)
		}
		if d.Module != "" && !strings.HasPrefix(d.Name, "sermon_"+d.Module+"_") {
			t.Errorf("%s: not named after module %s", d.Name, d.Module)
		}
	}
}

func TestCatalog_ModuleFilesAreChecked(t *testing.T) {
	for _, tc := range []struct{ file, content, why string }{
		{"nginx.json", `{"metrics":[{"name":"sermon_other_x","unit":"count","aggregation":"last"}]}`, "named"},
		{"nginx.json", `{"metrics":[{"name":"sermon_nginx_x","unit":"furlongs","aggregation":"last"}]}`, "unit"},
		{"nginx.json", `{"metrics":[{"name":"sermon_nginx_x","unit":"count","aggregation":"sum"}]}`, "aggregation"},
		{"nginx.json", `{"metrics":[{"name":"sermon_nginx_x","unit":"count","aggregation":"last","labels":["Bad"]}]}`, "label"},
		{"nginx.json", `{"metrics":[{"name":"sermon_nginx_x","unit":"count","aggregation":"last","extra":1}]}`, "unknown field"},
		{"nginx.json", `{"metrics":[{"name":"sermon_cpu_logical_count","unit":"count","aggregation":"last"}]}`, "named"},
		{"Nginx.json", `{"metrics":[]}`, "module id"},
		{"nginx.json", `{"metrics":[{"name":"sermon_nginx_x","unit":"count","aggregation":"last"},{"name":"sermon_nginx_x","unit":"count","aggregation":"last"}]}`, "twice"},
	} {
		_, err := metric.ParseModuleCatalog(tc.file, []byte(tc.content))
		if err == nil || !strings.Contains(err.Error(), tc.why) {
			t.Errorf("%s %s: %v, want %q", tc.file, tc.content, err, tc.why)
		}
	}
	defs, err := metric.ParseModuleCatalog("nginx.json", []byte(`{"metrics":[{"name":"sermon_nginx_requests_per_second","unit":"per_second","aggregation":"avg","labels":["zone","host"]}]}`))
	if err != nil || len(defs) != 1 || defs[0].Labels[0] != "host" || defs[0].Module != "nginx" {
		t.Errorf("%+v, %v", defs, err)
	}
}
