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
	Tags       map[string]string `json:"tags,omitempty"` // M2-29
}

func encodeMatchers(m routing.Matchers) ([]byte, error) {
	j := matchersJSON{Severities: m.Severities, Rules: m.Rules, Labels: m.Labels, Tags: m.Tags}
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
	m := routing.Matchers{Severities: j.Severities, Rules: j.Rules, Labels: j.Labels, Tags: j.Tags}
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

// Snapshot implements routing.Store: policies, enabled channels, states, the silences that end
// after now and the enabled maintenance windows, in one transaction.
func (s *RoutingStore) Snapshot(ctx context.Context, scope tenant.Scope, now time.Time) (routing.Snapshot, error) {
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
				LastSent: derefTime(r.LastSentAt), FirstSent: derefTime(r.FirstSentAt), Escalated: r.Escalated, Muted: r.Muted,
			})
		}
		if out.Silences, err = silences(ctx, q, now); err != nil {
			return err
		}
		windows, err := maintenanceWindows(ctx, q)
		if err != nil {
			return err
		}
		for _, w := range windows {
			if w.Enabled {
				out.Windows = append(out.Windows, w)
			}
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
				PolicyID: uuid.UUID(st.Policy), LastSentAt: optionalTime(st.LastSent), FirstSentAt: optionalTime(st.FirstSent),
				Escalated: st.Escalated, Muted: st.Muted,
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

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

func optionalUser(u identity.UserID) *uuid.UUID {
	if u.IsZero() {
		return nil
	}
	id := uuid.UUID(u)
	return &id
}

func userOf(id *uuid.UUID) identity.UserID {
	if id == nil {
		return identity.UserID{}
	}
	return identity.UserID(*id)
}

type silenceRow sqlcdb.GetAlertSilenceRow

func silenceOf(r silenceRow) (routing.Silence, error) {
	m, err := decodeMatchers(r.Matchers)
	if err != nil {
		return routing.Silence{}, err
	}
	s := routing.Silence{
		ID: routing.SilenceID(r.ID), Match: m, StartsAt: r.StartsAt, EndsAt: r.EndsAt, Comment: r.Comment,
		CreatedAt: r.CreatedAt, CreatedBy: userOf(r.CreatedBy), ExpiredBy: userOf(r.ExpiredBy),
	}
	for _, n := range r.NodeIds {
		s.Nodes = append(s.Nodes, identity.NodeID(n))
	}
	return s, nil
}

func silences(ctx context.Context, q *sqlcdb.Queries, since time.Time) ([]routing.Silence, error) {
	rows, err := q.ListAlertSilences(ctx, since)
	if err != nil {
		return nil, wrap(err, "list silences")
	}
	out := make([]routing.Silence, 0, len(rows))
	for _, r := range rows {
		s, err := silenceOf(silenceRow(r))
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// Silences implements routing.SilenceStore: the silences that end after since.
func (s *RoutingStore) Silences(ctx context.Context, scope tenant.Scope, since time.Time) ([]routing.Silence, error) {
	var out []routing.Silence
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		var err error
		out, err = silences(ctx, q, since)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Silence returns one silence; access.ErrNotFound outside the scope.
func (s *RoutingStore) Silence(ctx context.Context, scope tenant.Scope, id routing.SilenceID) (routing.Silence, error) {
	var out routing.Silence
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		r, err := q.GetAlertSilence(ctx, uuid.UUID(id))
		if err != nil {
			return accessError(err, "load silence")
		}
		out, err = silenceOf(silenceRow(r))
		return err
	})
	if err != nil {
		return routing.Silence{}, err
	}
	return out, nil
}

// CreateSilence stores a silence with its audit entry and drops silences that ended before
// purgeBefore.
func (s *RoutingStore) CreateSilence(ctx context.Context, scope tenant.Scope, sil routing.Silence, purgeBefore time.Time, audit access.AuditEntry) error {
	matchers, err := encodeMatchers(sil.Match)
	if err != nil {
		return err
	}
	nodes := make([]uuid.UUID, 0, len(sil.Nodes))
	for _, n := range sil.Nodes {
		nodes = append(nodes, uuid.UUID(n))
	}
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		if err := q.PurgeAlertSilences(ctx, purgeBefore); err != nil {
			return wrap(err, "purge silences")
		}
		if err := q.InsertAlertSilence(ctx, sqlcdb.InsertAlertSilenceParams{
			ID: uuid.UUID(sil.ID), Matchers: matchers, NodeIds: nodes, StartsAt: sil.StartsAt, EndsAt: sil.EndsAt,
			Comment: sil.Comment, CreatedAt: sil.CreatedAt, CreatedBy: optionalUser(sil.CreatedBy),
		}); err != nil {
			return wrap(err, "insert silence")
		}
		return appendAudit(ctx, q, audit)
	})
}

// ExpireSilence ends a silence at at; access.ErrNotFound if it does not exist in the scope or
// ended already.
func (s *RoutingStore) ExpireSilence(ctx context.Context, scope tenant.Scope, id routing.SilenceID, at time.Time, by identity.UserID, audit access.AuditEntry) error {
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		n, err := q.ExpireAlertSilence(ctx, sqlcdb.ExpireAlertSilenceParams{ID: uuid.UUID(id), At: at, ExpiredBy: optionalUser(by)})
		if err != nil {
			return wrap(err, "expire silence")
		}
		if n == 0 {
			return access.ErrNotFound
		}
		return appendAudit(ctx, q, audit)
	})
}

func maintenanceWindows(ctx context.Context, q *sqlcdb.Queries) ([]routing.MaintenanceWindow, error) {
	rows, err := q.ListMaintenanceWindows(ctx)
	if err != nil {
		return nil, wrap(err, "list maintenance windows")
	}
	out := make([]routing.MaintenanceWindow, 0, len(rows))
	for _, r := range rows {
		w := routing.MaintenanceWindow{
			ID: routing.WindowID(r.ID), Name: r.Name, Enabled: r.Enabled, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
			CreatedBy: userOf(r.CreatedBy),
			Schedule: routing.Schedule{
				Start: time.Duration(r.StartMinute) * time.Minute, Duration: time.Duration(r.DurationMinutes) * time.Minute, TimeZone: r.TimeZone,
			},
		}
		for _, g := range r.GroupIds {
			w.Groups = append(w.Groups, tenant.GroupID(g))
		}
		for _, d := range r.Days {
			w.Schedule.Days = append(w.Schedule.Days, time.Weekday(d))
		}
		out = append(out, w)
	}
	return out, nil
}

// Windows implements routing.WindowStore: the maintenance windows by name.
func (s *RoutingStore) Windows(ctx context.Context, scope tenant.Scope) ([]routing.MaintenanceWindow, error) {
	var out []routing.MaintenanceWindow
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		var err error
		out, err = maintenanceWindows(ctx, q)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func windowColumns(w routing.MaintenanceWindow) ([]uuid.UUID, []int16) {
	groups := make([]uuid.UUID, 0, len(w.Groups))
	for _, g := range w.Groups {
		groups = append(groups, uuid.UUID(g))
	}
	days := make([]int16, 0, len(w.Schedule.Days))
	for _, d := range w.Schedule.Days {
		days = append(days, int16(d)) //nolint:gosec // G115: a weekday
	}
	return groups, days
}

func minutes(d time.Duration) int32 { return int32(d / time.Minute) } //nolint:gosec // G115: at most a day

// CreateWindow stores a maintenance window with its audit entry.
func (s *RoutingStore) CreateWindow(ctx context.Context, scope tenant.Scope, w routing.MaintenanceWindow, audit access.AuditEntry) error {
	groups, days := windowColumns(w)
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		if err := q.InsertMaintenanceWindow(ctx, sqlcdb.InsertMaintenanceWindowParams{
			ID: uuid.UUID(w.ID), Name: w.Name, Enabled: w.Enabled, GroupIds: groups, Days: days,
			StartMinute: minutes(w.Schedule.Start), DurationMinutes: minutes(w.Schedule.Duration), TimeZone: w.Schedule.TimeZone,
			CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt, CreatedBy: optionalUser(w.CreatedBy),
		}); err != nil {
			return wrap(err, "insert maintenance window")
		}
		return appendAudit(ctx, q, audit)
	})
}

// UpdateWindow replaces a maintenance window; access.ErrNotFound outside the scope.
func (s *RoutingStore) UpdateWindow(ctx context.Context, scope tenant.Scope, w routing.MaintenanceWindow, audit access.AuditEntry) error {
	groups, days := windowColumns(w)
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		n, err := q.UpdateMaintenanceWindow(ctx, sqlcdb.UpdateMaintenanceWindowParams{
			ID: uuid.UUID(w.ID), Name: w.Name, Enabled: w.Enabled, GroupIds: groups, Days: days,
			StartMinute: minutes(w.Schedule.Start), DurationMinutes: minutes(w.Schedule.Duration), TimeZone: w.Schedule.TimeZone,
			UpdatedAt: w.UpdatedAt,
		})
		if err != nil {
			return wrap(err, "update maintenance window")
		}
		if n == 0 {
			return access.ErrNotFound
		}
		return appendAudit(ctx, q, audit)
	})
}

// DeleteWindow removes a maintenance window; access.ErrNotFound outside the scope.
func (s *RoutingStore) DeleteWindow(ctx context.Context, scope tenant.Scope, id routing.WindowID, audit access.AuditEntry) error {
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		n, err := q.DeleteMaintenanceWindow(ctx, uuid.UUID(id))
		if err != nil {
			return wrap(err, "delete maintenance window")
		}
		if n == 0 {
			return access.ErrNotFound
		}
		return appendAudit(ctx, q, audit)
	})
}
