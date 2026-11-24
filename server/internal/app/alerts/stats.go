package alerts

import (
	"cmp"
	"slices"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/alerting"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
)

// MaxStatisticsRules bounds the loudest rules of the statistics.
const MaxStatisticsRules = 10

// Figures are the counts of firings (an instance turning warning or critical from a state that
// doesn't fire) and the mean times to their acknowledgement and resolution; nil without any.
type Figures struct {
	Firings        int
	Acknowledged   int
	Resolved       int
	Open           int
	Unacknowledged int
	// Flapping counts the firings while the instance flapped.
	Flapping   int
	MTTA, MTTR *time.Duration
}

// RuleFigures are the figures of one rule over its instances.
type RuleFigures struct {
	Rule alert.RuleID
	Figures
	Nodes int
}

// Statistics are the figures of a time range, the loudest rules first.
type Statistics struct {
	From, To time.Time
	Totals   Figures
	Rules    []RuleFigures
	// Truncated is set if the history of the range held more than was read.
	Truncated bool
}

type sums struct {
	Figures
	ack, res time.Duration
	nodes    map[identity.NodeID]bool
}

func (s *sums) firing(start time.Time, ackAt, resAt *time.Time, flapping bool, node identity.NodeID) {
	s.Firings++
	if flapping {
		s.Flapping++
	}
	if ackAt != nil {
		s.Acknowledged++
		s.ack += ackAt.Sub(start)
	} else {
		s.Unacknowledged++
	}
	if resAt != nil {
		s.Resolved++
		s.res += resAt.Sub(start)
	} else {
		s.Open++
	}
	if s.nodes != nil {
		s.nodes[node] = true
	}
}

func mean(total time.Duration, n int) *time.Duration {
	if n == 0 {
		return nil
	}
	d := total / time.Duration(n)
	return &d
}

func (s *sums) figures() Figures {
	f := s.Figures
	f.MTTA, f.MTTR = mean(s.ack, s.Acknowledged), mean(s.res, s.Resolved)
	return f
}

// episode is a firing of an instance being followed.
type episode struct {
	start        time.Time
	ackAt, resAt *time.Time
	flapping     bool
}

// ComputeStatistics evaluates the history of a range (alert_history, oldest first or not): every
// firing that starts in [from, to) counts, with its acknowledgement and its resolution if they
// follow. A firing that began before the range is left out, also its end.
func ComputeStatistics(records []alerting.Record, from, to time.Time) Statistics {
	byKey := map[alert.Key][]alerting.Record{}
	for _, r := range records {
		byKey[r.Key] = append(byKey[r.Key], r)
	}
	total := sums{}
	rules := map[alert.RuleID]*sums{}
	for key, list := range byKey {
		slices.SortStableFunc(list, func(a, b alerting.Record) int { return a.Transition.At.Compare(b.Transition.At) })
		rule := rules[key.Rule]
		if rule == nil {
			rule = &sums{nodes: map[identity.NodeID]bool{}}
			rules[key.Rule] = rule
		}
		flapping := false
		var open *episode
		done := func(e *episode) {
			total.firing(e.start, e.ackAt, e.resAt, e.flapping, key.Node)
			rule.firing(e.start, e.ackAt, e.resAt, e.flapping, key.Node)
		}
		for _, r := range list {
			t := r.Transition
			at := t.At
			switch t.Kind {
			case alert.FlappingStarted:
				flapping = true
			case alert.FlappingStopped:
				flapping = false
			case alert.Acknowledged:
				if open != nil && open.ackAt == nil {
					open.ackAt = &at
				}
			case alert.StateChanged:
				switch {
				case t.To.Firing() && !t.From.Firing():
					if open != nil {
						done(open)
					}
					open = nil
					if !at.Before(from) && at.Before(to) {
						open = &episode{start: at, flapping: flapping}
					}
				case t.From.Firing() && !t.To.Firing():
					if open != nil {
						open.resAt = &at
						done(open)
						open = nil
					}
				}
			}
		}
		if open != nil {
			done(open)
		}
	}
	out := Statistics{From: from, To: to, Totals: total.figures()}
	for id, s := range rules {
		if s.Firings == 0 {
			continue
		}
		out.Rules = append(out.Rules, RuleFigures{Rule: id, Figures: s.figures(), Nodes: len(s.nodes)})
	}
	slices.SortFunc(out.Rules, func(a, b RuleFigures) int {
		return cmp.Or(cmp.Compare(b.Firings, a.Firings), cmp.Compare(a.Rule, b.Rule))
	})
	if len(out.Rules) > MaxStatisticsRules {
		out.Rules = out.Rules[:MaxStatisticsRules]
	}
	return out
}
