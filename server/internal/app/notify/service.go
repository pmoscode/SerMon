package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	"github.com/pmoscode/SerMon/server/internal/platform/ratelimit"
	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
	"github.com/pmoscode/SerMon/server/internal/secrets"
)

// TestsPerMinute bounds "send test" per organization: it sends to targets an admin chose.
const TestsPerMinute = 5

// ErrTooManyTests means the organization sent too many tests in the last minute.
var ErrTooManyTests = errors.New("too many test notifications, please wait a minute")

// MaxChannels bounds the channels of an organization (M2-38): each can mail or call out on its
// behalf, through the operator's server.
const MaxChannels = 50

// ErrTooManyChannels is returned when the organization has MaxChannels channels.
var ErrTooManyChannels = fmt.Errorf("at most %d notification channels per organization", MaxChannels)

// Status is where a delivery stands.
type Status string

// Statuses of a delivery.
const (
	StatusPending Status = "pending" // waiting for its (next) attempt
	StatusSending Status = "sending" // claimed by a server instance until ClaimedUntil
	StatusSent    Status = "sent"
	StatusFailed  Status = "failed" // for good
)

// Delivery is one notification to one channel, with its attempts: the delivery log.
type Delivery struct {
	ID            int64
	Channel       ChannelID
	Notification  Notification
	Test          bool
	Status        Status
	Attempts      int
	CreatedAt     time.Time
	NextAttemptAt time.Time
	ClaimedUntil  time.Time
	LastAttemptAt time.Time
	SentAt        time.Time
	LastError     string
}

// DeliveryFilter selects deliveries of the log, newest first.
type DeliveryFilter struct {
	Channel *ChannelID
	Before  int64 // ID; 0: from the newest
	Limit   int
}

// Store persists channels and deliveries; everything but Organizations runs in the scope of
// a whole organization. Channel writes store the audit entry in the same transaction.
type Store interface {
	CreateChannel(ctx context.Context, scope tenant.Scope, c StoredChannel, audit access.AuditEntry) error
	UpdateChannel(ctx context.Context, scope tenant.Scope, c StoredChannel, audit access.AuditEntry) error
	DeleteChannel(ctx context.Context, scope tenant.Scope, id ChannelID, audit access.AuditEntry) error
	Channels(ctx context.Context, scope tenant.Scope) ([]StoredChannel, error)
	// Channel returns access.ErrNotFound for a channel outside the scope.
	Channel(ctx context.Context, scope tenant.Scope, id ChannelID) (StoredChannel, error)

	Enqueue(ctx context.Context, scope tenant.Scope, deliveries []Delivery) error
	Deliveries(ctx context.Context, scope tenant.Scope, f DeliveryFilter) ([]Delivery, error)
	// Organizations lists all organizations (instance level).
	Organizations(ctx context.Context) ([]tenant.OrgID, error)
	// Claim marks up to limit due deliveries as sending until until and counts the attempt:
	// pending ones whose next attempt is due and sending ones whose claim expired.
	Claim(ctx context.Context, scope tenant.Scope, now, until time.Time, limit int) ([]Delivery, error)
	// Finish stores the outcome of an attempt – only while the delivery is still sending under
	// claim (the ClaimedUntil it was claimed with); ErrClaimLost otherwise (fencing, M2-38).
	Finish(ctx context.Context, scope tenant.Scope, d Delivery, claim time.Time) error
	// Purge deletes finished deliveries created before before.
	Purge(ctx context.Context, scope tenant.Scope, before time.Time) (int, error)
}

// Deps are the collaborators of Service and Dispatcher. Outbound is the policy for target URLs
// (the same as the notifiers' safehttp client); BaseURL is the origin of the UI for deep links.
type Deps struct {
	Store    Store
	Registry *Registry
	Box      *secrets.Box
	Policy   authz.Policy
	Outbound safehttp.Policy
	BaseURL  string
	Now      func() time.Time
	NewID    func() uuid.UUID
	Logger   *slog.Logger
	// MailsPerHour bounds the mails of one organization (0: DefaultMailsPerHour, M2-38).
	MailsPerHour int
}

// Service manages the channels of an organization (admins with ManageChannels).
type Service struct {
	d     Deps
	tests *ratelimit.Limiter
	send  *sender
}

// NewService returns the service.
func NewService(d Deps) *Service {
	return &Service{d: d, tests: ratelimit.New(TestsPerMinute, time.Minute, d.Now), send: newSender(d)}
}

// NewChannel is a channel to create.
type NewChannel struct {
	Type     ChannelType
	Name     string
	Lang     Lang
	Enabled  bool
	Settings map[string]string
	Secrets  map[string]string
}

// ChannelChange changes a channel; nil fields stay. Settings replace all settings; Secrets
// replace the named secrets and keep the others (they are never shown, so never sent back).
type ChannelChange struct {
	Name     *string
	Lang     *Lang
	Enabled  *bool
	Settings map[string]string
	Secrets  map[string]string
}

// TypeInfo describes a channel type for the form.
type TypeInfo struct {
	Type   ChannelType
	Fields []Field
}

func (s *Service) authorize(c access.Caller, stepUp bool) (tenant.Scope, error) {
	p := c.Principal
	if !s.d.Policy.Allowed(p, authz.ManageChannels, authz.InOrg(p.Org)) {
		return tenant.Scope{}, access.ErrForbidden
	}
	// Channels receive alerts with security events: a hijacked session must not redirect them.
	if stepUp && s.d.Now().Sub(c.StepUpAt) > access.StepUpMaxAge {
		return tenant.Scope{}, access.ErrStepUpRequired
	}
	scope, err := tenant.ForAllGroups(p.Org)
	if err != nil {
		return tenant.Scope{}, access.ErrForbidden
	}
	return scope, nil
}

// Types returns the channel types with their fields.
func (s *Service) Types(c access.Caller) ([]TypeInfo, error) {
	if _, err := s.authorize(c, false); err != nil {
		return nil, err
	}
	var out []TypeInfo
	for _, n := range s.d.Registry.Types() {
		out = append(out, TypeInfo{Type: n.Type(), Fields: n.Fields()})
	}
	return out, nil
}

// List returns the organization's channels.
func (s *Service) List(ctx context.Context, c access.Caller) ([]Channel, error) {
	scope, err := s.authorize(c, false)
	if err != nil {
		return nil, err
	}
	stored, err := s.d.Store.Channels(ctx, scope)
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}
	out := make([]Channel, len(stored))
	for i, sc := range stored {
		out[i] = sc.Channel
	}
	return out, nil
}

// check validates a channel and returns its trimmed name.
func (s *Service) check(t ChannelType, name string, l Lang, cfg Config) (string, error) {
	n, ok := s.d.Registry.Get(t)
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownType, t)
	}
	name, err := checkName(name)
	if err != nil {
		return "", err
	}
	if err := checkLang(l); err != nil {
		return "", err
	}
	if err := checkConfig(n, cfg, s.d.Outbound); err != nil {
		return "", err
	}
	return name, nil
}

func secretNames(values map[string]string) []string {
	return slices.Sorted(maps.Keys(values))
}

// secretsFollow refuses a new address with the stored secrets: they would go to whoever runs the
// new target, readable by the admin who changed it (M2-28). All secrets come anew with it.
func (s *Service) secretsFollow(ch Channel, change ChannelChange) error {
	n, ok := s.d.Registry.Get(ch.Type)
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownType, ch.Type)
	}
	for _, f := range n.Fields() {
		if f.Kind != FieldURL || f.Secret || ch.Settings[f.Name] == change.Settings[f.Name] {
			continue
		}
		for _, name := range ch.SecretNames {
			if _, sent := change.Secrets[name]; !sent {
				return fmt.Errorf("%w: a new %s needs the secrets again (%s)", ErrInvalidChannel, f.Name, strings.Join(ch.SecretNames, ", "))
			}
		}
	}
	return nil
}

// Create adds a channel (step-up required).
func (s *Service) Create(ctx context.Context, c access.Caller, nc NewChannel) (Channel, error) {
	scope, err := s.authorize(c, true)
	if err != nil {
		return Channel{}, err
	}
	cfg := Config{Settings: cloneMap(nc.Settings), Secrets: cloneMap(nc.Secrets)}
	name, err := s.check(nc.Type, nc.Name, nc.Lang, cfg)
	if err != nil {
		return Channel{}, err
	}
	existing, err := s.d.Store.Channels(ctx, scope)
	if err != nil {
		return Channel{}, fmt.Errorf("count channels: %w", err)
	}
	if len(existing) >= MaxChannels {
		return Channel{}, ErrTooManyChannels
	}
	now := s.d.Now()
	ch := Channel{
		ID: ChannelID(s.d.NewID()), Type: nc.Type, Name: name, Lang: nc.Lang, Enabled: nc.Enabled,
		Settings: cfg.Settings, SecretNames: secretNames(cfg.Secrets), CreatedAt: now, UpdatedAt: now, CreatedBy: c.Principal.User,
	}
	sealed, err := sealSecrets(s.d.Box, c.Principal.Org, ch.ID, cfg.Secrets)
	if err != nil {
		return Channel{}, err
	}
	audit := access.AuditEntry{
		Actor: c.Principal.User, Action: "notification_channel.create", Target: "notification_channel:" + ch.ID.String(),
		Details: map[string]string{"type": string(ch.Type), "name": ch.Name},
	}
	if err := s.d.Store.CreateChannel(ctx, scope, StoredChannel{Channel: ch, SealedSecrets: sealed}, audit); err != nil {
		return Channel{}, fmt.Errorf("store channel: %w", err)
	}
	return ch, nil
}

// Update changes a channel (step-up required).
func (s *Service) Update(ctx context.Context, c access.Caller, id ChannelID, change ChannelChange) (Channel, error) {
	scope, err := s.authorize(c, true)
	if err != nil {
		return Channel{}, err
	}
	stored, err := s.d.Store.Channel(ctx, scope, id)
	if err != nil {
		return Channel{}, fmt.Errorf("load channel: %w", err)
	}
	open, err := OpenSecrets(s.d.Box, c.Principal.Org, stored)
	if err != nil {
		return Channel{}, err
	}
	ch := stored.Channel
	var changed []string
	if change.Name != nil {
		ch.Name, changed = *change.Name, append(changed, "name")
	}
	if change.Lang != nil {
		ch.Lang, changed = *change.Lang, append(changed, "language")
	}
	if change.Enabled != nil {
		ch.Enabled, changed = *change.Enabled, append(changed, "enabled")
	}
	if change.Settings != nil {
		if err := s.secretsFollow(ch, change); err != nil {
			return Channel{}, err
		}
		ch.Settings, changed = cloneMap(change.Settings), append(changed, "settings")
	}
	if len(change.Secrets) > 0 {
		maps.Copy(open, change.Secrets)
		changed = append(changed, "secrets")
	}
	cfg := Config{Settings: ch.Settings, Secrets: open}
	if ch.Name, err = s.check(ch.Type, ch.Name, ch.Lang, cfg); err != nil {
		return Channel{}, err
	}
	ch.SecretNames, ch.UpdatedAt = secretNames(open), s.d.Now()
	sealed, err := sealSecrets(s.d.Box, c.Principal.Org, ch.ID, open)
	if err != nil {
		return Channel{}, err
	}
	audit := access.AuditEntry{
		Actor: c.Principal.User, Action: "notification_channel.update", Target: "notification_channel:" + id.String(),
		Details: map[string]string{"changed": strings.Join(changed, ",")},
	}
	if err := s.d.Store.UpdateChannel(ctx, scope, StoredChannel{Channel: ch, SealedSecrets: sealed}, audit); err != nil {
		return Channel{}, fmt.Errorf("store channel: %w", err)
	}
	return ch, nil
}

// Delete removes a channel (step-up required). Its deliveries stay in the log.
func (s *Service) Delete(ctx context.Context, c access.Caller, id ChannelID) error {
	scope, err := s.authorize(c, true)
	if err != nil {
		return err
	}
	audit := access.AuditEntry{Actor: c.Principal.User, Action: "notification_channel.delete", Target: "notification_channel:" + id.String()}
	if err := s.d.Store.DeleteChannel(ctx, scope, id, audit); err != nil {
		return fmt.Errorf("delete channel: %w", err)
	}
	return nil
}

// Test sends a test message at once, without retries, and records it in the log; the returned
// delivery tells whether it arrived (LastError without secrets).
func (s *Service) Test(ctx context.Context, c access.Caller, id ChannelID) (Delivery, error) {
	scope, err := s.authorize(c, false)
	if err != nil {
		return Delivery{}, err
	}
	stored, err := s.d.Store.Channel(ctx, scope, id)
	if err != nil {
		return Delivery{}, fmt.Errorf("load channel: %w", err)
	}
	if !s.tests.Allow(c.Principal.Org.String()) {
		return Delivery{}, ErrTooManyTests
	}
	now := s.d.Now()
	d := Delivery{Channel: id, Notification: TestNotification(now), Test: true, Status: StatusSending, Attempts: 1, CreatedAt: now, NextAttemptAt: now}
	sendErr := s.send.deliver(ctx, c.Principal.Org, stored, d.Notification)
	d.LastAttemptAt = s.d.Now()
	if sendErr != nil {
		d.Status, d.LastError = StatusFailed, sendErr.text
		s.d.Logger.WarnContext(ctx, "notify: test failed", "org", c.Principal.Org.String(), "channel", id.String(), "error", sendErr.text)
	} else {
		d.Status, d.SentAt = StatusSent, d.LastAttemptAt
	}
	if err := s.d.Store.Enqueue(ctx, scope, []Delivery{d}); err != nil {
		return Delivery{}, fmt.Errorf("record test delivery: %w", err)
	}
	return d, nil
}

// MaxDeliveries bounds a page of the delivery log.
const MaxDeliveries = 200

// Deliveries returns the delivery log, newest first.
func (s *Service) Deliveries(ctx context.Context, c access.Caller, f DeliveryFilter) ([]Delivery, error) {
	scope, err := s.authorize(c, false)
	if err != nil {
		return nil, err
	}
	if f.Limit <= 0 || f.Limit > MaxDeliveries {
		f.Limit = MaxDeliveries
	}
	list, err := s.d.Store.Deliveries(ctx, scope, f)
	if err != nil {
		return nil, fmt.Errorf("list deliveries: %w", err)
	}
	return list, nil
}

func cloneMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return maps.Clone(m)
}
