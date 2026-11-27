package secevents

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/secevent"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Bounds of a query.
const (
	DefaultPageSize = 100
	MaxPageSize     = 500
	MaxRange        = 366 * 24 * time.Hour
	MaxNodes        = 100
)

// Request is a filter with a page; the cursor comes from PageToken.
type Request struct {
	Filter
	PageSize  int
	PageToken string
}

// Page is one page of events, newest first; Next continues it (empty: the last page).
type Page struct {
	Events []secevent.Event
	Next   string
}

// Service lists the security events of the nodes a caller may see them of.
type Service struct {
	store  Store
	policy authz.Policy
	geo    Geo
}

// NewService returns the service.
func NewService(store Store, policy authz.Policy) *Service {
	return &Service{store: store, policy: policy}
}

// SetGeo turns on the countries of addresses (M3-18).
func (s *Service) SetGeo(g Geo) { s.geo = g }

// Country returns the country of an address, empty without GeoIP.
func (s *Service) Country(ip netip.Addr) string {
	if s.geo == nil || !ip.IsValid() {
		return ""
	}
	return s.geo.Country(ip)
}

// GeoNotice is the licence notice of the countries, empty without GeoIP.
func (s *Service) GeoNotice() string {
	if s.geo == nil {
		return ""
	}
	return s.geo.Notice()
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", secevent.ErrInvalid, fmt.Sprintf(format, args...))
}

// scope is the caller's scope for security events; access.ErrForbidden without
// ViewSecurityEvents on any group (or as a viewer while the organization denies them logs).
func (s *Service) scope(c access.Caller) (tenant.Scope, error) {
	scope, err := s.policy.ScopeForAction(c.Principal, authz.ViewSecurityEvents)
	if err != nil || (!scope.AllGroups() && len(scope.Groups()) == 0) {
		return tenant.Scope{}, access.ErrForbidden
	}
	return scope, nil
}

func checkRange(from, to time.Time, nodes int) error {
	switch {
	case from.IsZero() || to.IsZero() || !to.After(from):
		return invalid("the time range needs from before to")
	case to.Sub(from) > MaxRange:
		return invalid("the time range is at most %v", MaxRange)
	case nodes > MaxNodes:
		return invalid("at most %d nodes", MaxNodes)
	}
	return nil
}

func checkFilter(f Filter) error {
	if err := checkRange(f.From, f.To, len(f.Nodes)); err != nil {
		return err
	}
	switch {
	case f.User != nil && len(*f.User) > secevent.MaxUser:
		return invalid("user longer than %d bytes", secevent.MaxUser)
	case f.Network != nil && !f.Network.IsValid():
		return invalid("invalid network")
	}
	for _, k := range f.Kinds {
		if _, err := secevent.ParseKind(string(k)); err != nil {
			return err
		}
	}
	return nil
}

// List returns a page of the events of the request; access.ErrForbidden without
// ViewSecurityEvents on any group, secevent.ErrInvalid for a bad filter.
func (s *Service) List(ctx context.Context, c access.Caller, r Request) (Page, error) {
	scope, err := s.scope(c)
	if err != nil {
		return Page{}, err
	}
	f := r.Filter
	if r.PageSize < 0 || r.PageSize > MaxPageSize {
		return Page{}, invalid("page size between 0 and %d", MaxPageSize)
	}
	if err := checkFilter(f); err != nil {
		return Page{}, err
	}
	if r.PageToken != "" {
		cursor, err := parseToken(r.PageToken)
		if err != nil {
			return Page{}, err
		}
		f.Before = &cursor
	}
	size := r.PageSize
	if size == 0 {
		size = DefaultPageSize
	}
	f.Limit = size + 1
	events, err := s.store.List(ctx, scope, f)
	if err != nil {
		return Page{}, fmt.Errorf("list security events: %w", err)
	}
	page := Page{Events: events}
	if len(events) > size {
		page.Events = events[:size]
		last := page.Events[size-1]
		page.Next = token(Cursor{At: last.OccurredAt, ID: last.ID})
	}
	return page, nil
}

// Bounds of the evaluations and the export (M3-16).
const (
	MaxBuckets     = 200
	TopCount       = 10
	MaxExportRows  = 10_000
	MaxExportBytes = 8 << 20
)

// steps are the bucket widths of an overview, the finest that keeps MaxBuckets.
var steps = []time.Duration{
	time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour, 3 * time.Hour, 6 * time.Hour,
	12 * time.Hour, 24 * time.Hour, 48 * time.Hour, 7 * 24 * time.Hour,
}

// StepFor returns the bucket width for a range: the finest of steps with at most MaxBuckets.
func StepFor(r time.Duration) time.Duration {
	for _, step := range steps {
		if (r+step-1)/step <= MaxBuckets {
			return step
		}
	}
	return steps[len(steps)-1]
}

// OverviewRequest is the range of an overview and optionally its nodes.
type OverviewRequest struct {
	From, To time.Time
	Nodes    []identity.NodeID
}

// Overview evaluates the events of the range in the caller's scope (M3-16).
func (s *Service) Overview(ctx context.Context, c access.Caller, r OverviewRequest) (Overview, error) {
	scope, err := s.scope(c)
	if err != nil {
		return Overview{}, err
	}
	if err := checkRange(r.From, r.To, len(r.Nodes)); err != nil {
		return Overview{}, err
	}
	step := StepFor(r.To.Sub(r.From))
	o, err := s.store.Overview(ctx, scope, OverviewQuery{From: r.From, To: r.To, Nodes: r.Nodes, Step: step, Top: TopCount})
	if err != nil {
		return Overview{}, fmt.Errorf("security overview: %w", err)
	}
	o.Step = step
	if s.geo != nil {
		o.GeoNotice = s.geo.Notice()
		for i, c := range o.TopIPs {
			if ip, err := netip.ParseAddr(c.Value); err == nil {
				o.TopIPs[i].Country = s.geo.Country(ip)
			}
		}
	}
	return o, nil
}

// Export is a CSV file of events.
type Export struct {
	FileName  string
	Content   []byte
	Rows      int
	Truncated bool
}

// Export returns the events of the filter as CSV, newest first, at most MaxExportRows and
// MaxExportBytes (M3-16).
func (s *Service) Export(ctx context.Context, c access.Caller, f Filter) (Export, error) {
	scope, err := s.scope(c)
	if err != nil {
		return Export{}, err
	}
	if err := checkFilter(f); err != nil {
		return Export{}, err
	}
	f.Before, f.Limit = nil, MaxExportRows+1
	events, err := s.store.List(ctx, scope, f)
	if err != nil {
		return Export{}, fmt.Errorf("export security events: %w", err)
	}
	truncated := len(events) > MaxExportRows
	if truncated {
		events = events[:MaxExportRows]
	}
	var ids []identity.NodeID
	seen := map[identity.NodeID]bool{}
	for _, e := range events {
		if !seen[e.Node] {
			seen[e.Node] = true
			ids = append(ids, e.Node)
		}
	}
	names := map[identity.NodeID]string{}
	if len(ids) > 0 {
		if names, err = s.store.Hostnames(ctx, scope, ids); err != nil {
			return Export{}, fmt.Errorf("export security events: %w", err)
		}
	}
	var buf bytes.Buffer
	rows, cut, err := WriteCSV(&buf, events, names, s.Country, MaxExportBytes)
	if err != nil {
		return Export{}, err
	}
	const stamp = "20060102T1504Z"
	return Export{
		FileName: "sermon-security-events-" + f.From.UTC().Format(stamp) + "-" + f.To.UTC().Format(stamp) + ".csv",
		Content:  buf.Bytes(), Rows: rows, Truncated: truncated || cut,
	}, nil
}

func token(c Cursor) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(c.At.UnixMicro(), 10) + "." + strconv.FormatInt(c.ID, 10)))
}

func parseToken(t string) (Cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(t)
	if err != nil {
		return Cursor{}, invalid("invalid page token")
	}
	at, id, ok := strings.Cut(string(raw), ".")
	micros, errAt := strconv.ParseInt(at, 10, 64)
	n, errID := strconv.ParseInt(id, 10, 64)
	if !ok || errAt != nil || errID != nil {
		return Cursor{}, invalid("invalid page token")
	}
	return Cursor{At: time.UnixMicro(micros).UTC(), ID: n}, nil
}

// ParseNetwork reads an address (a /32 or /128) or a network in CIDR notation.
func ParseNetwork(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, invalid("network %q", s)
		}
		return netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-unmappedBits(p.Addr())).Masked(), nil
	}
	ip, err := netip.ParseAddr(s)
	if err != nil || ip.Zone() != "" {
		return netip.Prefix{}, invalid("address %q", s)
	}
	ip = ip.Unmap()
	return netip.PrefixFrom(ip, ip.BitLen()), nil
}

// unmappedBits is what an IPv4-mapped prefix loses when it becomes IPv4.
func unmappedBits(ip netip.Addr) int {
	if ip.Is4In6() {
		return 96
	}
	return 0
}

// HorizonWarning is how far ahead the partitions must reach before the maintenance complains.
const HorizonWarning = 365 * 24 * time.Hour

// Maintenance drops the partitions older than the retention (the instance's default of
// defaultDays, SERMON_EVENT_RETENTION; the database keeps longer periods of organizations, ADR 0018)
// and warns when the partitions created by the migrations end within HorizonWarning. It runs at
// once and then every interval until ctx ends.
func Maintenance(ctx context.Context, p Partitions, now func() time.Time, interval time.Duration, defaultDays uint32, logger *slog.Logger) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		at := now()
		if horizon, err := p.Horizon(ctx); err != nil {
			logger.ErrorContext(ctx, "security event partitions: horizon unknown", "error", err.Error())
		} else if horizon.Before(at.Add(HorizonWarning)) {
			logger.ErrorContext(ctx, "security event partitions end soon – update sermon-server (its migrations add more)",
				"horizon", horizon.Format(time.DateOnly))
		}
		if dropped, err := p.Expire(ctx, at.Add(-time.Duration(defaultDays)*24*time.Hour), defaultDays); err != nil {
			logger.ErrorContext(ctx, "security event partitions: retention failed", "error", err.Error())
		} else if dropped > 0 {
			logger.InfoContext(ctx, "security event partitions dropped (retention)", "count", dropped)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
