//go:build integration

package webapi_test

import (
	"context"
	"encoding/hex"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/pgtest"

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
	// M5-33: the configuration only for those who manage modules.
	if n.GetSettings()[0].GetConfigJson() != "" || !n.GetSettings()[0].GetEnabled() {
		t.Errorf("a viewer sees the configuration: %v", n.GetSettings()[0])
	}
	if own, err := ann.modules.GetNodeModules(ctx, connect.NewRequest(&webv1.GetNodeModulesRequest{NodeId: nodeAl})); err != nil ||
		own.Msg.GetSettings()[0].GetConfigJson() != `{"behavior":"fail"}` {
		t.Errorf("the admin's view: %v, %v", own, err)
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

// V1-25: an admin dismisses a suggestion for the group: gone for every member in the fleet and at
// the node, listed in the settings, audited; shown again, it is back. Viewers can't do either.
func TestModules_AdminsDismissSuggestions(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	seedModulesAlpha(t)
	ctx := context.Background()
	ann := login(t, s, "ann@example.org") // admin of Alpha
	mia := login(t, s, "mia@example.org") // viewer of Alpha's group
	dismiss := func(c webv1connect.ModuleServiceClient, module string, dismissed bool) error {
		_, err := c.SetModuleHintDismissed(ctx, connect.NewRequest(&webv1.SetModuleHintDismissedRequest{GroupId: groupAl, ModuleId: module, Dismissed: dismissed}))
		return err
	}
	open := func() (fleet, node int) {
		t.Helper()
		hints, err := mia.modules.ListModuleHints(ctx, connect.NewRequest(&webv1.ListModuleHintsRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		n, err := mia.modules.GetNodeModules(ctx, connect.NewRequest(&webv1.GetNodeModulesRequest{NodeId: nodeAl}))
		if err != nil {
			t.Fatal(err)
		}
		return len(hints.Msg.GetHints()), len(n.Msg.GetHints())
	}
	dismissedOf := func() []string {
		t.Helper()
		res, err := ann.modules.GetModuleSettings(ctx, connect.NewRequest(&webv1.GetModuleSettingsRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range res.Msg.GetGroups() {
			if g.GetGroupId() == groupAl {
				return g.GetDismissedHints()
			}
		}
		t.Fatal("no group")
		return nil
	}
	if f, n := open(); f != 1 || n != 1 {
		t.Fatalf("before: %d in the fleet, %d at the node", f, n)
	}
	if err := dismiss(mia.modules, "nginx", true); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("viewer dismisses: %v", err)
	}
	for name, tc := range map[string]struct {
		module string
		code   connect.Code
	}{"unknown module": {"nosuch", connect.CodeNotFound}, "invalid module": {"Nginx", connect.CodeInvalidArgument}} {
		if err := dismiss(ann.modules, tc.module, true); connect.CodeOf(err) != tc.code {
			t.Errorf("%s: %v, want %s", name, err, tc.code)
		}
	}
	if err := dismiss(ann.modules, "nginx", true); err != nil {
		t.Fatal(err)
	}
	if f, n := open(); f != 0 || n != 0 {
		t.Errorf("dismissed: %d in the fleet, %d at the node", f, n)
	}
	if got := strings.Join(dismissedOf(), ","); got != "nginx,redis" {
		t.Errorf("settings list %q as dismissed", got)
	}
	audit, err := ann.access.ListAuditLog(ctx, connect.NewRequest(&webv1.ListAuditLogRequest{}))
	if err != nil || !strings.Contains(audit.Msg.String(), "module.hint_dismiss") {
		t.Errorf("audit log lacks module.hint_dismiss: %v", err)
	}
	if err := dismiss(ann.modules, "nginx", false); err != nil {
		t.Fatal(err)
	}
	if f, n := open(); f != 1 || n != 1 {
		t.Errorf("shown again: %d in the fleet, %d at the node", f, n)
	}
	if got := strings.Join(dismissedOf(), ","); got != "redis" {
		t.Errorf("settings list %q as dismissed", got)
	}
}

// M5-05: a credential set with a module is never returned – the settings name the field only –
// and its value is in no table, the audit log included (the test reads them all).
func TestModules_CredentialsAreSealedAndNeverReturned(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	seedModulesAlpha(t)
	ctx := context.Background()
	ann := login(t, s, "ann@example.org")
	const plain = "plain-credential-of-alpha" // betterleaks:allow
	_, err := ann.modules.SetGroupModule(ctx, connect.NewRequest(&webv1.SetGroupModuleRequest{
		GroupId: groupAl, Setting: &webv1.ModuleSetting{ModuleId: "simulate", Enabled: true}, Secrets: map[string]string{"token": plain},
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = ann.modules.RemoveGroupModule(ctx, connect.NewRequest(&webv1.RemoveGroupModuleRequest{GroupId: groupAl, ModuleId: "simulate"}))
	})
	settings, err := ann.modules.GetModuleSettings(ctx, connect.NewRequest(&webv1.GetModuleSettingsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, g := range settings.Msg.GetGroups() {
		for _, m := range g.GetModules() {
			if g.GetGroupId() == groupAl && m.GetModuleId() == "simulate" {
				found = strings.Join(m.GetSecretsSet(), ",") == "token"
			}
		}
	}
	node, _ := ann.modules.GetNodeModules(ctx, connect.NewRequest(&webv1.GetNodeModulesRequest{NodeId: nodeAl}))
	audit, _ := ann.access.ListAuditLog(ctx, connect.NewRequest(&webv1.ListAuditLogRequest{}))
	if !found || strings.Contains(settings.Msg.String()+node.Msg.String()+audit.Msg.String(), plain) {
		t.Fatalf("settings %v", settings.Msg)
	}
	if !strings.Contains(audit.Msg.String(), "token") {
		t.Error("the audit log doesn't name the field")
	}
	// The scan finds what is there (a reason of the seeded hint).
	if hits := tablesContaining(t, "alpha-nginx"); !slices.Contains(hits, "module_hints") {
		t.Fatalf("the scan found the seeded hint in %v", hits)
	}
	for _, hit := range tablesContaining(t, plain) {
		t.Errorf("the credential is in plain text in %s", hit)
	}

	// Credentials outside the schema are refused.
	for name, secrets := range map[string]map[string]string{"no credential": {"value": "1"}, "too long": {"token": strings.Repeat("x", 257)}} {
		_, err := ann.modules.SetGroupModule(ctx, connect.NewRequest(&webv1.SetGroupModuleRequest{
			GroupId: groupAl, Setting: &webv1.ModuleSetting{ModuleId: "simulate"}, Secrets: secrets,
		}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := ann.modules.SetGroupModule(ctx, connect.NewRequest(&webv1.SetGroupModuleRequest{
		GroupId: groupAl, Setting: &webv1.ModuleSetting{ModuleId: "simulate"}, ClearSecrets: []string{"token"},
	})); err != nil {
		t.Fatal(err)
	}
	var left int
	ownerQuery(t, "SELECT count(*) FROM module_secrets WHERE group_id = '"+groupAl+"'", &left)
	if left != 0 {
		t.Errorf("%d credentials left after removing", left)
	}
}

// tablesContaining names the tables of the database whose rows contain s, as text or bytes.
func tablesContaining(t *testing.T, s string) []string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, pgtest.Shared(t).OwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	rows, err := conn.Query(ctx, `SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p')`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		_ = rows.Scan(&name)
		tables = append(tables, name)
	}
	rows.Close()
	hexed := hex.EncodeToString([]byte(s))
	var hits []string
	for _, table := range tables {
		var n int
		// The owner is a superuser here: RLS does not apply, every row is read.
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize()+" t WHERE strpos(t::text, $1) > 0 OR strpos(t::text, $2) > 0", s, hexed).Scan(&n); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if n > 0 {
			hits = append(hits, table)
		}
	}
	if len(tables) < 30 {
		t.Fatalf("read only %d tables", len(tables))
	}
	return hits
}
