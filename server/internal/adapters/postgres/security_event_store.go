package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/alerting"
	"github.com/pmoscode/SerMon/server/internal/app/secevents"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/secevent"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// SecurityEventStore stores and lists security events (M2-07) and expires their partitions.
type SecurityEventStore struct{ db *DB }

var (
	_ secevents.Writer     = (*SecurityEventStore)(nil)
	_ secevents.Store      = (*SecurityEventStore)(nil)
	_ secevents.Partitions = (*SecurityEventStore)(nil)
	_ alerting.EventReader = (*SecurityEventStore)(nil)
)

// NewSecurityEventStore returns the store on db.
func NewSecurityEventStore(db *DB) *SecurityEventStore { return &SecurityEventStore{db: db} }

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

// Write stores the batches above the node's event sequence and raises it, in one transaction
// with the node row locked: concurrent writes of the same batch store it once.
func (s *SecurityEventStore) Write(ctx context.Context, org tenant.OrgID, node identity.NodeID, batches []secevents.Batch) ([]secevents.Batch, error) {
	scope, err := tenant.ForAllGroups(org)
	if err != nil {
		return nil, fmt.Errorf("write security events: %w", err)
	}
	sorted := slices.SortedFunc(slices.Values(batches), func(a, b secevents.Batch) int { return compareSeq(a.Seq, b.Seq) })
	var written []secevents.Batch
	err = s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		stored, err := q.LockNodeEventSeq(ctx, uuid.UUID(node))
		if err != nil {
			return wrap(err, "lock node event sequence")
		}
		written = nil
		var rows []sqlcdb.InsertSecurityEventParams
		seq := stored
		for _, b := range sorted {
			if b.Seq > uint64(max(seq, 0)) {
				for _, e := range b.Events {
					row, err := insertParams(e)
					if err != nil {
						return err
					}
					rows = append(rows, row)
				}
				written = append(written, b)
				seq = int64(min(b.Seq, uint64(1<<63-1)))
			}
		}
		if len(written) == 0 {
			return nil
		}
		if len(rows) > 0 {
			results := q.InsertSecurityEvent(ctx, rows)
			var insertErr error
			results.Exec(func(_ int, err error) {
				if err != nil && insertErr == nil {
					insertErr = err
				}
			})
			if err := results.Close(); err != nil && insertErr == nil {
				insertErr = err
			}
			if insertErr != nil {
				return wrap(insertErr, "insert security events")
			}
		}
		return wrap(q.SetNodeEventSeq(ctx, sqlcdb.SetNodeEventSeqParams{Seq: seq, NodeID: uuid.UUID(node)}), "store node event sequence")
	})
	if err != nil {
		return nil, err
	}
	return written, nil
}

func compareSeq(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func insertParams(e secevent.Event) (sqlcdb.InsertSecurityEventParams, error) {
	details, err := json.Marshal(e.Details)
	if err != nil {
		return sqlcdb.InsertSecurityEventParams{}, fmt.Errorf("encode details: %w", err)
	}
	p := sqlcdb.InsertSecurityEventParams{
		NodeID: uuid.UUID(e.Node), OccurredAt: e.OccurredAt, Kind: string(e.Kind), UserName: pgtype.Text{String: e.User, Valid: e.User != "" || userKind(e.Kind)},
		Details: details, Unverified: e.Unverified,
	}
	if r := e.Remote; r != nil {
		if r.IP.IsValid() {
			ip := r.IP
			p.RemoteIp = &ip
		}
		p.RemoteHost = text(r.Host)
		p.RemotePort = pgtype.Int4{Int32: int32(r.Port), Valid: r.Port != 0}
	}
	return p, nil
}

// userKind reports whether the kind always has a user, possibly the empty one an attacker tried.
func userKind(k secevent.Kind) bool {
	switch k {
	case secevent.SSHLoginFailed, secevent.SSHInvalidUser, secevent.SSHLoginSucceeded, secevent.SudoAuthFailed,
		secevent.SudoNotAllowed, secevent.SudoCommand, secevent.AccountCreated, secevent.AccountDeleted:
		return true
	case secevent.SSHPreauthDisconnect, secevent.SystemReboot, secevent.OOMKill:
		return false
	}
	return false
}

// List returns the events of the filter, newest first.
func (s *SecurityEventStore) List(ctx context.Context, scope tenant.Scope, f secevents.Filter) ([]secevent.Event, error) {
	p := sqlcdb.ListSecurityEventsParams{
		FromTime: f.From, ToTime: f.To, AllGroups: scope.AllGroups(), GroupIds: groupIDs(scope),
		NodeIds: make([]uuid.UUID, len(f.Nodes)), Kinds: make([]string, len(f.Kinds)), Network: f.Network,
		MaxRows: int32(min(max(f.Limit, 0), 1<<20)),
	}
	for i, n := range f.Nodes {
		p.NodeIds[i] = uuid.UUID(n)
	}
	for i, k := range f.Kinds {
		p.Kinds[i] = string(k)
	}
	if f.User != nil {
		p.UserName = pgtype.Text{String: *f.User, Valid: true}
	}
	if f.Before != nil {
		at := f.Before.At
		p.BeforeTime, p.BeforeID = &at, pgtype.Int8{Int64: f.Before.ID, Valid: true}
	}
	var out []secevent.Event
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		rows, err := q.ListSecurityEvents(ctx, p)
		if err != nil {
			return wrap(err, "list security events")
		}
		out, err = eventsOf(rows, func(r sqlcdb.ListSecurityEventsRow) eventRow { return eventRow(r) })
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Received returns the events of one kind and the nodes received since q.Since, newest received
// first (alerting.EventReader, M2-08).
func (s *SecurityEventStore) Received(ctx context.Context, scope tenant.Scope, q alerting.EventQuery) ([]secevent.Event, error) {
	p := sqlcdb.ReceivedSecurityEventsParams{
		Kind: string(q.Kind), NodeIds: make([]uuid.UUID, len(q.Nodes)), Since: q.Since,
		MinOccurred: q.Since.Add(-secevent.MaxAge - time.Hour), MaxRows: int32(min(max(q.Limit, 0), 1<<20)),
	}
	for i, n := range q.Nodes {
		p.NodeIds[i] = uuid.UUID(n)
	}
	var out []secevent.Event
	err := s.db.InTenant(ctx, scope, func(qs *sqlcdb.Queries) error {
		rows, err := qs.ReceivedSecurityEvents(ctx, p)
		if err != nil {
			return wrap(err, "read received security events")
		}
		out, err = eventsOf(rows, func(r sqlcdb.ReceivedSecurityEventsRow) eventRow { return eventRow(r) })
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// eventRow is a selected security event (the queries select the same columns).
type eventRow sqlcdb.ListSecurityEventsRow

func eventsOf[R any](rows []R, conv func(R) eventRow) ([]secevent.Event, error) {
	out := make([]secevent.Event, 0, len(rows))
	for _, row := range rows {
		r := conv(row)
		e := secevent.Event{
			ID: r.ID, Node: identity.NodeID(r.NodeID), OccurredAt: r.OccurredAt, ReceivedAt: r.ReceivedAt,
			Kind: secevent.Kind(r.Kind), User: r.UserName.String, Unverified: r.Unverified,
		}
		if r.RemoteIp != nil || r.RemoteHost.Valid {
			e.Remote = &secevent.Remote{Host: r.RemoteHost.String, Port: uint16(max(min(r.RemotePort.Int32, 65535), 0))}
			if r.RemoteIp != nil {
				e.Remote.IP = *r.RemoteIp
			}
		}
		if err := json.Unmarshal(r.Details, &e.Details); err != nil {
			return nil, fmt.Errorf("decode details of event %d: %w", r.ID, err)
		}
		out = append(out, e)
	}
	return out, nil
}

// Horizon returns the end of the last partition.
func (s *SecurityEventStore) Horizon(ctx context.Context) (time.Time, error) {
	t, err := s.db.withoutTenant().SecurityEventHorizon(ctx)
	if err != nil {
		return time.Time{}, fmt.Errorf("security event horizon: %w", err)
	}
	return t, nil
}

// Expire drops the partitions that end at or before before.
func (s *SecurityEventStore) Expire(ctx context.Context, before time.Time) (int, error) {
	n, err := s.db.withoutTenant().ExpireSecurityEventPartitions(ctx, before)
	if err != nil {
		return 0, fmt.Errorf("expire security event partitions: %w", err)
	}
	return int(n), nil
}
