// Package logsettings manages which log entries the agents of an organization send and the
// organization's redaction rules (KONZEPT §10, ADR 0030, M3-03). Admins for all groups only;
// changes need a recent step-up, are audited and reach the connected agents at once.
package logsettings

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/logconfig"
	"github.com/pmoscode/SerMon/server/internal/domain/redact"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Group is a node group with the settings in effect.
type Group struct {
	ID       tenant.GroupID
	Name     string
	Settings logconfig.Settings
	// Custom is false while the group has the defaults.
	Custom bool
}

// NodeStatus is what the agent of a node reported about its configuration (ConfigApplied).
type NodeStatus struct {
	ID       identity.NodeID
	Hostname string
	Group    tenant.GroupID
	// AppliedVersion is the version the agent took; 0 before its first answer.
	AppliedVersion uint64
	// Error says why the agent kept its previous configuration (from the host: text only).
	Error string
	// LogsVetoed: agent.toml forbids sending logs.
	LogsVetoed bool
	ReportedAt *time.Time
}

// View is everything the organization has set.
type View struct {
	Groups  []Group
	Rules   []logconfig.Rule
	Version uint64
	// ViewersSeeLogs is the organization's switch (KONZEPT §7, M3-12).
	ViewersSeeLogs bool
	// Nodes is the state of every node of the organization.
	Nodes []NodeStatus
}

// MaxSampleBytes bounds the sample line of a redaction test (a log message's length).
const MaxSampleBytes = 16 * 1024

// Store keeps the settings. Every change raises the organization's configuration version and
// writes the audit entry in the same transaction.
type Store interface {
	View(ctx context.Context, scope tenant.Scope) (View, error)
	// PutGroup stores the settings of a group, nil returns it to the defaults; ErrNotFound for a
	// group the scope does not see.
	PutGroup(ctx context.Context, scope tenant.Scope, group tenant.GroupID, settings *logconfig.Settings, by identity.UserID, audit access.AuditEntry) error
	PutRules(ctx context.Context, scope tenant.Scope, rules []logconfig.Rule, by identity.UserID, audit access.AuditEntry) error
	// PutViewersSeeLogs stores the organization's switch; no agent configuration changes.
	PutViewersSeeLogs(ctx context.Context, scope tenant.Scope, see bool, by identity.UserID, audit access.AuditEntry) error
}

// Agents learn about a changed configuration (the connected agent sessions).
type Agents interface {
	ConfigChanged(org tenant.OrgID)
}

// Service is the use case.
type Service struct {
	store  Store
	policy authz.Policy
	agents Agents
	now    func() time.Time
}

// NewService returns the service.
func NewService(store Store, policy authz.Policy, agents Agents, now func() time.Time) *Service {
	return &Service{store: store, policy: policy, agents: agents, now: now}
}

// View returns the settings of every group and the rules.
func (s *Service) View(ctx context.Context, c access.Caller) (View, error) {
	scope, err := access.Authorize(s.policy, c, authz.ManageLogSettings, false, s.now())
	if err != nil {
		return View{}, err //nolint:wrapcheck // access errors are mapped by the transport
	}
	view, err := s.store.View(ctx, scope)
	if err != nil {
		return View{}, fmt.Errorf("log settings: %w", err)
	}
	return view, nil
}

// SetGroup replaces the settings of a group.
func (s *Service) SetGroup(ctx context.Context, c access.Caller, group tenant.GroupID, settings logconfig.Settings) error {
	scope, err := access.Authorize(s.policy, c, authz.ManageLogSettings, true, s.now())
	if err != nil {
		return err //nolint:wrapcheck // see View
	}
	if err := settings.Validate(); err != nil {
		return err
	}
	audit := access.AuditEntry{
		Actor: c.Principal.User, Action: "log_settings.update", Target: "group:" + group.String(),
		Details: map[string]string{
			"enabled":      strconv.FormatBool(settings.Enabled),
			"min_severity": strconv.Itoa(int(settings.MinSeverity)),
			"units":        strconv.Itoa(len(settings.ExcludeUnits)),
			"files":        strconv.Itoa(len(settings.Files)),
		},
	}
	if err := s.store.PutGroup(ctx, scope, group, &settings, c.Principal.User, audit); err != nil {
		return fmt.Errorf("set log settings: %w", err)
	}
	s.agents.ConfigChanged(c.Principal.Org)
	return nil
}

// ResetGroup returns a group to the defaults.
func (s *Service) ResetGroup(ctx context.Context, c access.Caller, group tenant.GroupID) error {
	scope, err := access.Authorize(s.policy, c, authz.ManageLogSettings, true, s.now())
	if err != nil {
		return err //nolint:wrapcheck // see View
	}
	audit := access.AuditEntry{Actor: c.Principal.User, Action: "log_settings.reset", Target: "group:" + group.String()}
	if err := s.store.PutGroup(ctx, scope, group, nil, c.Principal.User, audit); err != nil {
		return fmt.Errorf("reset log settings: %w", err)
	}
	s.agents.ConfigChanged(c.Principal.Org)
	return nil
}

// SetRules replaces the redaction rules. The audit entry counts them; the patterns stay out of
// it (they may name internal identifiers).
func (s *Service) SetRules(ctx context.Context, c access.Caller, rules []logconfig.Rule) error {
	scope, err := access.Authorize(s.policy, c, authz.ManageLogSettings, true, s.now())
	if err != nil {
		return err //nolint:wrapcheck // see View
	}
	if err := logconfig.ValidateRules(rules); err != nil {
		return err
	}
	audit := access.AuditEntry{
		Actor: c.Principal.User, Action: "redaction_rules.update", Target: "org:" + c.Principal.Org.String(),
		Details: map[string]string{"rules": strconv.Itoa(len(rules))},
	}
	if err := s.store.PutRules(ctx, scope, rules, c.Principal.User, audit); err != nil {
		return fmt.Errorf("set redaction rules: %w", err)
	}
	s.agents.ConfigChanged(c.Principal.Org)
	return nil
}

// SetViewersSeeLogs sets whether viewers may see logs and security events (KONZEPT §7, T22). It
// applies from the next request of every session: the switch comes with the principal.
func (s *Service) SetViewersSeeLogs(ctx context.Context, c access.Caller, see bool) error {
	scope, err := access.Authorize(s.policy, c, authz.ManageLogSettings, true, s.now())
	if err != nil {
		return err //nolint:wrapcheck // see View
	}
	audit := access.AuditEntry{
		Actor: c.Principal.User, Action: "log_settings.viewers", Target: "org:" + c.Principal.Org.String(),
		Details: map[string]string{"viewers_see_logs": strconv.FormatBool(see)},
	}
	if err := s.store.PutViewersSeeLogs(ctx, scope, see, c.Principal.User, audit); err != nil {
		return fmt.Errorf("set viewer switch: %w", err)
	}
	return nil
}

// TestRedaction returns sample as the agent would send it with rules (not saved yet): the
// built-in patterns, then the rules. Nothing changes, so no step-up.
func (s *Service) TestRedaction(_ context.Context, c access.Caller, rules []logconfig.Rule, sample string) (string, error) {
	if _, err := access.Authorize(s.policy, c, authz.ManageLogSettings, false, s.now()); err != nil {
		return "", err //nolint:wrapcheck // see View
	}
	if len(sample) > MaxSampleBytes {
		return "", &logconfig.InvalidError{Field: "sample", Index: -1, Reason: fmt.Sprintf("at most %d bytes", MaxSampleBytes)}
	}
	if err := logconfig.ValidateRules(rules); err != nil {
		return "", err
	}
	patterns := make([]string, 0, len(rules))
	for _, r := range rules {
		patterns = append(patterns, r.Pattern)
	}
	r, err := redact.New(patterns)
	if err != nil {
		return "", &logconfig.InvalidError{Field: "rule", Index: -1, Reason: err.Error()}
	}
	return r.Redact(sample), nil
}
