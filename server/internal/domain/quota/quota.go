// Package quota holds the quotas of organizations (M2-32, ADR 0029): the superadmin bounds an
// organization instead of the instance-wide values. At a quota something new is refused; what
// exists keeps running, and a lowered quota deletes nothing. 0 is the instance default.
package quota

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Quotas of an organization; 0 means the instance default (no limit for nodes and members).
type Quotas struct {
	MaxNodes         uint32
	MaxMembers       uint32 // members plus open invitations
	RetentionCapDays uint32 // applies once organizations set their own retention
	SamplesPerSecond uint32 // per node, before the agent is throttled
	SeriesPerNode    uint32
}

// ErrInvalid means a quota is out of its range.
var ErrInvalid = errors.New("quota out of range")

// Upper bounds, as in the protocol and the database.
const (
	MaxNodesLimit         = 1_000_000
	MaxMembersLimit       = 100_000
	MaxRetentionDays      = 3650
	MaxSamplesPerSecond   = 1_000_000
	MaxSeriesPerNodeLimit = 100_000
)

// Validate checks the ranges.
func (q Quotas) Validate() error {
	for _, c := range []struct {
		name       string
		value, max uint32
	}{
		{"max_nodes", q.MaxNodes, MaxNodesLimit},
		{"max_members", q.MaxMembers, MaxMembersLimit},
		{"retention_cap_days", q.RetentionCapDays, MaxRetentionDays},
		{"samples_per_second", q.SamplesPerSecond, MaxSamplesPerSecond},
		{"series_per_node", q.SeriesPerNode, MaxSeriesPerNodeLimit},
	} {
		if c.value > c.max {
			return fmt.Errorf("%w: %s must be at most %d", ErrInvalid, c.name, c.max)
		}
	}
	return nil
}

// String lists the set quotas as name=value in a fixed order, e.g. for the audit log.
func (q Quotas) String() string {
	var parts []string
	for _, f := range []struct {
		name  string
		value uint32
	}{
		{"max_nodes", q.MaxNodes},
		{"max_members", q.MaxMembers},
		{"retention_cap_days", q.RetentionCapDays},
		{"samples_per_second", q.SamplesPerSecond},
		{"series_per_node", q.SeriesPerNode},
	} {
		if f.value > 0 {
			parts = append(parts, f.name+"="+strconv.FormatUint(uint64(f.value), 10))
		}
	}
	return strings.Join(parts, ",")
}

// Reached reports whether count leaves no room under limit (0: no limit).
func Reached(limit uint32, count int64) bool {
	return limit > 0 && count >= int64(limit)
}

// Kind names a quota that refuses something.
type Kind string

// The quotas that refuse something new.
const (
	Nodes   Kind = "nodes"
	Members Kind = "members"
)

// ErrExceeded is matched by every Exceeded.
var ErrExceeded = errors.New("quota exceeded")

// Exceeded means a quota refuses something new; the message names it.
type Exceeded struct {
	Kind  Kind
	Limit uint32
}

func (e Exceeded) Error() string {
	return fmt.Sprintf("the organization's quota of %d %s is reached", e.Limit, e.Kind)
}

// Is makes errors.Is(err, ErrExceeded) true.
func (e Exceeded) Is(target error) bool { return target == ErrExceeded }

// Optional is the quota as an optional value (nil for 0, the default).
func Optional(v uint32) *uint32 {
	if v == 0 {
		return nil
	}
	return &v
}

// Value is an optional quota as a value (0 for nil).
func Value(v *uint32) uint32 {
	if v == nil {
		return 0
	}
	return *v
}
