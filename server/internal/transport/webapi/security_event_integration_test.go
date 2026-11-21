//go:build integration

package webapi_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
)

// The seeded event of Alpha (seedAccessFixtures): visible to the viewer of its group and to an
// admin on all groups, filtered by IP network and kind; not to the admin of another organization.
func TestSecurityEvents_ListedForTheCallersGroups(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ctx := context.Background()
	request := func(network string, kinds ...string) *connect.Request[webv1.ListSecurityEventsRequest] {
		return connect.NewRequest(&webv1.ListSecurityEventsRequest{
			From: timestamppb.New(time.Now().Add(-time.Hour)), To: timestamppb.New(time.Now().Add(time.Minute)),
			Network: network, Kinds: kinds,
		})
	}
	for _, email := range []string{"mia@example.org", "ann@example.org"} {
		res, err := login(t, s, email).events.ListSecurityEvents(ctx, request("192.0.2.0/24", "ssh.login.failed"))
		if err != nil {
			t.Fatalf("%s: %v", email, err)
		}
		events := res.Msg.GetEvents()
		if len(events) != 1 || events[0].GetUser() != "alpha-user" || events[0].GetNodeId() != nodeAl || events[0].GetRemoteIp() != "192.0.2.77" ||
			!events[0].GetUnverified() {
			t.Errorf("%s sees %v", email, events)
		}
	}
	mia := login(t, s, "mia@example.org")
	if res, err := mia.events.ListSecurityEvents(ctx, request("198.51.100.0/24")); err != nil || len(res.Msg.GetEvents()) != 0 {
		t.Errorf("another network: %v, %v", res, err)
	}
	if res, err := login(t, s, "bea@example.org").events.ListSecurityEvents(ctx, request("")); err != nil || len(res.Msg.GetEvents()) != 0 {
		t.Errorf("admin of Beta: %v, %v", res, err)
	}
	_, err := mia.events.ListSecurityEvents(ctx, request("example.org"))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("a host name as network: %v", err)
	}
	var ce *connect.Error
	_, err = mia.events.ListSecurityEvents(ctx, connect.NewRequest(&webv1.ListSecurityEventsRequest{From: timestamppb.Now(), To: timestamppb.New(time.Now().Add(-time.Hour))}))
	if !errors.As(err, &ce) || ce.Code() != connect.CodeInvalidArgument {
		t.Errorf("to before from: %v", err)
	}
}
