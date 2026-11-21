//go:build integration

package webapi_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
)

func TestSilenceAPI_OperatorsSilenceTheirGroups(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	seedChannelAlpha(t)
	ctx := context.Background()

	// Mia is a viewer of Alpha's group: she sees its silence but may not add one.
	mia := login(t, s, "mia@example.org")
	list, err := mia.silences.ListSilences(ctx, connect.NewRequest(&webv1.ListSilencesRequest{}))
	if err != nil || len(list.Msg.GetSilences()) != 1 || list.Msg.GetSilences()[0].GetSpec().GetComment() != "alpha-silence" {
		t.Fatalf("ListSilences() = %v, %v", list, err)
	}
	spec := &webv1.SilenceSpec{
		Matchers: &webv1.PolicyMatchers{GroupIds: []string{groupAl}}, NodeIds: []string{nodeAl},
		EndTime: timestamppb.New(time.Now().Add(2 * time.Hour)), Comment: "Kernel update",
	}
	if _, err := mia.silences.CreateSilence(ctx, connect.NewRequest(&webv1.CreateSilenceRequest{Spec: spec})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("viewer creates a silence: %v", err)
	}

	// As an operator of the group she silences its node, but not all groups.
	ownerSQL(t, "UPDATE role_bindings SET role = 'operator' WHERE id = '"+bindingAl+"'")
	t.Cleanup(func() { ownerSQL(t, "UPDATE role_bindings SET role = 'viewer' WHERE id = '"+bindingAl+"'") })
	mia = login(t, s, "mia@example.org")
	created, err := mia.silences.CreateSilence(ctx, connect.NewRequest(&webv1.CreateSilenceRequest{Spec: spec}))
	if err != nil {
		t.Fatal(err)
	}
	got := created.Msg.GetSilence()
	if got.GetId() == "" || got.GetSpec().GetStartTime() == nil || got.GetSpec().GetNodeIds()[0] != nodeAl || got.GetCreatedBy() != miaID {
		t.Errorf("created %v", got)
	}
	everywhere := &webv1.SilenceSpec{Matchers: &webv1.PolicyMatchers{Rules: []string{"security.*"}}, EndTime: spec.GetEndTime(), Comment: "noisy"}
	if _, err := mia.silences.CreateSilence(ctx, connect.NewRequest(&webv1.CreateSilenceRequest{Spec: everywhere})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("operator of one group silences all groups: %v", err)
	}
	tooLong := &webv1.SilenceSpec{Matchers: spec.GetMatchers(), EndTime: timestamppb.New(time.Now().Add(31 * 24 * time.Hour)), Comment: "x"}
	if _, err := mia.silences.CreateSilence(ctx, connect.NewRequest(&webv1.CreateSilenceRequest{Spec: tooLong})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("31 days: %v", err)
	}
	ownerSQL(t, "UPDATE sessions SET step_up_at = now() - interval '1 hour' WHERE user_id = '"+miaID+"'")
	if _, err := mia.silences.CreateSilence(ctx, connect.NewRequest(&webv1.CreateSilenceRequest{Spec: spec})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("create with an old login: %v, want FAILED_PRECONDITION", err)
	}
	// Ending needs no step-up; twice is not found.
	if _, err := mia.silences.ExpireSilence(ctx, connect.NewRequest(&webv1.ExpireSilenceRequest{SilenceId: got.GetId()})); err != nil {
		t.Errorf("expire: %v", err)
	}
	if _, err := mia.silences.ExpireSilence(ctx, connect.NewRequest(&webv1.ExpireSilenceRequest{SilenceId: got.GetId()})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("second expire: %v", err)
	}
}

func TestMaintenanceWindowAPI_AdminsManageWindows(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	seedChannelAlpha(t)
	ctx := context.Background()
	ann := login(t, s, "ann@example.org")
	spec := &webv1.MaintenanceWindowSpec{
		Name: "Patches", Enabled: true, GroupIds: []string{groupAl},
		Days: []webv1.Weekday{webv1.Weekday_WEEKDAY_SUNDAY, webv1.Weekday_WEEKDAY_WEDNESDAY}, StartMinute: 180, DurationMinutes: 120, TimeZone: "Europe/Berlin",
	}
	created, err := ann.windows.CreateMaintenanceWindow(ctx, connect.NewRequest(&webv1.CreateMaintenanceWindowRequest{Spec: spec}))
	if err != nil {
		t.Fatal(err)
	}
	w := created.Msg.GetWindow()
	if w.GetSpec().GetDays()[0] != webv1.Weekday_WEEKDAY_SUNDAY || w.GetSpec().GetStartMinute() != 180 || w.GetSpec().GetTimeZone() != "Europe/Berlin" {
		t.Errorf("created %v", w)
	}
	spec.TimeZone = "Mars/Olympus"
	if _, err := ann.windows.UpdateMaintenanceWindow(ctx, connect.NewRequest(&webv1.UpdateMaintenanceWindowRequest{WindowId: w.GetId(), Spec: spec})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("unknown time zone: %v", err)
	}
	list, err := ann.windows.ListMaintenanceWindows(ctx, connect.NewRequest(&webv1.ListMaintenanceWindowsRequest{}))
	if err != nil || len(list.Msg.GetWindows()) != 2 {
		t.Errorf("ListMaintenanceWindows() = %v, %v", list, err)
	}
	if _, err := login(t, s, "mia@example.org").windows.ListMaintenanceWindows(ctx, connect.NewRequest(&webv1.ListMaintenanceWindowsRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("viewer lists windows: %v", err)
	}
	if _, err := ann.windows.DeleteMaintenanceWindow(ctx, connect.NewRequest(&webv1.DeleteMaintenanceWindowRequest{WindowId: w.GetId()})); err != nil {
		t.Errorf("delete: %v", err)
	}
}
