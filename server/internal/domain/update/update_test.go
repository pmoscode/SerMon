package update_test

import (
	"strings"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/domain/update"
)

var t0 = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

func listed(packages ...update.Package) update.Report {
	return update.Report{CollectedAt: t0, Availability: update.Listed, Manager: update.Apt, Packages: packages}
}

func TestNormalized_EachUpdateOnceSecurityFirstAndCountsAtLeastTheList(t *testing.T) {
	r := listed(
		update.Package{Name: "vim", Candidate: "2"},
		update.Package{Name: "openssl", Candidate: "3.1", Security: true},
		update.Package{Name: "glibc", Candidate: "2.39", Security: true},
		// dnf: the same update for another architecture
		update.Package{Name: "glibc", Candidate: "2.39", Security: true},
		update.Package{Name: "glibc", Candidate: "2.40"},
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
	r = listed(update.Package{Name: "vim"})
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
	r := listed(update.Package{
		Name: "evil\u202egnp", Installed: strings.Repeat("\x01", 200), Candidate: "1\n2", Source: "x" + strings.Repeat("ä", 200),
	})
	r.Reason, r.RebootReason = "a\x1b[31mred", strings.Repeat("\u200b", 1000)
	n := r.Normalized()
	p := n.Packages[0]
	if p.Name != "evil\uFFFDgnp" || p.Candidate != "1\uFFFD2" || strings.ContainsAny(n.Reason, "\x1b") {
		t.Errorf("invisible characters left: %q %q %q", p.Name, p.Candidate, n.Reason)
	}
	for what, got := range map[string]int{"installed": len(p.Installed), "source": len(p.Source)} {
		if got > update.MaxText {
			t.Errorf("%s has %d bytes", what, got)
		}
	}
	if len(n.RebootReason) > update.MaxReason || !strings.HasPrefix(n.RebootReason, "\uFFFD") {
		t.Errorf("reboot reason %d bytes", len(n.RebootReason))
	}
	// Byte 256 is inside an ä: the cut goes before it.
	if !strings.HasPrefix(p.Source, "xää") || !utf8Valid(p.Source) || len(p.Source) != 255 {
		t.Errorf("source cut inside a character: %d bytes", len(p.Source))
	}
	if broken := listed(update.Package{Name: "a\xffb"}).Normalized(); broken.Packages[0].Name != "a\uFFFDb" {
		t.Errorf("invalid UTF-8 = %q", broken.Packages[0].Name)
	}
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "") == s }

func TestNormalized_AReportWithoutListHasNoPackages(t *testing.T) {
	r := listed(update.Package{Name: "vim"})
	r.Availability, r.Total, r.Security, r.Reason = update.NoHelper, 3, 1, "no helper"
	n := r.Normalized()
	if n.Packages != nil || n.Total != 0 || n.Security != 0 || n.Reason != "no helper" {
		t.Errorf("normalized = %+v", n)
	}
}

func TestValid(t *testing.T) {
	if !listed(update.Package{Name: "vim"}).Valid() {
		t.Error("a listed report is invalid")
	}
	for name, change := range map[string]func(*update.Report){
		"no time":         func(r *update.Report) { r.CollectedAt = time.Time{} },
		"no availability": func(r *update.Report) { r.Availability = update.AvailabilityUnknown },
		"availability":    func(r *update.Report) { r.Availability = update.Failed + 1 },
		"manager":         func(r *update.Report) { r.Manager = update.Apk + 1 },
		"too many": func(r *update.Report) {
			r.Packages = nil
			for range update.MaxPackages + 1 {
				r.Packages = append(r.Packages, update.Package{Name: "p"})
			}
		},
		"package unnamed":  func(r *update.Report) { r.Packages = []update.Package{{Candidate: "1"}} },
		"total too large":  func(r *update.Report) { r.Total = update.MaxCount + 1 },
		"security too big": func(r *update.Report) { r.Security = update.MaxCount + 1 },
	} {
		r := listed(update.Package{Name: "vim"})
		change(&r)
		if r.Valid() {
			t.Errorf("%s: valid", name)
		}
	}
}

func TestStatusOf(t *testing.T) {
	for _, c := range []struct {
		availability    update.Availability
		total, security uint32
		reported        bool
		want            update.Status
	}{
		{update.Listed, 5, 1, false, update.StatusUnknown},
		{update.Failed, 5, 1, true, update.StatusUnavailable},
		{update.Listed, 5, 1, true, update.StatusSecurity},
		{update.Listed, 5, 0, true, update.StatusUpdates},
		{update.Listed, 0, 0, true, update.StatusCurrent},
	} {
		if got := update.StatusOf(c.availability, c.total, c.security, c.reported); got != c.want {
			t.Errorf("StatusOf(%+v) = %d", c, got)
		}
	}
}
