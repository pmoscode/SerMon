//go:build integration

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/pgtest"
	"github.com/pmoscode/SerMon/server/internal/app/alerting"
	"github.com/pmoscode/SerMon/server/internal/app/alerting/ruleset"
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/app/metrics/memstore"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/secevent"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// dumpLine is one line of `sermon-agent simulate --dump`: a sample, an event or a change of the
// connection.
type dumpLine struct {
	Node   int               `json:"node"`
	At     int64             `json:"at"`
	Metric string            `json:"metric"`
	Labels map[string]string `json:"labels"`
	Value  float64           `json:"value"`
	Event  string            `json:"event"`
	User   string            `json:"user"`
	IP     string            `json:"ip"`
	Port   uint16            `json:"port"`
	Online *bool             `json:"online"`
}

// replay is a dump in the shape the alert engine reads: 10-s windows in the metric store (and,
// as time goes by, in the ring of recent values), the events, and when each node was online.
type replay struct {
	org   tenant.OrgID
	group tenant.GroupID
	scope tenant.Scope
	nodes []identity.NodeID
	store *memstore.Store
	// samples are the windows ordered by time, for the ring.
	samples []metrics.Aggregate
	events  []secevent.Event
	online  map[identity.NodeID][]onlineChange
	start   time.Time
}

type onlineChange struct {
	at     time.Time
	online bool
}

// loadDump runs the simulator's dump of the scenario for span and loads it.
func loadDump(t *testing.T, bin string, nodes int, span string, scenarios ...string) *replay {
	t.Helper()
	args := []string{"simulate", "--nodes", strconv.Itoa(nodes), "--seed", "7", "--dump", span}
	for _, s := range scenarios {
		args = append(args, "--scenario", s)
	}
	cmd := exec.CommandContext(t.Context(), bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("dump: %v\n%s", err, stderr.String())
	}
	org := tenant.OrgID(uuid.New())
	group := tenant.GroupID(uuid.New())
	scope, _ := tenant.ForAllGroups(org)
	r := &replay{org: org, group: group, scope: scope, store: memstore.New(), online: map[identity.NodeID][]onlineChange{}}
	for range nodes {
		id := identity.NodeID(uuid.New())
		r.nodes = append(r.nodes, id)
		r.store.AddNode(org, group, id)
	}
	var windows []metrics.Aggregate
	lines := bufio.NewScanner(bytes.NewReader(out))
	lines.Buffer(make([]byte, 64*1024), 1024*1024)
	for lines.Scan() {
		var l dumpLine
		if err := json.Unmarshal(lines.Bytes(), &l); err != nil {
			t.Fatalf("%s: %v", lines.Text(), err)
		}
		at, node := time.Unix(l.At, 0).UTC(), r.nodes[l.Node]
		if r.start.IsZero() || at.Before(r.start) {
			r.start = at
		}
		switch {
		case l.Online != nil:
			r.online[node] = append(r.online[node], onlineChange{at, *l.Online})
		case l.Event != "":
			if l.Event != "ssh.login.failed" {
				t.Fatalf("unexpected event %q", l.Event)
			}
			r.events = append(r.events, secevent.Event{
				Node: node, Kind: secevent.SSHLoginFailed, User: l.User, OccurredAt: at, ReceivedAt: at,
				Remote: &secevent.Remote{IP: netip.MustParseAddr(l.IP), Port: l.Port},
			})
		default:
			windows = append(windows, metrics.Aggregate{Node: node, Metric: l.Metric, Labels: l.Labels, Start: at, Avg: l.Value, Min: l.Value, Max: l.Value})
		}
	}
	if err := lines.Err(); err != nil {
		t.Fatal(err)
	}
	if err := r.store.Write(t.Context(), org, windows, nil); err != nil {
		t.Fatal(err)
	}
	r.samples = slices.SortedStableFunc(slices.Values(windows), func(a, b metrics.Aggregate) int { return a.Start.Compare(b.Start) })
	return r
}

// lastSeen is the node's last contact at now as the server records it: at most every 30 s
// (ingest.Config.SeenInterval) while it is online.
func (r *replay) lastSeen(node identity.NodeID, now time.Time) *time.Time {
	const every = 30 * time.Second
	var seen *time.Time
	for i, c := range r.online[node] {
		if c.at.After(now) || !c.online {
			continue
		}
		until := now
		if i+1 < len(r.online[node]) && r.online[node][i+1].at.Before(now) {
			until = r.online[node][i+1].at.Add(-time.Second)
		}
		at := r.start.Add(until.Sub(r.start).Truncate(every))
		seen = &at
	}
	return seen
}

// Received implements alerting.EventReader over the events that arrived by now.
type replayEvents struct {
	r   *replay
	now *time.Time
}

func (e replayEvents) Received(_ context.Context, _ tenant.Scope, q alerting.EventQuery) ([]secevent.Event, error) {
	var out []secevent.Event
	for _, ev := range slices.Backward(e.r.events) {
		if ev.Kind == q.Kind && slices.Contains(q.Nodes, ev.Node) && !ev.ReceivedAt.Before(q.Since) && !ev.ReceivedAt.After(*e.now) {
			out = append(out, ev)
		}
	}
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// transition is a change of an instance during the replay.
type transition struct {
	key alert.Key
	alert.Transition
}

// run evaluates the default rule every alerting.Interval over span like the engine does and
// returns the transitions.
func (r *replay) run(t *testing.T, id alert.RuleID, span time.Duration) []transition {
	t.Helper()
	defs, err := ruleset.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(defs, func(d ruleset.Definition) bool { return d.Rule.ID == id })
	if i < 0 {
		t.Fatalf("no default rule %s", id)
	}
	rule := defs[i].Rule
	instances := map[alert.Key]*alert.Instance{}
	var out []transition
	var now time.Time
	events := replayEvents{r: r, now: &now}
	// The engine reads through the server's reader, which picks the source by the server clock:
	// short windows come from the ring, which the ingest fills as the values arrive.
	ring := metrics.NewRing()
	reader := metrics.NewReader(r.store, ring, func() time.Time { return now })
	arrived := 0
	for now = r.start.Add(alerting.Interval); !now.After(r.start.Add(span)); now = now.Add(alerting.Interval) {
		for ; arrived < len(r.samples) && !r.samples[arrived].Start.After(now); arrived++ {
			a := r.samples[arrived]
			ring.Add(r.org, r.group, a.Node, a.Metric, a.Labels, a.Start, a.Avg)
		}
		var nodes []fleet.Node
		for _, n := range r.nodes {
			nodes = append(nodes, fleet.Node{ID: n, LastSeenAt: r.lastSeen(n, now)})
		}
		var open []alert.Key
		for k := range instances {
			open = append(open, k)
		}
		results, err := rule.Evaluator.Evaluate(t.Context(), alerting.Env{Scope: r.scope, Nodes: nodes, Now: now, Metrics: reader, Events: events, Open: open})
		if err != nil {
			t.Fatal(err)
		}
		for _, res := range results {
			key := alert.Key{Rule: rule.ID, Node: res.Node, Dimension: res.Dimension}
			inst, ok := instances[key]
			if !ok {
				inst = alert.NewInstance(key, now)
				instances[key] = inst
			}
			for _, tr := range inst.Observe(res.Observation, now, rule.Timing) {
				out = append(out, transition{key, tr})
			}
		}
	}
	return out
}

// firstTo is the time since the start at which an instance of node first changed to state.
func (r *replay) firstTo(trs []transition, node identity.NodeID, state alert.State) (time.Duration, bool) {
	for _, tr := range trs {
		if tr.key.Node == node && tr.Kind == alert.StateChanged && tr.To == state {
			return tr.At.Sub(r.start), true
		}
	}
	return 0, false
}

// TestAgentBinary_ScenariosFireTheirRules plays every scenario of the simulator (M2-30) in fast
// motion through the default rule it is made for: the samples and events come from
// `sermon-agent simulate --dump`, the rules and the state machine are the server's.
func TestAgentBinary_ScenariosFireTheirRules(t *testing.T) {
	bin := agentBinary(t)

	t.Run("cpu-high fires, holds between threshold and clear, resolves", func(t *testing.T) {
		r := loadDump(t, bin, 1, "40m", "cpu-high")
		trs := r.run(t, "system.cpu.usage_high", 40*time.Minute)
		critical, fired := r.firstTo(trs, r.nodes[0], alert.StateCritical)
		resolved, ok := r.firstTo(trs, r.nodes[0], alert.StateResolved)
		// Over the threshold for 15 minutes, held for 10, calm after 25: the alert resolves
		// only once the 10-minute average is above the clear level.
		if !fired || critical > 15*time.Minute || !ok || resolved < 25*time.Minute || resolved > 40*time.Minute {
			t.Fatalf("critical at %v (%v), resolved at %v (%v): %+v", critical, fired, resolved, ok, trs)
		}
		warning, held := r.firstTo(trs, r.nodes[0], alert.StateWarning)
		if !held || warning < critical || warning > resolved {
			t.Errorf("no hold at warning between critical and resolved: %+v", trs)
		}
	})

	t.Run("disk-filling forecasts a full disk", func(t *testing.T) {
		r := loadDump(t, bin, 1, "2h", "disk-filling")
		trs := r.run(t, "system.filesystem.full_soon", 2*time.Hour)
		// One hour of trend, then 30 minutes over the threshold.
		if at, ok := r.firstTo(trs, r.nodes[0], alert.StateCritical); !ok || at > 100*time.Minute {
			t.Fatalf("critical at %v (%v): %+v", at, ok, trs)
		}
	})

	t.Run("flapping is detected", func(t *testing.T) {
		r := loadDump(t, bin, 1, "30m", "flapping")
		trs := r.run(t, "system.filesystem.usage_high", 30*time.Minute)
		if !slices.ContainsFunc(trs, func(tr transition) bool { return tr.Kind == alert.FlappingStarted }) {
			t.Fatalf("no flapping: %+v", trs)
		}
	})

	t.Run("ssh-bruteforce fires on every node and across the fleet", func(t *testing.T) {
		r := loadDump(t, bin, 3, "6m", "ssh-bruteforce")
		brute := r.run(t, "security.ssh.bruteforce", 6*time.Minute)
		fleetWide := r.run(t, "security.ssh.fleet_correlation", 6*time.Minute)
		for _, n := range r.nodes {
			if _, ok := r.firstTo(brute, n, alert.StateCritical); !ok {
				t.Errorf("node %v: no critical brute force: %+v", n, brute)
			}
			if _, ok := r.firstTo(fleetWide, n, alert.StateWarning); !ok {
				t.Errorf("node %v: no fleet correlation: %+v", n, fleetWide)
			}
		}
	})

	t.Run("offline fires while silent and resolves on return", func(t *testing.T) {
		r := loadDump(t, bin, 1, "3m", "offline")
		trs := r.run(t, "system.node.offline", 3*time.Minute)
		critical, fired := r.firstTo(trs, r.nodes[0], alert.StateCritical)
		resolved, ok := r.firstTo(trs, r.nodes[0], alert.StateResolved)
		if !fired || critical > 2*time.Minute || !ok || resolved < 2*time.Minute {
			t.Fatalf("critical at %v (%v), resolved at %v (%v): %+v", critical, fired, resolved, ok, trs)
		}
	})
}

// TestAgentBinary_ScenariosAgainstTheServer runs `ssh-bruteforce` and `offline` live: three
// virtual nodes send parsed failed logins, the server's own engine opens the brute force and
// fleet alerts, then the nodes fall silent, are reported offline and come back (M2-30).
func TestAgentBinary_ScenariosAgainstTheServer(t *testing.T) {
	bin := agentBinary(t)
	srv := startTestServer(t, nil)
	defer srv.stop()
	const nodes = 3
	org := seedEnrollmentTokenWithUses(t, nodes)

	dir := t.TempDir()
	_, agentPort, _ := strings.Cut(srv.agent, ":")
	config := filepath.Join(dir, "agent.toml")
	toml := "[server]\nurl = \"http://" + srv.web + "\"\nagent_url = \"https://localhost:" + agentPort + "\"\n" +
		"[paths]\nstate_dir = \"" + filepath.Join(dir, "state") + "\"\n"
	if err := os.WriteFile(config, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	logs := &syncBuffer{}
	simulate := exec.CommandContext(ctx, bin, "simulate", "--config", config, "--nodes", strconv.Itoa(nodes), "--seed", "42",
		"--scenario", "ssh-bruteforce", "--scenario", "offline")
	simulate.Env = append(os.Environ(), "SERMON_ENROLLMENT_TOKEN="+fixtureToken)
	simulate.Stdout, simulate.Stderr = logs, logs
	if err := simulate.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = simulate.Process.Kill() }()

	conn, err := pgx.Connect(ctx, pgtest.Shared(t).OwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	// count is the number of nodes with an instance of rule in one of states.
	count := func(rule string, states ...alert.State) int {
		var n int
		s := make([]int16, len(states))
		for i, st := range states {
			s[i] = int16(st)
		}
		if err := conn.QueryRow(ctx, `SELECT count(DISTINCT node_id) FROM alert_instances
			WHERE org_id = $1 AND rule_id = $2 AND state = ANY($3)`, org, rule, s).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	waitUntil := func(what string, limit time.Duration, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(limit)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("%s did not happen within %v\nsimulator:\n%s\nserver:\n%s", what, limit, logs.String(), srv.logs.String())
			}
			time.Sleep(time.Second)
		}
	}
	firing := []alert.State{alert.StateWarning, alert.StateCritical}
	// The 30 s online at the start are enough for 15 failed logins per node and the fleet.
	waitUntil("the fleet correlation", 60*time.Second, func() bool {
		return count("security.ssh.fleet_correlation", firing...) == nodes
	})
	// Then 90 s of silence (30 s to 120 s after the start): offline after 60 s without contact,
	// which can be seen from about 60 s on (the last contact is written at most every 30 s).
	waitUntil("offline", 120*time.Second, func() bool { return count("system.node.offline", alert.StateCritical) == nodes })
	// Back at 120 s; the wait covers the rest of the silence plus reconnect and an engine round.
	waitUntil("the return", 100*time.Second, func() bool { return count("system.node.offline", alert.StateResolved, alert.StateOK) == nodes })
	if !strings.Contains(logs.String(), "virtual node goes silent") {
		t.Errorf("the simulator did not log the silence:\n%s", logs.String())
	}
	if strings.Contains(srv.logs.String(), "level=ERROR") {
		t.Errorf("server logged errors:\n%s", srv.logs.String())
	}

	if err := simulate.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := simulate.Wait(); err != nil {
		t.Errorf("simulator exit: %v\n%s", err, logs.String())
	}
}
