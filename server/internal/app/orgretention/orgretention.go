// Package orgretention lets organizations keep their logs and security events shorter than the
// instance (M5-30): admins set it within the limits and the cap, a job deletes what is older.
package orgretention

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/retention"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

const (
	// MaintainInterval is how often expired events are deleted.
	MaintainInterval = time.Hour
	// LogEraseEvery is how often Loki is asked to delete an organization's old logs: one delete
	// request a day per organization, so its store stays small.
	LogEraseEvery = 24 * time.Hour
)

// OrgSettings is an organization with its own retention.
type OrgSettings struct {
	Org          tenant.OrgID
	Settings     retention.Settings
	LogsErasedAt *time.Time
}

// Store persists the settings and deletes expired events.
type Store interface {
	// Get returns the organization's settings and its retention cap (0: none).
	Get(ctx context.Context, scope tenant.Scope) (retention.Settings, uint32, error)
	// Set stores the settings and writes the audit entry in the same transaction.
	Set(ctx context.Context, scope tenant.Scope, s retention.Settings, audit access.AuditEntry) error
	// Configured returns the organizations with a retention of their own (instance level).
	Configured(ctx context.Context) ([]OrgSettings, error)
	// ExpireEvents deletes the organization's security events and alert history before before.
	ExpireEvents(ctx context.Context, org tenant.OrgID, before time.Time) (int64, error)
	// MarkLogsErased records when Loki was last asked to delete the organization's old logs.
	MarkLogsErased(ctx context.Context, org tenant.OrgID, at time.Time) error
}

// LogEraser asks Loki to delete an organization's logs before a time.
type LogEraser interface {
	EraseBefore(ctx context.Context, org tenant.OrgID, before time.Time) error
}

// View is what admins see.
type View struct {
	Settings retention.Settings
	CapDays  uint32
}

// Service is the use case.
type Service struct {
	store  Store
	policy authz.Policy
	now    func() time.Time
	logs   LogEraser
	logger *slog.Logger
}

// Option configures a Service.
type Option func(*Service)

// WithLogs deletes old logs in Loki.
func WithLogs(e LogEraser) Option { return func(s *Service) { s.logs = e } }

// WithLogger logs the job.
func WithLogger(l *slog.Logger) Option { return func(s *Service) { s.logger = l } }

// NewService returns the use case.
func NewService(store Store, policy authz.Policy, now func() time.Time, options ...Option) *Service {
	s := &Service{store: store, policy: policy, now: now, logger: slog.New(slog.DiscardHandler)}
	for _, o := range options {
		o(s)
	}
	return s
}

// Get returns the organization's retention (admins of all groups).
func (s *Service) Get(ctx context.Context, c access.Caller) (View, error) {
	scope, err := access.Authorize(s.policy, c, authz.ManageRetention, false, s.now())
	if err != nil {
		return View{}, err //nolint:wrapcheck // the access errors are the API
	}
	set, capDays, err := s.store.Get(ctx, scope)
	if err != nil {
		return View{}, fmt.Errorf("load retention: %w", err)
	}
	return View{Settings: set, CapDays: capDays}, nil
}

// Set changes the organization's retention with a fresh step-up; it takes effect with the next
// run of the job (within an hour, logs within a day).
func (s *Service) Set(ctx context.Context, c access.Caller, set retention.Settings) error {
	scope, err := access.Authorize(s.policy, c, authz.ManageRetention, true, s.now())
	if err != nil {
		return err //nolint:wrapcheck // the access errors are the API
	}
	previous, capDays, err := s.store.Get(ctx, scope)
	if err != nil {
		return fmt.Errorf("load retention: %w", err)
	}
	if err := set.Validate(capDays); err != nil {
		return err
	}
	audit := access.AuditEntry{
		Actor: c.Principal.User, Action: "org.retention_set", Target: "org:" + scope.OrgID().String(),
		Details: map[string]string{"retention": set.String(), "previous": previous.String()},
	}
	if err := s.store.Set(ctx, scope, set, audit); err != nil {
		return fmt.Errorf("set retention: %w", err)
	}
	return nil
}

// Maintain deletes each organization's events before its period and, once a day, asks Loki to
// delete its logs before its period; it returns the errors after trying all.
func (s *Service) Maintain(ctx context.Context) error {
	now := s.now()
	orgs, err := s.store.Configured(ctx)
	if err != nil {
		return fmt.Errorf("organizations with a retention: %w", err)
	}
	var errs []error
	for _, o := range orgs {
		if o.Settings.EventDays > 0 {
			n, err := s.store.ExpireEvents(ctx, o.Org, now.Add(-o.Settings.Events()))
			if err != nil {
				errs = append(errs, fmt.Errorf("events of %s: %w", o.Org, err))
			} else if n > 0 {
				s.logger.InfoContext(ctx, "security events deleted (retention of the organization)", "org", o.Org.String(), "count", n)
			}
		}
		if o.Settings.LogDays > 0 && s.logs != nil && (o.LogsErasedAt == nil || now.Sub(*o.LogsErasedAt) >= LogEraseEvery) {
			if err := s.logs.EraseBefore(ctx, o.Org, now.Add(-o.Settings.Logs())); err != nil {
				errs = append(errs, fmt.Errorf("logs of %s: %w", o.Org, err))
				continue
			}
			if err := s.store.MarkLogsErased(ctx, o.Org, now); err != nil {
				errs = append(errs, fmt.Errorf("logs of %s: %w", o.Org, err))
			}
		}
	}
	return errors.Join(errs...)
}

// Run calls Maintain every MaintainInterval until ctx ends.
func (s *Service) Run(ctx context.Context) error {
	ticker := time.NewTicker(MaintainInterval)
	defer ticker.Stop()
	for {
		if err := s.Maintain(ctx); err != nil {
			s.logger.WarnContext(ctx, "retention of the organizations", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
