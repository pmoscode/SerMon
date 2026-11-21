package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// NotificationStore persists notification channels and the delivery log (notify.Store, M2-10).
type NotificationStore struct{ db *DB }

var _ notify.Store = (*NotificationStore)(nil)

// NewNotificationStore returns the store on db.
func NewNotificationStore(db *DB) *NotificationStore { return &NotificationStore{db: db} }

// channelRow is a selected channel (the queries select the same columns).
type channelRow sqlcdb.GetNotificationChannelRow

func channelOf(r channelRow) (notify.StoredChannel, error) {
	c := notify.StoredChannel{
		Channel: notify.Channel{
			ID: notify.ChannelID(r.ID), Type: notify.ChannelType(r.Type), Name: r.Name, Lang: notify.Lang(r.Language),
			Enabled: r.Enabled, SecretNames: r.SecretNames, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		},
		SealedSecrets: r.SealedSecrets,
	}
	if r.CreatedBy != nil {
		c.CreatedBy = identity.UserID(*r.CreatedBy)
	}
	if err := json.Unmarshal(r.Settings, &c.Settings); err != nil {
		return notify.StoredChannel{}, fmt.Errorf("decode settings of channel %s: %w", r.ID, err)
	}
	return c, nil
}

func encodeSettings(settings map[string]string) ([]byte, error) {
	if settings == nil {
		settings = map[string]string{}
	}
	data, err := json.Marshal(settings)
	if err != nil {
		return nil, fmt.Errorf("encode channel settings: %w", err)
	}
	return data, nil
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// CreateChannel stores a new channel with its audit entry.
func (s *NotificationStore) CreateChannel(ctx context.Context, scope tenant.Scope, c notify.StoredChannel, audit access.AuditEntry) error {
	settings, err := encodeSettings(c.Settings)
	if err != nil {
		return err
	}
	p := sqlcdb.InsertNotificationChannelParams{
		ID: uuid.UUID(c.ID), Type: string(c.Type), Name: c.Name, Language: string(c.Lang), Enabled: c.Enabled,
		Settings: settings, SecretNames: nonNilStrings(c.SecretNames), SealedSecrets: c.SealedSecrets,
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
	if !c.CreatedBy.IsZero() {
		by := uuid.UUID(c.CreatedBy)
		p.CreatedBy = &by
	}
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		if err := q.InsertNotificationChannel(ctx, p); err != nil {
			return wrap(err, "insert notification channel")
		}
		return appendAudit(ctx, q, audit)
	})
}

// UpdateChannel replaces a channel's configuration; access.ErrNotFound outside the scope.
func (s *NotificationStore) UpdateChannel(ctx context.Context, scope tenant.Scope, c notify.StoredChannel, audit access.AuditEntry) error {
	settings, err := encodeSettings(c.Settings)
	if err != nil {
		return err
	}
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		n, err := q.UpdateNotificationChannel(ctx, sqlcdb.UpdateNotificationChannelParams{
			ID: uuid.UUID(c.ID), Name: c.Name, Language: string(c.Lang), Enabled: c.Enabled, Settings: settings,
			SecretNames: nonNilStrings(c.SecretNames), SealedSecrets: c.SealedSecrets, UpdatedAt: c.UpdatedAt,
		})
		if err != nil {
			return wrap(err, "update notification channel")
		}
		if n == 0 {
			return access.ErrNotFound
		}
		return appendAudit(ctx, q, audit)
	})
}

// DeleteChannel removes a channel; its deliveries stay in the log.
func (s *NotificationStore) DeleteChannel(ctx context.Context, scope tenant.Scope, id notify.ChannelID, audit access.AuditEntry) error {
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		n, err := q.DeleteNotificationChannel(ctx, uuid.UUID(id))
		if err != nil {
			return wrap(err, "delete notification channel")
		}
		if n == 0 {
			return access.ErrNotFound
		}
		return appendAudit(ctx, q, audit)
	})
}

// Channels returns the organization's channels, oldest first.
func (s *NotificationStore) Channels(ctx context.Context, scope tenant.Scope) ([]notify.StoredChannel, error) {
	var out []notify.StoredChannel
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		rows, err := q.ListNotificationChannels(ctx)
		if err != nil {
			return wrap(err, "list notification channels")
		}
		out = make([]notify.StoredChannel, 0, len(rows))
		for _, r := range rows {
			c, err := channelOf(channelRow(r))
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Channel returns one channel; access.ErrNotFound outside the scope.
func (s *NotificationStore) Channel(ctx context.Context, scope tenant.Scope, id notify.ChannelID) (notify.StoredChannel, error) {
	var out notify.StoredChannel
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		r, err := q.GetNotificationChannel(ctx, uuid.UUID(id))
		if err != nil {
			return accessError(err, "load notification channel")
		}
		out, err = channelOf(channelRow(r))
		return err
	})
	if err != nil {
		return notify.StoredChannel{}, err
	}
	return out, nil
}

// Enqueue stores deliveries (new ones and finished test sends).
func (s *NotificationStore) Enqueue(ctx context.Context, scope tenant.Scope, deliveries []notify.Delivery) error {
	rows := make([]sqlcdb.InsertNotificationDeliveryParams, len(deliveries))
	for i, d := range deliveries {
		n, err := json.Marshal(d.Notification)
		if err != nil {
			return fmt.Errorf("encode notification: %w", err)
		}
		rows[i] = sqlcdb.InsertNotificationDeliveryParams{
			ChannelID: uuid.UUID(d.Channel), Notification: n, Test: d.Test, Status: string(d.Status), Attempts: int32(min(d.Attempts, 100)), //nolint:gosec // G115: bounded
			CreatedAt: d.CreatedAt, NextAttemptAt: d.NextAttemptAt, LastAttemptAt: optionalTime(d.LastAttemptAt),
			SentAt: optionalTime(d.SentAt), LastError: d.LastError,
		}
	}
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		results := q.InsertNotificationDelivery(ctx, rows)
		var first error
		results.Exec(func(_ int, err error) {
			if err != nil && first == nil {
				first = err
			}
		})
		if err := results.Close(); err != nil && first == nil {
			first = err
		}
		return wrap(first, "insert notification deliveries")
	})
}

// deliveryRow is a selected delivery (the queries select the same columns).
type deliveryRow sqlcdb.ListNotificationDeliveriesRow

func deliveryOf(r deliveryRow) (notify.Delivery, error) {
	d := notify.Delivery{
		ID: r.ID, Channel: notify.ChannelID(r.ChannelID), Test: r.Test, Status: notify.Status(r.Status), Attempts: int(r.Attempts),
		CreatedAt: r.CreatedAt, NextAttemptAt: r.NextAttemptAt, LastError: r.LastError,
	}
	for dst, src := range map[*time.Time]*time.Time{&d.ClaimedUntil: r.ClaimedUntil, &d.LastAttemptAt: r.LastAttemptAt, &d.SentAt: r.SentAt} {
		if src != nil {
			*dst = *src
		}
	}
	if err := json.Unmarshal(r.Notification, &d.Notification); err != nil {
		return notify.Delivery{}, fmt.Errorf("decode notification of delivery %d: %w", r.ID, err)
	}
	return d, nil
}

func deliveriesOf[R any](rows []R, conv func(R) deliveryRow) ([]notify.Delivery, error) {
	out := make([]notify.Delivery, 0, len(rows))
	for _, r := range rows {
		d, err := deliveryOf(conv(r))
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// Deliveries returns the delivery log, newest first.
func (s *NotificationStore) Deliveries(ctx context.Context, scope tenant.Scope, f notify.DeliveryFilter) ([]notify.Delivery, error) {
	p := sqlcdb.ListNotificationDeliveriesParams{Before: f.Before, MaxRows: int32(min(max(f.Limit, 0), 1000))}
	if f.Channel != nil {
		id := uuid.UUID(*f.Channel)
		p.ChannelID = &id
	}
	var out []notify.Delivery
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		rows, err := q.ListNotificationDeliveries(ctx, p)
		if err != nil {
			return wrap(err, "list notification deliveries")
		}
		out, err = deliveriesOf(rows, func(r sqlcdb.ListNotificationDeliveriesRow) deliveryRow { return deliveryRow(r) })
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Organizations lists all organizations (instance level).
func (s *NotificationStore) Organizations(ctx context.Context) ([]tenant.OrgID, error) {
	return NewAlertStore(s.db).Organizations(ctx)
}

// Claim marks due deliveries as sending until until.
func (s *NotificationStore) Claim(ctx context.Context, scope tenant.Scope, now, until time.Time, limit int) ([]notify.Delivery, error) {
	var out []notify.Delivery
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		rows, err := q.ClaimNotificationDeliveries(ctx, sqlcdb.ClaimNotificationDeliveriesParams{
			Until: &until, Now: now, MaxRows: int32(min(max(limit, 0), 1000)),
		})
		if err != nil {
			return wrap(err, "claim notification deliveries")
		}
		out, err = deliveriesOf(rows, func(r sqlcdb.ClaimNotificationDeliveriesRow) deliveryRow { return deliveryRow(r) })
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Finish stores the outcome of an attempt.
func (s *NotificationStore) Finish(ctx context.Context, scope tenant.Scope, d notify.Delivery) error {
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		return wrap(q.FinishNotificationDelivery(ctx, sqlcdb.FinishNotificationDeliveryParams{
			ID: d.ID, Status: string(d.Status), NextAttemptAt: d.NextAttemptAt, LastAttemptAt: optionalTime(d.LastAttemptAt),
			SentAt: optionalTime(d.SentAt), LastError: d.LastError,
		}), "finish notification delivery")
	})
}

// Purge deletes finished deliveries created before before.
func (s *NotificationStore) Purge(ctx context.Context, scope tenant.Scope, before time.Time) (int, error) {
	var n int64
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		var err error
		n, err = q.PurgeNotificationDeliveries(ctx, before)
		return wrap(err, "purge notification deliveries")
	})
	return int(n), err
}
