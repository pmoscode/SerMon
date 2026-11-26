// Package nodelife deactivates and deletes nodes (KONZEPT §6 "Widerruf und Lebenszyklus", ADR
// 0041): a deactivated node has its certificates revoked and is refused; a deleted one is
// deactivated and purged with its data after a grace, until then the deletion can be cancelled.
package nodelife

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Deletion grace (ADR 0041): operators set it with SERMON_NODE_DELETION_GRACE, not below
// MinDeletionGrace.
const (
	DefaultDeletionGrace = 7 * 24 * time.Hour
	MinDeletionGrace     = 24 * time.Hour
	// MaintainInterval is how often due nodes are purged.
	MaintainInterval = time.Hour
	// PurgeBatch bounds the nodes one run purges; the rest follow in the next.
	PurgeBatch = 100
)

var (
	// ErrHostnameMismatch means the confirmation does not match the node's host name.
	ErrHostnameMismatch = errors.New("type the host name of the node to confirm")
	// ErrNotScheduled means the node is not scheduled for deletion.
	ErrNotScheduled = errors.New("the node is not scheduled for deletion")
	// ErrGraceTooShort means the configured deletion grace is below MinDeletionGrace.
	ErrGraceTooShort = fmt.Errorf("the node deletion grace must be at least %v", MinDeletionGrace)
	// ErrPurging means the job has started to delete the node (its logs may be gone already).
	ErrPurging = errors.New("the node is being deleted already")
	// ErrDeactivated ends the session of a node that was deactivated or deleted.
	ErrDeactivated = errors.New("node deactivated by an admin; re-enroll it after a takeover is allowed")
)

// State is a node's lifecycle as its admin sees it.
type State struct {
	Group         tenant.GroupID
	Hostname      string
	DeactivatedAt *time.Time
	DeletionDueAt *time.Time
	// PurgeStartedAt is set once the job deletes the node: from then on no cancellation.
	PurgeStartedAt *time.Time
}

// Op is what a change does to a node.
type Op uint8

// Ops of a change.
const (
	OpNone Op = iota
	// OpDeactivate marks the node deactivated (if it is not) and revokes all its certificates.
	OpDeactivate
	// OpScheduleDeletion deactivates as OpDeactivate and sets the deadline Due.
	OpScheduleDeletion
	// OpCancelDeletion lifts the deadline; the node stays deactivated.
	OpCancelDeletion
)

// Change is what a decision on a node's state does, with its audit entry.
type Change struct {
	Op      Op
	At, Due time.Time
	Audit   access.AuditEntry
}

// Due is a node whose deletion is due.
type Due struct {
	Org  tenant.OrgID
	Node identity.NodeID
}

// Store persists the lifecycle. Every change writes its audit entry in the same transaction.
type Store interface {
	// Change locks the node if it lies within the scope's groups (access.ErrNotFound otherwise),
	// lets decide choose the change from its state and makes it with its audit entry, all in one
	// transaction; an error of decide is returned as is.
	Change(ctx context.Context, scope tenant.Scope, node identity.NodeID, decide func(State) (Change, error)) error
	// DueForDeletion returns at most limit deactivated nodes of all organizations that are due.
	DueForDeletion(ctx context.Context, at time.Time, limit int) ([]Due, error)
	// StartPurge marks a deactivated node that is due as being deleted (checked under its lock)
	// and reports whether it is; a node no longer due is left alone.
	StartPurge(ctx context.Context, org tenant.OrgID, node identity.NodeID, at time.Time) (bool, error)
	// Purge deletes a deactivated node that is due and marked with its data and writes node.purge
	// into the organization's audit log; it refuses anything else.
	Purge(ctx context.Context, org tenant.OrgID, node identity.NodeID, at time.Time) error
}

// Live ends the agent session of a node on this server; other instances end theirs at their
// next certificate check.
type Live interface {
	Terminate(node identity.NodeID, cause error)
}

// LogEraser deletes the logs of a node (Loki); an error leaves the node due.
type LogEraser interface {
	EraseNode(ctx context.Context, org tenant.OrgID, node identity.NodeID) error
}

// Service is the use case.
type Service struct {
	store  Store
	policy authz.Policy
	now    func() time.Time
	grace  time.Duration
	live   Live
	logs   LogEraser
	logger *slog.Logger
}

// Option configures a Service.
type Option func(*Service)

// WithLive ends the sessions of deactivated nodes at once.
func WithLive(l Live) Option { return func(s *Service) { s.live = l } }

// WithLogs deletes a node's logs before its data.
func WithLogs(e LogEraser) Option { return func(s *Service) { s.logs = e } }

// WithLogger logs the purges of the job.
func WithLogger(l *slog.Logger) Option { return func(s *Service) { s.logger = l } }

// NewService returns the use case; grace is the deletion grace (ErrGraceTooShort below the
// minimum).
func NewService(store Store, policy authz.Policy, now func() time.Time, grace time.Duration, options ...Option) (*Service, error) {
	if grace < MinDeletionGrace {
		return nil, ErrGraceTooShort
	}
	s := &Service{store: store, policy: policy, now: now, grace: grace, logger: slog.New(slog.DiscardHandler)}
	for _, o := range options {
		o(s)
	}
	return s, nil
}

// Grace is the configured deletion grace.
func (s *Service) Grace() time.Duration { return s.grace }

// authorize checks the caller (admin on a group, fresh step-up) and returns the groups it may
// change nodes in.
func (s *Service) authorize(c access.Caller) (tenant.Scope, error) {
	scope, err := s.policy.ScopeForAction(c.Principal, authz.ManageNodeLifecycle)
	if err != nil || (!scope.AllGroups() && len(scope.Groups()) == 0) {
		return tenant.Scope{}, access.ErrForbidden
	}
	if s.now().Sub(c.StepUpAt) > access.StepUpMaxAge {
		return tenant.Scope{}, access.ErrStepUpRequired
	}
	return scope, nil
}

// change authorizes the caller and lets decide choose the change from the node's state, checked
// and made in one transaction within the caller's groups (M5-33). It reports whether anything
// changed.
func (s *Service) change(ctx context.Context, c access.Caller, node identity.NodeID, decide func(State) (Change, error)) (bool, error) {
	scope, err := s.authorize(c)
	if err != nil {
		return false, err
	}
	changed := false
	err = s.store.Change(ctx, scope, node, func(st State) (Change, error) {
		ch, err := decide(st)
		changed = err == nil && ch.Op != OpNone
		return ch, err
	})
	if err != nil {
		return false, err //nolint:wrapcheck // the callers wrap
	}
	return changed, nil
}

func entry(c access.Caller, action string, node identity.NodeID, st State, extra map[string]string) access.AuditEntry {
	details := map[string]string{"group": st.Group.String(), "hostname": st.Hostname}
	for k, v := range extra {
		details[k] = v
	}
	return access.AuditEntry{Actor: c.Principal.User, Action: action, Target: "node:" + node.String(), Details: details}
}

// Deactivate revokes the node's certificates and ends its session; its data stays. A node
// deactivated already is left as it is.
func (s *Service) Deactivate(ctx context.Context, c access.Caller, node identity.NodeID) error {
	changed, err := s.change(ctx, c, node, func(st State) (Change, error) {
		if st.DeactivatedAt != nil {
			return Change{Op: OpNone}, nil
		}
		return Change{Op: OpDeactivate, At: s.now(), Audit: entry(c, "node.deactivate", node, st, nil)}, nil
	})
	if err != nil {
		return fmt.Errorf("deactivate node: %w", err)
	}
	if changed {
		s.terminate(node)
	}
	return nil
}

// Delete deactivates the node and schedules its deletion after the grace; typed must be its host
// name. A node scheduled already keeps its deadline.
func (s *Service) Delete(ctx context.Context, c access.Caller, node identity.NodeID, typed string) (time.Time, error) {
	var due time.Time
	changed, err := s.change(ctx, c, node, func(st State) (Change, error) {
		if strings.TrimSpace(typed) != st.Hostname {
			return Change{}, ErrHostnameMismatch
		}
		if st.DeletionDueAt != nil {
			due = *st.DeletionDueAt
			return Change{Op: OpNone}, nil
		}
		now := s.now()
		due = now.Add(s.grace)
		audit := entry(c, "node.schedule_deletion", node, st, map[string]string{"deletion_due_at": due.UTC().Format(time.RFC3339)})
		return Change{Op: OpScheduleDeletion, At: now, Due: due, Audit: audit}, nil
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("schedule deletion: %w", err)
	}
	if changed {
		s.terminate(node)
	}
	return due, nil
}

// CancelDeletion lifts a scheduled deletion; the node stays deactivated. Once the job has started
// to delete the node, it is too late (ErrPurging).
func (s *Service) CancelDeletion(ctx context.Context, c access.Caller, node identity.NodeID) error {
	_, err := s.change(ctx, c, node, func(st State) (Change, error) {
		switch {
		case st.DeletionDueAt == nil:
			return Change{}, ErrNotScheduled
		case st.PurgeStartedAt != nil:
			return Change{}, ErrPurging
		}
		return Change{Op: OpCancelDeletion, Audit: entry(c, "node.cancel_deletion", node, st, nil)}, nil
	})
	if err != nil {
		return fmt.Errorf("cancel deletion: %w", err)
	}
	return nil
}

func (s *Service) terminate(node identity.NodeID) {
	if s.live != nil {
		s.live.Terminate(node, ErrDeactivated)
	}
}

// Maintain purges the nodes that are due, each after its logs; it returns the first error after
// trying all.
func (s *Service) Maintain(ctx context.Context) error {
	now := s.now()
	due, err := s.store.DueForDeletion(ctx, now, PurgeBatch)
	if err != nil {
		return fmt.Errorf("due node deletions: %w", err)
	}
	var first error
	for _, d := range due {
		purged, err := s.purge(ctx, d, now)
		if err == nil {
			if purged {
				s.logger.InfoContext(ctx, "node deleted after its grace", "org", d.Org.String(), "node", d.Node.String())
			}
			continue
		}
		s.logger.ErrorContext(ctx, "node deletion failed", "org", d.Org.String(), "node", d.Node.String(), "error", err)
		if first == nil {
			first = fmt.Errorf("delete node: %w", err)
		}
	}
	return first
}

// purge marks the node under its lock before it erases the logs, so that a cancellation that
// came first keeps them and one that comes later is refused (M5-33).
func (s *Service) purge(ctx context.Context, d Due, now time.Time) (bool, error) {
	due, err := s.store.StartPurge(ctx, d.Org, d.Node, now)
	if err != nil {
		return false, fmt.Errorf("mark the node: %w", err)
	}
	if !due {
		return false, nil
	}
	if s.logs != nil {
		if err := s.logs.EraseNode(ctx, d.Org, d.Node); err != nil {
			return false, fmt.Errorf("delete the logs: %w", err)
		}
	}
	if err := s.store.Purge(ctx, d.Org, d.Node, now); err != nil {
		return false, fmt.Errorf("delete the data: %w", err)
	}
	return true, nil
}

// Run calls Maintain every MaintainInterval until ctx ends.
func (s *Service) Run(ctx context.Context) error {
	ticker := time.NewTicker(MaintainInterval)
	defer ticker.Stop()
	for {
		if err := s.Maintain(ctx); err != nil {
			s.logger.WarnContext(ctx, "node maintenance", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
