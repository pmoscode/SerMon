//go:build integration

package webapi_test

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/pmoscode/SerMon/server/internal/domain/update"
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
		AgentUpdate: webv1.AgentUpdate_AGENT_UPDATE_AUTO, Channel: webv1.AgentChannel_AGENT_CHANNEL_BETA,
		Window: &webv1.UpdateWindow{Days: []webv1.Weekday{webv1.Weekday_WEEKDAY_SUNDAY}, StartMinute: 180, DurationMinutes: 120, TimeZone: "Europe/Berlin"},
	}
	set, err := ann.updatePolicies.SetUpdatePolicy(ctx, connect.NewRequest(&webv1.SetUpdatePolicyRequest{GroupId: groupAl, Spec: spec}))
	if err != nil {
		t.Fatal(err)
	}
	if p := set.Msg.GetPolicy(); p.GetIsDefault() || p.GetUpdateTime() == nil || p.GetSpec().GetMaxParallel() != 2 || !p.GetSpec().GetAutoReboot() ||
		p.GetSpec().GetAgentUpdate() != webv1.AgentUpdate_AGENT_UPDATE_AUTO || p.GetSpec().GetChannel() != webv1.AgentChannel_AGENT_CHANNEL_BETA ||
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

// M4-11: Ann (admin) asks for fresh update information of Alpha's node once within 5 minutes;
// Mia (viewer) may not, Bea (Beta) doesn't find the node.
func TestUpdates_RefreshOnRequest(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ownerSQL(t, "DELETE FROM node_commands WHERE node_id = '"+nodeAl+"'")
	ctx := context.Background()
	refresh := func(l loggedIn) error {
		_, err := l.updates.RefreshUpdateInfo(ctx, connect.NewRequest(&webv1.RefreshUpdateInfoRequest{NodeId: nodeAl}))
		return err
	}
	ann := login(t, s, "ann@example.org")
	if err := refresh(ann); err != nil {
		t.Fatal(err)
	}
	if err := refresh(ann); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Errorf("a second refresh: %v, want RESOURCE_EXHAUSTED", err)
	}
	if err := refresh(login(t, s, "mia@example.org")); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("a viewer: %v", err)
	}
	if err := refresh(login(t, s, "bea@example.org")); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("another organization: %v", err)
	}
}

// M4-14: Ann (admin, fresh step-up) updates Alpha's node by hand and reboots it; both are in the
// audit log. Mia (viewer) may not.
func TestUpdates_ByHand(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ownerSQL(t, "DELETE FROM update_rollouts WHERE org_id = '"+orgAlpha+"'", "DELETE FROM node_commands WHERE org_id = '"+orgAlpha+"'")
	ctx := context.Background()
	ann := login(t, s, "ann@example.org")
	res, err := ann.updates.StartUpdates(ctx, connect.NewRequest(&webv1.StartUpdatesRequest{
		NodeIds: []string{nodeAl}, Scope: webv1.UpdateScope_UPDATE_SCOPE_SECURITY, Reboot: true,
	}))
	if err != nil || len(res.Msg.GetRolloutIds()) != 1 {
		t.Fatalf("StartUpdates() = %v, %v", res, err)
	}
	var got string
	if ownerQuery(t, "SELECT manual::text || ',' || mode || ',' || auto_reboot::text || ',' || state FROM update_rollouts WHERE id = '"+res.Msg.GetRolloutIds()[0]+"'", &got); got != "true,2,true,1" {
		t.Errorf("rollout = %q", got)
	}
	reboot, err := ann.updates.RebootNodes(ctx, connect.NewRequest(&webv1.RebootNodesRequest{NodeIds: []string{nodeAl}, Reason: "kernel update"}))
	if err != nil || len(reboot.Msg.GetRunIds()) != 1 {
		t.Fatalf("RebootNodes() = %v, %v", reboot, err)
	}
	if ownerQuery(t, "SELECT kind || ',' || reason FROM node_commands WHERE id = '"+reboot.Msg.GetRunIds()[0]+"'", &got); got != "3,kernel update" {
		t.Errorf("command = %q", got)
	}
	if ownerQuery(t, "SELECT string_agg(action, ',' ORDER BY seq) FROM audit_log WHERE org_id = '"+orgAlpha+"' AND action IN ('updates.start', 'nodes.reboot')", &got); !strings.HasSuffix(got, "updates.start,nodes.reboot") {
		t.Errorf("audit = %q", got)
	}
	mia := login(t, s, "mia@example.org")
	if _, err := mia.updates.StartUpdates(ctx, connect.NewRequest(&webv1.StartUpdatesRequest{NodeIds: []string{nodeAl}, Scope: webv1.UpdateScope_UPDATE_SCOPE_ALL})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("a viewer updates: %v", err)
	}
	if _, err := ann.updates.RebootNodes(ctx, connect.NewRequest(&webv1.RebootNodesRequest{NodeIds: []string{nodeAl}, Reason: "now\n-h"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("a reason with a newline: %v", err)
	}
	// Review M4-30: nor with a bidi override (schema and service).
	if _, err := ann.updates.RebootNodes(ctx, connect.NewRequest(&webv1.RebootNodesRequest{NodeIds: []string{nodeAl}, Reason: "kernel\u202eetadpu"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("a reason with a bidi override: %v", err)
	}
	// The session tells the UI where the caller may update and reboot.
	session, err := ann.auth.GetSession(ctx, connect.NewRequest(&webv1.GetSessionRequest{}))
	if err != nil || len(session.Msg.GetSession().GetRunUpdatesGroupIds()) == 0 || len(session.Msg.GetSession().GetRebootGroupIds()) == 0 {
		t.Errorf("session = %v, %v", session, err)
	}
	if s, _ := mia.auth.GetSession(ctx, connect.NewRequest(&webv1.GetSessionRequest{})); len(s.Msg.GetSession().GetRunUpdatesGroupIds()) != 0 {
		t.Errorf("a viewer may update: %v", s.Msg.GetSession().GetRunUpdatesGroupIds())
	}
}

// M4-15: the run history of Alpha's node, a run's output for those who may read logs, and the
// rollouts of the visible groups.
func TestUpdates_RunsAndRollouts(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ctx := context.Background()
	ann := login(t, s, "ann@example.org")
	runs, err := ann.updates.ListUpdateRuns(ctx, connect.NewRequest(&webv1.ListUpdateRunsRequest{NodeId: nodeAl}))
	if err != nil {
		t.Fatal(err)
	}
	var seeded *webv1.UpdateRun
	for _, r := range runs.Msg.GetRuns() {
		if r.GetRunId() == runAl {
			seeded = r
		}
	}
	if seeded == nil || seeded.GetState() != webv1.UpdateRunState_UPDATE_RUN_STATE_SUCCEEDED || seeded.GetExitCode() != 0 ||
		seeded.GetKind() != webv1.UpdateRunKind_UPDATE_RUN_KIND_UPDATE || seeded.GetOutput() != "" {
		t.Errorf("runs = %v", runs.Msg.GetRuns())
	}
	run, err := ann.updates.GetUpdateRun(ctx, connect.NewRequest(&webv1.GetUpdateRunRequest{RunId: runAl}))
	if err != nil || run.Msg.GetRun().GetOutput() != "Setting up alpha-openssl" {
		t.Errorf("run = %v, %v", run, err)
	}
	// A viewer of all groups while viewers may not read logs: the output is like a log.
	ownerSQL(t, "UPDATE organizations SET viewers_see_logs = false WHERE id = '"+orgAlpha+"'")
	defer ownerSQL(t, "UPDATE organizations SET viewers_see_logs = true WHERE id = '"+orgAlpha+"'")
	kai := login(t, s, "kai@example.org")
	if _, err := kai.updates.GetUpdateRun(ctx, connect.NewRequest(&webv1.GetUpdateRunRequest{RunId: runAl})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("a viewer without logs reads the output: %v", err)
	}
	if _, err := kai.updates.ListUpdateRuns(ctx, connect.NewRequest(&webv1.ListUpdateRunsRequest{NodeId: nodeAl})); err != nil {
		t.Errorf("a viewer lists the runs: %v", err)
	}
	rollouts, err := ann.updates.ListRollouts(ctx, connect.NewRequest(&webv1.ListRolloutsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rollouts.Msg.GetRollouts() {
		if r.GetRolloutId() == runAl {
			found = r.GetGroupName() == "alpha-web" && r.GetState() == webv1.RolloutState_ROLLOUT_STATE_DONE &&
				len(r.GetNodes()) == 1 && r.GetNodes()[0].GetState() == webv1.RolloutNodeState_ROLLOUT_NODE_STATE_DONE
		}
	}
	if !found {
		t.Errorf("rollouts = %v", rollouts.Msg.GetRollouts())
	}
}

// testReleases offers 9.9.9 in stable and 10.0.0-rc.1 in beta for arm64 and amd64.
type testReleases struct{}

func (testReleases) Latest(c update.Channel, _ string) (string, bool) {
	if c == update.ChannelBeta {
		return "10.0.0-rc.1", true
	}
	return "9.9.9", true
}

// M4-19: Ann updates the agent of Alpha's script-installed node by hand, the node shows its
// agent with the channel's release; a package installation is skipped; Mia (viewer) may not.
func TestUpdates_AgentsByHand(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ownerSQL(t, "UPDATE nodes SET install_method = 1, agent_version = '1.0.0' WHERE id = '"+nodeAl+"'",
		"DELETE FROM node_commands WHERE org_id = '"+orgAlpha+"' AND kind = 4")
	ctx := context.Background()
	ann := login(t, s, "ann@example.org")
	got, err := ann.updates.GetNodeUpdates(ctx, connect.NewRequest(&webv1.GetNodeUpdatesRequest{NodeId: nodeAl}))
	if a := got.Msg.GetAgent(); err != nil || a.GetVersion() != "1.0.0" || a.GetInstallMethod() != webv1.InstallMethod_INSTALL_METHOD_SCRIPT ||
		a.GetChannel() != webv1.AgentChannel_AGENT_CHANNEL_STABLE || a.GetLatest() != "9.9.9" || a.GetTarget() != "9.9.9" {
		t.Fatalf("agent = %v, %v", got.Msg.GetAgent(), err)
	}
	res, err := ann.updates.UpdateAgents(ctx, connect.NewRequest(&webv1.UpdateAgentsRequest{NodeIds: []string{nodeAl}}))
	if err != nil || len(res.Msg.GetRunIds()) != 1 || len(res.Msg.GetSkipped()) != 0 {
		t.Fatalf("UpdateAgents() = %v, %v", res, err)
	}
	var cmd string
	ownerQuery(t, "SELECT kind || ',' || version FROM node_commands WHERE id = '"+res.Msg.GetRunIds()[0]+"'", &cmd)
	if cmd != "4,9.9.9" {
		t.Errorf("command = %q", cmd)
	}
	ownerSQL(t, "UPDATE nodes SET install_method = 2 WHERE id = '"+nodeAl+"'")
	res, err = ann.updates.UpdateAgents(ctx, connect.NewRequest(&webv1.UpdateAgentsRequest{NodeIds: []string{nodeAl}}))
	if err != nil || len(res.Msg.GetRunIds()) != 0 || len(res.Msg.GetSkipped()) != 1 || !strings.Contains(res.Msg.GetSkipped()[0].GetReason(), "package") {
		t.Errorf("a package install: %v, %v", res, err)
	}
	mia := login(t, s, "mia@example.org")
	if _, err := mia.updates.UpdateAgents(ctx, connect.NewRequest(&webv1.UpdateAgentsRequest{NodeIds: []string{nodeAl}})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("a viewer: %v", err)
	}
	rel, err := mia.updates.ListAgentReleases(ctx, connect.NewRequest(&webv1.ListAgentReleasesRequest{}))
	if err != nil || len(rel.Msg.GetReleases()) != 4 {
		t.Errorf("releases = %v, %v", rel, err)
	}
}
