//go:build integration

package webapi_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"

	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// M5-06: admins set a group's modules (checked against the schema, audited, the version raised);
// viewers of the group see what its node has built in, runs and suggests.
func TestModules_AdminsSetGroupsViewersSeeTheirNodes(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	seedModulesAlpha(t)
	ctx := context.Background()
	ann := login(t, s, "ann@example.org") // admin of Alpha
	mia := login(t, s, "mia@example.org") // viewer of Alpha's group

	list, err := mia.modules.ListModules(ctx, connect.NewRequest(&webv1.ListModulesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	// The modules by ID; which come first depends on the build.
	var simulate *webv1.Module
	for _, mod := range list.Msg.GetModules() {
		if mod.GetId() == "simulate" {
			simulate = mod
		}
	}
	if simulate == nil || !strings.Contains(simulate.GetSchemaJson(), `"behavior"`) || strings.Join(simulate.GetSecretFields(), ",") != "token" {
		t.Fatalf("ListModules() lacks simulate: %v", list.Msg)
	}
	if _, err := mia.modules.GetModuleSettings(ctx, connect.NewRequest(&webv1.GetModuleSettingsRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("viewer reads the settings: %v", err)
	}
	set := func(c webv1connect.ModuleServiceClient, setting *webv1.ModuleSetting) error {
		_, err := c.SetGroupModule(ctx, connect.NewRequest(&webv1.SetGroupModuleRequest{GroupId: groupAl, Setting: setting}))
		return err
	}
	if err := set(mia.modules, &webv1.ModuleSetting{ModuleId: "simulate"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("viewer sets a module: %v", err)
	}
	before, err := ann.modules.GetModuleSettings(ctx, connect.NewRequest(&webv1.GetModuleSettingsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		setting *webv1.ModuleSetting
		code    connect.Code
		why     string
	}{
		{&webv1.ModuleSetting{ModuleId: "simulate", ConfigJson: `{"value":-1}`}, connect.CodeInvalidArgument, "value"},
		{&webv1.ModuleSetting{ModuleId: "simulate", ConfigJson: `{"token":"pw"}`}, connect.CodeInvalidArgument, "credential"},
		{&webv1.ModuleSetting{ModuleId: "simulate", Interval: durationpb.New(5 * time.Second)}, connect.CodeInvalidArgument, "interval"},
		{&webv1.ModuleSetting{ModuleId: "nosuch"}, connect.CodeNotFound, ""},
	} {
		if err := set(ann.modules, tc.setting); connect.CodeOf(err) != tc.code || !strings.Contains(err.Error(), tc.why) {
			t.Errorf("%v: %v, want %s %q", tc.setting, err, tc.code, tc.why)
		}
	}
	if err := set(ann.modules, &webv1.ModuleSetting{ModuleId: "simulate", Enabled: true, Interval: durationpb.New(20 * time.Second), ConfigJson: `{"behavior":"fail"}`}); err != nil {
		t.Fatalf("SetGroupModule() = %v", err)
	}
	after, err := ann.modules.GetModuleSettings(ctx, connect.NewRequest(&webv1.GetModuleSettingsRequest{}))
	if err != nil || after.Msg.GetVersion() != before.Msg.GetVersion()+1 {
		t.Fatalf("version %d → %v, %v", before.Msg.GetVersion(), after, err)
	}
	var group *webv1.GroupModuleSettings
	for _, g := range after.Msg.GetGroups() {
		if g.GetGroupId() == groupAl {
			group = g
		}
	}
	if group == nil || len(group.GetModules()) != 1 || group.GetModules()[0].GetConfigJson() != `{"behavior":"fail"}` ||
		group.GetModules()[0].GetInterval().AsDuration() != 20*time.Second || strings.Join(group.GetAvailable(), ",") != "nginx,simulate" {
		t.Fatalf("group = %v", group)
	}
	audit, err := ann.access.ListAuditLog(ctx, connect.NewRequest(&webv1.ListAuditLogRequest{}))
	if err != nil || !strings.Contains(audit.Msg.String(), "module.update") {
		t.Errorf("audit log lacks module.update: %v", err)
	}

	node, err := mia.modules.GetNodeModules(ctx, connect.NewRequest(&webv1.GetNodeModulesRequest{NodeId: nodeAl}))
	if err != nil {
		t.Fatal(err)
	}
	n := node.Msg
	if n.GetGroupId() != groupAl || strings.Join(n.GetBuiltin(), ",") != "simulate,nginx" || len(n.GetSettings()) != 1 ||
		len(n.GetHealth()) != 1 || n.GetHealth()[0].GetState() != webv1.ModuleState_MODULE_STATE_ERROR || n.GetHealth()[0].GetReason() != "alpha reason" ||
		len(n.GetHints()) != 1 || n.GetHints()[0].GetModuleId() != "nginx" || n.GetHintsCollectedAt() == nil {
		t.Errorf("GetNodeModules() = %v", n)
	}
	hints, err := mia.modules.ListModuleHints(ctx, connect.NewRequest(&webv1.ListModuleHintsRequest{}))
	if err != nil || len(hints.Msg.GetHints()) != 1 || hints.Msg.GetHints()[0].GetHostname() != "alpha-01" {
		t.Errorf("ListModuleHints() = %v, %v", hints, err)
	}

	if _, err := ann.modules.RemoveGroupModule(ctx, connect.NewRequest(&webv1.RemoveGroupModuleRequest{GroupId: groupAl, ModuleId: "simulate"})); err != nil {
		t.Fatal(err)
	}
	if _, err := ann.modules.RemoveGroupModule(ctx, connect.NewRequest(&webv1.RemoveGroupModuleRequest{GroupId: groupAl, ModuleId: "simulate"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("removing twice: %v", err)
	}
}
