package agentapi

import (
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/domain/module"
	agentv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1"
)

// M5-06: the modules' batches go to the ingest as reports, not as (empty) metrics.
func TestToBatch_ModuleReports(t *testing.T) {
	at := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	health := toBatch(&agentv1.Batch{Seq: 7, Content: &agentv1.Batch_ModuleHealth{ModuleHealth: &agentv1.ModuleHealthReport{
		CollectedAt: timestamppb.New(at),
		Modules: []*agentv1.ModuleHealth{
			{ModuleId: "nginx", State: agentv1.ModuleState_MODULE_STATE_ERROR, Reason: "refused", LastSuccess: timestamppb.New(at.Add(-time.Minute))},
			{ModuleId: "simulate", State: agentv1.ModuleState_MODULE_STATE_OK},
		},
	}}})
	r := health.ModuleHealth
	if health.Seq != 7 || health.Unhandled || r == nil || !r.CollectedAt.Equal(at) || len(r.Modules) != 2 || health.Samples != nil {
		t.Fatalf("health batch = %+v", health)
	}
	if m := r.Modules[0]; m.Module != "nginx" || m.State != module.StateError || m.Reason != "refused" || m.LastSuccess == nil || !m.LastSuccess.Equal(at.Add(-time.Minute)) {
		t.Errorf("nginx = %+v", m)
	}
	if m := r.Modules[1]; m.State != module.StateOK || m.LastSuccess != nil {
		t.Errorf("simulate = %+v", m)
	}

	hints := toBatch(&agentv1.Batch{Seq: 8, Content: &agentv1.Batch_DiscoveryHints{DiscoveryHints: &agentv1.DiscoveryHints{
		CollectedAt: timestamppb.New(at),
		Hints:       []*agentv1.DiscoveryHint{{ModuleId: "nginx", Reasons: []string{"process:nginx", "port:80"}, SuggestedConfigJson: `{"port":80}`}},
	}}})
	h := hints.ModuleHints
	if hints.Seq != 8 || h == nil || len(h.Hints) != 1 || h.Hints[0].Module != "nginx" || len(h.Hints[0].Reasons) != 2 || h.Hints[0].Suggested != `{"port":80}` {
		t.Fatalf("hints batch = %+v", hints)
	}
}

// V1-13 (review A I4): a node gets the credentials of its group, at most maxModuleSecrets; all
// credential fields of all modules together fit, so none is left out silently. A module with
// more fields must raise the bound (agent.proto) or bound the group's credentials.
func TestModuleSecrets_AllCredentialFieldsFit(t *testing.T) {
	catalog, err := module.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, m := range catalog.All() {
		total += len(m.Schema.Secrets())
	}
	if total == 0 || total > maxModuleSecrets {
		t.Errorf("%d credential fields, a node gets at most %d", total, maxModuleSecrets)
	}
}
