package alerts_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/alerting"
	"github.com/pmoscode/SerMon/server/internal/app/alerts"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
)

// M3-19: the statistics of the last days count only the nodes the caller sees and name the rules.
func TestStatistics_OfTheCallersNodes(t *testing.T) {
	now := t0.Add(24 * time.Hour)
	f := newFixture(now)
	f.store.history = []alerting.Record{
		changed(cpu, -10*24*time.Hour, alert.StateOK, alert.StateWarning), // before the 7 days
		changed(cpu, time.Hour, alert.StateOK, alert.StateWarning),
		changed(cpu, 2*time.Hour, alert.StateWarning, alert.StateResolved),
		changed(ssh, time.Hour, alert.StateOK, alert.StateCritical), // on db, group B
		changed(alert.Key{Rule: "acme.unknown.rule", Node: web}, time.Hour, alert.StateOK, alert.StateWarning),
	}
	s, err := f.svc.Statistics(context.Background(), caller(orgA, in(groupA, role.Viewer), time.Time{}), 7)
	if err != nil {
		t.Fatal(err)
	}
	if !f.store.since.Equal(now.AddDate(0, 0, -7)) || !s.From.Equal(f.store.since) || !s.To.Equal(now) || s.Truncated {
		t.Errorf("range %v – %v (read since %v)", s.From, s.To, f.store.since)
	}
	if s.Totals.Firings != 2 || len(s.Rules) != 2 {
		t.Fatalf("totals %+v, rules %+v; want cpu and the unknown rule of web only", s.Totals, s.Rules)
	}
	names := map[alert.RuleID]string{}
	for _, r := range s.Rules {
		names[r.Rule] = r.Name.En
	}
	if names[cpu.Rule] != "High CPU usage" || names["acme.unknown.rule"] != "acme.unknown.rule" || s.Rules[0].Labels == nil && s.Rules[1].Labels == nil {
		t.Errorf("names %v", names)
	}
	all30, err := f.svc.Statistics(context.Background(), caller(orgA, all(role.Viewer), time.Time{}), 30)
	if err != nil || all30.Totals.Firings != 4 {
		t.Errorf("30 days on all groups: %+v, %v", all30.Totals, err)
	}
	if _, err := f.svc.Statistics(context.Background(), caller(orgA, all(role.Viewer), time.Time{}), 14); !errors.Is(err, alerts.ErrInvalid) {
		t.Errorf("14 days: %v", err)
	}
	// Without a role the caller sees no nodes, like the list of alerts.
	if s, err := f.svc.Statistics(context.Background(), access.Caller{Principal: authz.Principal{User: alice, Org: orgA}}, 7); err != nil || s.Totals.Firings != 0 {
		t.Errorf("without a role: %+v, %v", s.Totals, err)
	}
}

func TestStatistics_MarksATruncatedHistory(t *testing.T) {
	f := newFixture(t0.Add(time.Hour))
	f.store.history = make([]alerting.Record, alerts.MaxStatisticsRecords+1)
	for i := range f.store.history {
		f.store.history[i] = changed(cpu, time.Duration(i)*time.Millisecond, alert.StateOK, alert.StateWarning)
	}
	s, err := f.svc.Statistics(context.Background(), caller(orgA, all(role.Viewer), time.Time{}), 7)
	if err != nil || !s.Truncated {
		t.Errorf("truncated = %v, %v", s.Truncated, err)
	}
}
