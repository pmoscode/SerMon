//go:build integration

package webapi_test

import (
	"context"
	"errors"
	"sort"
	"testing"

	"connectrpc.com/connect"

	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// isStepUpRequired reports whether err asks for a fresh step-up.
func isStepUpRequired(err error) bool {
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodeFailedPrecondition {
		return false
	}
	for _, d := range ce.Details() {
		if v, err := d.Value(); err == nil {
			if _, ok := v.(*webv1.StepUpRequired); ok {
				return true
			}
		}
	}
	return false
}

// M3-25: every procedure of stepUpRequired fails with StepUpRequired once the step-up is older
// than 15 minutes – called by Ann, admin of Alpha for all groups, on Alpha's own objects (the
// organizations by Sam, a superadmin).
func TestStepUp_SensitiveActionsNeedAFreshStepUp(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	seedChannelAlpha(t)
	seedLogAlpha(t)
	seedLogin(t, samID, "sam@example.org")
	ownerSQL(t, "INSERT INTO superadmins (user_id) VALUES ('"+samID+"') ON CONFLICT DO NOTHING")
	ann := login(t, s, "ann@example.org")
	sam := login(t, s, "sam@example.org")
	ownerSQL(t, "UPDATE sessions SET step_up_at = now() - interval '16 minutes' WHERE user_id IN ('"+annID+"', '"+samID+"')")

	hc := s.httpClient(ann.jar, s.origin)
	env := stepUpEnv{
		isolationEnv: isolationEnv{
			auth: ann.auth, access: ann.access, invitation: ann.invitation, organization: sam.organization, enrollment: ann.enrollment,
			node: ann.node, agentCA: sam.agentCA, metrics: ann.metrics, fleet: ann.fleet, events: ann.events, channels: ann.channels,
			updatePolicies: ann.updatePolicies,
			policies:       ann.policies, silences: ann.silences, alerts: ann.alerts, rules: ann.rules, windows: ann.windows,
			logSettings: ann.logSettings, logs: ann.logs, liveView: ann.liveView, oidcMappings: ann.oidcMappings,
			orgA: orgAlpha, groupA: groupAl, memberA: miaID, bindingA: bindingAl, invitationA: inviteAl, inviteTokenA: inviteTokenAl,
			enrollmentTokenA: tokenAl, nodeA: nodeAl, channelA: channelAl, policyA: policyAl, silenceA: silenceAl, windowA: windowAl,
			mappingA: mappingAl,
		},
		twoFactor: ann.twoFactor, policy: ann.policy, passkeys: webv1connect.NewPasskeyServiceClient(hc, s.server.URL),
	}
	ctx := context.Background()
	procedures := make([]string, 0, len(stepUpRequired))
	for p := range stepUpRequired {
		procedures = append(procedures, p)
	}
	sort.Strings(procedures)
	for _, p := range procedures {
		var err error
		if run, ok := stepUpOwnCases[p]; ok {
			err = run(ctx, env)
		} else {
			err = tenantIsolationCases[p].run(ctx, env.isolationEnv)
		}
		if !isStepUpRequired(err) {
			t.Errorf("%s with an aged step-up: %v, want FAILED_PRECONDITION with StepUpRequired", p, err)
		}
	}
}
