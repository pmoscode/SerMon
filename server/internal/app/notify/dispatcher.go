package notify

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
)

// The delivery worker: every server instance polls the queue of each organization, claims due
// deliveries for ClaimDuration and sends them. A claim that expires (the instance died) is taken
// over, so a notification may arrive twice but is not lost.
const (
	PollInterval  = 5 * time.Second
	ClaimDuration = 2 * time.Minute
	SendTimeout   = 30 * time.Second
	// Retention is how long the delivery log is kept.
	Retention  = 30 * 24 * time.Hour
	purgeEvery = time.Hour
	claimBatch = 20
	// MaxErrorBytes bounds the error text of a delivery.
	MaxErrorBytes = 500
)

// Stats are counters for the server's own metrics.
type Stats struct {
	Sent, Failed, Retried, Blocked, Errors atomic.Uint64
}

// Dispatcher queues notifications and delivers them.
type Dispatcher struct {
	d         Deps
	send      *sender
	stats     Stats
	mu        sync.Mutex
	lastPurge map[tenant.OrgID]time.Time
}

// NewDispatcher returns a dispatcher.
func NewDispatcher(d Deps) *Dispatcher {
	return &Dispatcher{d: d, send: newSender(d), lastPurge: map[tenant.OrgID]time.Time{}}
}

// Stats returns the counters.
func (d *Dispatcher) Stats() *Stats { return &d.stats }

// Enqueue queues a notification for channels of the organization (routing, M2-14).
func (d *Dispatcher) Enqueue(ctx context.Context, org tenant.OrgID, channels []ChannelID, n Notification) error {
	scope, err := tenant.ForAllGroups(org)
	if err != nil {
		return fmt.Errorf("enqueue: %w", err)
	}
	now := d.d.Now()
	deliveries := make([]Delivery, 0, len(channels))
	for _, id := range channels {
		if _, err := d.d.Store.Channel(ctx, scope, id); errors.Is(err, access.ErrNotFound) {
			return fmt.Errorf("enqueue to %s: %w", id, ErrChannelGone)
		} else if err != nil {
			return fmt.Errorf("enqueue: %w", err)
		}
		deliveries = append(deliveries, Delivery{Channel: id, Notification: n, Status: StatusPending, CreatedAt: now, NextAttemptAt: now})
	}
	if err := d.d.Store.Enqueue(ctx, scope, deliveries); err != nil {
		return fmt.Errorf("enqueue: %w", err)
	}
	return nil
}

// Run delivers every PollInterval until ctx ends.
func (d *Dispatcher) Run(ctx context.Context) error {
	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			d.Round(ctx)
		}
	}
}

// Round delivers the due notifications of every organization once.
func (d *Dispatcher) Round(ctx context.Context) {
	orgs, err := d.d.Store.Organizations(ctx)
	if err != nil {
		d.stats.Errors.Add(1)
		d.d.Logger.ErrorContext(ctx, "notify: list organizations failed", "error", err)
		return
	}
	for _, org := range orgs {
		if err := d.organization(ctx, org); err != nil {
			d.stats.Errors.Add(1)
			d.d.Logger.ErrorContext(ctx, "notify: delivery round failed", "org", org.String(), "error", err)
		}
	}
}

func (d *Dispatcher) organization(ctx context.Context, org tenant.OrgID) error {
	scope, err := tenant.ForAllGroups(org)
	if err != nil {
		return fmt.Errorf("scope: %w", err)
	}
	now := d.d.Now()
	if d.purgeDue(org, now) {
		if _, err := d.d.Store.Purge(ctx, scope, now.Add(-Retention)); err != nil {
			return fmt.Errorf("purge the delivery log: %w", err)
		}
	}
	claimed, err := d.d.Store.Claim(ctx, scope, now, now.Add(ClaimDuration), claimBatch)
	if err != nil {
		return fmt.Errorf("claim deliveries: %w", err)
	}
	for _, del := range claimed {
		if err := d.attempt(ctx, org, scope, del); err != nil {
			return err
		}
	}
	return nil
}

func (d *Dispatcher) purgeDue(org tenant.OrgID, now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if last, ok := d.lastPurge[org]; ok && now.Sub(last) < purgeEvery {
		return false
	}
	d.lastPurge[org] = now
	return true
}

func (d *Dispatcher) attempt(ctx context.Context, org tenant.OrgID, scope tenant.Scope, del Delivery) error {
	var failure *sendError
	stored, err := d.d.Store.Channel(ctx, scope, del.Channel)
	switch {
	case errors.Is(err, access.ErrNotFound):
		failure = &sendError{err: ErrChannelGone, text: ErrChannelGone.Error()}
	case err != nil:
		return fmt.Errorf("load channel: %w", err)
	case !stored.Enabled:
		failure = &sendError{err: ErrChannelGone, text: ErrChannelGone.Error()}
	default:
		failure = d.send.deliver(ctx, org, stored, del.Notification)
	}
	now := d.d.Now()
	del.LastAttemptAt, del.ClaimedUntil = now, time.Time{}
	if failure == nil {
		del.Status, del.SentAt, del.LastError = StatusSent, now, ""
		d.stats.Sent.Add(1)
	} else {
		del.LastError = failure.text
		if errors.Is(failure.err, safehttp.ErrBlocked) {
			d.stats.Blocked.Add(1)
		}
		if next, retry := NextAttempt(del.Attempts, failure.err, now); retry {
			del.Status, del.NextAttemptAt = StatusPending, next
			d.stats.Retried.Add(1)
		} else {
			del.Status = StatusFailed
			d.stats.Failed.Add(1)
		}
		d.d.Logger.WarnContext(ctx, "notify: delivery failed", "org", org.String(), "channel", del.Channel.String(),
			"attempt", del.Attempts, "final", del.Status == StatusFailed, "error", failure.text)
	}
	if err := d.d.Store.Finish(ctx, scope, del); err != nil {
		return fmt.Errorf("store delivery %d: %w", del.ID, err)
	}
	return nil
}

// sendError is a failed send with its text for the log (secrets removed).
type sendError struct {
	err  error
	text string
}

// sender renders and sends one notification over a stored channel.
type sender struct{ d Deps }

func newSender(d Deps) *sender { return &sender{d: d} }

func (s *sender) deliver(ctx context.Context, org tenant.OrgID, stored StoredChannel, n Notification) *sendError {
	notifier, ok := s.d.Registry.Get(stored.Type)
	if !ok {
		err := Permanent(fmt.Errorf("%w: %q", ErrUnknownType, stored.Type))
		return &sendError{err: err, text: err.Error()}
	}
	open, err := OpenSecrets(s.d.Box, org, stored)
	if err != nil {
		err = Permanent(err)
		return &sendError{err: err, text: "the channel's secrets cannot be opened"}
	}
	ctx, cancel := context.WithTimeout(ctx, SendTimeout)
	defer cancel()
	m := Render(n, stored.Lang, s.d.BaseURL)
	if err := notifier.Send(ctx, Config{Settings: cloneMap(stored.Settings), Secrets: open}, m); err != nil {
		return &sendError{err: err, text: describe(err, open)}
	}
	return nil
}

// describe is the text of a send error for the log and the UI: without the channel's secrets
// (a service may quote them back), on one line, at most MaxErrorBytes.
func describe(err error, secrets map[string]string) string {
	text := err.Error()
	for _, v := range secrets {
		if len(v) >= 4 {
			text = strings.ReplaceAll(text, v, "***")
		}
	}
	text = strings.Join(strings.Fields(oneLine(text)), " ")
	if len(text) <= MaxErrorBytes {
		return text
	}
	cut := MaxErrorBytes - len("…")
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}
