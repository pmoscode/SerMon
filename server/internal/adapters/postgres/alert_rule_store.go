package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/alerting/ruleset"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// AlertRuleStore persists the organizations' rule overrides (ruleset.OverrideStore, M2-04) in
// their tenant transaction with RLS.
type AlertRuleStore struct{ db *DB }

var _ ruleset.OverrideStore = (*AlertRuleStore)(nil)

// NewAlertRuleStore returns the store on db.
func NewAlertRuleStore(db *DB) *AlertRuleStore { return &AlertRuleStore{db: db} }

// Overrides returns the overrides of the organization.
func (s *AlertRuleStore) Overrides(ctx context.Context, org tenant.OrgID) ([]ruleset.Override, error) {
	scope, err := tenant.ForAllGroups(org)
	if err != nil {
		return nil, fmt.Errorf("rule overrides: %w", err)
	}
	var out []ruleset.Override
	err = s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		rows, err := q.ListAlertRuleOverrides(ctx)
		if err != nil {
			return wrap(err, "list rule overrides")
		}
		out = make([]ruleset.Override, len(rows))
		for i, r := range rows {
			out[i] = ruleset.Override{Rule: alert.RuleID(r.RuleID), Disabled: r.Disabled, Document: r.Document}
		}
		return nil
	})
	return out, err
}

// PutOverride stores the override of a rule (the rule editor checks the document, M2-17).
func (s *AlertRuleStore) PutOverride(ctx context.Context, scope tenant.Scope, o ruleset.Override, by identity.UserID) error {
	p := sqlcdb.UpsertAlertRuleOverrideParams{RuleID: string(o.Rule), Disabled: o.Disabled, Document: o.Document}
	if by != (identity.UserID{}) {
		id := uuid.UUID(by)
		p.UpdatedBy = &id
	}
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		return wrap(q.UpsertAlertRuleOverride(ctx, p), "store rule override")
	})
}

// DeleteOverride restores the default of a rule (or removes a rule of the organization); false
// if there was no override.
func (s *AlertRuleStore) DeleteOverride(ctx context.Context, scope tenant.Scope, rule alert.RuleID) (bool, error) {
	var rows int64
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		n, err := q.DeleteAlertRuleOverride(ctx, string(rule))
		rows = n
		return wrap(err, "delete rule override")
	})
	return rows == 1, err
}
