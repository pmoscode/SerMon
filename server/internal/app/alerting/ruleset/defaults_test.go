package ruleset_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/alerting"
	"github.com/pmoscode/SerMon/server/internal/app/alerting/ruleset"
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	now   = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	orgA  = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000a0"))
	group = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000b0"))
	web   = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001"))
)

// querier answers from fixed series, filtered like metrics.Reader.
type querier []metrics.Series

func (s querier) Query(_ context.Context, scope tenant.Scope, q metrics.Query) ([]metrics.Series, error) {
	if !scope.Valid() {
		return nil, errors.New("query without scope")
	}
	var out []metrics.Series
	for _, one := range s {
		if one.Labels["__metric"] != q.Metric || !slices.Contains(q.Nodes, one.Node) {
			continue
		}
		labels := metrics.Labels{}
		match := true
		for k, v := range one.Labels {
			if k != "__metric" {
				labels[k] = v
			}
		}
		for k, v := range q.Labels {
			match = match && labels[k] == v
		}
		if !match {
			continue
		}
		var points []metrics.Point
		for _, p := range one.Points {
			if !p.Time.Before(q.From) && p.Time.Before(q.To) {
				points = append(points, p)
			}
		}
		out = append(out, metrics.Series{Node: one.Node, Labels: labels, Points: points})
	}
	return out, nil
}

// at returns points ending just before now, one per step.
func at(step time.Duration, values ...float64) []metrics.Point {
	out := make([]metrics.Point, len(values))
	for i, v := range values {
		out[i] = metrics.Point{Time: now.Add(-time.Duration(len(values)-i) * step), Avg: v, Min: v, Max: v}
	}
	return out
}

// steady is ten minutes of 10-s points with one value.
func steady(v float64) []metrics.Point {
	values := make([]float64, 60)
	for i := range values {
		values[i] = v
	}
	return at(10*time.Second, values...)
}

// was is history: hourly points with before, then a current value.
func was(before, current float64) []metrics.Point {
	return append(at(time.Hour, before, before, before), metrics.Point{Time: now.Add(-30 * time.Second), Avg: current, Min: current, Max: current})
}

func series(metric string, labels metrics.Labels, points []metrics.Point) metrics.Series {
	l := metrics.Labels{"__metric": metric}
	for k, v := range labels {
		l[k] = v
	}
	return metrics.Series{Node: web, Labels: l, Points: points}
}

func one(metric string, points []metrics.Point) []metrics.Series {
	return []metrics.Series{series(metric, nil, points)}
}

func labeled(metric string, labels metrics.Labels, points []metrics.Point) []metrics.Series {
	return []metrics.Series{series(metric, labels, points)}
}

// share is a metric relative to a total of 1000.
func share(part, total string, labels metrics.Labels, value float64) []metrics.Series {
	return []metrics.Series{series(part, labels, steady(value)), series(total, labels, steady(1000))}
}

func falling(start, step float64) []metrics.Point {
	values := make([]float64, 13)
	for i := range values {
		values[i] = start - float64(i)*step
	}
	return at(time.Hour, values...)
}

// scenario is data that triggers a rule (at the given level) and data that does not, in the
// units the agent sends (ppm for ratios). seen is the time since the node's last contact.
type scenario struct {
	fire, calm         []metrics.Series
	fireSeen, calmSeen time.Duration
	want               alert.Level
}

var (
	idle     = metrics.Labels{"mode": "idle"}
	root     = metrics.Labels{"mountpoint": "/"}
	eth0     = metrics.Labels{"interface": "eth0"}
	sda      = metrics.Labels{"device": "sda"}
	memSome  = metrics.Labels{"resource": "memory", "kind": "some"}
	cpuSome  = metrics.Labels{"resource": "cpu", "kind": "some"}
	load15   = metrics.Labels{"period": "15m"}
	load1    = metrics.Labels{"period": "1m"}
	iowait   = metrics.Labels{"mode": "iowait"}
	steal    = metrics.Labels{"mode": "steal"}
	mib      = 1024.0 * 1024
	critical = alert.LevelCritical
	warning  = alert.LevelWarning
)

func networkRate(direction, kind string) scenario {
	m, packets := "sermon_network_"+direction+"_"+kind+"_per_second", "sermon_network_"+direction+"_packets_per_second"
	return scenario{fire: share(m, packets, eth0, 5), calm: share(m, packets, eth0, 0), want: warning}
}

// Every shipped rule needs one (TestDefaults_EachRuleFiresOnItsData).
var scenarios = map[alert.RuleID]scenario{
	"system.node.offline":         {fireSeen: 90 * time.Second, calmSeen: 20 * time.Second, want: critical},
	"system.agent.buffer_filling": {fire: one("sermon_agent_buffer_bytes", steady(190*mib)), calm: one("sermon_agent_buffer_bytes", steady(mib)), want: critical},
	"system.cpu.usage_high": {
		fire: append(labeled("sermon_cpu_utilization_ratio", idle, steady(100_000)), labeled("sermon_cpu_utilization_ratio", iowait, steady(900_000))...),
		calm: labeled("sermon_cpu_utilization_ratio", idle, steady(500_000)), want: warning,
	},
	"system.cpu.iowait_high": {fire: labeled("sermon_cpu_utilization_ratio", iowait, steady(300_000)), calm: labeled("sermon_cpu_utilization_ratio", iowait, steady(10_000)), want: warning},
	"system.cpu.steal_high":  {fire: labeled("sermon_cpu_utilization_ratio", steal, steady(300_000)), calm: labeled("sermon_cpu_utilization_ratio", steal, steady(0)), want: critical},
	"system.load.per_cpu_high": {
		fire: append(labeled("sermon_load_average_per_cpu", load15, steady(2)), labeled("sermon_load_average_per_cpu", load1, steady(0.1))...),
		calm: labeled("sermon_load_average_per_cpu", load15, steady(0.5)), want: warning,
	},
	"system.memory.usage_high": {fire: one("sermon_memory_utilization_ratio", steady(950_000)), calm: one("sermon_memory_utilization_ratio", steady(400_000)), want: critical},
	"system.swap.usage_high":   {fire: one("sermon_swap_utilization_ratio", steady(600_000)), calm: one("sermon_swap_utilization_ratio", steady(100_000)), want: warning},
	"system.memory.pressure_high": {
		fire: append(labeled("sermon_pressure_stall_ratio", memSome, steady(400_000)), labeled("sermon_pressure_stall_ratio", cpuSome, steady(0))...),
		calm: labeled("sermon_pressure_stall_ratio", memSome, steady(0)), want: critical,
	},
	"system.filesystem.usage_high":        {fire: labeled("sermon_filesystem_utilization_ratio", root, steady(850_000)), calm: labeled("sermon_filesystem_utilization_ratio", root, steady(500_000)), want: warning},
	"system.filesystem.inodes_high":       {fire: share("sermon_filesystem_inodes_used", "sermon_filesystem_inodes_total", root, 950), calm: share("sermon_filesystem_inodes_used", "sermon_filesystem_inodes_total", root, 10), want: critical},
	"system.filesystem.full_soon":         {fire: labeled("sermon_filesystem_avail_bytes", root, falling(170e9, 10e9)), calm: labeled("sermon_filesystem_avail_bytes", root, falling(100e9, 0)), want: critical},
	"system.disk.busy_high":               {fire: labeled("sermon_disk_busy_ratio", sda, steady(950_000)), calm: labeled("sermon_disk_busy_ratio", sda, steady(100_000)), want: warning},
	"system.filesystem.readonly":          {fire: labeled("sermon_filesystem_readonly", root, was(0, 1)), calm: labeled("sermon_filesystem_readonly", root, was(0, 0)), want: critical},
	"system.network.interface_down":       {fire: labeled("sermon_network_up", eth0, was(1, 0)), calm: labeled("sermon_network_up", eth0, was(1, 1)), want: warning},
	"system.network.receive_errors_high":  networkRate("receive", "errors"),
	"system.network.transmit_errors_high": networkRate("transmit", "errors"),
	"system.network.receive_drops_high":   networkRate("receive", "drops"),
	"system.network.transmit_drops_high":  networkRate("transmit", "drops"),
	"system.tcp.listen_overflows":         {fire: one("sermon_tcp_listen_overflows_per_second", steady(1)), calm: one("sermon_tcp_listen_overflows_per_second", steady(0)), want: critical},
	"system.conntrack.usage_high":         {fire: one("sermon_conntrack_utilization_ratio", steady(950_000)), calm: one("sermon_conntrack_utilization_ratio", steady(100_000)), want: critical},
	"system.file_descriptors.usage_high":  {fire: share("sermon_system_file_descriptors_used", "sermon_system_file_descriptors_limit", nil, 850), calm: share("sermon_system_file_descriptors_used", "sermon_system_file_descriptors_limit", nil, 100), want: warning},
	"system.processes.zombies_high":       {fire: one("sermon_system_zombies", steady(60)), calm: one("sermon_system_zombies", steady(0)), want: critical},
}

func evaluate(t *testing.T, rule alerting.Rule, data []metrics.Series, seen time.Duration) []alerting.Result {
	t.Helper()
	if seen == 0 {
		seen = 10 * time.Second
	}
	last := now.Add(-seen)
	scope, _ := tenant.ForAllGroups(orgA)
	results, err := rule.Evaluator.Evaluate(context.Background(), alerting.Env{
		Scope: scope, Now: now, Metrics: querier(data),
		Nodes: []fleet.Node{{ID: web, Group: group, Hostname: "web-01", LastSeenAt: &last}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return results
}

// fires reports whether the observation fires an alert once it held for the rule's duration.
func fires(rule alerting.Rule, o alert.Observation) bool {
	inst := alert.NewInstance(alert.Key{Rule: rule.ID, Node: web}, now)
	for at := now; !at.After(now.Add(rule.Timing.For + alerting.Interval)); at = at.Add(alerting.Interval) {
		inst.Observe(o, at, rule.Timing)
	}
	return inst.State.Firing()
}

func TestDefaults_EachRuleFiresOnItsData(t *testing.T) {
	defs, err := ruleset.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != len(scenarios) {
		t.Errorf("%d default rules, %d scenarios", len(defs), len(scenarios))
	}
	for _, def := range defs {
		rule := def.Rule
		t.Run(string(rule.ID), func(t *testing.T) {
			sc, ok := scenarios[rule.ID]
			if !ok {
				t.Fatal("no scenario: add data that triggers the rule")
			}
			fired := evaluate(t, rule, sc.fire, sc.fireSeen)
			if len(fired) != 1 || fired[0].Observation.Level != sc.want || !fires(rule, fired[0].Observation) {
				t.Errorf("triggering data: %+v, want one %v alert", fired, sc.want)
			}
			calm := evaluate(t, rule, sc.calm, sc.calmSeen)
			if len(calm) != 1 || calm[0].Observation.Level != alert.LevelOK || fires(rule, calm[0].Observation) {
				t.Errorf("calm data: %+v, want one instance that stays ok", calm)
			}
		})
	}
	for id := range scenarios {
		if !slices.ContainsFunc(defs, func(d ruleset.Definition) bool { return d.Rule.ID == id }) {
			t.Errorf("scenario for %s, which is no default rule", id)
		}
	}
}

// The defaults hold back flapping: a clear below the threshold (states are 0 or 1 and need none)
// and a duration, except where a single observation is the alert (offline, read-only, overflows;
// zombies take the minimum over the window).
func TestDefaults_HaveHysteresisOrDuration(t *testing.T) {
	defs, err := ruleset.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	instant := []alert.RuleID{"system.node.offline", "system.filesystem.readonly", "system.tcp.listen_overflows", "system.processes.zombies_high"}
	for _, def := range defs {
		spec := def.Doc.GetSpec()
		if !slices.Contains(instant, def.Rule.ID) && ((spec.GetClear() == nil && spec.GetType() != "state") || def.Rule.Timing.For == 0) {
			t.Errorf("%s: clear %v, for %v; set both", def.Rule.ID, spec.GetClear(), def.Rule.Timing.For)
		}
		if def.Doc.GetMetadata().GetLabels()["category"] == "" {
			t.Errorf("%s has no category", def.Rule.ID)
		}
	}
}
