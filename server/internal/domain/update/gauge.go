package update

import (
	"math"
	"time"
)

// The derived series of a node's updates (M4-16): the server writes them with the node's metrics,
// so rules and charts read them like any other metric.
const (
	MetricPending               = "sermon_updates_pending"
	MetricSecurityPending       = "sermon_updates_security_pending"
	MetricSecurityAge           = "sermon_updates_security_age_seconds"
	MetricRebootRequired        = "sermon_updates_reboot_required"
	MetricRebootRequiredSeconds = "sermon_updates_reboot_required_seconds"
)

// Gauge is what the stored report of a node says for the derived series.
type Gauge struct {
	// Listed: the counts are known (the package manager listed them).
	Listed          bool
	Total, Security uint32
	// OldestSecurity is when SerMon first saw the oldest open security update (zero: none stored).
	OldestSecurity time.Time
	RebootRequired bool
	// RebootSince is when a report first said the reboot is needed.
	RebootSince time.Time
}

// Value is one sample of a derived series.
type Value struct {
	Metric string
	Value  float64
}

// Values are the samples of the derived series at at: the reboot always, the counts and the age
// of the oldest security update only for a listed report. Ages count in whole seconds, never
// below zero.
func (g Gauge) Values(at time.Time) []Value {
	age := func(since time.Time) float64 {
		if since.IsZero() {
			return 0
		}
		return math.Max(0, math.Floor(at.Sub(since).Seconds()))
	}
	reboot, rebootFor := 0.0, 0.0
	if g.RebootRequired {
		reboot, rebootFor = 1, age(g.RebootSince)
	}
	out := []Value{{MetricRebootRequired, reboot}, {MetricRebootRequiredSeconds, rebootFor}}
	if !g.Listed {
		return out
	}
	securityAge := 0.0
	if g.Security > 0 {
		securityAge = age(g.OldestSecurity)
	}
	return append(out,
		Value{MetricPending, float64(g.Total)}, Value{MetricSecurityPending, float64(g.Security)},
		Value{MetricSecurityAge, securityAge})
}
