package ruleset_test

import (
	"context"
	"errors"
	"math"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/alerting"
	"github.com/pmoscode/SerMon/server/internal/app/alerting/ruleset"
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/secevent"
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

// burst is steady at base for 10 minutes in 10 s steps, the last n points at peak.
func burst(base, peak float64, n int) []metrics.Point {
	values := make([]float64, 60)
	for i := range values {
		values[i] = base
		if i >= len(values)-n {
			values[i] = peak
		}
	}
	return at(10*time.Second, values...)
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
// Event rules get security events instead; instances is how many fire (fleet correlation).
type scenario struct {
	fire, calm             []metrics.Series
	fireEvents, calmEvents []secevent.Event
	fireSeen, calmSeen     time.Duration
	want                   alert.Level
	instances              int
}

var (
	idle     = metrics.Labels{"mode": "idle"}
	root     = metrics.Labels{"mountpoint": "/"}
	eth0     = metrics.Labels{"interface": "eth0"}
	md0      = metrics.Labels{"device": "md0"}
	backup   = metrics.Labels{"unit": "backup.service"}
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

var (
	site           = metrics.Labels{"target": "https://127.0.0.1/health"}
	port5432       = metrics.Labels{"target": "127.0.0.1:5432"}
	replica        = metrics.Labels{"replica": "standby-1"}
	nginxContainer = metrics.Labels{"container": "3f2a9c1b7d4e", "name": "nginx"}
	runcContainer  = metrics.Labels{"container": "3f2a9c1b7d4e", "name": "nginx", "runtime": "docker"}
	day            = 86400.0
)

// moduleDown is a module's "up" going from 1 to 0.
func moduleDown(metric string, labels metrics.Labels, level alert.Level) scenario {
	return scenario{fire: labeled(metric, labels, was(1, 0)), calm: labeled(metric, labels, was(1, 1)), want: level}
}

func networkRate(direction, kind string) scenario {
	m, packets := "sermon_network_"+direction+"_"+kind+"_per_second", "sermon_network_"+direction+"_packets_per_second"
	return scenario{fire: share(m, packets, eth0, 5), calm: share(m, packets, eth0, 0), want: warning}
}

// events answers like the store: the events of the queried kind and nodes received since.
type events []secevent.Event

func (s events) Received(_ context.Context, scope tenant.Scope, q alerting.EventQuery) ([]secevent.Event, error) {
	if !scope.Valid() {
		return nil, errors.New("query without scope")
	}
	var out []secevent.Event
	for _, e := range s {
		if e.Kind == q.Kind && slices.Contains(q.Nodes, e.Node) && !e.ReceivedAt.Before(q.Since) {
			out = append(out, e)
		}
	}
	return out, nil
}

var (
	db  = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000002"))
	app = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000003"))
)

// event is an event of a node that occurred and arrived ago before now.
func event(kind secevent.Kind, node identity.NodeID, ago time.Duration, ip, user string) secevent.Event {
	e := secevent.Event{Node: node, Kind: kind, User: user, OccurredAt: now.Add(-ago), ReceivedAt: now.Add(-ago)}
	if ip != "" {
		e.Remote = &secevent.Remote{IP: netip.MustParseAddr(ip), Port: 50022}
	}
	return e
}

// repeat is n events of web, one every 2 s up to now.
func repeat(kind secevent.Kind, n int, ip, user string) []secevent.Event {
	out := make([]secevent.Event, n)
	for i := range out {
		out[i] = event(kind, web, time.Duration(i)*2*time.Second, ip, user)
	}
	return out
}

const attacker, colleague = "192.0.2.7", "198.51.100.20"

// newSource marks a login as the first from its source, as the store does.
func newSource(e secevent.Event) secevent.Event {
	e.NewSource = true
	return e
}

// Every shipped rule needs one (TestDefaults_EachRuleFiresOnItsData).
var scenarios = map[alert.RuleID]scenario{
	"system.node.offline":         {fireSeen: 90 * time.Second, calmSeen: 20 * time.Second, want: critical},
	"system.agent.buffer_filling": {fire: one("sermon_agent_buffer_bytes", steady(190*mib)), calm: one("sermon_agent_buffer_bytes", steady(mib)), want: critical},
	// M2-24: the counter of dropped batches grew (increase over the window).
	"system.agent.data_dropped": {fire: one("sermon_agent_buffer_dropped_batches", at(time.Minute, 3, 3, 4)), calm: one("sermon_agent_buffer_dropped_batches", at(time.Minute, 3, 3, 3)), want: warning},
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
	// M2-22: state collectors.
	"system.raid.degraded":       {fire: labeled("sermon_raid_degraded", md0, was(0, 1)), calm: labeled("sermon_raid_degraded", md0, was(0, 0)), want: critical},
	"system.time.unsynchronized": {fire: one("sermon_time_synchronized", steady(0)), calm: one("sermon_time_synchronized", steady(1)), want: warning},
	"system.systemd.unit_failed": {fire: labeled("sermon_systemd_unit_failed", backup, was(0, 1)), calm: labeled("sermon_systemd_unit_failed", backup, was(1, 0)), want: warning},

	// M5-20: the modules' rules, in the units the modules send (seconds, 0 or 1).
	"module.http_check.down":           moduleDown("sermon_http_check_up", site, critical),
	"module.tcp_check.down":            moduleDown("sermon_tcp_check_up", port5432, critical),
	"module.nginx.down":                moduleDown("sermon_nginx_up", nil, critical),
	"module.apache.down":               moduleDown("sermon_apache_up", nil, critical),
	"module.redis.down":                moduleDown("sermon_redis_up", nil, critical),
	"module.redis.replication_broken":  moduleDown("sermon_redis_master_link_up", nil, warning),
	"module.prometheus.target_down":    moduleDown("sermon_prometheus_up", site, warning),
	"module.systemd_units.not_active":  moduleDown("sermon_systemd_units_active", backup, warning),
	"module.smart.failed":              moduleDown("sermon_smart_passed", sda, critical),
	"module.mysql.replication_stopped": moduleDown("sermon_mysql_replication_running", nil, critical),
	"module.docker_socket.down":        moduleDown("sermon_docker_socket_up", nil, critical),
	"module.docker_socket.unhealthy":   moduleDown("sermon_docker_socket_healthy", nginxContainer, warning),
	"module.docker_socket.restarting": {
		fire: labeled("sermon_docker_socket_restarts", nginxContainer, at(time.Minute, 0, 0, 2, 5, 8, 12)),
		calm: labeled("sermon_docker_socket_restarts", nginxContainer, steady(3)), want: critical,
	},
	"module.docker.oom_kill": {
		fire: labeled("sermon_docker_oom_kills", runcContainer, at(time.Minute, 0, 0, 1)),
		calm: labeled("sermon_docker_oom_kills", runcContainer, steady(1)), want: warning,
	},
	"module.tls.chain_invalid": moduleDown("sermon_tls_chain_valid", site, warning),
	"module.tls.name_invalid":  moduleDown("sermon_tls_name_valid", site, warning),
	"module.tls.unreadable":    moduleDown("sermon_tls_up", site, warning),
	"module.mysql.replication_lag": {
		fire: one("sermon_mysql_replication_lag_seconds", steady(400)),
		calm: one("sermon_mysql_replication_lag_seconds", steady(1)), want: critical,
	},
	"module.tls.cert_expiring": {
		fire: labeled("sermon_tls_cert_expires_in_seconds", site, steady(5*day)),
		calm: labeled("sermon_tls_cert_expires_in_seconds", site, steady(60*day)), want: critical,
	},
	"module.http_check.cert_expiring": {
		fire: labeled("sermon_http_check_cert_expires_in_seconds", site, steady(14*day)),
		calm: labeled("sermon_http_check_cert_expires_in_seconds", site, steady(60*day)), want: warning,
	},
	"module.postgres.replication_lag": {
		fire: labeled("sermon_postgres_replication_lag_seconds", replica, steady(120)),
		calm: labeled("sermon_postgres_replication_lag_seconds", replica, steady(2)), want: warning,
	},

	"system.node.rebooted":   {fireEvents: []secevent.Event{event(secevent.SystemReboot, web, time.Minute, "", "")}, calmEvents: []secevent.Event{event(secevent.SystemReboot, web, 20*time.Minute, "", "")}, want: warning},
	"system.memory.oom_kill": {fireEvents: []secevent.Event{event(secevent.OOMKill, web, time.Minute, "", "")}, calmEvents: []secevent.Event{event(secevent.OOMKill, web, 20*time.Minute, "", "")}, want: critical},
	"security.ssh.bruteforce": {
		fireEvents: repeat(secevent.SSHLoginFailed, 50, attacker, "root"), calmEvents: repeat(secevent.SSHLoginFailed, 3, colleague, "alice"), want: critical,
	},
	"security.ssh.fleet_correlation": {
		fireEvents: []secevent.Event{
			event(secevent.SSHLoginFailed, web, time.Minute, attacker, "root"), event(secevent.SSHLoginFailed, db, 2*time.Minute, attacker, "admin"),
			event(secevent.SSHLoginFailed, app, 3*time.Minute, attacker, "oracle"),
		},
		calmEvents: []secevent.Event{event(secevent.SSHLoginFailed, web, time.Minute, colleague, "alice")}, want: warning, instances: 3,
	},
	"security.ssh.login_after_failures": {
		fireEvents: append(repeat(secevent.SSHLoginFailed, 6, attacker, "deploy"), event(secevent.SSHLoginSucceeded, web, 0, attacker, "deploy")),
		calmEvents: append(repeat(secevent.SSHLoginFailed, 2, colleague, "alice"), event(secevent.SSHLoginSucceeded, web, 0, colleague, "alice")), want: critical,
	},
	"security.ssh.root_login": {
		fireEvents: []secevent.Event{event(secevent.SSHLoginSucceeded, web, time.Minute, colleague, "root")},
		calmEvents: []secevent.Event{event(secevent.SSHLoginSucceeded, web, time.Minute, colleague, "alice")}, want: warning,
	},
	// M3-16: the first login of a user from a source; the node of the scenarios has no
	// enrollment time, so its learning week is over.
	"security.ssh.login_new_source": {
		fireEvents: []secevent.Event{newSource(event(secevent.SSHLoginSucceeded, web, time.Minute, colleague, "alice"))},
		calmEvents: []secevent.Event{event(secevent.SSHLoginSucceeded, web, time.Minute, colleague, "alice")}, want: warning,
	},
	"security.sudo.auth_failed": {
		fireEvents: repeat(secevent.SudoAuthFailed, 3, "", "alice"), calmEvents: repeat(secevent.SudoAuthFailed, 1, "", "alice"), want: warning,
	},
	"security.sudo.not_allowed": {
		fireEvents: []secevent.Event{event(secevent.SudoNotAllowed, web, time.Minute, "", "bob")},
		calmEvents: []secevent.Event{event(secevent.SudoNotAllowed, web, time.Hour, "", "bob")}, want: warning,
	},
	"security.account.created": {
		fireEvents: []secevent.Event{event(secevent.AccountCreated, web, time.Minute, "", "bob")},
		calmEvents: []secevent.Event{event(secevent.AccountCreated, web, time.Hour, "", "bob")}, want: warning,
	},
	// M2-24: events of the server itself (T3, T4).
	"security.enrollment.token_used": {
		fireEvents: []secevent.Event{event(secevent.EnrollmentTokenUsed, web, time.Minute, "192.0.2.30", "")},
		calmEvents: []secevent.Event{event(secevent.EnrollmentTokenUsed, web, time.Hour, "192.0.2.30", "")}, want: warning,
	},
	"security.node.locked": {
		fireEvents: []secevent.Event{event(secevent.NodeLocked, web, time.Minute, "", "")},
		calmEvents: []secevent.Event{event(secevent.NodeLocked, web, 2*time.Hour, "", "")}, want: critical,
	},
	// M4-16: the derived series of the updates (seconds since the oldest open security update,
	// since the reboot became needed) and the rollout's server events.
	"system.updates.security_pending": {
		fire: one("sermon_updates_security_age_seconds", steady(15*86400)), calm: one("sermon_updates_security_age_seconds", steady(2*86400)), want: critical,
	},
	"system.updates.reboot_required": {
		fire: one("sermon_updates_reboot_required_seconds", steady(2*86400)), calm: one("sermon_updates_reboot_required_seconds", steady(3600)), want: warning,
	},
	"system.updates.run_failed": {
		fireEvents: []secevent.Event{event(secevent.UpdateRunFailed, web, time.Minute, "", "")},
		calmEvents: []secevent.Event{event(secevent.UpdateRunFailed, web, 2*time.Hour, "", "")}, want: critical,
	},
	"system.updates.rollout_stopped": {
		fireEvents: []secevent.Event{event(secevent.UpdateRolloutStopped, web, time.Minute, "", "")},
		calmEvents: []secevent.Event{event(secevent.UpdateRolloutStopped, web, 2*time.Hour, "", "")}, want: critical,
	},
	"system.agent.self_update_failed": {
		fireEvents: []secevent.Event{event(secevent.AgentSelfUpdateFailed, web, time.Minute, "", "")},
		calmEvents: []secevent.Event{event(secevent.AgentSelfUpdateFailed, web, 2*time.Hour, "", "")}, want: warning,
	},
	// M4-24: the anomaly rate the worker writes (0..1 in ppm).
	// A burst of a minute is no alert: averaged over 10 minutes it stays under 10 %.
	"system.anomaly.rate_high": {
		fire: one("sermon_anomaly_rate_ratio", steady(300_000)), calm: one("sermon_anomaly_rate_ratio", burst(20_000, 600_000, 6)), want: critical,
	},
	"security.node.rotation_new_ip": {
		fireEvents: []secevent.Event{event(secevent.NodeRotationNewIP, web, time.Minute, "192.0.2.31", "")},
		calmEvents: []secevent.Event{event(secevent.NodeRotationNewIP, web, time.Hour, "192.0.2.31", "")}, want: warning,
	},
}

// evaluate runs the rule over web (with metrics and events) and two nodes that only send events.
func evaluate(t *testing.T, rule alerting.Rule, data []metrics.Series, evs []secevent.Event, seen time.Duration) []alerting.Result {
	t.Helper()
	if seen == 0 {
		seen = 10 * time.Second
	}
	last := now.Add(-seen)
	scope, _ := tenant.ForAllGroups(orgA)
	results, err := rule.Evaluator.Evaluate(context.Background(), alerting.Env{
		Scope: scope, Now: now, Metrics: querier(data), Events: events(evs),
		Nodes: []fleet.Node{{ID: web, Group: group, Hostname: "web-01", LastSeenAt: &last}, {ID: db, Group: group, Hostname: "db-01"}, {ID: app, Group: group, Hostname: "app-01"}},
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

// The CPU alert shows the use, not the idle time it is measured from: at 97 % load its value is
// 0.97 (shown as 97 %), not 0.03.
func TestDefaults_TheCPUAlertShowsTheUse(t *testing.T) {
	defs, err := ruleset.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(defs, func(d ruleset.Definition) bool { return d.Rule.ID == "system.cpu.usage_high" })
	if i < 0 {
		t.Fatal("no rule system.cpu.usage_high")
	}
	fired := evaluate(t, defs[i].Rule, labeled("sermon_cpu_utilization_ratio", idle, steady(30_000)), nil, 0)
	if len(fired) != 1 {
		t.Fatalf("%+v, want one alert", fired)
	}
	if o := fired[0].Observation; o.Level != alert.LevelCritical || math.Abs(o.Value-0.97) > 1e-9 {
		t.Errorf("%+v, want critical at 0.97", o)
	}
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
			instances := max(sc.instances, 1)
			fired := evaluate(t, rule, sc.fire, sc.fireEvents, sc.fireSeen)
			if len(fired) != instances {
				t.Errorf("triggering data: %+v, want %d alerts", fired, instances)
			}
			for _, f := range fired {
				if f.Observation.Level != sc.want || !fires(rule, f.Observation) {
					t.Errorf("triggering data: %+v, want a %v alert", f, sc.want)
				}
				if def.Doc.GetSpec().GetType() == "event" && len(f.Observation.Fields) == 0 {
					t.Errorf("event alert without fields: %+v", f)
				}
			}
			// Calm data is nothing at all or instances that stay ok.
			for _, c := range evaluate(t, rule, sc.calm, sc.calmEvents, sc.calmSeen) {
				if c.Observation.Level != alert.LevelOK || fires(rule, c.Observation) {
					t.Errorf("calm data: %+v, want it to stay ok", c)
				}
			}
			if def.Doc.GetSpec().GetType() != "event" && len(evaluate(t, rule, sc.calm, nil, sc.calmSeen)) != 1 {
				t.Error("calm data: want one instance that stays ok")
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
// zombies take the minimum over the window). Event rules hold for their count window instead.
func TestDefaults_HaveHysteresisOrDuration(t *testing.T) {
	defs, err := ruleset.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	instant := []alert.RuleID{"system.node.offline", "system.filesystem.readonly", "system.tcp.listen_overflows", "system.processes.zombies_high"}
	for _, def := range defs {
		spec := def.Doc.GetSpec()
		if spec.GetType() == "event" {
			if len(spec.GetNotifyFields()) == 0 {
				t.Errorf("%s: an event rule names its notifyFields", def.Rule.ID)
			}
		} else if !slices.Contains(instant, def.Rule.ID) && ((spec.GetClear() == nil && spec.GetType() != "state") || def.Rule.Timing.For == 0) {
			t.Errorf("%s: clear %v, for %v; set both", def.Rule.ID, spec.GetClear(), def.Rule.Timing.For)
		}
		if def.Doc.GetMetadata().GetLabels()["category"] == "" {
			t.Errorf("%s has no category", def.Rule.ID)
		}
	}
}

// M5-20: every rule on a module's metric names the module and applies to no node that doesn't
// run it – a node without the module gets no alert.
func TestDefaults_ModuleRulesOnlyForNodesRunningTheModule(t *testing.T) {
	defs, err := ruleset.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, def := range defs {
		if def.Doc.GetMetadata().GetLabels()["category"] != "modules" {
			continue
		}
		count++
		mod := def.Rule.Module
		if mod == "" || !strings.HasPrefix(string(def.Rule.ID), "module."+mod+".") {
			t.Errorf("%s: module %q", def.Rule.ID, mod)
		}
		without := fleet.Node{ID: web, Group: group, Modules: []string{"other"}}
		with := fleet.Node{ID: db, Group: group, Modules: []string{"other", mod}}
		if got := def.Rule.Nodes([]fleet.Node{without, with}); len(got) != 1 || got[0].ID != db {
			t.Errorf("%s applies to %+v", def.Rule.ID, got)
		}
	}
	if count < 10 {
		t.Errorf("%d module rules", count)
	}
}
