//go:build integration

package webapi_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
)

// M5-30: an admin of the organization sets its retention with a fresh step-up, within the
// instance's limits and the superadmin's cap; a viewer may not; Beta keeps its own; the change is
// audited.
func TestAccessAPI_RetentionPerOrganization(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ann := login(t, s, "ann@example.org") // admin of Alpha
	mia := login(t, s, "mia@example.org") // viewer of Alpha's group
	bea := login(t, s, "bea@example.org") // admin of Beta
	ctx := context.Background()
	t.Cleanup(func() {
		ownerSQL(t, "UPDATE organizations SET event_retention_days = 77, log_retention_days = NULL, retention_cap_days = NULL WHERE id = '"+orgAlpha+"'")
	})

	got, err := ann.access.GetRetention(ctx, connect.NewRequest(&webv1.GetRetentionRequest{}))
	if err != nil || got.Msg.GetEventRetentionDays() != 77 || got.Msg.GetLogRetentionDays() != 0 || got.Msg.GetLogMaxDays() != 14 ||
		got.Msg.GetEventMinDays() != 7 || got.Msg.GetEventMaxDays() != 365 || got.Msg.GetCapDays() != 0 ||
		got.Msg.GetEventDefaultDays() != 90 {
		t.Fatalf("GetRetention = %v, %v", got, err)
	}
	if _, err := mia.access.GetRetention(ctx, connect.NewRequest(&webv1.GetRetentionRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("viewer reads: %v", err)
	}
	set := func(c loggedIn, logs, events uint32) error {
		_, err := c.access.SetRetention(ctx, connect.NewRequest(&webv1.SetRetentionRequest{LogRetentionDays: logs, EventRetentionDays: events}))
		return err
	}
	if err := set(mia, 3, 30); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("viewer sets: %v", err)
	}
	if err := set(ann, 3, 6); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("events below the limit: %v", err)
	}
	if err := set(ann, 15, 30); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("logs beyond the instance: %v", err)
	}
	ownerSQL(t, "UPDATE organizations SET retention_cap_days = 60 WHERE id = '"+orgAlpha+"'")
	if err := set(ann, 3, 90); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("events above the cap: %v", err)
	}
	if err := set(ann, 3, 30); err != nil {
		t.Fatal(err)
	}
	got, err = ann.access.GetRetention(ctx, connect.NewRequest(&webv1.GetRetentionRequest{}))
	if err != nil || got.Msg.GetLogRetentionDays() != 3 || got.Msg.GetEventRetentionDays() != 30 || got.Msg.GetCapDays() != 60 {
		t.Errorf("after the change: %v, %v", got, err)
	}
	if b, err := bea.access.GetRetention(ctx, connect.NewRequest(&webv1.GetRetentionRequest{})); err != nil || b.Msg.GetLogRetentionDays() != 0 || b.Msg.GetEventRetentionDays() != 0 {
		t.Errorf("Beta = %v, %v; want the defaults", b, err)
	}
	audit, err := ann.access.ListAuditLog(ctx, connect.NewRequest(&webv1.ListAuditLogRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range audit.Msg.GetEntries() {
		if e.GetAction() == "org.retention_set" && e.GetDetails()["retention"] == "logs=3d,events=30d" && e.GetDetails()["previous"] == "logs=default,events=77d" {
			found = true
		}
	}
	if !found {
		t.Error("the change is not in the audit log")
	}
	if err := set(ann, 0, 0); err != nil {
		t.Errorf("back to the defaults: %v", err)
	}
	ownerSQL(t, "UPDATE sessions SET step_up_at = now() - interval '1 hour' WHERE user_id = '"+annID+"'")
	if err := set(ann, 3, 30); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("old login: %v, want FAILED_PRECONDITION", err)
	}
}
