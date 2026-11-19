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
