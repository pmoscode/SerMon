package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/ingest"
	"github.com/pmoscode/SerMon/server/internal/app/modules"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/module"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// ModuleStore keeps the module settings of node groups and what the agents report about their
// modules (M5-06, ADR 0039).
type ModuleStore struct{ db *DB }

var (
	_ modules.Store       = (*ModuleStore)(nil)
	_ ingest.ModuleWriter = (*ModuleStore)(nil)
)

// NewModuleStore returns the store.
func NewModuleStore(db *DB) *ModuleStore { return &ModuleStore{db: db} }

// compactJSON writes a jsonb document as the server checked it: without the spaces of its text
// form.
func compactJSON(doc string) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(doc)); err != nil {
		return doc
	}
	return buf.String()
}

func settingFrom(moduleID string, enabled bool, interval pgtype.Int4, config string) module.Setting {
	s := module.Setting{Module: moduleID, Enabled: enabled, Config: compactJSON(config)}
	if interval.Valid {
		s.Interval = time.Duration(interval.Int32) * time.Second
	}
	return s
}

// View returns the module settings of every group of the scope's organization.
func (s *ModuleStore) View(ctx context.Context, scope tenant.Scope) (modules.View, error) {
	var view modules.View
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		org, err := q.OrganizationAgentConfig(ctx)
		if err != nil {
			return fmt.Errorf("organization config: %w", err)
		}
		view.Version = uint64(max(org.AgentConfigVersion, 1))
		groups, err := q.ListNodeGroups(ctx, sqlcdb.ListNodeGroupsParams{AllGroups: scope.AllGroups(), GroupIds: groupIDs(scope)})
		if err != nil {
			return fmt.Errorf("list groups: %w", err)
		}
		rows, err := q.ListModuleSettings(ctx)
		if err != nil {
			return fmt.Errorf("list module settings: %w", err)
		}
		builtin, err := q.ListGroupBuiltinModules(ctx)
		if err != nil {
			return fmt.Errorf("list built-in modules: %w", err)
		}
		for _, g := range groups {
			group := modules.Group{ID: tenant.GroupID(g.ID), Name: g.Name}
			for _, b := range builtin {
				if b.GroupID == g.ID {
					group.Available = b.Modules
				}
			}
			for _, r := range rows {
				if r.GroupID == g.ID {
					group.Modules = append(group.Modules, settingFrom(r.ModuleID, r.Enabled, r.IntervalSeconds, r.Config))
				}
			}
			view.Groups = append(view.Groups, group)
		}
		return nil
	})
	return view, wrap(err, "module settings")
}

// Put stores the setting of a group's module, raises the version and writes the audit entry in
// one transaction.
func (s *ModuleStore) Put(ctx context.Context, scope tenant.Scope, group tenant.GroupID, setting module.Setting, by identity.UserID, audit access.AuditEntry) error {
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		exists, err := q.NodeGroupExists(ctx, uuid.UUID(group))
		if err != nil {
			return fmt.Errorf("find group: %w", err)
		}
		if !exists {
			return access.ErrNotFound
		}
		view, err := q.ListModuleSettings(ctx)
		if err != nil {
			return fmt.Errorf("list module settings: %w", err)
		}
		count, known := 0, false
		for _, r := range view {
			if r.GroupID == uuid.UUID(group) {
				count++
				known = known || r.ModuleID == setting.Module
			}
		}
		if !known && count >= module.MaxModules {
			return modules.ErrTooMany
		}
		var interval pgtype.Int4
		if setting.Interval > 0 {
			interval = pgtype.Int4{Int32: int32(min(setting.Interval/time.Second, module.MaxInterval/time.Second)), Valid: true}
		}
		if err := q.UpsertModuleSetting(ctx, sqlcdb.UpsertModuleSettingParams{
			GroupID: uuid.UUID(group), ModuleID: setting.Module, Enabled: setting.Enabled,
			IntervalSeconds: interval, Config: []byte(setting.Config), UpdatedBy: optionalUser(by),
		}); err != nil {
			return fmt.Errorf("store module setting: %w", err)
		}
		return bumpAndAudit(ctx, q, audit)
	})
}

// Remove deletes the setting of a group's module, raises the version and audits it.
func (s *ModuleStore) Remove(ctx context.Context, scope tenant.Scope, group tenant.GroupID, moduleID string, _ identity.UserID, audit access.AuditEntry) error {
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		n, err := q.DeleteModuleSetting(ctx, sqlcdb.DeleteModuleSettingParams{GroupID: uuid.UUID(group), ModuleID: moduleID})
		if err != nil {
			return fmt.Errorf("remove module setting: %w", err)
		}
		if n == 0 {
			return access.ErrNotFound
		}
		return bumpAndAudit(ctx, q, audit)
	})
}

// Node returns what a node of the scope's groups has and reports.
func (s *ModuleStore) Node(ctx context.Context, scope tenant.Scope, node identity.NodeID) (modules.Node, error) {
	var out modules.Node
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		n, err := q.VisibleNodeModules(ctx, sqlcdb.VisibleNodeModulesParams{ID: uuid.UUID(node), AllGroups: scope.AllGroups(), GroupIds: groupIDs(scope)})
		if errors.Is(err, pgx.ErrNoRows) {
			return access.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("find node: %w", err)
		}
		out = modules.Node{ID: identity.NodeID(n.ID), Group: tenant.GroupID(n.GroupID), Builtin: n.Modules, Vetoed: n.ModulesVetoed}
		settings, err := q.ModuleSettingsForNode(ctx, n.ID)
		if err != nil {
			return fmt.Errorf("module settings: %w", err)
		}
		for _, r := range settings {
			out.Settings = append(out.Settings, settingFrom(r.ModuleID, r.Enabled, r.IntervalSeconds, r.Config))
		}
		health, err := q.ModuleHealthOfNode(ctx, n.ID)
		if err != nil {
			return fmt.Errorf("module health: %w", err)
		}
		for _, h := range health {
			if out.Health == nil {
				out.Health = &module.HealthReport{CollectedAt: h.CollectedAt}
			}
			out.Health.Modules = append(out.Health.Modules, module.Health{
				Module: h.ModuleID, State: module.State(min(max(h.State, 0), 255)), Reason: h.Reason, LastSuccess: h.LastSuccess,
			})
		}
		hints, err := q.ModuleHintsOfNode(ctx, n.ID)
		if err != nil {
			return fmt.Errorf("module hints: %w", err)
		}
		for _, h := range hints {
			if out.Hints == nil {
				out.Hints = &module.Hints{CollectedAt: h.CollectedAt}
			}
			out.Hints.Hints = append(out.Hints.Hints, module.Hint{Module: h.ModuleID, Reasons: h.Reasons, Suggested: compactJSON(h.Suggested)})
		}
		return nil
	})
	return out, wrap(err, "node modules")
}

// OpenHints returns the suggestions of the scope's nodes for modules their group doesn't run.
func (s *ModuleStore) OpenHints(ctx context.Context, scope tenant.Scope, limit int) ([]modules.NodeHint, error) {
	var out []modules.NodeHint
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		rows, err := q.ListOpenModuleHints(ctx, sqlcdb.ListOpenModuleHintsParams{
			AllGroups: scope.AllGroups(), GroupIds: groupIDs(scope), MaxRows: int32(min(max(limit, 0), modules.MaxHints)),
		})
		if err != nil {
			return err //nolint:wrapcheck // wrapped below
		}
		for _, r := range rows {
			out = append(out, modules.NodeHint{
				Node: identity.NodeID(r.NodeID), Hostname: r.Hostname, Group: tenant.GroupID(r.GroupID),
				Hint: module.Hint{Module: r.ModuleID, Reasons: r.Reasons, Suggested: compactJSON(r.Suggested)},
			})
		}
		return nil
	})
	return out, wrap(err, "module hints")
}

func agentScope(org tenant.OrgID) (tenant.Scope, error) {
	scope, err := tenant.ForAllGroups(org)
	if err != nil {
		return tenant.Scope{}, fmt.Errorf("agent scope: %w", err)
	}
	return scope, nil
}

// WriteModuleHealth replaces the node's health report (ingest.ModuleWriter). Organization and
// node come from the agent's certificate.
func (s *ModuleStore) WriteModuleHealth(ctx context.Context, org tenant.OrgID, node identity.NodeID, r module.HealthReport, receivedAt time.Time) error {
	scope, err := agentScope(org)
	if err != nil {
		return err
	}
	return wrap(s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		if err := q.DeleteModuleHealth(ctx, uuid.UUID(node)); err != nil {
			return fmt.Errorf("clear module health: %w", err)
		}
		for _, h := range r.Modules {
			if err := q.InsertModuleHealth(ctx, sqlcdb.InsertModuleHealthParams{
				NodeID: uuid.UUID(node), ModuleID: h.Module, State: int16(h.State), Reason: h.Reason,
				LastSuccess: h.LastSuccess, CollectedAt: r.CollectedAt, ReceivedAt: receivedAt,
			}); err != nil {
				return fmt.Errorf("store module health: %w", err)
			}
		}
		return nil
	}), "write module health")
}

// WriteModuleHints replaces the node's discovery hints (ingest.ModuleWriter).
func (s *ModuleStore) WriteModuleHints(ctx context.Context, org tenant.OrgID, node identity.NodeID, h module.Hints, receivedAt time.Time) error {
	scope, err := agentScope(org)
	if err != nil {
		return err
	}
	return wrap(s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		if err := q.DeleteModuleHints(ctx, uuid.UUID(node)); err != nil {
			return fmt.Errorf("clear module hints: %w", err)
		}
		for _, hint := range h.Hints {
			var suggested []byte
			if hint.Suggested != "" {
				suggested = []byte(hint.Suggested)
			}
			if err := q.InsertModuleHint(ctx, sqlcdb.InsertModuleHintParams{
				NodeID: uuid.UUID(node), ModuleID: hint.Module, Reasons: slices.Clone(hint.Reasons), Suggested: suggested,
				CollectedAt: h.CollectedAt, ReceivedAt: receivedAt,
			}); err != nil {
				return fmt.Errorf("store module hint: %w", err)
			}
		}
		return nil
	}), "write module hints")
}
