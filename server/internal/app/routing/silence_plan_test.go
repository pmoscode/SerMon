package routing_test

import (
	"slices"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/alerting"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/app/routing"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// webSilence mutes everything of web-01 from t0 for an hour.
func webSilence() routing.Silence {
	return routing.Silence{Match: routing.Matchers{Groups: []tenant.GroupID{groupA}}, Nodes: []identity.NodeID{web}, StartsAt: t0, EndsAt: t0.Add(time.Hour), Comment: "patch"}
}

func escalating() routing.Policy {
	p := ops()
	p.Repeat = time.Hour
	p.Escalation = &routing.Escalation{After: 15 * time.Minute, Channels: []notify.ChannelID{chanC}}
	return p
}

func TestPlan_SilenceHoldsBackAndCatchesUp(t *testing.T) {
	inst := firing(cpu, alert.StateCritical, t0.Add(time.Minute))
	in := routing.Input{
		Now: t0.Add(time.Minute), Policies: []routing.Policy{escalating()}, Target: target, Silences: []routing.Silence{webSilence()},
		Records:   []alerting.Record{changed(cpu, alert.StateOK, alert.StateCritical, t0.Add(time.Minute)), changed(ssh, alert.StateOK, alert.StateWarning, t0.Add(time.Minute))},
		Instances: []alert.Instance{inst, firing(ssh, alert.StateWarning, t0.Add(time.Minute))},
	}
	out := routing.Plan(in)
	// The silence names web-01 only: db-01's alert goes out.
	want(t, out, sent{chanA, ssh, notify.Firing, ""})
	var held routing.State
	for _, s := range out.Save {
		if s.Key == cpu {
			held = s
		}
	}
	if !held.Muted || !held.LastSent.IsZero() {
		t.Fatalf("held back: %+v", held)
	}

	// While silenced: no reminder, no escalation.
	in.Records, in.Instances, in.States, in.Now = nil, []alert.Instance{inst}, []routing.State{held}, t0.Add(59*time.Minute)
	want(t, routing.Plan(in))

	// The silence ended: the alert comes once, saying so; the escalation counts from now.
	in.Now = t0.Add(time.Hour)
	out = routing.Plan(in)
	want(t, out, sent{chanA, cpu, notify.Firing, notify.ReasonUnmuted})
	if s := out.Save[0]; s.Muted || !s.LastSent.Equal(in.Now) || !s.FirstSent.Equal(in.Now) {
		t.Errorf("caught up: %+v", s)
	}
	in.States, in.Now = out.Save, t0.Add(time.Hour+14*time.Minute)
	want(t, routing.Plan(in))
	in.Now = t0.Add(time.Hour + 15*time.Minute)
	want(t, routing.Plan(in), sent{chanC, cpu, notify.Firing, notify.ReasonEscalation})
}

func TestPlan_SilenceMutesRemindersOfKnownAlerts(t *testing.T) {
	inst := firing(cpu, alert.StateCritical, t0.Add(-2*time.Hour))
	known := routing.State{StateKey: routing.StateKey{Key: cpu, Policy: pOps}, LastSent: t0.Add(-2 * time.Hour), FirstSent: t0.Add(-2 * time.Hour)}
	in := routing.Input{Now: t0.Add(time.Minute), Policies: []routing.Policy{escalating()}, Target: target, Silences: []routing.Silence{webSilence()}, Instances: []alert.Instance{inst}, States: []routing.State{known}}
	out := routing.Plan(in)
	want(t, out)
	if len(out.Save) != 0 || len(out.Delete) != 0 {
		t.Errorf("a silence alone changes no state: %+v", out)
	}
	// Known alerts still end audibly; their acknowledgement is held back.
	in.Records = []alerting.Record{{Key: cpu, Transition: alert.Transition{Kind: alert.Acknowledged, From: alert.StateCritical, To: alert.StateCritical, At: in.Now, By: alice}}}
	want(t, routing.Plan(in))
	in.Records, in.Instances = []alerting.Record{changed(cpu, alert.StateCritical, alert.StateResolved, in.Now)}, nil
	out = routing.Plan(in)
	want(t, out, sent{chanA, cpu, notify.Resolved, ""})
	if len(out.Delete) != 1 {
		t.Errorf("delete %+v", out.Delete)
	}
}

// What nobody heard of ends quietly.
func TestPlan_MutedAlertEndsQuietly(t *testing.T) {
	held := routing.State{StateKey: routing.StateKey{Key: cpu, Policy: pOps}, Muted: true}
	for _, silences := range [][]routing.Silence{{webSilence()}, nil} {
		out := routing.Plan(routing.Input{
			Now: t0.Add(30 * time.Minute), Policies: []routing.Policy{ops()}, Target: target, Silences: silences,
			Records: []alerting.Record{changed(cpu, alert.StateCritical, alert.StateResolved, t0.Add(30*time.Minute))},
			States:  []routing.State{held},
		})
		want(t, out)
		if len(out.Delete) != 1 {
			t.Errorf("silences %d: delete %+v", len(silences), out.Delete)
		}
	}
}

func TestPlan_SilenceOutsideItsPeriodMutesNothing(t *testing.T) {
	for _, at := range []time.Time{t0.Add(-time.Second), t0.Add(time.Hour)} {
		out := routing.Plan(routing.Input{
			Now: at, Policies: []routing.Policy{ops()}, Target: target, Silences: []routing.Silence{webSilence()},
			Records:   []alerting.Record{changed(cpu, alert.StateOK, alert.StateWarning, at)},
			Instances: []alert.Instance{firing(cpu, alert.StateWarning, at)},
		})
		want(t, out, sent{chanA, cpu, notify.Firing, ""})
	}
}

func TestPlan_MaintenanceWindowMutesRebootAndOffline(t *testing.T) {
	offline := alert.Key{Rule: "system.node.offline", Node: web}
	window := routing.MaintenanceWindow{
		Name: "always", Enabled: true, Groups: []tenant.GroupID{groupA},
		Schedule: routing.Schedule{Days: []time.Weekday{t0.Weekday()}, Start: 0, Duration: 24 * time.Hour, TimeZone: "UTC"},
	}
	out := routing.Plan(routing.Input{
		Now: t0, Fallback: []notify.ChannelID{chanA}, Target: target, Windows: []routing.MaintenanceWindow{window},
		Records:   []alerting.Record{changed(offline, alert.StateOK, alert.StateCritical, t0), changed(cpu, alert.StateOK, alert.StateWarning, t0)},
		Instances: []alert.Instance{firing(offline, alert.StateCritical, t0), firing(cpu, alert.StateWarning, t0)},
	})
	want(t, out, sent{chanA, cpu, notify.Firing, ""})
	// The fallback remembers what it held back, so it can catch up after the window.
	muted := 0
	for _, s := range out.Save {
		if s.Key == offline && s.Muted && s.Policy == (routing.PolicyID{}) {
			muted++
		}
	}
	if muted != 1 {
		t.Errorf("save %+v", out.Save)
	}
	// During the window the held-back state stays.
	during := routing.Plan(routing.Input{
		Now: t0.Add(time.Hour), Fallback: []notify.ChannelID{chanA}, Target: target, Windows: []routing.MaintenanceWindow{window},
		Instances: []alert.Instance{firing(offline, alert.StateCritical, t0)}, States: out.Save,
	})
	if len(during.Sends) != 0 || slices.ContainsFunc(during.Delete, func(k routing.StateKey) bool { return k.Key == offline }) {
		t.Errorf("during the window: %+v", during)
	}
	after := routing.Plan(routing.Input{
		Now: t0.Add(24 * time.Hour), Fallback: []notify.ChannelID{chanA}, Target: target, Windows: []routing.MaintenanceWindow{window},
		Instances: []alert.Instance{firing(offline, alert.StateCritical, t0)}, States: out.Save,
	})
	want(t, after, sent{chanA, offline, notify.Firing, notify.ReasonUnmuted})
}
