package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/logsettings"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/logconfig"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// LogSettingsStore keeps the log settings of node groups and the redaction rules of an
// organization (M3-03, ADR 0030), and what the agents reported back.
type LogSettingsStore struct{ db *DB }

var _ logsettings.Store = (*LogSettingsStore)(nil)

// NewLogSettingsStore returns the store.
func NewLogSettingsStore(db *DB) *LogSettingsStore { return &LogSettingsStore{db: db} }

// View returns the settings of every group of the scope's organization and its rules.
func (s *LogSettingsStore) View(ctx context.Context, scope tenant.Scope) (logsettings.View, error) {
	var view logsettings.View
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		org, err := q.OrganizationAgentConfig(ctx)
		if err != nil {
			return fmt.Errorf("organization config: %w", err)
		}
		view.Version = uint64(max(org.AgentConfigVersion, 1))
		view.ViewersSeeLogs = org.ViewersSeeLogs
		if view.Rules, err = decodeRules(org.RedactionRules); err != nil {
			return err
		}
		nodes, err := q.ListNodeConfigStatus(ctx)
		if err != nil {
			return fmt.Errorf("node config status: %w", err)
		}
		for _, n := range nodes {
			view.Nodes = append(view.Nodes, logsettings.NodeStatus{
				ID: identity.NodeID(n.ID), Hostname: n.Hostname, Group: tenant.GroupID(n.GroupID),
				AppliedVersion: uint64(max(n.ConfigVersion, 0)), Error: n.ConfigError, LogsVetoed: n.LogsVetoed,
				ReportedAt: n.ConfigReportedAt,
			})
		}
		rows, err := q.ListGroupLogSettings(ctx)
		if err != nil {
			return fmt.Errorf("list log settings: %w", err)
		}
		for _, row := range rows {
			settings, custom, err := decodeSettings(row.Settings)
			if err != nil {
				return err
			}
			view.Groups = append(view.Groups, logsettings.Group{ID: tenant.GroupID(row.ID), Name: row.Name, Settings: settings, Custom: custom})
		}
		return nil
	})
	return view, wrap(err, "log settings")
}

// PutGroup stores (or with nil removes) the settings of a group, raises the version and writes
// the audit entry, all in one transaction.
func (s *LogSettingsStore) PutGroup(ctx context.Context, scope tenant.Scope, group tenant.GroupID, settings *logconfig.Settings, by identity.UserID, audit access.AuditEntry) error {
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		exists, err := q.NodeGroupExists(ctx, uuid.UUID(group))
		if err != nil {
			return fmt.Errorf("find group: %w", err)
		}
		if !exists {
			return access.ErrNotFound
		}
		if settings == nil {
			err = q.DeleteGroupLogSettings(ctx, uuid.UUID(group))
		} else {
			var doc []byte
			if doc, err = json.Marshal(settings); err != nil {
				return fmt.Errorf("encode log settings: %w", err)
			}
			err = q.UpsertGroupLogSettings(ctx, sqlcdb.UpsertGroupLogSettingsParams{GroupID: uuid.UUID(group), Settings: doc, UpdatedBy: optionalUser(by)})
		}
		if err != nil {
			return fmt.Errorf("store log settings: %w", err)
		}
		return bumpAndAudit(ctx, q, audit)
	})
}

// PutRules replaces the redaction rules, raises the version and writes the audit entry.
func (s *LogSettingsStore) PutRules(ctx context.Context, scope tenant.Scope, rules []logconfig.Rule, _ identity.UserID, audit access.AuditEntry) error {
	if rules == nil {
		rules = []logconfig.Rule{}
	}
	doc, err := json.Marshal(rules)
	if err != nil {
		return fmt.Errorf("encode redaction rules: %w", err)
	}
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		if err := q.SetRedactionRules(ctx, doc); err != nil {
			return fmt.Errorf("store redaction rules: %w", err)
		}
		return bumpAndAudit(ctx, q, audit)
	})
}

// PutViewersSeeLogs implements logsettings.Store.
func (s *LogSettingsStore) PutViewersSeeLogs(ctx context.Context, scope tenant.Scope, see bool, _ identity.UserID, audit access.AuditEntry) error {
	return wrap(s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		if err := q.SetViewersSeeLogs(ctx, see); err != nil {
			return fmt.Errorf("store viewer switch: %w", err)
		}
		return appendAudit(ctx, q, audit)
	}), "viewer switch")
}

func bumpAndAudit(ctx context.Context, q *sqlcdb.Queries, audit access.AuditEntry) error {
	if _, err := q.BumpAgentConfigVersion(ctx); err != nil {
		return fmt.Errorf("raise config version: %w", err)
	}
	return appendAudit(ctx, q, audit)
}

// ForNode returns the configuration of a node: the settings of its group and the rules of its
// organization. Organization and node come from the agent's certificate.
func (s *LogSettingsStore) ForNode(ctx context.Context, org tenant.OrgID, node identity.NodeID) (logconfig.NodeConfig, error) {
	scope, err := tenant.ForAllGroups(org)
	if err != nil {
		return logconfig.NodeConfig{}, fmt.Errorf("agent config: %w", err)
	}
	var config logconfig.NodeConfig
	err = s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		row, err := q.AgentConfigForNode(ctx, uuid.UUID(node))
		if errors.Is(err, pgx.ErrNoRows) {
			return access.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("agent config: %w", err)
		}
		config.Version = uint64(max(row.AgentConfigVersion, 1))
		if config.Rules, err = decodeRules(row.RedactionRules); err != nil {
			return err
		}
		config.Settings, _, err = decodeSettings(row.Settings)
		return err
	})
	return config, wrap(err, "agent config")
}

// Applied records an agent's answer to a configuration.
func (s *LogSettingsStore) Applied(ctx context.Context, org tenant.OrgID, node identity.NodeID, applied logconfig.Applied) error {
	scope, err := tenant.ForAllGroups(org)
	if err != nil {
		return fmt.Errorf("record config applied: %w", err)
	}
	return wrap(s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		at := applied.At
		_, err := q.RecordAgentConfigApplied(ctx, sqlcdb.RecordAgentConfigAppliedParams{
			Version:    int64(min(applied.Version, 1<<62)),
			Error:      pgtype.Text{String: applied.Error, Valid: applied.Error != ""},
			LogsVetoed: applied.LogsVetoed,
			ReportedAt: &at,
			NodeID:     uuid.UUID(node),
		})
		return err //nolint:wrapcheck // wrapped below
	}), "record config applied")
}

func decodeRules(doc []byte) ([]logconfig.Rule, error) {
	var rules []logconfig.Rule
	if len(doc) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(doc, &rules); err != nil {
		return nil, fmt.Errorf("decode redaction rules: %w", err)
	}
	return rules, nil
}

// decodeSettings reads a stored document; without one the group has the defaults.
func decodeSettings(doc []byte) (logconfig.Settings, bool, error) {
	if len(doc) == 0 {
		return logconfig.Default(), false, nil
	}
	var settings logconfig.Settings
	if err := json.Unmarshal(doc, &settings); err != nil {
		return logconfig.Settings{}, false, fmt.Errorf("decode log settings: %w", err)
	}
	return settings, true, nil
}
