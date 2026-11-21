package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/app/routing"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// RoutingStore persists the notification policies and the routing state (routing.PolicyStore
// and routing.Store, M2-14).
type RoutingStore struct{ db *DB }

var (
	_ routing.PolicyStore = (*RoutingStore)(nil)
	_ routing.Store       = (*RoutingStore)(nil)
)

// NewRoutingStore returns the store on db.
func NewRoutingStore(db *DB) *RoutingStore { return &RoutingStore{db: db} }

// matchersJSON is how the matchers of a policy are stored.
type matchersJSON struct {
	Severities []notify.Severity `json:"severities,omitempty"`
	Groups     []uuid.UUID       `json:"groups,omitempty"`
	Rules      []string          `json:"rules,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
}

func encodeMatchers(m routing.Matchers) ([]byte, error) {
	j := matchersJSON{Severities: m.Severities, Rules: m.Rules, Labels: m.Labels}
	for _, g := range m.Groups {
		j.Groups = append(j.Groups, uuid.UUID(g))
	}
	data, err := json.Marshal(j)
	if err != nil {
		return nil, fmt.Errorf("encode policy matchers: %w", err)
	}
	return data, nil
}

func decodeMatchers(data []byte) (routing.Matchers, error) {
	var j matchersJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return routing.Matchers{}, fmt.Errorf("decode policy matchers: %w", err)
	}
	m := routing.Matchers{Severities: j.Severities, Rules: j.Rules, Labels: j.Labels}
	for _, g := range j.Groups {
		m.Groups = append(m.Groups, tenant.GroupID(g))
	}
	return m, nil
}

func seconds(d time.Duration) int32 { return int32(d / time.Second) } //nolint:gosec // G115: bounded by Validate and the table

func policies(ctx context.Context, q *sqlcdb.Queries) ([]routing.Policy, error) {
	rows, err := q.ListNotificationPolicies(ctx)
	if err != nil {
		return nil, wrap(err, "list notification policies")
	}
	links, err := q.ListNotificationPolicyChannels(ctx)
	if err != nil {
		return nil, wrap(err, "list notification policy channels")
	}
	out := make([]routing.Policy, 0, len(rows))
	index := make(map[uuid.UUID]int, len(rows))
	for _, r := range rows {
		m, err := decodeMatchers(r.Matchers)
		if err != nil {
			return nil, err
		}
		p := routing.Policy{
			ID: routing.PolicyID(r.ID), Name: r.Name, Enabled: r.Enabled, Match: m, Grouping: routing.Grouping(r.Grouping),
			Repeat: time.Duration(r.RepeatSeconds) * time.Second, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		}
		if r.EscalationSeconds.Valid {
			p.Escalation = &routing.Escalation{After: time.Duration(r.EscalationSeconds.Int32) * time.Second}
		}
		if r.CreatedBy != nil {
			p.CreatedBy = identity.UserID(*r.CreatedBy)
		}
		index[r.ID] = len(out)
		out = append(out, p)
	}
	for _, l := range links {
		i, ok := index[l.PolicyID]
		if !ok {
			continue
		}
		p := &out[i]
		switch {
		case !l.Escalation:
			p.Channels = append(p.Channels, notify.ChannelID(l.ChannelID))
		case p.Escalation != nil:
			p.Escalation.Channels = append(p.Escalation.Channels, notify.ChannelID(l.ChannelID))
		}
	}
	return out, nil
}

// Policies returns the organization's policies by name.
func (s *RoutingStore) Policies(ctx context.Context, scope tenant.Scope) ([]routing.Policy, error) {
	var out []routing.Policy
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		var err error
		out, err = policies(ctx, q)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func escalationSeconds(p routing.Policy) pgtype.Int4 {
	if p.Escalation == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: seconds(p.Escalation.After), Valid: true}
}

// linkChannels replaces the channels of a policy.
func linkChannels(ctx context.Context, q *sqlcdb.Queries, p routing.Policy) error {
	if err := q.DeleteNotificationPolicyChannels(ctx, uuid.UUID(p.ID)); err != nil {
		return wrap(err, "unlink policy channels")
	}
	link := func(channels []notify.ChannelID, escalation bool) error {
		for i, c := range channels {
			if err := q.InsertNotificationPolicyChannel(ctx, sqlcdb.InsertNotificationPolicyChannelParams{
				PolicyID: uuid.UUID(p.ID), ChannelID: uuid.UUID(c), Escalation: escalation, Position: int16(i),
			}); err != nil {
				return wrap(err, "link policy channel")
			}
		}
		return nil
	}
	if err := link(p.Channels, false); err != nil {
		return err
	}
	if p.Escalation != nil {
		return link(p.Escalation.Channels, true)
	}
	return nil
}

// CreatePolicy stores a new policy with its channels and audit entry.
func (s *RoutingStore) CreatePolicy(ctx context.Context, scope tenant.Scope, p routing.Policy, audit access.AuditEntry) error {
	matchers, err := encodeMatchers(p.Match)
	if err != nil {
		return err
	}
	params := sqlcdb.InsertNotificationPolicyParams{
		ID: uuid.UUID(p.ID), Name: p.Name, Enabled: p.Enabled, Matchers: matchers, Grouping: string(p.Grouping),
		RepeatSeconds: seconds(p.Repeat), EscalationSeconds: escalationSeconds(p), CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
	if !p.CreatedBy.IsZero() {
		by := uuid.UUID(p.CreatedBy)
		params.CreatedBy = &by
	}
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		if err := q.InsertNotificationPolicy(ctx, params); err != nil {
			return wrap(err, "insert notification policy")
		}
		if err := linkChannels(ctx, q, p); err != nil {
			return err
		}
		return appendAudit(ctx, q, audit)
	})
}

// UpdatePolicy replaces a policy; access.ErrNotFound outside the scope.
func (s *RoutingStore) UpdatePolicy(ctx context.Context, scope tenant.Scope, p routing.Policy, audit access.AuditEntry) error {
	matchers, err := encodeMatchers(p.Match)
	if err != nil {
		return err
	}
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		n, err := q.UpdateNotificationPolicy(ctx, sqlcdb.UpdateNotificationPolicyParams{
			ID: uuid.UUID(p.ID), Name: p.Name, Enabled: p.Enabled, Matchers: matchers, Grouping: string(p.Grouping),
			RepeatSeconds: seconds(p.Repeat), EscalationSeconds: escalationSeconds(p), UpdatedAt: p.UpdatedAt,
		})
		if err != nil {
			return wrap(err, "update notification policy")
		}
		if n == 0 {
			return access.ErrNotFound
		}
		if err := linkChannels(ctx, q, p); err != nil {
			return err
		}
		return appendAudit(ctx, q, audit)
	})
}

// DeletePolicy removes a policy with its channels and states.
func (s *RoutingStore) DeletePolicy(ctx context.Context, scope tenant.Scope, id routing.PolicyID, audit access.AuditEntry) error {
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		n, err := q.DeleteNotificationPolicy(ctx, uuid.UUID(id))
		if err != nil {
			return wrap(err, "delete notification policy")
		}
		if n == 0 {
			return access.ErrNotFound
		}
		return appendAudit(ctx, q, audit)
	})
}

// Snapshot implements routing.Store: policies, enabled channels and states in one transaction.
func (s *RoutingStore) Snapshot(ctx context.Context, scope tenant.Scope) (routing.Snapshot, error) {
	var out routing.Snapshot
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		var err error
		if out.Policies, err = policies(ctx, q); err != nil {
			return err
		}
		ids, err := q.ListEnabledNotificationChannelIDs(ctx)
		if err != nil {
			return wrap(err, "list enabled channels")
		}
		for _, id := range ids {
			out.Channels = append(out.Channels, notify.ChannelID(id))
		}
		rows, err := q.ListAlertRoutingStates(ctx)
		if err != nil {
			return wrap(err, "list routing states")
		}
		for _, r := range rows {
			out.States = append(out.States, routing.State{
				StateKey: routing.StateKey{
					Key:    alert.Key{Rule: alert.RuleID(r.RuleID), Node: identity.NodeID(r.NodeID), Dimension: r.Dimension},
					Policy: routing.PolicyID(r.PolicyID),
				},
				LastSent: r.LastSentAt, Escalated: r.Escalated,
			})
		}
		return nil
	})
	if err != nil {
		return routing.Snapshot{}, err
	}
	return out, nil
}

// Commit implements routing.Store: the deliveries are queued together with the states.
func (s *RoutingStore) Commit(ctx context.Context, scope tenant.Scope, deliveries []notify.Delivery, save []routing.State, del []routing.StateKey) error {
	rows, err := deliveryParams(deliveries)
	if err != nil {
		return err
	}
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		if err := insertDeliveries(ctx, q, rows); err != nil {
			return err
		}
		for _, st := range save {
			if err := q.UpsertAlertRoutingState(ctx, sqlcdb.UpsertAlertRoutingStateParams{
				RuleID: string(st.Key.Rule), NodeID: uuid.UUID(st.Key.Node), Dimension: st.Key.Dimension,
				PolicyID: uuid.UUID(st.Policy), LastSentAt: st.LastSent, Escalated: st.Escalated,
			}); err != nil {
				return wrap(err, "save routing state")
			}
		}
		for _, k := range del {
			if err := q.DeleteAlertRoutingState(ctx, sqlcdb.DeleteAlertRoutingStateParams{
				RuleID: string(k.Key.Rule), NodeID: uuid.UUID(k.Key.Node), Dimension: k.Key.Dimension, PolicyID: uuid.UUID(k.Policy),
			}); err != nil {
				return wrap(err, "delete routing state")
			}
		}
		return nil
	})
}
