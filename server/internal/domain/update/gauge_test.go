package update_test

import (
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/domain/update"
)

func values(vs []update.Value) map[string]float64 {
	out := map[string]float64{}
	for _, v := range vs {
		out[v.Metric] = v.Value
	}
	return out
}

func TestGauge_ValuesOfAListedReport(t *testing.T) {
	g := update.Gauge{
		Listed: true, Total: 7, Security: 2, OldestSecurity: t0.Add(-72 * time.Hour),
		RebootRequired: true, RebootSince: t0.Add(-90 * time.Minute),
	}
	got := values(g.Values(t0.Add(500 * time.Millisecond)))
	want := map[string]float64{
		update.MetricPending: 7, update.MetricSecurityPending: 2, update.MetricSecurityAge: 72 * 3600,
		update.MetricRebootRequired: 1, update.MetricRebootRequiredSeconds: 90 * 60,
	}
	if len(got) != len(want) {
		t.Fatalf("values = %v", got)
	}
	for m, v := range want {
		if got[m] != v {
			t.Errorf("%s = %v, want %v", m, got[m], v)
		}
	}
}

func TestGauge_ZeroWithoutSecurityUpdatesOrReboot(t *testing.T) {
	// The oldest security update stays from before: without security updates it counts nothing.
	g := update.Gauge{Listed: true, Total: 3, OldestSecurity: t0.Add(-time.Hour), RebootSince: t0.Add(-time.Hour)}
	got := values(g.Values(t0))
	for _, m := range []string{update.MetricSecurityAge, update.MetricSecurityPending, update.MetricRebootRequired, update.MetricRebootRequiredSeconds} {
		if got[m] != 0 {
			t.Errorf("%s = %v", m, got[m])
		}
	}
	// A clock behind the report: no negative age; security updates without a stored package: 0.
	g = update.Gauge{Listed: true, Security: 1, OldestSecurity: t0.Add(time.Hour), RebootRequired: true}
	got = values(g.Values(t0))
	if got[update.MetricSecurityAge] != 0 || got[update.MetricRebootRequiredSeconds] != 0 || got[update.MetricRebootRequired] != 1 {
		t.Errorf("values = %v", got)
	}
}

func TestGauge_WithoutListOnlyTheReboot(t *testing.T) {
	got := values(update.Gauge{Total: 5, Security: 5, RebootRequired: true, RebootSince: t0.Add(-time.Minute)}.Values(t0))
	if len(got) != 2 || got[update.MetricRebootRequiredSeconds] != 60 {
		t.Errorf("values = %v", got)
	}
}
