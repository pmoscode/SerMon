//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/domain/retention"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// seedAges gives the organization's node a security event and an alert history entry for each age.
func seedAges(t *testing.T, ownerDSN string, org, node uuid.UUID, ages ...time.Duration) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	for _, age := range ages {
		at := time.Now().Add(-age)
		for _, sql := range []string{
			`INSERT INTO security_events (org_id, node_id, occurred_at, kind, user_name) VALUES ($1, $2, $3, 'ssh.login.failed', 'alice')`,
			`INSERT INTO alert_history (org_id, rule_id, node_id, kind, from_state, to_state, at, value) VALUES ($1, 'security.ssh.bruteforce', $2, 1, 0, 2, $3, 1)`,
		} {
			if _, err := conn.Exec(ctx, sql, org, node, at); err != nil {
				t.Fatalf("%s: %v", sql, err)
			}
		}
	}
}

// ages returns the days of the organization's events and alert history, oldest first.
func ages(t *testing.T, conn *pgx.Conn, table, column string, org uuid.UUID) []int {
	t.Helper()
	rows, err := conn.Query(context.Background(),
		"SELECT floor(extract(epoch FROM now() - "+column+") / 86400)::int FROM "+table+" WHERE org_id = $1 ORDER BY 1 DESC", org)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[int])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func equal(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// M5-30: an organization's own retention deletes its events and alert history after its period,
// not earlier and never those of the last 7 days, and nothing of other organizations; the
// settings round-trip with their audit entry and the job finds them.
func TestRetention_ExpiresEachOrganizationAfterItsPeriod(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	a := seedPurgeOrg(t, f.db.OwnerDSN, uuid.Nil)
	b := seedPurgeOrg(t, f.db.OwnerDSN, uuid.Nil)
	day := 24 * time.Hour
	seedAges(t, f.db.OwnerDSN, a.org, a.node, 45*day, 25*day+time.Hour, 3*day+time.Hour)
	seedAges(t, f.db.OwnerDSN, b.org, b.node, 45*day)
	owner, err := pgx.Connect(ctx, f.db.OwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close(ctx) }()
	before := ages(t, owner, "security_events", "occurred_at", b.org)

	store := postgres.NewRetentionStore(open(t, f.appDSN))
	scopeA, _ := tenant.ForAllGroups(tenant.OrgID(a.org))
	if set, capDays, err := store.Get(ctx, scopeA); err != nil || set != (retention.Settings{}) || capDays != 0 {
		t.Fatalf("Get = %v, %d, %v", set, capDays, err)
	}
	if err := store.Set(ctx, scopeA, retention.Settings{LogDays: 3, EventDays: 30}, access.AuditEntry{Action: "org.retention_set", Target: "org:" + a.org.String()}); err != nil {
		t.Fatal(err)
	}
	if set, _, err := store.Get(ctx, scopeA); err != nil || set != (retention.Settings{LogDays: 3, EventDays: 30}) {
		t.Fatalf("after Set: %v, %v", set, err)
	}
	configured, err := store.Configured(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range configured {
		if o.Org == tenant.OrgID(b.org) {
			t.Error("an organization with the defaults is listed")
		}
		if o.Org == tenant.OrgID(a.org) {
			found = o.Settings == (retention.Settings{LogDays: 3, EventDays: 30}) && o.LogsErasedAt == nil
		}
	}
	if !found {
		t.Fatalf("configured = %+v, want A", configured)
	}

	// 30 days: the event of 45 days goes, those of 25 and 3 days stay.
	if _, err := store.ExpireEvents(ctx, tenant.OrgID(a.org), time.Now().Add(-30*day)); err != nil {
		t.Fatal(err)
	}
	// seedPurgeOrg adds an event of now.
	if got := ages(t, owner, "security_events", "occurred_at", a.org); !equal(got, []int{25, 3, 0}) {
		t.Errorf("events of A after 30 days: %v, want [25 3 0]", got)
	}
	if got := ages(t, owner, "alert_history", "at", a.org); !equal(got, []int{25, 3}) {
		t.Errorf("alert history of A after 30 days: %v, want [25 3]", got)
	}
	// Whatever the caller asks, the last 7 days stay.
	if _, err := store.ExpireEvents(ctx, tenant.OrgID(a.org), time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := ages(t, owner, "security_events", "occurred_at", a.org); !equal(got, []int{3, 0}) {
		t.Errorf("events of A after expiring now: %v, want [3 0]", got)
	}
	if got := ages(t, owner, "security_events", "occurred_at", b.org); !equal(got, before) {
		t.Errorf("events of B: %v, had %v", got, before)
	}
	if got := ages(t, owner, "alert_history", "at", b.org); !equal(got, []int{45}) {
		t.Errorf("alert history of B: %v, want [45]", got)
	}

	at := time.Now().Truncate(time.Second)
	if err := store.MarkLogsErased(ctx, tenant.OrgID(a.org), at); err != nil {
		t.Fatal(err)
	}
	configured, _ = store.Configured(ctx)
	for _, o := range configured {
		if o.Org == tenant.OrgID(a.org) && (o.LogsErasedAt == nil || !o.LogsErasedAt.Equal(at)) {
			t.Errorf("logs erased at = %v, want %v", o.LogsErasedAt, at)
		}
	}
	// The checks of the table hold even if the application didn't.
	ownerExecErr(t, f.db.OwnerDSN, "UPDATE organizations SET log_retention_days = 15 WHERE id = $1", a.org)
	ownerExecErr(t, f.db.OwnerDSN, "UPDATE organizations SET event_retention_days = 6 WHERE id = $1", a.org)
}
