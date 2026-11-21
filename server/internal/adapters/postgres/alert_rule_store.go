package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/access"
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
			if r.Source.Valid {
				out[i].Source = []byte(r.Source.String)
			}
		}
		return nil
	})
	return out, err
}

// Apply stores overrides (the rule editor checked their documents, M2-17) with one audit entry,
// all or none.
func (s *AlertRuleStore) Apply(ctx context.Context, scope tenant.Scope, put []ruleset.Override, by identity.UserID, audit access.AuditEntry) error {
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		for _, o := range put {
			p := sqlcdb.UpsertAlertRuleOverrideParams{RuleID: string(o.Rule), Disabled: o.Disabled, Document: o.Document, UpdatedBy: optionalUser(by)}
			if len(o.Source) > 0 {
				p.Source = pgtype.Text{String: string(o.Source), Valid: true}
			}
			if err := q.UpsertAlertRuleOverride(ctx, p); err != nil {
				return wrap(err, "store rule override")
			}
		}
		return appendAudit(ctx, q, audit)
	})
}

// Reset restores the default of a rule (or removes a rule of the organization) with an audit
// entry; access.ErrNotFound if there was no override.
func (s *AlertRuleStore) Reset(ctx context.Context, scope tenant.Scope, rule alert.RuleID, audit access.AuditEntry) error {
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		n, err := q.DeleteAlertRuleOverride(ctx, string(rule))
		if err != nil {
			return wrap(err, "delete rule override")
		}
		if n == 0 {
			return access.ErrNotFound
		}
		return appendAudit(ctx, q, audit)
	})
}
