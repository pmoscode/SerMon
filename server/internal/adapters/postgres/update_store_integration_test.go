//go:build integration

package postgres_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/updates"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var updatesNow = time.Now().UTC().Truncate(time.Microsecond)

type updateFixture struct {
	store      *postgres.UpdateStore
	org        tenant.OrgID
	node, peer identity.NodeID
}

// A fresh organization with two nodes in different groups; limit bounds its packages.
func newUpdateFixture(t *testing.T, limit int) updateFixture {
	t.Helper()
	f := setup(t)
	org := uuid.New()
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO organizations (id, name) VALUES ($1, 'Updates ' || $1::uuid::text)", org)
	node, peer := uuid.New(), uuid.New()
	createNode(t, f.db.OwnerDSN, org, node)
	createNode(t, f.db.OwnerDSN, org, peer)
	return updateFixture{
		store: postgres.NewUpdateStore(open(t, f.appDSN), limit), org: tenant.OrgID(org),
		node: identity.NodeID(node), peer: identity.NodeID(peer),
	}
}

func pkg(name, candidate string, security bool) updates.Package {
	return updates.Package{Name: name, Installed: "1", Candidate: candidate, Source: "main", Security: security}
}

func updateReport(at time.Time, packages ...updates.Package) updates.Report {
	return updates.Report{
		CollectedAt: at, Availability: updates.Listed, Manager: updates.Apt, Packages: packages,
	}.Normalized()
}

func (f updateFixture) write(t *testing.T, node identity.NodeID, r updates.Report) bool {
	t.Helper()
	stored, err := f.store.Write(context.Background(), f.org, node, r, updatesNow)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

func (f updateFixture) get(t *testing.T, scope tenant.Scope, node identity.NodeID) (updates.NodeUpdates, bool) {
	t.Helper()
	n, ok, err := f.store.Node(context.Background(), scope, node)
	if err != nil {
		t.Fatal(err)
	}
	return n, ok
}

func names(n updates.NodeUpdates) string {
	var out []string
	for _, p := range n.Report.Packages {
		out = append(out, p.Name+"="+p.Candidate)
	}
	return strings.Join(out, " ")
}

// A newer report replaces the old one with its packages; an older one or a resend changes nothing.
// An update listed again keeps when it was first seen, so the oldest security update ages.
func TestUpdateStore_ANewerReportReplacesTheOld(t *testing.T) {
	f := newUpdateFixture(t, 0)
	scope := scopeFor(t, uuid.UUID(f.org))
	first := updateReport(updatesNow.Add(-2*time.Hour), pkg("openssl", "3.1", true), pkg("vim", "2", false), pkg("curl", "8", false))
	first.RebootRequired, first.RebootReason = true, "linux-image"
	last := updatesNow.Add(-48 * time.Hour)
	first.LastUpgradeAt = &last
	first.Permissions = &updates.Permissions{RemoteUpdates: true, SelfUpdate: true}
	if !f.write(t, f.node, first) {
		t.Fatal("the first report was not stored")
	}
	n, ok := f.get(t, scope, f.node)
	if !ok || n.Report == nil || names(n) != "openssl=3.1 curl=8 vim=2" || n.Status() != updates.StatusSecurity {
		t.Fatalf("node = %+v (%s)", n, names(n))
	}
	r := n.Report
	if !r.CollectedAt.Equal(first.CollectedAt) || !n.ReceivedAt.Equal(updatesNow) || r.Total != 3 || r.Security != 1 ||
		!r.RebootRequired || r.RebootReason != "linux-image" || r.LastUpgradeAt == nil || !r.LastUpgradeAt.Equal(last) ||
		r.Permissions == nil || *r.Permissions != *first.Permissions || n.PackagesOmitted || r.Manager != updates.Apt {
		t.Errorf("report = %+v", r)
	}
	if n.OldestSecurity == nil || !n.OldestSecurity.Equal(first.CollectedAt) || !r.Packages[0].FirstSeenAt.Equal(first.CollectedAt) {
		t.Errorf("oldest security = %v, first seen %v", n.OldestSecurity, r.Packages[0].FirstSeenAt)
	}

	// The same report again, and an older one: nothing changes.
	if f.write(t, f.node, first) || f.write(t, f.node, updateReport(updatesNow.Add(-3*time.Hour))) {
		t.Error("a resend or an older report was stored")
	}
	if n, _ := f.get(t, scope, f.node); names(n) != "openssl=3.1 curl=8 vim=2" {
		t.Errorf("after an older report: %s", names(n))
	}

	// Newer: vim got a newer candidate, curl went, openssl stays with its first sighting.
	second := updateReport(updatesNow.Add(-time.Hour), pkg("openssl", "3.1", true), pkg("vim", "3", false), pkg("zlib", "1.3", true))
	if !f.write(t, f.node, second) {
		t.Fatal("a newer report was not stored")
	}
	n, _ = f.get(t, scope, f.node)
	if names(n) != "openssl=3.1 zlib=1.3 vim=3" || n.Report.RebootRequired || n.Report.Permissions != nil || n.Report.LastUpgradeAt != nil {
		t.Errorf("after the newer report: %s, %+v", names(n), n.Report)
	}
	for _, p := range n.Report.Packages {
		want := second.CollectedAt
		if p.Name == "openssl" {
			want = first.CollectedAt
		}
		if !p.FirstSeenAt.Equal(want) {
			t.Errorf("%s first seen %v, want %v", p.Name, p.FirstSeenAt, want)
		}
	}
	if n.OldestSecurity == nil || !n.OldestSecurity.Equal(first.CollectedAt) {
		t.Errorf("oldest security = %v, want openssl's first sighting", n.OldestSecurity)
	}

	// Nothing to update, then the helper is gone: no packages, no oldest security update.
	if !f.write(t, f.node, updateReport(updatesNow.Add(-30*time.Minute))) {
		t.Fatal("an empty report was not stored")
	}
	n, _ = f.get(t, scope, f.node)
	if len(n.Report.Packages) != 0 || n.OldestSecurity != nil || n.Status() != updates.StatusCurrent {
		t.Errorf("after an empty report: %+v", n)
	}
	gone := updates.Report{CollectedAt: updatesNow, Availability: updates.NoHelper, Reason: "no helper"}.Normalized()
	f.write(t, f.node, gone)
	if n, _ = f.get(t, scope, f.node); n.Status() != updates.StatusUnavailable || n.Report.Reason != "no helper" {
		t.Errorf("without helper: %+v", n.Report)
	}
}

// Two sessions of a node can't exist (M2-23), but a write and its retry might race: the newer
// report wins either way.
func TestUpdateStore_ConcurrentWritesKeepTheNewest(t *testing.T) {
	f := newUpdateFixture(t, 0)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := updateReport(updatesNow.Add(time.Duration(i)*time.Second), pkg("p", string(rune('a'+i)), false))
			if _, err := f.store.Write(context.Background(), f.org, f.node, r, updatesNow); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n, _ := f.get(t, scopeFor(t, uuid.UUID(f.org)), f.node); names(n) != "p=h" {
		t.Errorf("stored %s, want the newest", names(n))
	}
}

// The organization's limit: a report keeps its counts and stores what fits, security first.
func TestUpdateStore_TheOrganizationsLimitBoundsThePackages(t *testing.T) {
	f := newUpdateFixture(t, 4)
	f.write(t, f.peer, updateReport(updatesNow, pkg("a", "1", false), pkg("b", "1", false)))
	f.write(t, f.node, updateReport(updatesNow, pkg("c", "1", false), pkg("d", "1", true), pkg("e", "1", false)))
	scope := scopeFor(t, uuid.UUID(f.org))
	n, _ := f.get(t, scope, f.node)
	if names(n) != "d=1 c=1" || n.Report.Total != 3 || n.Report.Security != 1 || !n.PackagesOmitted {
		t.Errorf("node = %s, %+v", names(n), n)
	}
	// The node's own packages don't count against it: the next report replaces them.
	f.write(t, f.node, updateReport(updatesNow.Add(time.Second), pkg("f", "1", false), pkg("g", "1", false)))
	if n, _ := f.get(t, scope, f.node); names(n) != "f=1 g=1" || n.PackagesOmitted {
		t.Errorf("node = %s, omitted %v", names(n), n.PackagesOmitted)
	}
	if p, _ := f.get(t, scope, f.peer); names(p) != "a=1 b=1" {
		t.Errorf("peer = %s", names(p))
	}
}

// Another organization sees nothing of it, a group scope only its group's nodes; a node without
// report is there without one.
func TestUpdateStore_KeepsOrganizationsAndGroupsApart(t *testing.T) {
	f := newUpdateFixture(t, 0)
	f.write(t, f.node, updateReport(updatesNow, pkg("openssl", "3", true)))
	other := newUpdateFixture(t, 0)
	otherScope := scopeFor(t, uuid.UUID(other.org))
	if _, ok := f.get(t, otherScope, f.node); ok {
		t.Error("another organization sees the node")
	}
	fleet, err := other.store.Fleet(context.Background(), otherScope)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range fleet {
		if n.Node == f.node || n.Node == f.peer {
			t.Errorf("another organization lists %v", n.Node)
		}
	}
	// Writing as the other organization reaches no node of this one.
	if _, err := other.store.Write(context.Background(), other.org, f.node, updateReport(updatesNow.Add(time.Hour)), updatesNow); err == nil {
		t.Error("a report for a node of another organization was stored")
	}
	if n, _ := f.get(t, scopeFor(t, uuid.UUID(f.org)), f.node); names(n) != "openssl=3" {
		t.Errorf("node = %s after the foreign write", names(n))
	}

	group := nodeGroup(t, uuid.UUID(f.org), uuid.UUID(f.peer))
	scope, err := tenant.ForGroups(f.org, group)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.get(t, scope, f.node); ok {
		t.Error("a group scope sees a node of another group")
	}
	peer, ok := f.get(t, scope, f.peer)
	if !ok || peer.Report != nil || peer.Status() != updates.StatusUnknown || peer.Hostname == "" {
		t.Errorf("peer without report = %+v, %v", peer, ok)
	}
	fleet, err = f.store.Fleet(context.Background(), scope)
	if err != nil || len(fleet) != 1 || fleet[0].Node != f.peer {
		t.Errorf("fleet of the group = %+v, %v", fleet, err)
	}
	all, err := f.store.Fleet(context.Background(), scopeFor(t, uuid.UUID(f.org)))
	if err != nil || len(all) != 2 {
		t.Fatalf("fleet = %+v, %v", all, err)
	}
	for _, n := range all {
		if n.Node == f.node && (n.Report == nil || n.Report.Security != 1 || n.Report.Packages != nil) {
			t.Errorf("fleet entry = %+v, want the counts without packages", n)
		}
	}
}

// The table refuses what the ingest would never write.
func TestUpdateStore_TheTablesCheckTheBounds(t *testing.T) {
	f := newUpdateFixture(t, 0)
	ctx := context.Background()
	for name, r := range map[string]updates.Report{
		"long name":        {CollectedAt: updatesNow, Availability: updates.Listed, Total: 1, Packages: []updates.Package{{Name: strings.Repeat("a", 257)}}},
		"security > total": {CollectedAt: updatesNow, Availability: updates.Listed, Total: 1, Security: 2},
		"availability":     {CollectedAt: updatesNow, Availability: 9},
	} {
		if _, err := f.store.Write(ctx, f.org, f.node, r, updatesNow); err == nil {
			t.Errorf("%s: stored", name)
		}
	}
}

// The rows go with the node (and so with a purged organization).
func TestUpdateStore_RowsGoWithTheNode(t *testing.T) {
	f := newUpdateFixture(t, 0)
	f.write(t, f.node, updateReport(updatesNow, pkg("openssl", "3", true)))
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, setup(t).db.OwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var state, packages int
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT set_config('sermon.org_id', $1, true)", uuid.UUID(f.org).String()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM nodes WHERE id = $1", uuid.UUID(f.node)); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM node_update_state WHERE org_id = $1", uuid.UUID(f.org)).Scan(&state); err != nil {
			return err
		}
		return tx.QueryRow(ctx, "SELECT count(*) FROM node_update_packages WHERE org_id = $1", uuid.UUID(f.org)).Scan(&packages)
	})
	if err != nil {
		t.Fatal(err)
	}
	if state != 0 || packages != 0 {
		t.Errorf("left after the node: %d states, %d packages", state, packages)
	}
}
