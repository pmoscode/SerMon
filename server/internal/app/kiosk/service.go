// Package kiosk keeps the kiosk tokens of shared dashboards and serves the displays that use them
// (KONZEPT §16, ADR 0040, M5-26). A token is a read-only credential without a session: it opens
// one shared dashboard of one organization, and its widgets are read with the current rights of
// the admin who created it, through the same readers as the UI. The display names a widget, the
// query comes from the stored document.
package kiosk

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/alerts"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/dashboards"
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/app/logs"
	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/domain/dashboard"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/logql"
	"github.com/pmoscode/SerMon/server/internal/domain/metric"
	"github.com/pmoscode/SerMon/server/internal/domain/naming"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Limits of the tokens.
const (
	// Prefix starts every secret, so that scanners and people recognize it.
	Prefix          = "smk_"
	secretBytes     = 32
	MaxOpen         = 10
	MinValidity     = 24 * time.Hour
	MaxValidity     = 365 * 24 * time.Hour
	maxName         = 100
	logLines        = 50
	maxLogSpan      = 14 * 24 * time.Hour
	maxQueryNodes   = 100
	secretTextBytes = len(Prefix) + 43 // base64url of 32 bytes without padding
)

var (
	// ErrInvalidToken means the secret opens nothing (unknown, revoked, expired, dashboard not
	// shared, creator without the right); not distinguished on purpose.
	ErrInvalidToken = errors.New("invalid kiosk token")
	// ErrNotShared means the dashboard is private: only shared ones get tokens.
	ErrNotShared = errors.New("share the dashboard first: kiosk tokens open shared dashboards only")
	// ErrTooMany means the dashboard has MaxOpen open tokens.
	ErrTooMany = fmt.Errorf("at most %d open kiosk tokens per dashboard", MaxOpen)
	// ErrInvalid means a request the service doesn't take.
	ErrInvalid = errors.New("invalid kiosk token request")
)

// TokenID identifies a token.
type TokenID uuid.UUID

// String returns the UUID.
func (id TokenID) String() string { return uuid.UUID(id).String() }

// Token is a stored token without its secret.
type Token struct {
	ID            TokenID
	Dashboard     dashboards.ID
	Name          string
	CreatedBy     identity.UserID
	CreatedByName string
	CreatedAt     time.Time
	ExpiresAt     time.Time
	LastUsedAt    *time.Time
	RevokedAt     *time.Time
}

// Display is an authenticated kiosk display: what its token opens and with whose rights.
type Display struct {
	Token     TokenID
	Org       tenant.OrgID
	Dashboard dashboards.ID
	caller    access.Caller
}

// Found is a usable token of the lookup.
type Found struct {
	Token     TokenID
	Org       tenant.OrgID
	Dashboard dashboards.ID
	Creator   identity.UserID
	Hash      [sha256.Size]byte
}

// Store keeps the tokens.
type Store interface {
	List(ctx context.Context, scope tenant.Scope, dashboard dashboards.ID) ([]Token, error)
	// Create stores the token if its dashboard is shared (ErrNotShared) and has fewer than
	// MaxOpen open tokens (ErrTooMany), with the audit entry.
	Create(ctx context.Context, scope tenant.Scope, t Token, hash [sha256.Size]byte, audit access.AuditEntry) error
	// Revoke ends a token (access.ErrNotFound if there is none), with the audit entry.
	Revoke(ctx context.Context, scope tenant.Scope, id TokenID, at time.Time, audit access.AuditEntry) error
	// Lookup finds a usable token by the hash of its secret, outside any tenant.
	Lookup(ctx context.Context, hash [sha256.Size]byte, at time.Time) (Found, bool, error)
	// Touch notes the use of a token (at most once a minute).
	Touch(ctx context.Context, scope tenant.Scope, id TokenID, at time.Time) error
}

// Principals loads the current rights of a user in an organization (access.Loader).
type Principals interface {
	ForUser(ctx context.Context, org tenant.OrgID, user identity.UserID) (authz.Principal, error)
}

// Dashboards reads a dashboard as a user sees it (dashboards.Store).
type Dashboards interface {
	Get(ctx context.Context, scope tenant.Scope, user identity.UserID, id dashboards.ID) (dashboards.Dashboard, error)
}

// Readers are the readers of the UI (fleet.Service, logs.Service, alerts.Service).
type Readers struct {
	Fleet interface {
		Summary(ctx context.Context, c access.Caller, group *tenant.GroupID) (fleet.Summary, error)
		Query(ctx context.Context, c access.Caller, q metrics.Query) ([]metrics.Series, time.Duration, error)
	}
	Logs interface {
		Query(ctx context.Context, c access.Caller, f logs.Filter, limit int, oldestFirst bool) (logs.Page, error)
	}
	Alerts interface {
		List(ctx context.Context, c access.Caller) ([]alerts.Alert, error)
	}
}

// Deps are the dependencies of the service.
type Deps struct {
	Store      Store
	Dashboards Dashboards
	Principals Principals
	Readers    Readers
	Policy     authz.Policy
	Now        func() time.Time
	NewID      func() uuid.UUID
}

// Service manages the tokens and serves the displays.
type Service struct{ d Deps }

// NewService returns the service.
func NewService(d Deps) *Service { return &Service{d: d} }

// List returns the tokens of a dashboard (admins of all groups).
func (s *Service) List(ctx context.Context, c access.Caller, dashboard dashboards.ID) ([]Token, error) {
	scope, err := access.Authorize(s.d.Policy, c, authz.ManageKioskTokens, false, s.d.Now())
	if err != nil {
		return nil, err //nolint:wrapcheck // access errors
	}
	if _, err := s.d.Dashboards.Get(ctx, scope, c.Principal.User, dashboard); err != nil {
		return nil, fmt.Errorf("kiosk tokens: %w", err)
	}
	list, err := s.d.Store.List(ctx, scope, dashboard)
	if err != nil {
		return nil, fmt.Errorf("list kiosk tokens: %w", err)
	}
	return list, nil
}

// Create adds a token for a shared dashboard the caller sees (step-up, audit) and returns it with
// its secret, which is not kept.
func (s *Service) Create(ctx context.Context, c access.Caller, dashboard dashboards.ID, name string, validFor time.Duration) (Token, string, error) {
	now := s.d.Now()
	scope, err := access.Authorize(s.d.Policy, c, authz.ManageKioskTokens, true, now)
	if err != nil {
		return Token{}, "", err //nolint:wrapcheck // access errors
	}
	clean, ok := naming.Clean(name, maxName)
	if !ok {
		return Token{}, "", fmt.Errorf("%w: the name needs 1 to %d visible characters", ErrInvalid, maxName)
	}
	if validFor < MinValidity || validFor > MaxValidity {
		return Token{}, "", fmt.Errorf("%w: a token is valid for 1 to 365 days", ErrInvalid)
	}
	d, err := s.d.Dashboards.Get(ctx, scope, c.Principal.User, dashboard)
	if err != nil {
		return Token{}, "", fmt.Errorf("kiosk token: %w", err)
	}
	if !d.Shared {
		return Token{}, "", ErrNotShared
	}
	secret, hash, err := newSecret()
	if err != nil {
		return Token{}, "", err
	}
	t := Token{
		ID: TokenID(s.d.NewID()), Dashboard: dashboard, Name: clean, CreatedBy: c.Principal.User,
		CreatedAt: now, ExpiresAt: now.Add(validFor),
	}
	audit := access.AuditEntry{
		Actor: c.Principal.User, Action: "kiosk_token.create", Target: "kiosk_token:" + t.ID.String(),
		Details: map[string]string{"dashboard": dashboard.String(), "name": clean, "expires_at": t.ExpiresAt.UTC().Format(time.RFC3339)},
	}
	if err := s.d.Store.Create(ctx, scope, t, hash, audit); err != nil {
		return Token{}, "", fmt.Errorf("create kiosk token: %w", err)
	}
	return t, secret, nil
}

// Revoke ends a token (audit); revoking needs no step-up, it takes access away.
func (s *Service) Revoke(ctx context.Context, c access.Caller, id TokenID) error {
	now := s.d.Now()
	scope, err := access.Authorize(s.d.Policy, c, authz.ManageKioskTokens, false, now)
	if err != nil {
		return err //nolint:wrapcheck // access errors
	}
	audit := access.AuditEntry{Actor: c.Principal.User, Action: "kiosk_token.revoke", Target: "kiosk_token:" + id.String()}
	if err := s.d.Store.Revoke(ctx, scope, id, now, audit); err != nil {
		return fmt.Errorf("revoke kiosk token: %w", err)
	}
	return nil
}

func newSecret() (string, [sha256.Size]byte, error) {
	raw := make([]byte, secretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", [sha256.Size]byte{}, fmt.Errorf("kiosk token secret: %w", err)
	}
	secret := Prefix + base64.RawURLEncoding.EncodeToString(raw)
	return secret, sha256.Sum256([]byte(secret)), nil
}

// Authenticate checks the secret of a display: a usable token whose creator is still an admin of
// all groups of the organization. Anything else is ErrInvalidToken.
func (s *Service) Authenticate(ctx context.Context, secret string) (Display, error) {
	if len(secret) != secretTextBytes || !strings.HasPrefix(secret, Prefix) {
		return Display{}, ErrInvalidToken
	}
	if _, err := base64.RawURLEncoding.DecodeString(secret[len(Prefix):]); err != nil {
		return Display{}, ErrInvalidToken
	}
	hash := sha256.Sum256([]byte(secret))
	now := s.d.Now()
	found, ok, err := s.d.Store.Lookup(ctx, hash, now)
	if err != nil {
		return Display{}, fmt.Errorf("look up kiosk token: %w", err)
	}
	if !ok || subtle.ConstantTimeCompare(found.Hash[:], hash[:]) != 1 {
		return Display{}, ErrInvalidToken
	}
	p, err := s.d.Principals.ForUser(ctx, found.Org, found.Creator)
	switch {
	case errors.Is(err, access.ErrNoMembership):
		return Display{}, ErrInvalidToken
	case err != nil:
		return Display{}, fmt.Errorf("rights of the kiosk token: %w", err)
	}
	if !s.d.Policy.Allowed(p, authz.ManageKioskTokens, authz.InOrg(found.Org)) {
		return Display{}, ErrInvalidToken
	}
	scope, err := tenant.ForAllGroups(found.Org)
	if err != nil {
		return Display{}, ErrInvalidToken
	}
	if err := s.d.Store.Touch(ctx, scope, found.Token, now); err != nil {
		return Display{}, fmt.Errorf("note kiosk token use: %w", err)
	}
	// The display is no session: no step-up, ever.
	return Display{Token: found.Token, Org: found.Org, Dashboard: found.Dashboard, caller: access.Caller{Principal: p}}, nil
}

// Dashboard returns the token's dashboard while it is shared.
func (s *Service) Dashboard(ctx context.Context, d Display) (dashboards.Dashboard, error) {
	scope, err := tenant.ForAllGroups(d.Org)
	if err != nil {
		return dashboards.Dashboard{}, ErrInvalidToken
	}
	b, err := s.d.Dashboards.Get(ctx, scope, d.caller.Principal.User, d.Dashboard)
	if errors.Is(err, access.ErrNotFound) || (err == nil && !b.Shared) {
		return dashboards.Dashboard{}, ErrInvalidToken
	}
	if err != nil {
		return dashboards.Dashboard{}, fmt.Errorf("kiosk dashboard: %w", err)
	}
	return b, nil
}

// Result is the data of one widget; exactly one part is set by the kind.
type Result struct {
	From, To  time.Time
	Unit      metric.Unit
	Series    []metrics.Series
	Hostnames map[identity.NodeID]string
	Logs      *logs.Page
	Alerts    []alerts.Alert
	Kind      dashboard.Kind
}

// Widget reads one widget of the token's dashboard for its default range ending now; the
// variables take their defaults (or the first visible node or group).
func (s *Service) Widget(ctx context.Context, d Display, widgetID string) (Result, error) {
	b, err := s.Dashboard(ctx, d)
	if err != nil {
		return Result{}, err
	}
	w, ok := b.Spec.Widget(widgetID)
	if !ok {
		return Result{}, access.ErrNotFound
	}
	summary, err := s.d.Readers.Fleet.Summary(ctx, d.caller, nil)
	if err != nil {
		return Result{}, fmt.Errorf("kiosk fleet: %w", err)
	}
	nodes := summary.Nodes
	slices.SortFunc(nodes, func(a, b fleet.NodeStatus) int { return strings.Compare(a.Hostname, b.Hostname) })
	to := s.d.Now()
	res := Result{From: to.Add(-b.Spec.TimeRange.Duration()), To: to, Kind: w.Kind, Hostnames: map[identity.NodeID]string{}}
	for _, n := range nodes {
		res.Hostnames[n.ID] = n.Hostname
	}
	target, group, all := resolve(w, b.Spec, nodes)
	switch w.Kind {
	case dashboard.KindLogStream:
		return s.logs(ctx, d, w, res, target, group)
	case dashboard.KindAlertList:
		return s.alerts(ctx, d, res, target, all)
	case dashboard.KindTimeseries, dashboard.KindGauge, dashboard.KindStat, dashboard.KindTable:
	}
	def, _ := metric.Lookup(w.Query.Metric)
	res.Unit = def.Unit
	if len(target) == 0 {
		return res, nil
	}
	q := metrics.Query{Metric: w.Query.Metric, Nodes: capped(target), Labels: w.Query.Labels, From: res.From, To: res.To}
	series, _, err := s.d.Readers.Fleet.Query(ctx, d.caller, q)
	if err != nil {
		return Result{}, fmt.Errorf("kiosk metrics: %w", err)
	}
	res.Series = series
	return res, nil
}

func (s *Service) logs(ctx context.Context, d Display, w dashboard.Widget, res Result, target []identity.NodeID, group *tenant.GroupID) (Result, error) {
	res.Logs = &logs.Page{}
	if len(target) == 0 && group == nil {
		return res, nil
	}
	f := logs.Filter{From: maxTime(res.From, res.To.Add(-maxLogSpan)), To: res.To, Text: w.Query.Text, Group: group}
	if group == nil {
		f.Nodes = capped(target)
	}
	if code := w.Query.MinSeverity; code > 0 && code <= len(logql.Severities)-1 {
		f.MinSeverity = logql.Severities[code-1]
	}
	page, err := s.d.Readers.Logs.Query(ctx, d.caller, f, logLines, false)
	if err != nil {
		return Result{}, fmt.Errorf("kiosk logs: %w", err)
	}
	res.Logs = &page
	return res, nil
}

func (s *Service) alerts(ctx context.Context, d Display, res Result, target []identity.NodeID, all bool) (Result, error) {
	list, err := s.d.Readers.Alerts.List(ctx, d.caller)
	if err != nil {
		return Result{}, fmt.Errorf("kiosk alerts: %w", err)
	}
	res.Alerts = []alerts.Alert{}
	for _, a := range list {
		if all || slices.Contains(target, a.Key.Node) {
			res.Alerts = append(res.Alerts, a)
		}
	}
	return res, nil
}

// resolve returns the nodes of the widget among the visible ones (sorted by name), its group and
// whether it means all nodes, as the UI resolves them without a choice of the viewer.
func resolve(w dashboard.Widget, spec dashboard.Spec, nodes []fleet.NodeStatus) ([]identity.NodeID, *tenant.GroupID, bool) {
	ofGroup := func(g tenant.GroupID) ([]identity.NodeID, *tenant.GroupID, bool) {
		var out []identity.NodeID
		for _, n := range nodes {
			if n.Group == g {
				out = append(out, n.ID)
			}
		}
		return out, &g, false
	}
	t := w.Query.Target
	switch {
	case len(t.Nodes) > 0:
		return slices.Clone(t.Nodes), nil, false
	case t.Group != nil:
		return ofGroup(*t.Group)
	case t.Variable != "":
		i := slices.IndexFunc(spec.Variables, func(v dashboard.Variable) bool { return v.Name == t.Variable })
		if i < 0 {
			return nil, nil, false
		}
		v := spec.Variables[i]
		if v.Kind == dashboard.VariableGroup {
			g, ok := defaultGroup(v, nodes)
			if !ok {
				return nil, nil, false
			}
			return ofGroup(g)
		}
		if n, ok := defaultNode(v, nodes); ok {
			return []identity.NodeID{n}, nil, false
		}
		return nil, nil, false
	}
	return nil, nil, w.Kind == dashboard.KindAlertList
}

func defaultNode(v dashboard.Variable, nodes []fleet.NodeStatus) (identity.NodeID, bool) {
	if slices.ContainsFunc(nodes, func(n fleet.NodeStatus) bool { return n.ID == identity.NodeID(v.Default) }) {
		return identity.NodeID(v.Default), true
	}
	if len(nodes) > 0 {
		return nodes[0].ID, true
	}
	return identity.NodeID{}, false
}

func defaultGroup(v dashboard.Variable, nodes []fleet.NodeStatus) (tenant.GroupID, bool) {
	if slices.ContainsFunc(nodes, func(n fleet.NodeStatus) bool { return n.Group == tenant.GroupID(v.Default) }) {
		return tenant.GroupID(v.Default), true
	}
	byName := slices.Clone(nodes)
	slices.SortFunc(byName, func(a, b fleet.NodeStatus) int { return strings.Compare(a.GroupName, b.GroupName) })
	if len(byName) > 0 {
		return byName[0].Group, true
	}
	return tenant.GroupID{}, false
}

func capped(nodes []identity.NodeID) []identity.NodeID {
	if len(nodes) > maxQueryNodes {
		return nodes[:maxQueryNodes]
	}
	return nodes
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
