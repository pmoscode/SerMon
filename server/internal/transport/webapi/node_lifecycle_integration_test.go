//go:build integration

package webapi_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/loki"
	"github.com/pmoscode/SerMon/server/internal/adapters/loki/lokitest"
	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/nodelife"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/transport/agentapi"
)

func mustNodeLife(t *testing.T, pg *postgres.DB) *nodelife.Service {
	t.Helper()
	svc, err := nodelife.NewService(postgres.NewNodeLifecycleStore(pg), authz.NewPolicy(), time.Now, nodelife.DefaultDeletionGrace,
		nodelife.WithLive(agentapi.NewSessions(0)), nodelife.WithLogs(loki.NewEraser(loki.New(lokitest.Shared(t)))))
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// seedLifecycleNode adds a node of its own to Alpha's group, so the shared fixture node stays
// active for the other tests.
func seedLifecycleNode(t *testing.T, hostname string) string {
	t.Helper()
	id := uuid.NewString()
	ownerSQL(t,
		"SELECT set_config('sermon.org_id', '"+orgAlpha+"', false)",
		"INSERT INTO nodes (id, org_id, group_id, hostname, machine_id, os_id, os_version, kernel_release, architecture, cpu_count, memory_bytes) VALUES ('"+id+"', '"+orgAlpha+"', '"+groupAl+"', '"+hostname+"', '"+strings.ReplaceAll(id, "-", "")+"', 'debian', '13', '6.12.0', 'amd64', 2, 1073741824)",
	)
	return id
}

// M5-29 (ADR 0041): an admin deactivates and deletes a node with a fresh step-up, the deletion
// confirmed by its host name; node and fleet show it, a cancel lifts the deadline, a takeover is
// refused while it is scheduled; a viewer may not; everything lands in the audit log.
func TestNodeAPI_DeactivateDeleteAndCancel(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ann := login(t, s, "ann@example.org")
	mia := login(t, s, "mia@example.org") // viewer
	ctx := context.Background()
	node := seedLifecycleNode(t, "alpha-old")
	get := func() *webv1.Node {
		t.Helper()
		res, err := ann.node.GetNode(ctx, connect.NewRequest(&webv1.GetNodeRequest{NodeId: node}))
		if err != nil {
			t.Fatal(err)
		}
		return res.Msg.GetNode()
	}
	inFleet := func() *webv1.NodeSummary {
		t.Helper()
		res, err := ann.fleet.GetFleetSummary(ctx, connect.NewRequest(&webv1.GetFleetSummaryRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range res.Msg.GetNodes() {
			if n.GetNodeId() == node {
				return n
			}
		}
		t.Fatal("node missing in the fleet")
		return nil
	}
	for who, want := range map[*loggedIn]int{&ann: 1, &mia: 0} {
		session, err := who.auth.GetSession(ctx, connect.NewRequest(&webv1.GetSessionRequest{}))
		if err != nil || len(session.Msg.GetSession().GetLifecycleGroupIds()) != want {
			t.Errorf("lifecycle groups = %v, %v; want %d", session.Msg.GetSession().GetLifecycleGroupIds(), err, want)
		}
	}
	if n := get(); n.GetDeactivatedAt() != nil || n.GetDeletionDueAt() != nil || inFleet().GetDeactivated() {
		t.Fatalf("new node = %v", n)
	}

	if _, err := mia.node.DeactivateNode(ctx, connect.NewRequest(&webv1.DeactivateNodeRequest{NodeId: node})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("viewer deactivates: %v, want PERMISSION_DENIED", err)
	}
	if _, err := ann.node.DeactivateNode(ctx, connect.NewRequest(&webv1.DeactivateNodeRequest{NodeId: node})); err != nil {
		t.Fatal(err)
	}
	if n := get(); n.GetDeactivatedAt() == nil || n.GetDeletionDueAt() != nil || !inFleet().GetDeactivated() {
		t.Errorf("after deactivation = %v", n)
	}

	if _, err := ann.node.DeleteNode(ctx, connect.NewRequest(&webv1.DeleteNodeRequest{NodeId: node, ConfirmHostname: "alpha-01"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("delete with another host name: %v, want INVALID_ARGUMENT", err)
	}
	res, err := ann.node.DeleteNode(ctx, connect.NewRequest(&webv1.DeleteNodeRequest{NodeId: node, ConfirmHostname: "alpha-old"}))
	if err != nil {
		t.Fatal(err)
	}
	due := res.Msg.GetDeletionDueAt().AsTime()
	if d := time.Until(due); d < nodelife.DefaultDeletionGrace-time.Minute || d > nodelife.DefaultDeletionGrace {
		t.Errorf("deletion due in %v, want the grace", d)
	}
	if n := get(); !n.GetDeletionDueAt().AsTime().Equal(due.Truncate(time.Microsecond)) {
		t.Errorf("node deadline = %v, want %v", n.GetDeletionDueAt().AsTime(), due)
	}
	if _, err := ann.node.AllowNodeTakeover(ctx, connect.NewRequest(&webv1.AllowNodeTakeoverRequest{NodeId: node})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("takeover while scheduled: %v, want FAILED_PRECONDITION", err)
	}

	if _, err := ann.node.CancelNodeDeletion(ctx, connect.NewRequest(&webv1.CancelNodeDeletionRequest{NodeId: node})); err != nil {
		t.Fatal(err)
	}
	if n := get(); n.GetDeletionDueAt() != nil || n.GetDeactivatedAt() == nil {
		t.Errorf("after cancel = %v, want deactivated without deadline", n)
	}
	if _, err := ann.node.CancelNodeDeletion(ctx, connect.NewRequest(&webv1.CancelNodeDeletionRequest{NodeId: node})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("cancel without a deadline: %v, want FAILED_PRECONDITION", err)
	}
	if _, err := ann.node.AllowNodeTakeover(ctx, connect.NewRequest(&webv1.AllowNodeTakeoverRequest{NodeId: node})); err != nil {
		t.Errorf("takeover after the cancel: %v", err)
	}

	audit, err := ann.access.ListAuditLog(ctx, connect.NewRequest(&webv1.ListAuditLogRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range audit.Msg.GetEntries() {
		if e.GetTarget() == "node:"+node {
			seen[e.GetAction()] = true
		}
	}
	for _, action := range []string{"node.deactivate", "node.schedule_deletion", "node.cancel_deletion"} {
		if !seen[action] {
			t.Errorf("audit log lacks %s: %v", action, seen)
		}
	}
}
