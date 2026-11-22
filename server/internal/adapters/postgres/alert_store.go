package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/alerting"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// AlertStore persists the alert engine's leases, instances and history (alerting.Store, M2-02);
// everything runs in the organization's tenant transaction with RLS.
type AlertStore struct{ db *DB }

var _ alerting.Store = (*AlertStore)(nil)

// NewAlertStore returns the store on db.
func NewAlertStore(db *DB) *AlertStore { return &AlertStore{db: db} }

// Organizations lists all organizations (SECURITY DEFINER function, no tenant data).
func (s *AlertStore) Organizations(ctx context.Context) ([]tenant.OrgID, error) {
	rows, err := s.db.withoutTenant().AdminListOrganizations(ctx)
	if err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}
	orgs := make([]tenant.OrgID, 0, len(rows))
	for _, r := range rows {
		if r.Locked {
			continue // a locked organization rests (M2-33)
		}
		orgs = append(orgs, tenant.OrgID(r.ID))
	}
	return orgs, nil
}

// Lease gives holder the organization until until unless another holder's lease is still valid.
func (s *AlertStore) Lease(ctx context.Context, org tenant.OrgID, holder string, now, until time.Time) (bool, error) {
	scope, err := tenant.ForAllGroups(org)
	if err != nil {
		return false, fmt.Errorf("lease: %w", err)
	}
	var rows int64
	err = s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		n, err := q.LeaseAlertOrganization(ctx, sqlcdb.LeaseAlertOrganizationParams{Holder: holder, Until: until, Now: now})
		rows = n
		return wrap(err, "lease organization")
	})
	if err != nil {
		return false, err
	}
	return rows == 1, nil
}

// Instances returns the stored instances of the organization.
func (s *AlertStore) Instances(ctx context.Context, scope tenant.Scope) ([]alert.Instance, error) {
	var out []alert.Instance
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		rows, err := q.ListAlertInstances(ctx)
		if err != nil {
			return wrap(err, "list alert instances")
		}
		out = make([]alert.Instance, 0, len(rows))
		for _, r := range rows {
			inst := alert.Instance{
				Key:   alert.Key{Rule: alert.RuleID(r.RuleID), Node: identity.NodeID(r.NodeID), Dimension: r.Dimension},
				State: alert.State(r.State), Since: r.Since, Flapping: r.Flapping, Value: r.Value, Flips: r.Flips,
			}
			var err error
			if inst.Fields, err = decodeFields(r.Fields); err != nil {
				return err
			}
			if r.PendingSince != nil {
				inst.PendingSince = *r.PendingSince
			}
			if r.FiredAt != nil {
				inst.FiredAt = *r.FiredAt
			}
			if r.AckBy != nil && r.AckAt != nil {
				inst.Ack = &alert.Ack{By: identity.UserID(*r.AckBy), At: *r.AckAt}
			}
			out = append(out, inst)
		}
		return nil
	})
	return out, err
}

// Save writes changed instances, deletes removed ones and appends the history in one transaction.
func (s *AlertStore) Save(ctx context.Context, scope tenant.Scope, changed []alert.Instance, removed []alert.Key, history []alerting.Record) error {
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		for _, i := range changed {
			fields, err := encodeFields(i.Fields)
			if err != nil {
				return err
			}
			p := sqlcdb.UpsertAlertInstanceParams{
				RuleID: string(i.Key.Rule), NodeID: uuid.UUID(i.Key.Node), Dimension: i.Key.Dimension,
				State: int16(i.State), Since: i.Since, PendingSince: optionalTime(i.PendingSince), FiredAt: optionalTime(i.FiredAt), //nolint:gosec // G115: 0..4
				Flapping: i.Flapping, Value: i.Value, Flips: nonNilTimes(i.Flips), Fields: fields,
			}
			if i.Ack != nil {
				by := uuid.UUID(i.Ack.By)
				p.AckBy, p.AckAt = &by, &i.Ack.At
			}
			if err := q.UpsertAlertInstance(ctx, p); err != nil {
				return wrap(err, "store alert instance")
			}
		}
		for _, k := range removed {
			if err := q.DeleteAlertInstance(ctx, sqlcdb.DeleteAlertInstanceParams{RuleID: string(k.Rule), NodeID: uuid.UUID(k.Node), Dimension: k.Dimension}); err != nil {
				return wrap(err, "delete alert instance")
			}
		}
		return appendHistory(ctx, q, history)
	})
}

// appendHistory appends transitions within a tenant transaction.
func appendHistory(ctx context.Context, q *sqlcdb.Queries, history []alerting.Record) error {
	for _, r := range history {
		t := r.Transition
		fields, err := encodeFields(t.Fields)
		if err != nil {
			return err
		}
		p := sqlcdb.InsertAlertHistoryParams{
			RuleID: string(r.Key.Rule), NodeID: uuid.UUID(r.Key.Node), Dimension: r.Key.Dimension,
			Kind: int16(t.Kind), FromState: int16(t.From), ToState: int16(t.To), At: t.At, Value: t.Value, //nolint:gosec // G115: small enums
			Fields: fields, ByUser: optionalUser(t.By),
		}
		if err := q.InsertAlertHistory(ctx, p); err != nil {
			return wrap(err, "append alert history")
		}
	}
	return nil
}

// History returns the newest transitions of an instance, newest first.
func (s *AlertStore) History(ctx context.Context, scope tenant.Scope, key alert.Key, limit int32) ([]alerting.Record, error) {
	var out []alerting.Record
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		rows, err := q.AlertInstanceHistory(ctx, sqlcdb.AlertInstanceHistoryParams{
			RuleID: string(key.Rule), NodeID: uuid.UUID(key.Node), Dimension: key.Dimension, MaxEntries: limit,
		})
		if err != nil {
			return wrap(err, "list alert history")
		}
		for _, r := range rows {
			t := alert.Transition{
				Kind: alert.TransitionKind(r.Kind), From: alert.State(r.FromState), To: alert.State(r.ToState), At: r.At, Value: r.Value,
			}
			if r.ByUser != nil {
				t.By = identity.UserID(*r.ByUser)
			}
			var err error
			if t.Fields, err = decodeFields(r.Fields); err != nil {
				return err
			}
			out = append(out, alerting.Record{Key: key, Transition: t})
		}
		return nil
	})
	return out, err
}

// encodeFields stores the fields of an alert as a JSON object ({} without any).
func encodeFields(f alert.Fields) ([]byte, error) {
	if len(f) == 0 {
		return []byte("{}"), nil
	}
	data, err := json.Marshal(f)
	if err != nil {
		return nil, fmt.Errorf("encode alert fields: %w", err)
	}
	return data, nil
}

func decodeFields(data []byte) (alert.Fields, error) {
	var f alert.Fields
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("decode alert fields: %w", err)
	}
	if len(f) == 0 {
		return nil, nil
	}
	return f, nil
}

func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func nonNilTimes(ts []time.Time) []time.Time {
	if ts == nil {
		return []time.Time{}
	}
	return ts
}

// Acknowledge marks a firing, unacknowledged alert and appends the transition, together;
// alert.ErrNotFiring if it does not fire anymore or someone acknowledged it already.
func (s *AlertStore) Acknowledge(ctx context.Context, scope tenant.Scope, key alert.Key, ack alert.Ack, rec alerting.Record) error {
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		n, err := q.AcknowledgeAlertInstance(ctx, sqlcdb.AcknowledgeAlertInstanceParams{
			RuleID: string(key.Rule), NodeID: uuid.UUID(key.Node), Dimension: key.Dimension, AckBy: optionalUser(ack.By), AckAt: &ack.At,
		})
		if err != nil {
			return wrap(err, "acknowledge alert")
		}
		if n == 0 {
			return alert.ErrNotFiring
		}
		return appendHistory(ctx, q, []alerting.Record{rec})
	})
}

// PurgeHistory deletes the history before before; the table's policy keeps the last year.
func (s *AlertStore) PurgeHistory(ctx context.Context, scope tenant.Scope, before time.Time) (int, error) {
	var n int64
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		var err error
		n, err = q.PurgeAlertHistory(ctx, before)
		return wrap(err, "purge alert history")
	})
	return int(n), err
}
