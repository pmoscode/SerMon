package routing_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/alerting/ruleset"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/app/routing"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

type noOverrides struct{}

func (noOverrides) Overrides(context.Context, tenant.OrgID) ([]ruleset.Override, error) {
	return nil, nil
}

// Each kind of rule reads its value in its unit.
func TestRuleInfos_FromTheDefaultRules(t *testing.T) {
	defaults, err := ruleset.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	infos, err := routing.RulesOf(ruleset.NewSource(defaults, noOverrides{}, slog.New(slog.DiscardHandler))).RuleInfos(context.Background(), orgA)
	if err != nil {
		t.Fatal(err)
	}
	for rule, unit := range map[alert.RuleID]notify.Unit{
		"system.cpu.usage_high":             notify.UnitRatio,     // threshold on a ratio
		"system.agent.buffer_filling":       notify.UnitBytes,     // threshold on bytes
		"system.node.offline":               notify.UnitSeconds,   // absence
		"system.filesystem.full_soon":       notify.UnitHours,     // prediction
		"security.ssh.bruteforce":           notify.UnitCount,     // event
		"system.network.receive_drops_high": notify.UnitRatio,     // rate relative to another
		"system.tcp.listen_overflows":       notify.UnitPerMinute, // rate per minute
		"system.processes.zombies_high":     notify.UnitCount,     // threshold on a count
		"system.network.interface_down":     "",                   // state of a boolean
	} {
		if got := infos[rule].Unit; got != unit {
			t.Errorf("%s: unit %q, want %q", rule, got, unit)
		}
	}
	cpu := infos["system.cpu.usage_high"]
	if cpu.Name.En != "High CPU usage" || cpu.Labels["category"] == "" || len(infos) != len(defaults) {
		t.Errorf("cpu %+v, %d infos for %d rules", cpu, len(infos), len(defaults))
	}
}

type members []access.Member

func (m members) ListMembers(_ context.Context, scope tenant.Scope) ([]access.Member, error) {
	if scope.OrgID() != orgA {
		return nil, nil
	}
	return m, nil
}

func TestUsersOf_NamesMembers(t *testing.T) {
	names, err := routing.UsersOf(members{{User: alice, DisplayName: "Alice"}, {User: ann, Email: "bob@example.org"}}).DisplayNames(context.Background(), mustScope(t, orgA))
	if err != nil || names[alice] != "Alice" || names[ann] != "bob@example.org" {
		t.Errorf("names %v, %v", names, err)
	}
}

func mustScope(t *testing.T, org tenant.OrgID) tenant.Scope {
	t.Helper()
	s, err := tenant.ForAllGroups(org)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
