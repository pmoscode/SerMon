package liveview

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Bounds of a viewer's stream.
const (
	// RecheckEvery checks the session, the node's visibility and the switch again.
	RecheckEvery = 30 * time.Second
	// MaxDuration ends a stream; the client opens a new one.
	MaxDuration = 15 * time.Minute
)

// Directory lists the nodes of a scope (fleet.Directory).
type Directory interface {
	List(ctx context.Context, scope tenant.Scope) ([]fleet.Node, error)
}

// Settings reads the organization's switch for command lines.
type Settings interface {
	HideCommandLines(ctx context.Context, scope tenant.Scope) (bool, error)
}

// Service is the use case of the viewers.
type Service struct {
	hub       *Hub
	directory Directory
	settings  Settings
	policy    authz.Policy
	now       func() time.Time
	recheck   time.Duration
}

// Option configures the service.
type Option func(*Service)

// WithClock sets the clock (tests).
func WithClock(now func() time.Time) Option { return func(s *Service) { s.now = now } }

// WithRecheck sets the interval of the rechecks (tests).
func WithRecheck(d time.Duration) Option { return func(s *Service) { s.recheck = d } }

// NewService returns the service.
func NewService(hub *Hub, directory Directory, settings Settings, policy authz.Policy, opts ...Option) *Service {
	s := &Service{hub: hub, directory: directory, settings: settings, policy: policy, now: time.Now, recheck: RecheckEvery}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Run maintains the hub until ctx ends.
func (s *Service) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.hub.Maintain(s.now())
		}
	}
}

// access returns the scope and the switch if the caller may watch the node: access.ErrForbidden
// without use_live_view, access.ErrNotFound for a node it doesn't see.
func (s *Service) access(ctx context.Context, c access.Caller, node identity.NodeID) (tenant.Scope, bool, error) {
	scope, err := s.policy.ScopeForAction(c.Principal, authz.UseLiveView)
	if err != nil || (!scope.AllGroups() && len(scope.Groups()) == 0) {
		return tenant.Scope{}, false, access.ErrForbidden
	}
	nodes, err := s.directory.List(ctx, scope)
	if err != nil {
		return tenant.Scope{}, false, fmt.Errorf("visible nodes: %w", err)
	}
	if !slices.ContainsFunc(nodes, func(n fleet.Node) bool { return n.ID == node }) {
		return tenant.Scope{}, false, access.ErrNotFound
	}
	hide, err := s.settings.HideCommandLines(ctx, scope)
	if err != nil {
		return tenant.Scope{}, false, fmt.Errorf("live view settings: %w", err)
	}
	return scope, hide, nil
}

// Watch sends the frames of the node's live view until ctx ends, MaxDuration is over or the agent
// refuses. Every recheck runs recheck (the caller's session) and checks access and the switch
// again: a lost right ends with its error, a changed switch normally (the client reconnects).
func (s *Service) Watch(ctx context.Context, c access.Caller, node identity.NodeID, send func(Frame) error, recheck func() error) error {
	scope, hide, err := s.access(ctx, c, node)
	if err != nil {
		return err
	}
	start := s.now()
	sub := s.hub.Subscribe(scope.OrgID(), node, hide, start)
	defer func() { sub.Close(s.now()) }()
	ticker := time.NewTicker(s.recheck)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err() //nolint:wrapcheck // cancellation by the client
		case frame := <-sub.Frames():
			if err := send(frame); err != nil {
				return err
			}
			if frame.Refused != "" {
				return nil
			}
		case <-ticker.C:
			if s.now().Sub(start) >= MaxDuration {
				return nil
			}
			if recheck != nil {
				if err := recheck(); err != nil {
					return err
				}
			}
			_, now, err := s.access(ctx, c, node)
			if err != nil {
				return err
			}
			if now != hide {
				return nil
			}
		}
	}
}
