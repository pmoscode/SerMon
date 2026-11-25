package updates_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/updates"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	org    = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
	groupX = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000aa"))
	user   = authz.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001"))
	node   = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000002"))
	t0     = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
)

func listed(packages ...updates.Package) updates.Report {
	return updates.Report{CollectedAt: t0, Availability: updates.Listed, Manager: updates.Apt, Packages: packages}
}

func TestNormalized_EachUpdateOnceSecurityFirstAndCountsAtLeastTheList(t *testing.T) {
	r := listed(
		updates.Package{Name: "vim", Candidate: "2"},
		updates.Package{Name: "openssl", Candidate: "3.1", Security: true},
		updates.Package{Name: "glibc", Candidate: "2.39", Security: true},
		// dnf: the same update for another architecture
		updates.Package{Name: "glibc", Candidate: "2.39", Security: true},
		updates.Package{Name: "glibc", Candidate: "2.40"},
	).Normalized()
	var names []string
	for _, p := range r.Packages {
		names = append(names, p.Name+"="+p.Candidate)
	}
	if got := strings.Join(names, " "); got != "glibc=2.39 openssl=3.1 glibc=2.40 vim=2" {
		t.Errorf("packages = %s", got)
	}
	if r.Total != 4 || r.Security != 2 {
		t.Errorf("counts = %d, %d; want 4, 2", r.Total, r.Security)
	}
	// Counts above the list (the agent sent only the first 5000) stay.
	r = listed(updates.Package{Name: "vim"})
	r.Total, r.Security = 9000, 7
	if n := r.Normalized(); n.Total != 9000 || n.Security != 7 {
		t.Errorf("counts = %d, %d; want 9000, 7", n.Total, n.Security)
	}
	r.Total = 3 // fewer than the security updates
	if n := r.Normalized(); n.Total != 7 {
		t.Errorf("total = %d, want at least the security updates", n.Total)
	}
}

// Texts come from the package sources of a third party.
func TestNormalized_TextsAreBoundedAndVisible(t *testing.T) {
	r := listed(updates.Package{
		Name: "evil\u202egnp", Installed: strings.Repeat("\x01", 200), Candidate: "1\n2", Source: "x" + strings.Repeat("ä", 200),
	})
	r.Reason, r.RebootReason = "a\x1b[31mred", strings.Repeat("\u200b", 1000)
	n := r.Normalized()
	p := n.Packages[0]
	if p.Name != "evil\uFFFDgnp" || p.Candidate != "1\uFFFD2" || strings.ContainsAny(n.Reason, "\x1b") {
		t.Errorf("invisible characters left: %q %q %q", p.Name, p.Candidate, n.Reason)
	}
	for what, got := range map[string]int{"installed": len(p.Installed), "source": len(p.Source)} {
		if got > updates.MaxText {
			t.Errorf("%s has %d bytes", what, got)
		}
	}
	if len(n.RebootReason) > updates.MaxReason || !strings.HasPrefix(n.RebootReason, "\uFFFD") {
		t.Errorf("reboot reason %d bytes", len(n.RebootReason))
	}
	// Byte 256 is inside an ä: the cut goes before it.
	if !strings.HasPrefix(p.Source, "xää") || !utf8Valid(p.Source) || len(p.Source) != 255 {
		t.Errorf("source cut inside a character: %d bytes", len(p.Source))
	}
	if broken := listed(updates.Package{Name: "a\xffb"}).Normalized(); broken.Packages[0].Name != "a\uFFFDb" {
		t.Errorf("invalid UTF-8 = %q", broken.Packages[0].Name)
	}
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "") == s }

func TestNormalized_AReportWithoutListHasNoPackages(t *testing.T) {
	r := listed(updates.Package{Name: "vim"})
	r.Availability, r.Total, r.Security, r.Reason = updates.NoHelper, 3, 1, "no helper"
	n := r.Normalized()
	if n.Packages != nil || n.Total != 0 || n.Security != 0 || n.Reason != "no helper" {
		t.Errorf("normalized = %+v", n)
	}
}

func TestValid(t *testing.T) {
	if !listed(updates.Package{Name: "vim"}).Valid() {
		t.Error("a listed report is invalid")
	}
	for name, change := range map[string]func(*updates.Report){
		"no time":         func(r *updates.Report) { r.CollectedAt = time.Time{} },
		"no availability": func(r *updates.Report) { r.Availability = updates.AvailabilityUnknown },
		"availability":    func(r *updates.Report) { r.Availability = updates.Failed + 1 },
		"manager":         func(r *updates.Report) { r.Manager = updates.Apk + 1 },
		"too many": func(r *updates.Report) {
			r.Packages = nil
			for range updates.MaxPackages + 1 {
				r.Packages = append(r.Packages, updates.Package{Name: "p"})
			}
		},
		"package unnamed":  func(r *updates.Report) { r.Packages = []updates.Package{{Candidate: "1"}} },
		"total too large":  func(r *updates.Report) { r.Total = updates.MaxCount + 1 },
		"security too big": func(r *updates.Report) { r.Security = updates.MaxCount + 1 },
	} {
		r := listed(updates.Package{Name: "vim"})
		change(&r)
		if r.Valid() {
			t.Errorf("%s: valid", name)
		}
	}
}

func TestStatusOf(t *testing.T) {
	for _, c := range []struct {
		availability    updates.Availability
		total, security uint32
		reported        bool
		want            updates.Status
	}{
		{updates.Listed, 5, 1, false, updates.StatusUnknown},
		{updates.Failed, 5, 1, true, updates.StatusUnavailable},
		{updates.Listed, 5, 1, true, updates.StatusSecurity},
		{updates.Listed, 5, 0, true, updates.StatusUpdates},
		{updates.Listed, 0, 0, true, updates.StatusCurrent},
	} {
		if got := updates.StatusOf(c.availability, c.total, c.security, c.reported); got != c.want {
			t.Errorf("StatusOf(%+v) = %d", c, got)
		}
	}
}

// store has one node in groupX with a report and one without; it records the scope.
type store struct {
	scope tenant.Scope
	fail  bool
}

func (s *store) visible(scope tenant.Scope) bool {
	return scope.AllGroups() || scope.AllowsGroup(groupX)
}

func (s *store) Node(_ context.Context, scope tenant.Scope, id identity.NodeID) (updates.NodeUpdates, bool, error) {
	s.scope = scope
	if s.fail {
		return updates.NodeUpdates{}, false, errors.New("database down")
	}
	if id != node || !s.visible(scope) {
		return updates.NodeUpdates{}, false, nil
	}
	r := listed(updates.Package{Name: "openssl", Security: true})
	r.Total, r.Security = 1, 1
	return updates.NodeUpdates{Node: node, Group: groupX, Report: &r}, true, nil
}

func (s *store) Fleet(_ context.Context, scope tenant.Scope) ([]updates.NodeUpdates, error) {
	s.scope = scope
	if s.fail {
		return nil, errors.New("database down")
	}
	if !s.visible(scope) {
		return []updates.NodeUpdates{}, nil
	}
	security := updates.Report{Availability: updates.Listed, Total: 3, Security: 1, RebootRequired: true}
	current := updates.Report{Availability: updates.Listed}
	return []updates.NodeUpdates{{Node: node, Report: &security}, {Report: &current}, {Report: &current}, {}}, nil
}

func caller(r role.Role, all bool) access.Caller {
	b := authz.Binding{Group: groupX, Role: r, AllGroups: all}
	return access.Caller{Principal: authz.Principal{User: user, Org: org, Bindings: []authz.Binding{b}}}
}

func TestService_ViewersOfTheGroupSeeTheReport(t *testing.T) {
	s := &store{}
	svc := updates.NewService(s, authz.NewPolicy())
	n, err := svc.Node(context.Background(), caller(role.Viewer, false), node)
	if err != nil || n.Status() != updates.StatusSecurity || len(n.Report.Packages) != 1 {
		t.Fatalf("Node() = %+v, %v", n, err)
	}
	if s.scope.AllGroups() || !s.scope.AllowsGroup(groupX) || s.scope.OrgID() != org {
		t.Errorf("scope = %+v, want groupX of the caller's organization", s.scope)
	}
	fleet, err := svc.Fleet(context.Background(), caller(role.Viewer, true))
	if err != nil {
		t.Fatal(err)
	}
	if !s.scope.AllGroups() {
		t.Error("a viewer of all groups gets a narrower scope")
	}
	want := map[updates.Status]int{updates.StatusSecurity: 1, updates.StatusCurrent: 2, updates.StatusUnknown: 1}
	if len(fleet.Nodes) != 4 || fleet.RebootRequired != 1 || len(fleet.Counts) != len(want) {
		t.Errorf("fleet = %+v", fleet)
	}
	for status, n := range want {
		if fleet.Counts[status] != n {
			t.Errorf("count of %d = %d, want %d", status, fleet.Counts[status], n)
		}
	}
}

func TestService_OtherNodesAreNotFoundAndNoRightIsForbidden(t *testing.T) {
	svc := updates.NewService(&store{}, authz.NewPolicy())
	ctx := context.Background()
	other := identity.NodeID(uuid.New())
	if _, err := svc.Node(ctx, caller(role.Admin, true), other); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("Node(other) = %v, want not found", err)
	}
	none := access.Caller{Principal: authz.Principal{User: user, Org: org}}
	if _, err := svc.Node(ctx, none, node); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("Node() without a role = %v, want forbidden", err)
	}
	if _, err := svc.Fleet(ctx, none); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("Fleet() without a role = %v, want forbidden", err)
	}
	failing := updates.NewService(&store{fail: true}, authz.NewPolicy())
	if _, err := failing.Node(ctx, caller(role.Admin, true), node); err == nil || errors.Is(err, access.ErrNotFound) {
		t.Errorf("Node() on a failing store = %v", err)
	}
	if _, err := failing.Fleet(ctx, caller(role.Admin, true)); err == nil {
		t.Error("Fleet() on a failing store succeeded")
	}
}
