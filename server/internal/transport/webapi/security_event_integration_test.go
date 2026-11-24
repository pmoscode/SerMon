//go:build integration

package webapi_test

import (
	"context"
	"errors"
	"strings"
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

// M3-16 over the real stack: the overview and the CSV export of the viewer's group, a formula in
// a user name defused; with the organization's switch off the viewer gets neither.
func TestSecurityEvents_OverviewAndExport(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ownerSQL(t,
		"SELECT set_config('sermon.org_id', '"+orgAlpha+"', false)",
		"INSERT INTO security_events (org_id, node_id, occurred_at, kind, user_name, remote_ip) SELECT '"+orgAlpha+"', '"+nodeAl+"', now() - interval '2 minutes', 'ssh.login.invalid_user', '=HYPERLINK(\"http://evil.example\")', '203.0.113.9' WHERE NOT EXISTS (SELECT 1 FROM security_events WHERE remote_ip = '203.0.113.9')",
		"INSERT INTO security_events (org_id, node_id, occurred_at, kind, user_name, remote_ip, new_source) SELECT '"+orgAlpha+"', '"+nodeAl+"', now() - interval '3 minutes', 'ssh.login.success', 'root', '203.0.113.10', true WHERE NOT EXISTS (SELECT 1 FROM security_events WHERE remote_ip = '203.0.113.10')",
	)
	ctx := context.Background()
	from, to := timestamppb.New(time.Now().Add(-time.Hour)), timestamppb.New(time.Now().Add(time.Minute))
	mia := login(t, s, "mia@example.org") // viewer on the group of alpha-01
	overview := func() (*webv1.GetSecurityOverviewResponse, error) {
		res, err := mia.events.GetSecurityOverview(ctx, connect.NewRequest(&webv1.GetSecurityOverviewRequest{From: from, To: to}))
		if err != nil {
			return nil, err
		}
		return res.Msg, nil
	}
	o, err := overview()
	if err != nil {
		t.Fatal(err)
	}
	if o.GetStep().AsDuration() != time.Minute || len(o.GetFailedLogins()) != 1 || o.GetFailedLogins()[0].GetNodeId() != nodeAl ||
		o.GetTotals().GetFailedLogins() < 2 || o.GetTotals().GetRootLogins() != 1 || o.GetTotals().GetNewSourceLogins() != 1 {
		t.Errorf("overview = %v", o)
	}
	ips := map[string]uint64{}
	for _, c := range o.GetTopIps() {
		ips[c.GetValue()] = c.GetUnverified()
	}
	if u, ok := ips["192.0.2.77"]; !ok || u != 1 {
		t.Errorf("top IPs = %v, want 192.0.2.77 with its unverified event", o.GetTopIps())
	}
	listed, err := mia.events.ListSecurityEvents(ctx, connect.NewRequest(&webv1.ListSecurityEventsRequest{From: from, To: to, NewSourcesOnly: true}))
	if err != nil || len(listed.Msg.GetEvents()) != 1 || !listed.Msg.GetEvents()[0].GetNewSource() || listed.Msg.GetEvents()[0].GetUser() != "root" {
		t.Errorf("new sources only = %v, %v", listed, err)
	}

	export := func() (*webv1.ExportSecurityEventsResponse, error) {
		res, err := mia.events.ExportSecurityEvents(ctx, connect.NewRequest(&webv1.ExportSecurityEventsRequest{
			Filter: &webv1.SecurityEventFilter{From: from, To: to, Network: "203.0.113.0/24"},
		}))
		if err != nil {
			return nil, err
		}
		return res.Msg, nil
	}
	e, err := export()
	if err != nil {
		t.Fatal(err)
	}
	content := string(e.GetContent())
	if e.GetRows() != 2 || e.GetTruncated() || !strings.HasSuffix(e.GetFileName(), ".csv") ||
		!strings.Contains(content, `,alpha-01,ssh.login.invalid_user,"'=HYPERLINK(""http://evil.example"")",203.0.113.9,`) ||
		strings.Contains(content, `,"=HYPERLINK`) || strings.Contains(content, "alpha-user") {
		t.Errorf("export %s (%d rows):\n%s", e.GetFileName(), e.GetRows(), content)
	}

	ann := login(t, s, "ann@example.org")
	if _, err := ann.logSettings.SetViewerLogAccess(ctx, connect.NewRequest(&webv1.SetViewerLogAccessRequest{})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = ann.logSettings.SetViewerLogAccess(ctx, connect.NewRequest(&webv1.SetViewerLogAccessRequest{ViewersSeeLogs: true}))
	})
	if _, err := overview(); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("overview with the switch off: %v", err)
	}
	if _, err := export(); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("export with the switch off: %v", err)
	}
	if _, err := ann.events.ExportSecurityEvents(ctx, connect.NewRequest(&webv1.ExportSecurityEventsRequest{
		Filter: &webv1.SecurityEventFilter{From: to, To: from},
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("to before from: %v", err)
	}
}
