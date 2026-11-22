package quota_test

import (
	"errors"
	"testing"

	"github.com/pmoscode/SerMon/server/internal/domain/quota"
)

func TestQuotas_Validate(t *testing.T) {
	valid := []quota.Quotas{
		{},
		{MaxNodes: 1, MaxMembers: 1, RetentionCapDays: 1, SamplesPerSecond: 1, SeriesPerNode: 1},
		{MaxNodes: 1_000_000, MaxMembers: 100_000, RetentionCapDays: 3650, SamplesPerSecond: 1_000_000, SeriesPerNode: 100_000},
	}
	for _, q := range valid {
		if err := q.Validate(); err != nil {
			t.Errorf("%+v: %v", q, err)
		}
	}
	invalid := []quota.Quotas{
		{MaxNodes: 1_000_001}, {MaxMembers: 100_001}, {RetentionCapDays: 3651}, {SamplesPerSecond: 1_000_001}, {SeriesPerNode: 100_001},
	}
	for _, q := range invalid {
		if err := q.Validate(); !errors.Is(err, quota.ErrInvalid) {
			t.Errorf("%+v: %v, want ErrInvalid", q, err)
		}
	}
}

// Reached: only a set quota refuses, and only once the count reaches it.
func TestReached(t *testing.T) {
	cases := []struct {
		limit uint32
		count int64
		want  bool
	}{{0, 1_000_000, false}, {5, 4, false}, {5, 5, true}, {5, 9, true}}
	for _, c := range cases {
		if got := quota.Reached(c.limit, c.count); got != c.want {
			t.Errorf("Reached(%d, %d) = %v", c.limit, c.count, got)
		}
	}
}

// The error names the quota, for the agent's log and the UI.
func TestExceeded_NamesTheQuota(t *testing.T) {
	err := error(quota.Exceeded{Kind: quota.Nodes, Limit: 20})
	if !errors.Is(err, quota.ErrExceeded) || err.Error() != "the organization's quota of 20 nodes is reached" {
		t.Errorf("%v", err)
	}
	var e quota.Exceeded
	if !errors.As(err, &e) || e.Kind != quota.Nodes || e.Limit != 20 {
		t.Errorf("%+v", e)
	}
	if got := (quota.Exceeded{Kind: quota.Members, Limit: 1}).Error(); got != "the organization's quota of 1 members is reached" {
		t.Errorf("%q", got)
	}
}

func TestQuotas_Uint32Pointers(t *testing.T) {
	q := quota.Quotas{MaxNodes: 3}
	if p := quota.Optional(q.MaxNodes); p == nil || *p != 3 {
		t.Errorf("%v", p)
	}
	if quota.Optional(0) != nil {
		t.Error("0 is unset")
	}
	if quota.Value(nil) != 0 {
		t.Error("nil is 0")
	}
}
