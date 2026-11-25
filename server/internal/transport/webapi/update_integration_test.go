//go:build integration

package webapi_test

import (
	"context"
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
