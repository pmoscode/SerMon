//go:build integration

package webapi_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/pgtest"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
)

func TestAlertAPI_ListHistoryAndAcknowledge(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	seedChannelAlpha(t)
	ctx := context.Background()
	key := &webv1.AlertKey{RuleId: "security.ssh.bruteforce", NodeId: nodeAl, Dimension: "ip=192.0.2.7"}

	// Mia (viewer of the group) sees the alert with its fields, but may not acknowledge it.
	mia := login(t, s, "mia@example.org")
	list, err := mia.alerts.ListAlerts(ctx, connect.NewRequest(&webv1.ListAlertsRequest{}))
	if err != nil || len(list.Msg.GetAlerts()) != 1 {
		t.Fatalf("ListAlerts() = %v, %v", list, err)
	}
	a := list.Msg.GetAlerts()[0]
	if a.GetState() != webv1.AlertState_ALERT_STATE_CRITICAL || a.GetHostname() != "alpha-01" || a.GetFields()["user"] != "alpha-user" ||
		a.GetDimensionLabels()["ip"] != "192.0.2.7" || a.GetCanAcknowledge() || a.GetRuleName().GetEn() != "security.ssh.bruteforce" ||
		!a.GetMuted() {
		t.Errorf("alert %v", a)
	}
	if _, err := mia.alerts.AcknowledgeAlert(ctx, connect.NewRequest(&webv1.AcknowledgeAlertRequest{Key: key})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("viewer acknowledges: %v", err)
	}
	history, err := mia.alerts.GetAlertHistory(ctx, connect.NewRequest(&webv1.GetAlertHistoryRequest{Key: key}))
	if err != nil || len(history.Msg.GetEntries()) != 1 || history.Msg.GetEntries()[0].GetTo() != webv1.AlertState_ALERT_STATE_CRITICAL {
		t.Fatalf("GetAlertHistory() = %v, %v", history, err)
	}

	// Alpha's silence mutes the group; without it the channel hears of the acknowledgement.
	ownerSQL(t, "DELETE FROM alert_silences WHERE id = '"+silenceAl+"'")

	// Ann (admin) acknowledges; a security alert needs a recent step-up.
	ann := login(t, s, "ann@example.org")
	ownerSQL(t, "UPDATE sessions SET step_up_at = now() - interval '1 hour' WHERE user_id = '"+annID+"'")
	if _, err := ann.alerts.AcknowledgeAlert(ctx, connect.NewRequest(&webv1.AcknowledgeAlertRequest{Key: key})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("acknowledge with an old login: %v", err)
	}
	ann = login(t, s, "ann@example.org")
	if _, err := ann.alerts.AcknowledgeAlert(ctx, connect.NewRequest(&webv1.AcknowledgeAlertRequest{Key: key})); err != nil {
		t.Fatal(err)
	}
	if _, err := ann.alerts.AcknowledgeAlert(ctx, connect.NewRequest(&webv1.AcknowledgeAlertRequest{Key: key})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("second acknowledgement: %v", err)
	}
	list, _ = ann.alerts.ListAlerts(ctx, connect.NewRequest(&webv1.ListAlertsRequest{}))
	if a = list.Msg.GetAlerts()[0]; a.GetAcknowledgedBy() == "" || a.GetAcknowledgeTime() == nil || a.GetCanAcknowledge() {
		t.Errorf("acknowledged alert %v", a)
	}
	history, _ = ann.alerts.GetAlertHistory(ctx, connect.NewRequest(&webv1.GetAlertHistoryRequest{Key: key}))
	if e := history.Msg.GetEntries()[0]; e.GetKind() != webv1.AlertHistoryKind_ALERT_HISTORY_KIND_ACKNOWLEDGED || e.GetBy() == "" {
		t.Errorf("history %v", history.Msg.GetEntries())
	}
	// Without policies the enabled channel of Alpha hears of it.
	var queued int
	ownerQuery(t, "SELECT count(*) FROM notification_deliveries WHERE org_id = '"+orgAlpha+"' AND notification->>'kind' = 'acknowledged'", &queued)
	if queued != 1 {
		t.Errorf("%d acknowledgement notifications queued", queued)
	}
}

// ownerQuery reads one value as the owner (bypasses RLS).
func ownerQuery(t *testing.T, sql string, dest any) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, pgtest.Shared(t).OwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if err := conn.QueryRow(ctx, sql).Scan(dest); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}
