package agentapi

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// M4-31: an organization fetches at most FetchesAtOncePerOrg releases at once, the instance at
// most FetchesAtOnce; the hour's fetches of nodes that stopped fetching are forgotten.
func TestFetchLimits_BoundTheOrganizationAndTheInstance(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	var l fetchLimits
	org, other := tenant.OrgID(uuid.New()), tenant.OrgID(uuid.New())
	var taken []identity.NodeID
	for range FetchesAtOncePerOrg {
		n := identity.NodeID(uuid.New())
		if !l.take(org, n, now) {
			t.Fatal("refused within the organization's limit")
		}
		taken = append(taken, n)
	}
	if l.take(org, identity.NodeID(uuid.New()), now) {
		t.Error("one more of the organization at once")
	}
	if !l.take(other, identity.NodeID(uuid.New()), now) {
		t.Error("another organization is refused")
	}
	l.done(org, taken[0])
	if !l.take(org, identity.NodeID(uuid.New()), now) {
		t.Error("refused after a fetch ended")
	}
	var full fetchLimits
	for i := range FetchesAtOnce {
		if !full.take(tenant.OrgID(uuid.New()), identity.NodeID(uuid.New()), now) {
			t.Fatalf("refused at %d, within the instance's limit", i)
		}
	}
	if full.take(tenant.OrgID(uuid.New()), identity.NodeID(uuid.New()), now) {
		t.Error("one more of the instance at once")
	}
	var old fetchLimits
	n := identity.NodeID(uuid.New())
	old.take(org, n, now)
	old.done(org, n)
	old.take(org, identity.NodeID(uuid.New()), now.Add(2*time.Hour))
	if _, kept := old.recent[n]; kept {
		t.Error("the fetches of an hour ago are kept")
	}
}
