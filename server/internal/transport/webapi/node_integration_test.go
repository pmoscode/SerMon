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

// Tags (M2-29): an admin sets them with a fresh step-up; node, fleet summary and audit log show
// them; a viewer may not, malformed tags are INVALID_ARGUMENT, and empty tags clear them.
func TestNodeAPI_TagsShowInNodeFleetAndAudit(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ann := login(t, s, "ann@example.org")
	ctx := context.Background()
	ownerSQL(t, "UPDATE nodes SET tags = '{}' WHERE id = '"+nodeAl+"'")
	set := func(c loggedIn, tags map[string]string) error {
		_, err := c.node.SetNodeTags(ctx, connect.NewRequest(&webv1.SetNodeTagsRequest{NodeId: nodeAl, Tags: tags}))
		return err
	}

	mia := login(t, s, "mia@example.org") // viewer
	if err := set(mia, map[string]string{"env": "prod"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("viewer: %v, want PERMISSION_DENIED", err)
	}
	if err := set(ann, map[string]string{"env": "<prod>"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("malformed: %v, want INVALID_ARGUMENT", err)
	}
	if err := set(ann, map[string]string{"env": "prod", "role": "db"}); err != nil {
		t.Fatal(err)
	}

	got, err := ann.node.GetNode(ctx, connect.NewRequest(&webv1.GetNodeRequest{NodeId: nodeAl}))
	if err != nil || got.Msg.GetNode().GetTags()["env"] != "prod" || got.Msg.GetNode().GetTags()["role"] != "db" {
		t.Fatalf("GetNode() = %v, %v", got.Msg.GetNode().GetTags(), err)
	}
	fleet, err := ann.fleet.GetFleetSummary(ctx, connect.NewRequest(&webv1.GetFleetSummaryRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range fleet.Msg.GetNodes() {
		if n.GetNodeId() == nodeAl {
			found = n.GetTags()["env"] == "prod"
		}
	}
	if !found {
		t.Errorf("fleet summary without the tags of %s", nodeAl)
	}
	var tagsDetail, previous string
	ownerQueryRow(t, "SELECT details->>'tags', details->>'previous' FROM audit_log WHERE action = 'node.tags_set' AND target = 'node:"+nodeAl+"' ORDER BY id DESC LIMIT 1", &tagsDetail, &previous)
	if tagsDetail != "env=prod,role=db" || previous != "" {
		t.Errorf("audit details tags=%q previous=%q", tagsDetail, previous)
	}

	if err := set(ann, nil); err != nil {
		t.Fatal(err)
	}
	var stored string
	ownerQueryRow(t, "SELECT tags::text FROM nodes WHERE id = '"+nodeAl+"'", &stored)
	if stored != "{}" {
		t.Errorf("cleared tags stored as %s", stored)
	}

	ownerSQL(t, "UPDATE sessions SET step_up_at = now() - interval '1 hour' WHERE user_id = '"+annID+"'")
	if err := set(ann, map[string]string{"env": "prod"}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("old login: %v, want FAILED_PRECONDITION", err)
	}
}

// The node shows where its agent reads security events (M2-31); without a report it is
// unspecified, so the UI shows nothing.
func TestNodeAPI_ShowsTheSecuritySource(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	mia := login(t, s, "mia@example.org") // viewer: the hint is for everyone who sees the node
	ctx := context.Background()
	for stored, want := range map[string]webv1.SecuritySource{
		"":                webv1.SecuritySource_SECURITY_SOURCE_UNSPECIFIED,
		"journal":         webv1.SecuritySource_SECURITY_SOURCE_JOURNAL,
		"file_verified":   webv1.SecuritySource_SECURITY_SOURCE_FILE_VERIFIED,
		"file_unverified": webv1.SecuritySource_SECURITY_SOURCE_FILE_UNVERIFIED,
		"none":            webv1.SecuritySource_SECURITY_SOURCE_NONE,
	} {
		ownerSQL(t, "UPDATE nodes SET security_source = '"+stored+"' WHERE id = '"+nodeAl+"'")
		got, err := mia.node.GetNode(ctx, connect.NewRequest(&webv1.GetNodeRequest{NodeId: nodeAl}))
		if err != nil || got.Msg.GetNode().GetSecuritySource() != want {
			t.Errorf("stored %q: GetNode() = %v, %v; want %v", stored, got.Msg.GetNode().GetSecuritySource(), err, want)
		}
	}
	ownerSQL(t, "UPDATE nodes SET security_source = '' WHERE id = '"+nodeAl+"'")
}

// ownerQueryRow reads one row as the owner (past row-level security), for checks only.
func ownerQueryRow(t *testing.T, query string, dest ...any) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, pgtest.Shared(t).OwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if err := conn.QueryRow(ctx, query).Scan(dest...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}
