package alerts_test

import (
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/alerting"
	"github.com/pmoscode/SerMon/server/internal/app/alerts"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
)

func changed(key alert.Key, at time.Duration, from, to alert.State) alerting.Record {
	return alerting.Record{Key: key, Transition: alert.Transition{Kind: alert.StateChanged, From: from, To: to, At: t0.Add(at)}}
}

func acked(key alert.Key, at time.Duration) alerting.Record {
	return alerting.Record{Key: key, Transition: alert.Transition{Kind: alert.Acknowledged, From: alert.StateWarning, To: alert.StateWarning, At: t0.Add(at), By: alice}}
}

func flap(key alert.Key, at time.Duration, started bool) alerting.Record {
	kind := alert.FlappingStopped
	if started {
		kind = alert.FlappingStarted
	}
	return alerting.Record{Key: key, Transition: alert.Transition{Kind: kind, At: t0.Add(at)}}
}

const (
	ok, pending, warning, critical, resolved = alert.StateOK, alert.StatePending, alert.StateWarning, alert.StateCritical, alert.StateResolved
)

func TestStatistics_FiringsMTTAAndMTTR(t *testing.T) {
	m := time.Minute
	records := []alerting.Record{
		// cpu on web: pending, fires, acknowledged after 10 min, critical, resolved after 30 min.
		changed(cpu, 0, ok, pending),
		changed(cpu, 5*m, pending, warning),
		acked(cpu, 15*m),
		changed(cpu, 20*m, warning, critical),
		changed(cpu, 35*m, critical, resolved),
		changed(cpu, 40*m, resolved, ok),
		// It fires again, never acknowledged, resolved after 10 min.
		changed(cpu, 60*m, ok, warning),
		changed(cpu, 70*m, warning, resolved),
		// disk on web fires and still does (open, not acknowledged).
		changed(disk, 10*m, ok, critical),
		// mem was firing before the window: its end without a start doesn't count.
		acked(mem, 2*m),
		changed(mem, 3*m, warning, resolved),
		// A pending that never fired is no firing.
		changed(ssh, 0, ok, pending),
		changed(ssh, 1*m, pending, ok),
	}
	s := alerts.ComputeStatistics(records, t0.Add(-time.Hour), t0.Add(2*time.Hour))
	tot := s.Totals
	if tot.Firings != 3 || tot.Acknowledged != 1 || tot.Resolved != 2 || tot.Open != 1 || tot.Unacknowledged != 2 {
		t.Errorf("totals = %+v", tot)
	}
	if tot.MTTA == nil || *tot.MTTA != 10*m || tot.MTTR == nil || *tot.MTTR != 20*m {
		t.Errorf("MTTA %v, MTTR %v; want 10m and the mean of 30m and 10m", tot.MTTA, tot.MTTR)
	}
	if len(s.Rules) != 2 || s.Rules[0].Rule != cpu.Rule || s.Rules[1].Rule != disk.Rule {
		t.Fatalf("rules = %+v, want cpu (2) before disk (1)", s.Rules)
	}
	c, d := s.Rules[0], s.Rules[1]
	if c.Firings != 2 || c.Nodes != 1 || *c.MTTA != 10*m || *c.MTTR != 20*m || c.Open != 0 {
		t.Errorf("cpu = %+v", c)
	}
	if d.Firings != 1 || d.Open != 1 || d.MTTA != nil || d.MTTR != nil {
		t.Errorf("disk = %+v", d)
	}
}

func TestStatistics_FlappingAndNodes(t *testing.T) {
	m := time.Minute
	other := alert.Key{Rule: cpu.Rule, Node: db}
	var records []alerting.Record
	records = append(records, flap(cpu, 0, true))
	for i := range 5 {
		at := time.Duration(i*10) * m
		records = append(records, changed(cpu, at+m, ok, warning), changed(cpu, at+3*m, warning, resolved), changed(cpu, at+4*m, resolved, ok))
	}
	records = append(records, flap(cpu, 50*m, false), changed(cpu, 60*m, ok, warning), changed(cpu, 62*m, warning, resolved))
	records = append(records, changed(other, 5*m, ok, critical))
	s := alerts.ComputeStatistics(records, t0.Add(-time.Hour), t0.Add(2*time.Hour))
	if len(s.Rules) != 1 {
		t.Fatalf("rules = %+v", s.Rules)
	}
	r := s.Rules[0]
	if r.Firings != 7 || r.Nodes != 2 || r.Flapping != 5 || r.Open != 1 || *r.MTTR != 2*m {
		t.Errorf("rule = %+v, MTTR %v", r, *r.MTTR)
	}
}

func TestStatistics_OnlyFiringsThatStartInTheWindow(t *testing.T) {
	records := []alerting.Record{
		changed(cpu, -2*time.Hour, ok, warning),
		changed(cpu, -90*time.Minute, warning, resolved),
		changed(cpu, 0, ok, warning),
	}
	s := alerts.ComputeStatistics(records, t0.Add(-time.Hour), t0.Add(time.Hour))
	if s.Totals.Firings != 1 || s.Totals.Open != 1 || s.Totals.MTTR != nil {
		t.Errorf("totals = %+v", s.Totals)
	}
	if empty := alerts.ComputeStatistics(nil, t0.Add(-time.Hour), t0); empty.Totals.Firings != 0 || empty.Totals.MTTA != nil || len(empty.Rules) != 0 {
		t.Errorf("empty = %+v", empty)
	}
}

func TestStatistics_BoundsTheRules(t *testing.T) {
	var records []alerting.Record
	for i := range alerts.MaxStatisticsRules + 5 {
		key := alert.Key{Rule: alert.RuleID("system.test.rule_" + string(rune('a'+i))), Node: web}
		for j := range i + 1 {
			records = append(records, changed(key, time.Duration(j)*time.Minute, ok, warning), changed(key, time.Duration(j)*time.Minute+time.Second, warning, resolved))
		}
	}
	s := alerts.ComputeStatistics(records, t0.Add(-time.Hour), t0.Add(time.Hour))
	if len(s.Rules) != alerts.MaxStatisticsRules || s.Rules[0].Firings != alerts.MaxStatisticsRules+5 {
		t.Errorf("%d rules, loudest %d", len(s.Rules), s.Rules[0].Firings)
	}
	if s.Totals.Firings == s.Rules[0].Firings {
		t.Error("the totals count every rule")
	}
}

// Becoming critical clears an acknowledgement; the time to the first one counts.
func TestStatistics_TheFirstAcknowledgementCounts(t *testing.T) {
	m := time.Minute
	records := []alerting.Record{
		changed(cpu, 0, ok, warning), acked(cpu, 4*m), changed(cpu, 10*m, warning, critical), acked(cpu, 30*m),
		changed(cpu, 40*m, critical, resolved),
	}
	s := alerts.ComputeStatistics(records, t0.Add(-time.Hour), t0.Add(time.Hour))
	if s.Totals.Acknowledged != 1 || *s.Totals.MTTA != 4*m || *s.Totals.MTTR != 40*m {
		t.Errorf("totals = %+v, MTTA %v", s.Totals, *s.Totals.MTTA)
	}
}
