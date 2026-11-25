// Package updates keeps what the nodes could update (KONZEPT §14.1, M4-09): the ingest stores the
// latest report of a node with its packages, the UI reads it for the callers with ViewNodes.
//
// Names, versions, sources and reasons come from the host's package manager and its sources,
// which a third party controls: they are bounded and their invisible characters replaced before
// they are stored (textsafe), and the UI shows them as text only.
package updates

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	"github.com/pmoscode/SerMon/server/internal/domain/textsafe"
)

// Bounds of a report (the agent protocol has the same, sermon.agent.v1.UpdateReport).
const (
	MaxPackages = 5000
	MaxText     = 256
	MaxReason   = 1024
	MaxCount    = 1_000_000
	// DefaultOrgPackageLimit bounds the packages stored for all nodes of an organization; beyond
	// it a report keeps its counts but stores only the packages that still fit.
	DefaultOrgPackageLimit = 1_000_000
)

// Availability says whether the agent could list the updates.
type Availability uint8

// The availabilities, as in the agent protocol.
const (
	AvailabilityUnknown Availability = iota
	Listed
	NoHelper
	HelperTooOld
	Unsupported
	Failed
)

// Manager is the package manager that listed the updates.
type Manager uint8

// The package managers, as in the agent protocol.
const (
	ManagerUnknown Manager = iota
	Apt
	Dnf
	Apk
)

// Package is one available update.
type Package struct {
	Name, Installed, Candidate, Source string
	Security                           bool
	// FirstSeenAt is when a report first listed this update of the node (read side).
	FirstSeenAt time.Time
}

// Permissions are what the host allows remotely (/etc/sermon/helper.toml).
type Permissions struct {
	RemoteUpdates, RemoteReboot, SelfUpdate bool
}

// Report is what a node could update at CollectedAt (its agent's clock).
type Report struct {
	CollectedAt  time.Time
	Availability Availability
	Reason       string
	Manager      Manager
	// Packages are the listed updates, security updates first; Total and Security count all of
	// them, also those the agent or the store left out.
	Packages        []Package
	Total, Security uint32
	RebootRequired  bool
	RebootReason    string
	LastUpgradeAt   *time.Time
	// Permissions are nil without helper.
	Permissions *Permissions
	Refreshed   bool
}

// Valid reports whether the report is complete enough to store.
func (r Report) Valid() bool {
	if r.CollectedAt.IsZero() || r.Availability == AvailabilityUnknown || r.Availability > Failed || r.Manager > Apk ||
		len(r.Packages) > MaxPackages || r.Total > MaxCount || r.Security > MaxCount {
		return false
	}
	return !slices.ContainsFunc(r.Packages, func(p Package) bool { return p.Name == "" })
}

// Normalized makes a valid report safe to store and show: texts bounded and without invisible
// characters, each update (name and candidate) once with security updates first, counts no lower
// than the packages listed. A report that lists nothing has no packages and counts.
func (r Report) Normalized() Report {
	r.Reason = safe(r.Reason, MaxReason)
	r.RebootReason = safe(r.RebootReason, MaxReason)
	if r.Availability != Listed {
		r.Packages, r.Total, r.Security = nil, 0, 0
		return r
	}
	seen := make(map[[2]string]bool, len(r.Packages))
	packages := make([]Package, 0, len(r.Packages))
	var security uint32
	for _, p := range r.Packages {
		p = Package{
			Name: safe(p.Name, MaxText), Installed: safe(p.Installed, MaxText), Candidate: safe(p.Candidate, MaxText),
			Source: safe(p.Source, MaxText), Security: p.Security,
		}
		key := [2]string{p.Name, p.Candidate}
		if seen[key] {
			continue // dnf lists a package once per architecture
		}
		seen[key] = true
		if p.Security {
			security++
		}
		packages = append(packages, p)
	}
	slices.SortStableFunc(packages, func(a, b Package) int {
		if a.Security != b.Security {
			if a.Security {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.Name, b.Name)
	})
	r.Packages = packages
	r.Security = max(r.Security, security)
	r.Total = max(r.Total, uint32(len(packages)), r.Security) //nolint:gosec // at most MaxPackages
	return r
}

// safe replaces invisible characters (one byte becomes three) and cuts to limit bytes at a
// character boundary.
func safe(s string, limit int) string {
	s = textsafe.Visible(strings.ToValidUTF8(s, string(utf8.RuneError)))
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// Status is where a node stands with its updates.
type Status uint8

// The statuses, from the least to the most pressing; RebootRequired is separate.
const (
	// StatusUnknown: no report yet.
	StatusUnknown Status = iota
	// StatusUnavailable: the agent couldn't list (no helper, unsupported, failed).
	StatusUnavailable
	// StatusCurrent: nothing to update.
	StatusCurrent
	// StatusUpdates: updates, none of them for security.
	StatusUpdates
	// StatusSecurity: security updates.
	StatusSecurity
)

// StatusOf is the status of a report (nil: none yet).
func StatusOf(availability Availability, total, security uint32, reported bool) Status {
	switch {
	case !reported:
		return StatusUnknown
	case availability != Listed:
		return StatusUnavailable
	case security > 0:
		return StatusSecurity
	case total > 0:
		return StatusUpdates
	}
	return StatusCurrent
}

// Writer stores the reports of the ingest.
type Writer interface {
	// Write makes r the node's current report unless the stored one was collected at the same
	// time or later (a resend, or an older batch): then it returns false and changes nothing.
	// Packages beyond the organization's limit are left out; their counts stay.
	Write(ctx context.Context, org tenant.OrgID, node identity.NodeID, r Report, receivedAt time.Time) (bool, error)
}

// NodeUpdates is a node with its current report.
type NodeUpdates struct {
	Node     identity.NodeID
	Hostname string
	Group    tenant.GroupID
	// Report is nil before the first one.
	Report     *Report
	ReceivedAt time.Time
	// PackagesOmitted: fewer packages are stored than the report counts (the agent's or the
	// organization's limit).
	PackagesOmitted bool
	// OldestSecurity is when the oldest open security update was first listed.
	OldestSecurity *time.Time
}

// Status is the node's status.
func (n NodeUpdates) Status() Status {
	if n.Report == nil {
		return StatusOf(AvailabilityUnknown, 0, 0, false)
	}
	return StatusOf(n.Report.Availability, n.Report.Total, n.Report.Security, true)
}

// Store reads the reports within a scope (only nodes of the scope's groups).
type Store interface {
	// Node returns the node with its report and packages; false if it isn't in the scope.
	Node(ctx context.Context, scope tenant.Scope, node identity.NodeID) (NodeUpdates, bool, error)
	// Fleet returns the nodes of the scope with their reports without packages, by host name.
	Fleet(ctx context.Context, scope tenant.Scope) ([]NodeUpdates, error)
}
