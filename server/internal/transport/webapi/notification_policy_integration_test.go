//go:build integration

package webapi_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

func TestNotificationPolicyAPI_AdminManagesPolicies(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	seedChannelAlpha(t)
	ownerSQL(t, "DELETE FROM notification_policies WHERE org_id = '"+orgAlpha+"'")
	ann := login(t, s, "ann@example.org")
	c := ann.policies
	ctx := context.Background()

	spec := &webv1.PolicySpec{
		Name: "Security", Enabled: true, ChannelIds: []string{channelAl}, Grouping: webv1.PolicyGrouping_POLICY_GROUPING_DIGEST,
		RepeatMinutes: 240,
		Matchers: &webv1.PolicyMatchers{
			Severities: []webv1.AlertSeverity{webv1.AlertSeverity_ALERT_SEVERITY_CRITICAL}, GroupIds: []string{groupAl},
			Rules: []string{"security.*"}, Labels: map[string]string{"category": "security"},
		},
		Escalation: &webv1.PolicyEscalation{AfterMinutes: 15, ChannelIds: []string{channelAl}},
	}
	created, err := c.CreatePolicy(ctx, connect.NewRequest(&webv1.CreatePolicyRequest{Spec: spec}))
	if err != nil {
		t.Fatal(err)
	}
	p := created.Msg.GetPolicy()
	got := p.GetSpec()
	if p.GetId() == "" || got.GetName() != "Security" || got.GetRepeatMinutes() != 240 || got.GetGrouping() != webv1.PolicyGrouping_POLICY_GROUPING_DIGEST ||
		got.GetEscalation().GetAfterMinutes() != 15 || got.GetMatchers().GetRules()[0] != "security.*" || got.GetMatchers().GetGroupIds()[0] != groupAl ||
		got.GetMatchers().GetSeverities()[0] != webv1.AlertSeverity_ALERT_SEVERITY_CRITICAL {
		t.Errorf("created %v", p)
	}
	spec.Name, spec.Escalation, spec.RepeatMinutes = "Security night", nil, 0
	updated, err := c.UpdatePolicy(ctx, connect.NewRequest(&webv1.UpdatePolicyRequest{PolicyId: p.GetId(), Spec: spec}))
	if err != nil || updated.Msg.GetPolicy().GetSpec().GetName() != "Security night" || updated.Msg.GetPolicy().GetSpec().GetEscalation() != nil {
		t.Fatalf("UpdatePolicy() = %v, %v", updated, err)
	}
	list, err := c.ListPolicies(ctx, connect.NewRequest(&webv1.ListPoliciesRequest{}))
	if err != nil || len(list.Msg.GetPolicies()) != 1 {
		t.Fatalf("ListPolicies() = %v, %v", list, err)
	}

	// Checks of the application: a repeat below 5 minutes, an unknown group.
	bad := &webv1.PolicySpec{Name: "x", ChannelIds: []string{channelAl}, Grouping: webv1.PolicyGrouping_POLICY_GROUPING_NODE, RepeatMinutes: 1}
	if _, err := c.CreatePolicy(ctx, connect.NewRequest(&webv1.CreatePolicyRequest{Spec: bad})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("repeat of 1 minute: %v", err)
	}
	bad.RepeatMinutes, bad.Matchers = 0, &webv1.PolicyMatchers{GroupIds: []string{"0193f5a0-0000-7000-8000-00000000dead"}}
	if _, err := c.CreatePolicy(ctx, connect.NewRequest(&webv1.CreatePolicyRequest{Spec: bad})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("unknown group: %v", err)
	}

	// A viewer may not, and an old login must step up first.
	mia := login(t, s, "mia@example.org")
	if _, err := mia.policies.ListPolicies(ctx, connect.NewRequest(&webv1.ListPoliciesRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("viewer lists policies: %v", err)
	}
	ownerSQL(t, "UPDATE sessions SET step_up_at = now() - interval '1 hour' WHERE user_id = '"+annID+"'")
	if _, err := c.DeletePolicy(ctx, connect.NewRequest(&webv1.DeletePolicyRequest{PolicyId: p.GetId()})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("delete with an old login: %v, want FAILED_PRECONDITION", err)
	}
	fresh := webv1connect.NewNotificationPolicyServiceClient(s.httpClient(login(t, s, "ann@example.org").jar, s.origin), s.server.URL)
	if _, err := fresh.DeletePolicy(ctx, connect.NewRequest(&webv1.DeletePolicyRequest{PolicyId: p.GetId()})); err != nil {
		t.Errorf("delete: %v", err)
	}
}
