package routing_test

import (
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/alerting"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/app/routing"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
)

var (
	t0    = time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	web   = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001"))
	db    = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000002"))
	alice = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000e1"))
	cpu   = alert.Key{Rule: "system.cpu.usage_high", Node: web}
	ssh   = alert.Key{Rule: "security.ssh.bruteforce", Node: db, Dimension: "ip=192.0.2.7\x1f"}
	pOps  = routing.PolicyID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000f1"))
	pSec  = routing.PolicyID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000f2"))
)

// target knows the rules' labels and the nodes' groups.
func target(k alert.Key) routing.Target {
	t := routing.Target{Rule: k.Rule, Group: groupA, Labels: map[string]string{"category": "cpu"}}
	if k.Rule == ssh.Rule {
		t.Group, t.Labels = groupB, map[string]string{"category": "security"}
	}
	return t
}

func ops() routing.Policy {
	return routing.Policy{ID: pOps, Name: "ops", Enabled: true, Channels: []notify.ChannelID{chanA}, Grouping: routing.GroupByNode}
}

func security() routing.Policy {
	return routing.Policy{
		ID: pSec, Name: "security", Enabled: true, Channels: []notify.ChannelID{chanB}, Grouping: routing.GroupDigest,
		Match: routing.Matchers{Rules: []string{"security.*"}},
	}
}

func firing(k alert.Key, s alert.State, firedAt time.Time) alert.Instance {
	return alert.Instance{Key: k, State: s, Since: firedAt, FiredAt: firedAt, Value: 0.97}
}

func changed(k alert.Key, from, to alert.State, at time.Time) alerting.Record {
	return alerting.Record{Key: k, Transition: alert.Transition{Kind: alert.StateChanged, From: from, To: to, At: at, Value: 0.97}}
}

type sent struct {
	channel notify.ChannelID
	key     alert.Key
	kind    notify.Kind
	reason  notify.Reason
}

func sends(out routing.Output) []sent {
	var s []sent
	for _, x := range out.Sends {
		s = append(s, sent{x.Channel, x.Event.Key, x.Event.Kind, x.Event.Reason})
	}
	return s
}

func want(t *testing.T, out routing.Output, expected ...sent) {
	t.Helper()
	got := sends(out)
	if !slices.Equal(got, expected) {
		t.Errorf("sends\n got %+v\nwant %+v", got, expected)
	}
}

func TestPlan_RoutesChangesToMatchingPolicies(t *testing.T) {
	out := routing.Plan(routing.Input{
		Now: t0, Policies: []routing.Policy{ops(), security()}, Target: target,
		Records: []alerting.Record{
			changed(cpu, alert.StateOK, alert.StatePending, t0), // pending is not notified
			changed(ssh, alert.StateOK, alert.StateWarning, t0),
		},
		Instances: []alert.Instance{firing(ssh, alert.StateWarning, t0), {Key: cpu, State: alert.StatePending, Since: t0}},
	})
	// ops matches everything, security only its rules.
	want(t, out, sent{chanA, ssh, notify.Firing, ""}, sent{chanB, ssh, notify.Firing, ""})
	if out.Sends[0].Grouping != routing.GroupByNode || out.Sends[1].Grouping != routing.GroupDigest || out.Sends[0].Event.Severity != notify.Warning {
		t.Errorf("sends %+v", out.Sends)
	}
	// Both policies remember the time for repeats.
	if len(out.Save) != 2 || !out.Save[0].LastSent.Equal(t0) || out.Save[0].Escalated {
		t.Errorf("save %+v", out.Save)
	}
}

func TestPlan_WithoutPoliciesAllEnabledChannels(t *testing.T) {
	in := routing.Input{
		Now: t0, Fallback: []notify.ChannelID{chanA, chanC}, Target: target,
		Records:   []alerting.Record{changed(cpu, alert.StatePending, alert.StateCritical, t0)},
		Instances: []alert.Instance{firing(cpu, alert.StateCritical, t0)},
	}
	out := routing.Plan(in)
	want(t, out, sent{chanA, cpu, notify.Firing, ""}, sent{chanC, cpu, notify.Firing, ""})
	if len(out.Save) != 0 || out.Sends[0].Event.Severity != notify.Critical || out.Sends[0].Grouping != routing.GroupByNode {
		t.Errorf("fallback keeps no state: %+v / %+v", out.Save, out.Sends)
	}
	// The fallback never repeats.
	in.Now, in.Records = t0.Add(48*time.Hour), nil
	want(t, routing.Plan(in))
}

// Each channel hears of a change once, with the grouping of the first policy that names it.
func TestPlan_OneMessagePerChannel(t *testing.T) {
	sec := security()
	sec.Channels = []notify.ChannelID{chanA, chanB}
	out := routing.Plan(routing.Input{
		Now: t0, Policies: []routing.Policy{sec, ops()}, Target: target,
		Records:   []alerting.Record{changed(ssh, alert.StateOK, alert.StateWarning, t0)},
		Instances: []alert.Instance{firing(ssh, alert.StateWarning, t0)},
	})
	want(t, out, sent{chanA, ssh, notify.Firing, ""}, sent{chanB, ssh, notify.Firing, ""})
	if out.Sends[0].Grouping != routing.GroupDigest {
		t.Errorf("grouping %s", out.Sends[0].Grouping)
	}
}

func TestPlan_ResolvedEndsTheState(t *testing.T) {
	p := ops()
	p.Escalation = &routing.Escalation{After: 15 * time.Minute, Channels: []notify.ChannelID{chanC}}
	out := routing.Plan(routing.Input{
		Now: t0.Add(time.Hour), Policies: []routing.Policy{p}, Target: target,
		Records: []alerting.Record{changed(cpu, alert.StateCritical, alert.StateResolved, t0.Add(time.Hour))},
		States:  []routing.State{{StateKey: routing.StateKey{Key: cpu, Policy: pOps}, LastSent: t0, Escalated: true}},
	})
	// The escalation's channels learn that it is over, too.
	want(t, out, sent{chanA, cpu, notify.Resolved, ""}, sent{chanC, cpu, notify.Resolved, ""})
	if out.Sends[0].Event.Severity != notify.Critical || len(out.Delete) != 1 || out.Delete[0].Key != cpu || len(out.Save) != 0 {
		t.Errorf("severity %s, delete %+v, save %+v", out.Sends[0].Event.Severity, out.Delete, out.Save)
	}
}

func TestPlan_RepeatsWhileFiringAndUnacknowledged(t *testing.T) {
	p := ops()
	p.Repeat = time.Hour
	inst := firing(cpu, alert.StateCritical, t0)
	state := routing.State{StateKey: routing.StateKey{Key: cpu, Policy: pOps}, LastSent: t0}
	in := routing.Input{Policies: []routing.Policy{p}, Target: target, Instances: []alert.Instance{inst}, States: []routing.State{state}}

	in.Now = t0.Add(59 * time.Minute)
	want(t, routing.Plan(in))

	in.Now = t0.Add(time.Hour)
	out := routing.Plan(in)
	want(t, out, sent{chanA, cpu, notify.Firing, notify.ReasonRepeat})
	if out.Sends[0].Event.FiringFor != time.Hour || len(out.Save) != 1 || !out.Save[0].LastSent.Equal(in.Now) {
		t.Errorf("firing for %v, save %+v", out.Sends[0].Event.FiringFor, out.Save)
	}

	// The next repeat counts from the last one.
	in.States, in.Now = out.Save, t0.Add(90*time.Minute)
	want(t, routing.Plan(in))

	// Acknowledged: no more repeats.
	inst.Ack = &alert.Ack{By: alice, At: t0.Add(time.Minute)}
	in.Instances, in.Now = []alert.Instance{inst}, t0.Add(5*time.Hour)
	want(t, routing.Plan(in))
}

// A policy created while an alert fires counts from when it fired.
func TestPlan_RepeatWithoutStateCountsFromFiring(t *testing.T) {
	p := ops()
	p.Repeat = time.Hour
	out := routing.Plan(routing.Input{Now: t0.Add(2 * time.Hour), Policies: []routing.Policy{p}, Target: target, Instances: []alert.Instance{firing(cpu, alert.StateWarning, t0)}})
	want(t, out, sent{chanA, cpu, notify.Firing, notify.ReasonRepeat})
}

func TestPlan_EscalatesOnceWithoutAcknowledgement(t *testing.T) {
	p := ops()
	p.Repeat = time.Hour
	p.Escalation = &routing.Escalation{After: 15 * time.Minute, Channels: []notify.ChannelID{chanA, chanC}}
	inst := firing(cpu, alert.StateCritical, t0)
	in := routing.Input{
		Policies: []routing.Policy{p}, Target: target, Instances: []alert.Instance{inst},
		States: []routing.State{{StateKey: routing.StateKey{Key: cpu, Policy: pOps}, LastSent: t0}},
	}
	in.Now = t0.Add(14 * time.Minute)
	want(t, routing.Plan(in))

	in.Now = t0.Add(15 * time.Minute)
	out := routing.Plan(in)
	// chanA has the alert already; only the new channel hears of it.
	want(t, out, sent{chanC, cpu, notify.Firing, notify.ReasonEscalation})
	if len(out.Save) != 1 || !out.Save[0].Escalated || !out.Save[0].LastSent.Equal(t0) {
		t.Errorf("save %+v", out.Save)
	}

	in.States, in.Now = out.Save, t0.Add(20*time.Minute)
	want(t, routing.Plan(in))

	// Repeats go to the escalation's channels as well.
	in.Now = t0.Add(time.Hour)
	want(t, routing.Plan(in), sent{chanA, cpu, notify.Firing, notify.ReasonRepeat}, sent{chanC, cpu, notify.Firing, notify.ReasonRepeat})
}

func TestPlan_AcknowledgementStopsTheEscalation(t *testing.T) {
	p := ops()
	p.Escalation = &routing.Escalation{After: 15 * time.Minute, Channels: []notify.ChannelID{chanC}}
	inst := firing(cpu, alert.StateCritical, t0)
	inst.Ack = &alert.Ack{By: alice, At: t0.Add(10 * time.Minute)}
	out := routing.Plan(routing.Input{
		Now: t0.Add(10 * time.Minute), Policies: []routing.Policy{p}, Target: target, Instances: []alert.Instance{inst},
		Records: []alerting.Record{{Key: cpu, Transition: alert.Transition{Kind: alert.Acknowledged, From: alert.StateCritical, To: alert.StateCritical, At: t0.Add(10 * time.Minute), By: alice}}},
	})
	want(t, out, sent{chanA, cpu, notify.Acknowledged, ""})
	if out.Sends[0].Event.By != alice {
		t.Errorf("by %v", out.Sends[0].Event.By)
	}
	out = routing.Plan(routing.Input{Now: t0.Add(time.Hour), Policies: []routing.Policy{p}, Target: target, Instances: []alert.Instance{inst}})
	want(t, out)
}

func TestPlan_ThrottlesWhileFlapping(t *testing.T) {
	at := t0.Add(time.Minute)
	flipping := firing(cpu, alert.StateWarning, at)
	flipping.Flapping = true
	// The change that makes the alert flap is sent, with the notice; the next ones are not.
	out := routing.Plan(routing.Input{
		Now: at, Policies: []routing.Policy{ops()}, Target: target, Instances: []alert.Instance{flipping},
		Records: []alerting.Record{
			changed(cpu, alert.StateResolved, alert.StateWarning, at),
			{Key: cpu, Transition: alert.Transition{Kind: alert.FlappingStarted, From: alert.StateWarning, To: alert.StateWarning, At: at}},
		},
	})
	want(t, out, sent{chanA, cpu, notify.Firing, ""}, sent{chanA, cpu, notify.FlappingStarted, ""})

	resolved := flipping
	resolved.State = alert.StateResolved
	out = routing.Plan(routing.Input{
		Now: at.Add(time.Minute), Policies: []routing.Policy{ops()}, Target: target, Instances: []alert.Instance{resolved},
		Records: []alerting.Record{changed(cpu, alert.StateWarning, alert.StateResolved, at.Add(time.Minute))},
	})
	want(t, out)

	// Stable again: the notice comes, and the next change is sent again.
	stable := resolved
	stable.Flapping = false
	out = routing.Plan(routing.Input{
		Now: at.Add(time.Hour), Policies: []routing.Policy{ops()}, Target: target, Instances: []alert.Instance{stable},
		Records: []alerting.Record{
			{Key: cpu, Transition: alert.Transition{Kind: alert.FlappingStopped, From: alert.StateResolved, To: alert.StateResolved, At: at.Add(time.Hour)}},
			changed(cpu, alert.StateResolved, alert.StateOK, at.Add(time.Hour)),
		},
	})
	want(t, out, sent{chanA, cpu, notify.FlappingStopped, ""})
}

func TestPlan_CleansUpStates(t *testing.T) {
	gone := routing.PolicyID(uuid.New())
	out := routing.Plan(routing.Input{
		Now: t0.Add(time.Minute), Policies: []routing.Policy{ops()}, Target: target,
		Instances: []alert.Instance{firing(cpu, alert.StateWarning, t0), {Key: ssh, State: alert.StateResolved, Since: t0}},
		States: []routing.State{
			{StateKey: routing.StateKey{Key: cpu, Policy: pOps}, LastSent: t0},                                                    // stays
			{StateKey: routing.StateKey{Key: cpu, Policy: gone}, LastSent: t0},                                                    // policy deleted or disabled
			{StateKey: routing.StateKey{Key: ssh, Policy: pOps}, LastSent: t0},                                                    // no longer firing
			{StateKey: routing.StateKey{Key: alert.Key{Rule: "system.memory.usage_high", Node: web}, Policy: pOps}, LastSent: t0}, // instance gone
		},
	})
	want(t, out)
	if len(out.Delete) != 3 || slices.ContainsFunc(out.Delete, func(k routing.StateKey) bool { return k.Key == cpu && k.Policy == pOps }) {
		t.Errorf("delete %+v", out.Delete)
	}
}
