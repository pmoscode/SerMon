//go:build integration

package webapi_test

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"

	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
)

// The seeded report of Alpha's node (seedAccessFixtures, M4-09): visible to the viewer of its
// group and to an admin on all groups, with its package; not to the admin of another organization.
func TestUpdates_ShownForTheCallersGroups(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ctx := context.Background()
	for _, email := range []string{"mia@example.org", "ann@example.org"} {
		l := login(t, s, email)
		res, err := l.updates.GetNodeUpdates(ctx, connect.NewRequest(&webv1.GetNodeUpdatesRequest{NodeId: nodeAl}))
		if err != nil {
			t.Fatalf("%s: %v", email, err)
		}
		n, packages := res.Msg.GetNode(), res.Msg.GetPackages()
		if n.GetStatus() != webv1.UpdateStatus_UPDATE_STATUS_SECURITY || n.GetHostname() != "alpha-01" || n.GetGroupId() != groupAl ||
			n.GetAvailability() != webv1.UpdateAvailability_UPDATE_AVAILABILITY_LISTED || n.GetPackageManager() != webv1.PackageManager_PACKAGE_MANAGER_APT ||
			n.GetSecurityCount() != 1 || !n.GetRebootRequired() || n.GetOldestSecuritySince() != nil || n.GetPermissions() != nil {
			t.Errorf("%s: node = %v", email, n)
		}
		if len(packages) != 1 || packages[0].GetName() != "alpha-openssl" || !packages[0].GetSecurity() || packages[0].GetFirstSeenAt() == nil {
			t.Errorf("%s: packages = %v", email, packages)
		}
		fleet, err := l.updates.ListFleetUpdates(ctx, connect.NewRequest(&webv1.ListFleetUpdatesRequest{}))
		if err != nil {
			t.Fatalf("%s: %v", email, err)
		}
		// Other tests enroll nodes into Alpha's group, without reports.
		m := fleet.Msg
		var alpha *webv1.NodeUpdates
		for _, n := range m.GetNodes() {
			if n.GetNodeId() == nodeAl {
				alpha = n
			}
		}
		if alpha.GetStatus() != webv1.UpdateStatus_UPDATE_STATUS_SECURITY || m.GetWithSecurity() != 1 || m.GetRebootRequired() != 1 ||
			int(m.GetUnknown()+m.GetCurrent()+m.GetWithUpdates()+m.GetUnavailable()+m.GetWithSecurity()) != len(m.GetNodes()) {
			t.Errorf("%s: fleet = %v", email, m)
		}
	}
	bea := login(t, s, "bea@example.org")
	if _, err := bea.updates.GetNodeUpdates(ctx, connect.NewRequest(&webv1.GetNodeUpdatesRequest{NodeId: nodeAl})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("admin of Beta: %v, want NOT_FOUND", err)
	}
	if _, err := bea.updates.GetNodeUpdates(ctx, connect.NewRequest(&webv1.GetNodeUpdatesRequest{NodeId: "not-a-uuid"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("a bad ID: %v, want INVALID_ARGUMENT", err)
	}
}

// Update policies (M4-10): Ann (admin of Alpha) sets, exports and imports the policy of Alpha's
// group; Mia (viewer of the group) reads it but can't change it; Bea of Beta sees nothing.
func TestUpdatePolicies_AdminsSetViewersRead(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ctx := context.Background()
	ann, mia, bea := login(t, s, "ann@example.org"), login(t, s, "mia@example.org"), login(t, s, "bea@example.org")
	spec := &webv1.UpdatePolicySpec{
		Mode: webv1.UpdateMode_UPDATE_MODE_SECURITY_AUTO, AutoReboot: true, MaxParallel: 2, StopOnFailure: true, HealthCheckMinutes: 15,
		Window: &webv1.UpdateWindow{Days: []webv1.Weekday{webv1.Weekday_WEEKDAY_SUNDAY}, StartMinute: 180, DurationMinutes: 120, TimeZone: "Europe/Berlin"},
	}
	set, err := ann.updatePolicies.SetUpdatePolicy(ctx, connect.NewRequest(&webv1.SetUpdatePolicyRequest{GroupId: groupAl, Spec: spec}))
	if err != nil {
		t.Fatal(err)
	}
	if p := set.Msg.GetPolicy(); p.GetIsDefault() || p.GetUpdateTime() == nil || p.GetSpec().GetMaxParallel() != 2 || !p.GetSpec().GetAutoReboot() ||
		!p.GetSpec().GetStopOnFailure() || p.GetSpec().GetHealthCheckMinutes() != 15 || p.GetSpec().GetWindow().GetStartMinute() != 180 {
		t.Errorf("set = %v", p)
	}
	got, err := mia.updatePolicies.GetUpdatePolicy(ctx, connect.NewRequest(&webv1.GetUpdatePolicyRequest{GroupId: groupAl}))
	if err != nil || got.Msg.GetPolicy().GetSpec().GetWindow().GetTimeZone() != "Europe/Berlin" ||
		got.Msg.GetPolicy().GetSpec().GetMode() != webv1.UpdateMode_UPDATE_MODE_SECURITY_AUTO {
		t.Errorf("Mia reads %v, %v", got, err)
	}
	list, err := mia.updatePolicies.ListUpdatePolicies(ctx, connect.NewRequest(&webv1.ListUpdatePoliciesRequest{}))
	if err != nil || len(list.Msg.GetPolicies()) != 1 || list.Msg.GetPolicies()[0].GetGroupName() != "alpha-web" {
		t.Errorf("Mia lists %v, %v", list, err)
	}
	if _, err := mia.updatePolicies.SetUpdatePolicy(ctx, connect.NewRequest(&webv1.SetUpdatePolicyRequest{GroupId: groupAl, Spec: spec})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("a viewer sets: %v", err)
	}
	exported, err := ann.updatePolicies.ExportUpdatePolicy(ctx, connect.NewRequest(&webv1.ExportUpdatePolicyRequest{GroupId: groupAl}))
	if err != nil || !strings.Contains(exported.Msg.GetYaml(), "mode: security-auto") {
		t.Fatalf("export = %v, %v", exported, err)
	}
	changed := strings.Replace(exported.Msg.GetYaml(), "maxParallel: 2", "maxParallel: 3", 1)
	imported, err := ann.updatePolicies.ImportUpdatePolicy(ctx, connect.NewRequest(&webv1.ImportUpdatePolicyRequest{GroupId: groupAl, Yaml: changed}))
	if err != nil || imported.Msg.GetPolicy().GetSpec().GetMaxParallel() != 3 {
		t.Errorf("import = %v, %v", imported, err)
	}
	if _, err := ann.updatePolicies.ImportUpdatePolicy(ctx, connect.NewRequest(&webv1.ImportUpdatePolicyRequest{GroupId: groupAl, Yaml: "kind: UpdatePolicy\nspec:\n  mode: all-auto\n"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("all-auto without window: %v", err)
	}
	if _, err := bea.updatePolicies.GetUpdatePolicy(ctx, connect.NewRequest(&webv1.GetUpdatePolicyRequest{GroupId: groupAl})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("Bea reads: %v", err)
	}
	if _, err := ann.updatePolicies.ResetUpdatePolicy(ctx, connect.NewRequest(&webv1.ResetUpdatePolicyRequest{GroupId: groupAl})); err != nil {
		t.Fatal(err)
	}
	if got, _ := ann.updatePolicies.GetUpdatePolicy(ctx, connect.NewRequest(&webv1.GetUpdatePolicyRequest{GroupId: groupAl})); !got.Msg.GetPolicy().GetIsDefault() {
		t.Errorf("after reset: %v", got.Msg.GetPolicy())
	}
}
