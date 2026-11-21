package routing

import (
	"slices"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/alerting"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
)

// StateKey names what a policy remembers about an instance.
type StateKey struct {
	Key    alert.Key
	Policy PolicyID
}

// State is what a policy (or the fallback, with the zero ID) remembers about a firing instance:
// when it first and last notified (escalation, repeats), whether it escalated, and whether a
// silence or maintenance window held its firing back (Muted: it is sent once that ends).
type State struct {
	StateKey
	LastSent  time.Time
	FirstSent time.Time
	Escalated bool
	Muted     bool
}

// Event is something to tell about an instance.
type Event struct {
	Key       alert.Key
	Kind      notify.Kind
	Severity  notify.Severity
	At        time.Time
	Value     float64
	Fields    alert.Fields
	By        identity.UserID // Acknowledged
	Reason    notify.Reason
	FiringFor time.Duration // reminders and escalations
}

// Send is an event for a channel, grouped as the policy that chose the channel says.
type Send struct {
	Channel  notify.ChannelID
	Grouping Grouping
	Event    Event
}

// Input is what a round of routing looks at.
type Input struct {
	Now time.Time
	// Policies are the enabled policies in order.
	Policies []Policy
	// Fallback are the enabled channels; they get every alert while the organization has no
	// policies (the router passes none otherwise).
	Fallback []notify.ChannelID
	// Records are the round's transitions, Instances the instances after it.
	Records   []alerting.Record
	Instances []alert.Instance
	States    []State
	// Silences and Windows mute matching alerts while they are active.
	Silences []Silence
	Windows  []MaintenanceWindow
	// ChangesOnly routes the records alone: no reminders, escalations or clean-up (a round of
	// the API, such as an acknowledgement, next to the engine's rounds).
	ChangesOnly bool
	// Target describes an instance for the matchers (without node and severity).
	Target func(alert.Key) Target
}

// Output is what to send and what to remember.
type Output struct {
	Sends  []Send
	Save   []State
	Delete []StateKey
}

func severityOf(s alert.State) notify.Severity {
	if s == alert.StateCritical {
		return notify.Critical
	}
	return notify.Warning
}

// eventOf turns a transition into an event; false for what is not notified (pending, OK).
func eventOf(r alerting.Record) (Event, bool) {
	t := r.Transition
	e := Event{Key: r.Key, Severity: severityOf(t.To), At: t.At, Value: t.Value, Fields: t.Fields}
	switch t.Kind {
	case alert.StateChanged:
		switch {
		case t.To.Firing():
			e.Kind = notify.Firing
		case t.To == alert.StateResolved:
			e.Kind, e.Severity = notify.Resolved, severityOf(t.From)
		default:
			return Event{}, false
		}
	case alert.Acknowledged:
		e.Kind, e.By = notify.Acknowledged, t.By
	case alert.FlappingStarted:
		e.Kind = notify.FlappingStarted
	case alert.FlappingStopped:
		e.Kind = notify.FlappingStopped
	default:
		return Event{}, false
	}
	return e, true
}

type planner struct {
	in       Input
	out      Output
	states   map[StateKey]State
	dirty    map[StateKey]bool
	policies []Policy
	sent     map[sendKey]bool
}

type sendKey struct {
	channel notify.ChannelID
	key     alert.Key
	kind    notify.Kind
}

// Plan decides one round: the changes go to the channels of the matching policies (unless the
// instance flaps), firing instances nobody acknowledged repeat and escalate, and states of what
// no longer fires are dropped.
func Plan(in Input) Output {
	p := &planner{in: in, states: map[StateKey]State{}, dirty: map[StateKey]bool{}, sent: map[sendKey]bool{}, policies: in.Policies}
	if len(in.Policies) == 0 && len(in.Fallback) > 0 {
		p.policies = []Policy{{Channels: in.Fallback, Grouping: GroupByNode}}
	}
	for _, s := range in.States {
		p.states[s.StateKey] = s
	}
	instances := make(map[alert.Key]alert.Instance, len(in.Instances))
	for _, i := range in.Instances {
		instances[i.Key] = i
	}

	touched := p.changes(instances)
	if in.ChangesOnly {
		return p.out
	}
	for _, i := range in.Instances {
		if i.State.Firing() && i.Ack == nil && !touched[i.Key] {
			p.timers(i)
		}
	}
	p.cleanUp(instances)
	return p.out
}

// changes routes the records and returns the instances they concern.
func (p *planner) changes(instances map[alert.Key]alert.Instance) map[alert.Key]bool {
	touched := map[alert.Key]bool{}
	flapping := map[alert.Key]bool{}
	for _, r := range p.in.Records {
		if _, seen := touched[r.Key]; !seen {
			flapping[r.Key] = flappingBefore(r.Key, instances[r.Key].Flapping, p.in.Records)
		}
		touched[r.Key] = true
		e, ok := eventOf(r)
		switch r.Transition.Kind {
		case alert.FlappingStarted:
			flapping[r.Key] = true
		case alert.FlappingStopped:
			flapping[r.Key] = false
		case alert.StateChanged, alert.Acknowledged:
		}
		if !ok || flapping[r.Key] && (e.Kind == notify.Firing || e.Kind == notify.Resolved) {
			continue
		}
		p.route(e)
	}
	return touched
}

// flappingBefore is whether the instance flapped before the round: its flag now, undone by the
// round's first flapping record.
func flappingBefore(key alert.Key, now bool, records []alerting.Record) bool {
	for _, r := range records {
		if r.Key != key {
			continue
		}
		switch r.Transition.Kind {
		case alert.FlappingStarted:
			return false
		case alert.FlappingStopped:
			return true
		case alert.StateChanged, alert.Acknowledged:
		}
	}
	return now
}

func (p *planner) matching(key alert.Key, s notify.Severity) []Policy {
	t := p.in.Target(key)
	t.Node, t.Severity = key.Node, s
	var out []Policy
	for _, pol := range p.policies {
		if pol.Match.Match(t) {
			out = append(out, pol)
		}
	}
	return out
}

// muted reports whether an active silence or maintenance window mutes the instance.
func (p *planner) muted(key alert.Key, s notify.Severity) bool {
	t := p.in.Target(key)
	t.Node, t.Severity = key.Node, s
	for _, sil := range p.in.Silences {
		if sil.ActiveAt(p.in.Now) && sil.Mutes(t) {
			return true
		}
	}
	for _, w := range p.in.Windows {
		if w.Mutes(t, p.in.Now) {
			return true
		}
	}
	return false
}

// route sends a change to the policies' channels (and to the escalation's, once escalated) and
// updates the states. While muted, a firing is held back; an end is still told to channels that
// heard of the alert, and never to those that did not.
func (p *planner) route(e Event) {
	muted := p.muted(e.Key, e.Severity)
	for _, pol := range p.matching(e.Key, e.Severity) {
		k := StateKey{Key: e.Key, Policy: pol.ID}
		st, known := p.states[k]
		st.StateKey = k
		switch e.Kind {
		case notify.Firing:
			if muted {
				st.Muted = true
				p.save(st)
				continue
			}
			p.send(pol, e, p.audience(pol, st))
			p.notified(st)
		case notify.Resolved:
			if !st.Muted && (known || !muted) {
				p.send(pol, e, p.audience(pol, st))
			}
			if known {
				p.drop(k)
			}
		case notify.Acknowledged, notify.FlappingStarted, notify.FlappingStopped, notify.Test, notify.KindDigest:
			if !muted && !st.Muted {
				p.send(pol, e, p.audience(pol, st))
			}
		}
	}
}

// notified records that the channels heard of a firing now.
func (p *planner) notified(st State) {
	st.LastSent, st.Muted = p.in.Now, false
	if st.FirstSent.IsZero() {
		st.FirstSent = p.in.Now
	}
	p.save(st)
}

// timers repeats and escalates a firing, unacknowledged instance, and sends what a silence held
// back once it ended. The escalation counts from when the channels first heard of the alert.
func (p *planner) timers(i alert.Instance) {
	firingFor := p.in.Now.Sub(i.FiredAt)
	e := Event{Key: i.Key, Kind: notify.Firing, Severity: severityOf(i.State), At: p.in.Now, Value: i.Value, Fields: i.Fields, FiringFor: firingFor}
	if p.muted(i.Key, e.Severity) {
		return
	}
	for _, pol := range p.matching(i.Key, e.Severity) {
		k := StateKey{Key: i.Key, Policy: pol.ID}
		st, known := p.states[k]
		st.StateKey = k
		if st.Muted {
			e.Reason = notify.ReasonUnmuted
			p.send(pol, e, p.audience(pol, st))
			p.notified(st)
			continue
		}
		heard := i.FiredAt
		if known && !st.FirstSent.IsZero() {
			heard = st.FirstSent
		}
		if esc := pol.Escalation; esc != nil && !st.Escalated && p.in.Now.Sub(heard) >= esc.After {
			e.Reason = notify.ReasonEscalation
			var fresh []notify.ChannelID
			for _, c := range esc.Channels {
				if !slices.Contains(pol.Channels, c) {
					fresh = append(fresh, c)
				}
			}
			p.send(pol, e, fresh)
			st.Escalated = true
			p.save(st)
		}
		last := st.LastSent
		if !known || last.IsZero() {
			last = i.FiredAt
		}
		if pol.Repeat > 0 && p.in.Now.Sub(last) >= pol.Repeat {
			e.Reason = notify.ReasonRepeat
			p.send(pol, e, p.audience(pol, st))
			st.LastSent = p.in.Now
			p.save(st)
		}
	}
}

// audience is who hears of an instance under a policy: its channels, after an escalation also
// the escalation's.
func (p *planner) audience(pol Policy, st State) []notify.ChannelID {
	out := slices.Clone(pol.Channels)
	if st.Escalated && pol.Escalation != nil {
		for _, c := range pol.Escalation.Channels {
			if !slices.Contains(out, c) {
				out = append(out, c)
			}
		}
	}
	return out
}

func (p *planner) send(pol Policy, e Event, channels []notify.ChannelID) {
	for _, c := range channels {
		k := sendKey{channel: c, key: e.Key, kind: e.Kind}
		if p.sent[k] {
			continue
		}
		p.sent[k] = true
		p.out.Sends = append(p.out.Sends, Send{Channel: c, Grouping: pol.Grouping, Event: e})
	}
}

func (p *planner) save(st State) {
	p.states[st.StateKey] = st
	if !p.dirty[st.StateKey] {
		p.dirty[st.StateKey] = true
		p.out.Save = append(p.out.Save, st)
		return
	}
	for i := range p.out.Save {
		if p.out.Save[i].StateKey == st.StateKey {
			p.out.Save[i] = st
		}
	}
}

func (p *planner) drop(k StateKey) {
	delete(p.states, k)
	p.out.Delete = append(p.out.Delete, k)
}

// cleanUp drops the states of instances that no longer fire and of policies that are gone or
// disabled (and of the fallback once policies exist).
func (p *planner) cleanUp(instances map[alert.Key]alert.Instance) {
	active := map[PolicyID]bool{}
	for _, pol := range p.policies {
		active[pol.ID] = true
	}
	for _, s := range p.in.States {
		if _, still := p.states[s.StateKey]; !still {
			continue // dropped already
		}
		if i, ok := instances[s.Key]; !ok || !i.State.Firing() || !active[s.Policy] {
			p.drop(s.StateKey)
		}
	}
}
