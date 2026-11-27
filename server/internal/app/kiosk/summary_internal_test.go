package kiosk

import (
	"context"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
)

// V1-13: the widgets of a refresh run at once: they wait for one read, and each gets nodes of
// its own to sort (run with -race).
func TestSummaries_OneReadForCallsAtOnceAndNodesOfTheirOwn(t *testing.T) {
	var s summaries
	var reads atomic.Int32
	release := make(chan struct{})
	read := func() (fleet.Summary, error) { //nolint:unparam // the reader of summaries.get
		reads.Add(1)
		<-release
		return fleet.Summary{Nodes: []fleet.NodeStatus{
			{Node: fleet.Node{Hostname: "web-02"}}, {Node: fleet.Node{Hostname: "db-01"}}, {Node: fleet.Node{Hostname: "app-01"}},
		}}, nil
	}
	token := TokenID(uuid.New())
	c := access.Caller{Principal: authz.Principal{User: authz.UserID(uuid.New()), Bindings: []authz.Binding{{AllGroups: true, Role: role.Viewer}}}}
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			got, err := s.get(context.Background(), token, c, now, read)
			if err != nil {
				t.Error(err)
				return
			}
			slices.SortFunc(got.Nodes, func(a, b fleet.NodeStatus) int { return strings.Compare(a.Hostname, b.Hostname) })
		})
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := reads.Load(); n != 1 {
		t.Errorf("%d reads for calls at once, want 1", n)
	}
}

// The rights are the same in any order of the bindings, and differ with any binding.
func TestFingerprint(t *testing.T) {
	a := authz.Binding{Role: role.Viewer, AllGroups: true}
	b := authz.Binding{Role: role.Admin}
	p := authz.Principal{Bindings: []authz.Binding{a, b}}
	q := authz.Principal{Bindings: []authz.Binding{b, a}}
	if fingerprint(p) != fingerprint(q) {
		t.Error("the order of the bindings counts")
	}
	for _, other := range []authz.Principal{
		{Bindings: []authz.Binding{a}},
		{Bindings: []authz.Binding{a, {Role: role.Operator}}},
		{Bindings: []authz.Binding{a, b}, ViewersWithoutLogs: true},
	} {
		if fingerprint(other) == fingerprint(p) {
			t.Errorf("%+v: same rights as %+v", other, p)
		}
	}
}
