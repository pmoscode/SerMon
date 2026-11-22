//go:build integration

package webapi_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/pgtest"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
)

func TestNodeAPI_OnDemandRotationRevokesOfflineNodesAndNeedsAStepUp(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ann := login(t, s, "ann@example.org")
	ctx := context.Background()
	req := func() *connect.Request[webv1.RotateNodeKeysRequest] {
		return connect.NewRequest(&webv1.RotateNodeKeysRequest{Target: &webv1.RotateNodeKeysRequest_GroupId{GroupId: groupAl}})
	}

	mia := login(t, s, "mia@example.org") // viewer
	if _, err := mia.node.RotateNodeKeys(ctx, req()); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("viewer: %v, want PERMISSION_DENIED", err)
	}

	res, err := ann.node.RotateNodeKeys(ctx, req())
	// The group may hold nodes of other tests (shared database); none of them is connected here.
	if err != nil || res.Msg.GetOffline() < 1 || res.Msg.GetOnline() != 0 {
		t.Fatalf("RotateNodeKeys() = %v, %v; want offline nodes only", res.Msg, err)
	}
	conn, err := pgx.Connect(ctx, pgtest.Shared(t).OwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var revoked bool
	if err := conn.QueryRow(ctx, "SELECT revoked_at IS NOT NULL FROM agent_certificates WHERE serial = $1", certAl).Scan(&revoked); err != nil || !revoked {
		t.Errorf("certificate revoked = %v, %v", revoked, err)
	}

	ownerSQL(t, "UPDATE sessions SET step_up_at = now() - interval '1 hour' WHERE user_id = '"+annID+"'")
	if _, err := ann.node.RotateNodeKeys(ctx, req()); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("old login: %v, want FAILED_PRECONDITION", err)
	}
}

// revoke_only never asks a connected agent to rotate (it could be a thief): every node of the
// group has its certificates revoked and is counted as revoked.
func TestNodeAPI_RevokeOnlyRevokesWithoutRotation(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ann := login(t, s, "ann@example.org")
	ctx := context.Background()

	res, err := ann.node.RotateNodeKeys(ctx, connect.NewRequest(&webv1.RotateNodeKeysRequest{
		Target: &webv1.RotateNodeKeysRequest_GroupId{GroupId: groupAl}, RevokeOnly: true,
	}))
	if err != nil || res.Msg.GetRevoked() < 1 || res.Msg.GetOnline() != 0 || res.Msg.GetOffline() != 0 {
		t.Fatalf("RotateNodeKeys(revoke_only) = %v, %v; want revoked nodes only", res.Msg, err)
	}
	conn, err := pgx.Connect(ctx, pgtest.Shared(t).OwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var revoked bool
	if err := conn.QueryRow(ctx, "SELECT revoked_at IS NOT NULL FROM agent_certificates WHERE serial = $1", certAl).Scan(&revoked); err != nil || !revoked {
		t.Errorf("certificate revoked = %v, %v", revoked, err)
	}
}

// Node identity (M2-23): an operator unlocks a locked node and allows its takeover, both with a
// fresh step-up; the node shows lock, reason, last address and the window; a viewer may not.
func TestNodeAPI_UnlockAndTakeoverShowInTheNode(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ann := login(t, s, "ann@example.org")
	ctx := context.Background()
	ownerSQL(t, "UPDATE nodes SET locked_at = now(), lock_reason = 'second session', last_ip = '192.0.2.10', takeover_until = NULL WHERE id = '"+nodeAl+"'")

	node := func() *webv1.Node {
		res, err := ann.node.GetNode(ctx, connect.NewRequest(&webv1.GetNodeRequest{NodeId: nodeAl}))
		if err != nil {
			t.Fatal(err)
		}
		return res.Msg.GetNode()
	}
	n := node()
	if n.GetLockedAt() == nil || n.GetLockReason() != "second session" || n.GetLastIp() != "192.0.2.10" || n.GetTakeoverUntil() != nil {
		t.Fatalf("locked node = %v", n)
	}

	mia := login(t, s, "mia@example.org") // viewer
	if _, err := mia.node.UnlockNode(ctx, connect.NewRequest(&webv1.UnlockNodeRequest{NodeId: nodeAl})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("viewer unlock: %v, want PERMISSION_DENIED", err)
	}
	if _, err := ann.node.UnlockNode(ctx, connect.NewRequest(&webv1.UnlockNodeRequest{NodeId: nodeAl})); err != nil {
		t.Fatal(err)
	}
	res, err := ann.node.AllowNodeTakeover(ctx, connect.NewRequest(&webv1.AllowNodeTakeoverRequest{NodeId: nodeAl}))
	if err != nil || res.Msg.GetUntil() == nil {
		t.Fatalf("AllowNodeTakeover() = %v, %v", res, err)
	}
	if n := node(); n.GetLockedAt() != nil || n.GetLockReason() != "" || !n.GetTakeoverUntil().AsTime().Equal(res.Msg.GetUntil().AsTime().Truncate(time.Microsecond)) {
		t.Errorf("after unlock and takeover: %v", n)
	}

	ownerSQL(t, "UPDATE sessions SET step_up_at = now() - interval '1 hour' WHERE user_id = '"+annID+"'")
	if _, err := ann.node.AllowNodeTakeover(ctx, connect.NewRequest(&webv1.AllowNodeTakeoverRequest{NodeId: nodeAl})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("old login: %v, want FAILED_PRECONDITION", err)
	}
	ownerSQL(t, "UPDATE nodes SET takeover_until = NULL WHERE id = '"+nodeAl+"'")
}
