package secevents

import (
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
	"github.com/pmoscode/SerMon/server/internal/domain/secevent"
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
}

// NewService returns the service.
func NewService(store Store, policy authz.Policy) *Service {
	return &Service{store: store, policy: policy}
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", secevent.ErrInvalid, fmt.Sprintf(format, args...))
}

// List returns a page of the events of the request; access.ErrForbidden without
// ViewSecurityEvents on any group, secevent.ErrInvalid for a bad filter.
func (s *Service) List(ctx context.Context, c access.Caller, r Request) (Page, error) {
	scope, err := s.policy.ScopeForAction(c.Principal, authz.ViewSecurityEvents)
	if err != nil || (!scope.AllGroups() && len(scope.Groups()) == 0) {
		return Page{}, access.ErrForbidden
	}
	f := r.Filter
	switch {
	case f.From.IsZero() || f.To.IsZero() || !f.To.After(f.From):
		return Page{}, invalid("the time range needs from before to")
	case f.To.Sub(f.From) > MaxRange:
		return Page{}, invalid("the time range is at most %v", MaxRange)
	case r.PageSize < 0 || r.PageSize > MaxPageSize:
		return Page{}, invalid("page size between 0 and %d", MaxPageSize)
	case len(f.Nodes) > MaxNodes:
		return Page{}, invalid("at most %d nodes", MaxNodes)
	case f.User != nil && len(*f.User) > secevent.MaxUser:
		return Page{}, invalid("user longer than %d bytes", secevent.MaxUser)
	case f.Network != nil && !f.Network.IsValid():
		return Page{}, invalid("invalid network")
	}
	for _, k := range f.Kinds {
		if _, err := secevent.ParseKind(string(k)); err != nil {
			return Page{}, err
		}
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

// Maintenance drops the partitions older than the retention (a year, ADR 0018) and warns when the
// partitions created by the migrations end within HorizonWarning. It runs at once and then every
// interval until ctx ends.
func Maintenance(ctx context.Context, p Partitions, now func() time.Time, interval time.Duration, logger *slog.Logger) error {
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
		if dropped, err := p.Expire(ctx, at.AddDate(-1, 0, 0)); err != nil {
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
