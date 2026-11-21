//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
)

var notifyTime = time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)

func newNotifyOrg(t *testing.T) (*postgres.NotificationStore, uuid.UUID, string) {
	t.Helper()
	f := setup(t)
	org := uuid.New()
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO organizations (id, name) VALUES ($1, 'Notify')", org)
	return postgres.NewNotificationStore(open(t, f.appDSN)), org, f.db.OwnerDSN
}

func storedChannel() notify.StoredChannel {
	return notify.StoredChannel{
		Channel: notify.Channel{
			ID: notify.ChannelID(uuid.New()), Type: "webhook", Name: "Ops", Lang: notify.German, Enabled: true,
			Settings: map[string]string{"url": "https://hooks.example/x"}, SecretNames: []string{"secret"},
			CreatedAt: notifyTime, UpdatedAt: notifyTime,
		},
		SealedSecrets: []byte("sealed-bytes"),
	}
}

func audit(action string) access.AuditEntry {
	return access.AuditEntry{Action: action, Target: "notification_channel:x", Details: map[string]string{"type": "webhook"}}
}

func TestNotificationStore_ChannelsRoundTripWithAudit(t *testing.T) {
	store, org, ownerDSN := newNotifyOrg(t)
	ctx := context.Background()
	scope := scopeFor(t, org)
	c := storedChannel()
	if err := store.CreateChannel(ctx, scope, c, audit("notification_channel.create")); err != nil {
		t.Fatal(err)
	}
	got, err := store.Channel(ctx, scope, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Ops" || got.Type != "webhook" || got.Lang != notify.German || !got.Enabled || got.Settings["url"] != "https://hooks.example/x" ||
		len(got.SecretNames) != 1 || string(got.SealedSecrets) != "sealed-bytes" || !got.CreatedAt.Equal(notifyTime) {
		t.Errorf("channel = %+v", got)
	}
	c.Name, c.Enabled, c.UpdatedAt = "Ops 2", false, notifyTime.Add(time.Hour)
	if err := store.UpdateChannel(ctx, scope, c, audit("notification_channel.update")); err != nil {
		t.Fatal(err)
	}
	list, err := store.Channels(ctx, scope)
	if err != nil || len(list) != 1 || list[0].Name != "Ops 2" || list[0].Enabled || !list[0].UpdatedAt.Equal(notifyTime.Add(time.Hour)) {
		t.Errorf("Channels() = %+v, %v", list, err)
	}
	if err := store.DeleteChannel(ctx, scope, c.ID, audit("notification_channel.delete")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Channel(ctx, scope, c.ID); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("deleted channel: %v", err)
	}
	var audits int
	queryOwner(t, ownerDSN, "SELECT count(*) FROM audit_log WHERE org_id = $1 AND action LIKE 'notification_channel.%'", []any{org}, &audits)
	if audits != 3 {
		t.Errorf("%d audit entries, want 3", audits)
	}
}

func TestNotificationStore_KeepsOrganizationsApart(t *testing.T) {
	store, org, _ := newNotifyOrg(t)
	other, otherOrg, _ := newNotifyOrg(t)
	ctx := context.Background()
	c := storedChannel()
	if err := store.CreateChannel(ctx, scopeFor(t, org), c, audit("notification_channel.create")); err != nil {
		t.Fatal(err)
	}
	if err := store.Enqueue(ctx, scopeFor(t, org), []notify.Delivery{{Channel: c.ID, Status: notify.StatusPending, CreatedAt: notifyTime, NextAttemptAt: notifyTime}}); err != nil {
		t.Fatal(err)
	}
	foreign := scopeFor(t, otherOrg)
	if _, err := other.Channel(ctx, foreign, c.ID); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("foreign channel: %v", err)
	}
	if list, _ := other.Channels(ctx, foreign); len(list) != 0 {
		t.Errorf("foreign channels: %+v", list)
	}
	c.Name = "hijacked"
	if err := other.UpdateChannel(ctx, foreign, c, audit("notification_channel.update")); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("update foreign: %v", err)
	}
	if err := other.DeleteChannel(ctx, foreign, c.ID, audit("notification_channel.delete")); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("delete foreign: %v", err)
	}
	if list, _ := other.Deliveries(ctx, foreign, notify.DeliveryFilter{Limit: 10}); len(list) != 0 {
		t.Errorf("foreign deliveries: %+v", list)
	}
	if claimed, _ := other.Claim(ctx, foreign, notifyTime, notifyTime.Add(time.Minute), 10); len(claimed) != 0 {
		t.Errorf("claimed foreign deliveries: %+v", claimed)
	}
	if got, _ := store.Channel(ctx, scopeFor(t, org), c.ID); got.Name != "Ops" {
		t.Errorf("channel changed by another organization: %+v", got)
	}
}

func TestNotificationStore_QueueClaimFinishAndPurge(t *testing.T) {
	store, org, _ := newNotifyOrg(t)
	ctx := context.Background()
	scope := scopeFor(t, org)
	c := storedChannel()
	if err := store.CreateChannel(ctx, scope, c, audit("notification_channel.create")); err != nil {
		t.Fatal(err)
	}
	n := notify.Notification{
		Kind: notify.Firing, Severity: notify.Critical, Rule: "security.ssh.bruteforce", RuleName: notify.Text{De: "SSH", En: "SSH"},
		Node: identity.NodeID(uuid.New()), Hostname: "web-01", Value: 12, Unit: notify.UnitCount, At: notifyTime,
		Fields: alert.Fields{"user": "root", "ip": "192.0.2.7"},
	}
	var batch []notify.Delivery
	for i := range 3 {
		batch = append(batch, notify.Delivery{Channel: c.ID, Notification: n, Status: notify.StatusPending, CreatedAt: notifyTime, NextAttemptAt: notifyTime.Add(time.Duration(i) * time.Minute)})
	}
	if err := store.Enqueue(ctx, scope, batch); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.Claim(ctx, scope, notifyTime.Add(90*time.Second), notifyTime.Add(5*time.Minute), 10)
	if err != nil || len(claimed) != 2 {
		t.Fatalf("Claim() = %d, %v; want the two due ones", len(claimed), err)
	}
	d := claimed[0]
	if d.Status != notify.StatusSending || d.Attempts != 1 || !d.ClaimedUntil.Equal(notifyTime.Add(5*time.Minute)) ||
		d.Notification.Fields["user"] != "root" || d.Notification.Hostname != "web-01" || d.Channel != c.ID {
		t.Errorf("claimed = %+v", d)
	}
	// Claimed ones are not claimed again while the claim lasts.
	if again, _ := store.Claim(ctx, scope, notifyTime.Add(2*time.Minute), notifyTime.Add(5*time.Minute), 10); len(again) != 1 {
		t.Errorf("second claim got %d, want only the third", len(again))
	}
	d.Status, d.SentAt, d.LastAttemptAt = notify.StatusSent, notifyTime.Add(time.Minute), notifyTime.Add(time.Minute)
	if err := store.Finish(ctx, scope, d); err != nil {
		t.Fatal(err)
	}
	f := claimed[1]
	f.Status, f.LastError, f.NextAttemptAt = notify.StatusPending, "connection reset", notifyTime.Add(10*time.Minute)
	if err := store.Finish(ctx, scope, f); err != nil {
		t.Fatal(err)
	}
	list, err := store.Deliveries(ctx, scope, notify.DeliveryFilter{Channel: &c.ID, Limit: 10})
	if err != nil || len(list) != 3 || list[0].ID < list[2].ID {
		t.Fatalf("Deliveries() = %+v, %v; want three, newest first", list, err)
	}
	byID := map[int64]notify.Delivery{}
	for _, x := range list {
		byID[x.ID] = x
	}
	if got := byID[d.ID]; got.Status != notify.StatusSent || !got.SentAt.Equal(d.SentAt) || !got.ClaimedUntil.IsZero() {
		t.Errorf("finished = %+v", got)
	}
	if got := byID[f.ID]; got.Status != notify.StatusPending || got.LastError != "connection reset" || !got.NextAttemptAt.Equal(f.NextAttemptAt) {
		t.Errorf("retry = %+v", got)
	}
	if page, _ := store.Deliveries(ctx, scope, notify.DeliveryFilter{Before: list[0].ID, Limit: 1}); len(page) != 1 || page[0].ID != list[1].ID {
		t.Errorf("page = %+v", page)
	}
	// Purge removes finished deliveries before the time, never open ones.
	if n, err := store.Purge(ctx, scope, notifyTime.Add(time.Hour)); err != nil || n != 1 {
		t.Errorf("Purge() = %d, %v; want the sent one", n, err)
	}
}

// Server instances claim concurrently; each delivery goes to exactly one of them.
func TestNotificationStore_ConcurrentClaimsDoNotOverlap(t *testing.T) {
	store, org, _ := newNotifyOrg(t)
	ctx := context.Background()
	scope := scopeFor(t, org)
	c := storedChannel()
	if err := store.CreateChannel(ctx, scope, c, audit("notification_channel.create")); err != nil {
		t.Fatal(err)
	}
	var batch []notify.Delivery
	for range 40 {
		batch = append(batch, notify.Delivery{Channel: c.ID, Status: notify.StatusPending, CreatedAt: notifyTime, NextAttemptAt: notifyTime})
	}
	if err := store.Enqueue(ctx, scope, batch); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	seen := map[int64]int{}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			got, err := store.Claim(ctx, scope, notifyTime, notifyTime.Add(time.Minute), 10)
			if err != nil {
				t.Error(err)
			}
			mu.Lock()
			defer mu.Unlock()
			for _, d := range got {
				seen[d.ID]++
			}
		})
	}
	wg.Wait()
	if len(seen) != 40 {
		t.Errorf("%d deliveries claimed, want 40", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("delivery %d claimed %d times", id, n)
		}
	}
}

// The tables have row-level security and hold the secrets sealed only.
func TestNotificationStore_TablesHaveRLSAndNoPlainSecrets(t *testing.T) {
	_, _, ownerDSN := newNotifyOrg(t)
	for _, table := range []string{"notification_channels", "notification_deliveries"} {
		var enabled, forced bool
		queryOwner(t, ownerDSN, "SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE relname = $1", []any{table}, &enabled, &forced)
		if !enabled || !forced {
			t.Errorf("%s: RLS enabled %v, forced %v", table, enabled, forced)
		}
	}
	// Without a tenant the application sees nothing.
	f := setup(t)
	conn, err := pgx.Connect(context.Background(), f.appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	for _, table := range []string{"notification_channels", "notification_deliveries"} {
		var n int
		if err := conn.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s without tenant: %d rows, %v", table, n, err)
		}
	}
	var columns string
	queryOwner(t, ownerDSN, "SELECT string_agg(column_name, ',') FROM information_schema.columns WHERE table_name = 'notification_channels'", nil, &columns)
	if strings.Contains(columns, "token") || strings.Contains(columns, "password") || !strings.Contains(columns, "sealed_secrets") {
		t.Errorf("columns %s", columns)
	}
}

// queryOwner reads one row as the schema owner (bypasses RLS).
func queryOwner(t *testing.T, dsn, sql string, args []any, dest ...any) {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	if err := conn.QueryRow(context.Background(), sql, args...).Scan(dest...); err != nil {
		t.Fatal(err)
	}
}
