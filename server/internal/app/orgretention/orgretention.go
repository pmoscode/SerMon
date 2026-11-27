// Package orgretention lets organizations keep their logs and security events shorter than the
// instance (M5-30): admins set it within the limits and the cap, a job deletes what is older –
// the events of organizations without a setting of their own after the instance's default
// (SERMON_EVENT_RETENTION, V1-22).
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

// OrgSettings is an organization with its retention (zero: the instance's defaults).
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
	// Organizations returns every organization with its settings (instance level).
	Organizations(ctx context.Context) ([]OrgSettings, error)
	// ExpireEvents deletes the organization's security events and alert history before before:
	// its own period, or defaultDays without one (the database checks both against its clock).
	ExpireEvents(ctx context.Context, org tenant.OrgID, before time.Time, defaultDays uint32) (int64, error)
	// MarkLogsErased records when Loki was last asked to delete the organization's old logs.
	MarkLogsErased(ctx context.Context, org tenant.OrgID, at time.Time) error
}

// LogEraser asks Loki to delete an organization's logs before a time.
type LogEraser interface {
	EraseBefore(ctx context.Context, org tenant.OrgID, before time.Time) error
}

// LogCountEraser deletes an organization's counts of log lines before a time (V1-24).
type LogCountEraser interface {
	ExpireLogCounts(ctx context.Context, org tenant.OrgID, before time.Time) (int64, error)
}

// View is what admins see.
type View struct {
	Settings retention.Settings
	CapDays  uint32
	// EventDefaultDays is the instance's default for events (SERMON_EVENT_RETENTION).
	EventDefaultDays uint32
}

// Service is the use case.
type Service struct {
	store        Store
	policy       authz.Policy
	now          func() time.Time
	logs         LogEraser
	counts       LogCountEraser
	logger       *slog.Logger
	eventDefault uint32
}

// Option configures a Service.
type Option func(*Service)

// WithLogs deletes old logs in Loki.
func WithLogs(e LogEraser) Option { return func(s *Service) { s.logs = e } }

// WithLogCounts deletes the counts of the log histogram with the logs (V1-24).
func WithLogCounts(c LogCountEraser) Option { return func(s *Service) { s.counts = c } }

// WithEventDefault sets the instance's default for the events of organizations without a period
// of their own (days within retention.MinEventDays and MaxEventDays; config.Load checks it).
func WithEventDefault(days uint32) Option { return func(s *Service) { s.eventDefault = days } }

// WithLogger logs the job.
func WithLogger(l *slog.Logger) Option { return func(s *Service) { s.logger = l } }

// NewService returns the use case.
func NewService(store Store, policy authz.Policy, now func() time.Time, options ...Option) *Service {
	s := &Service{store: store, policy: policy, now: now, logger: slog.New(slog.DiscardHandler), eventDefault: retention.MaxEventDays}
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
	return View{Settings: set, CapDays: capDays, EventDefaultDays: s.eventDefault}, nil
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

// Maintain deletes each organization's events before its period (the instance's default without
// one) and, once a day, asks Loki to delete its logs before its own period; it returns the errors
// after trying all.
func (s *Service) Maintain(ctx context.Context) error {
	now := s.now()
	orgs, err := s.store.Organizations(ctx)
	if err != nil {
		return fmt.Errorf("organizations: %w", err)
	}
	var errs []error
	for _, o := range orgs {
		n, err := s.store.ExpireEvents(ctx, o.Org, now.Add(-o.Settings.EventsOr(s.eventDefault)), s.eventDefault)
		if err != nil {
			errs = append(errs, fmt.Errorf("events of %s: %w", o.Org, err))
		} else if n > 0 {
			s.logger.InfoContext(ctx, "security events deleted (retention)", "org", o.Org.String(), "count", n)
		}
		if o.Settings.LogDays > 0 && s.logs != nil && (o.LogsErasedAt == nil || now.Sub(*o.LogsErasedAt) >= LogEraseEvery) {
			before := now.Add(-o.Settings.Logs())
			if err := s.logs.EraseBefore(ctx, o.Org, before); err != nil {
				errs = append(errs, fmt.Errorf("logs of %s: %w", o.Org, err))
				continue
			}
			if s.counts != nil {
				if _, err := s.counts.ExpireLogCounts(ctx, o.Org, before); err != nil {
					errs = append(errs, fmt.Errorf("log counts of %s: %w", o.Org, err))
					continue
				}
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
