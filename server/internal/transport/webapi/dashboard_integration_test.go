//go:build integration

package webapi_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect"

	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
)

// M5-23: a member of Alpha with a role on its group (Mia, viewer) keeps dashboards; Ann, admin
// of all groups, changes shared ones, never private ones; Kai (viewer on all groups) only reads.
func TestDashboards_SharingAndRights(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	mia, ann := login(t, s, "mia@example.org"), login(t, s, "ann@example.org")
	ctx := context.Background()

	created, err := mia.dashboards.CreateDashboard(ctx, connect.NewRequest(&webv1.CreateDashboardRequest{Spec: chartSpecOf(nodeAl)}))
	if err != nil {
		t.Fatal(err)
	}
	d := created.Msg.GetDashboard()
	if d.GetShared() || !d.GetCanEdit() || d.GetRevision() != 1 || d.GetOwnerId() != miaID {
		t.Errorf("created = %v", d)
	}
	if _, err := ann.dashboards.GetDashboard(ctx, connect.NewRequest(&webv1.GetDashboardRequest{DashboardId: d.GetId()})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("an admin reads a private dashboard of another: %v", err)
	}
	if _, err := ann.dashboards.UpdateDashboard(ctx, connect.NewRequest(&webv1.UpdateDashboardRequest{
		DashboardId: d.GetId(), Revision: 1, Spec: alertSpec(),
	})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("an admin changes a private dashboard of another: %v, want NOT_FOUND", err)
	}
	if _, err := ann.dashboards.DeleteDashboard(ctx, connect.NewRequest(&webv1.DeleteDashboardRequest{DashboardId: d.GetId()})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("an admin deletes a private dashboard of another: %v, want NOT_FOUND", err)
	}
	if _, err := mia.dashboards.SetDashboardShared(ctx, connect.NewRequest(&webv1.SetDashboardSharedRequest{DashboardId: d.GetId(), Shared: true})); err != nil {
		t.Fatal(err)
	}
	got, err := ann.dashboards.GetDashboard(ctx, connect.NewRequest(&webv1.GetDashboardRequest{DashboardId: d.GetId()}))
	if err != nil || !got.Msg.GetDashboard().GetCanEdit() || got.Msg.GetDashboard().GetSpec().GetWidgets()[0].GetQuery().GetTarget().GetNodeIds()[0] != nodeAl {
		t.Fatalf("shared for the admin = %v, %v", got.Msg.GetDashboard(), err)
	}
	updated, err := ann.dashboards.UpdateDashboard(ctx, connect.NewRequest(&webv1.UpdateDashboardRequest{
		DashboardId: d.GetId(), Revision: 2, Spec: alertSpec(),
	}))
	if err != nil || updated.Msg.GetDashboard().GetRevision() != 3 {
		t.Fatalf("admin updates a shared dashboard: %v, %v", updated, err)
	}
	if _, err := mia.dashboards.UpdateDashboard(ctx, connect.NewRequest(&webv1.UpdateDashboardRequest{
		DashboardId: d.GetId(), Revision: 2, Spec: chartSpecOf(nodeAl),
	})); connect.CodeOf(err) != connect.CodeAborted {
		t.Errorf("a stale revision: %v, want ABORTED", err)
	}
	kai := login(t, s, "kai@example.org")
	if _, err := kai.dashboards.DeleteDashboard(ctx, connect.NewRequest(&webv1.DeleteDashboardRequest{DashboardId: d.GetId()})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("a viewer deletes a shared dashboard of another: %v", err)
	}
	list, err := kai.dashboards.ListDashboards(ctx, connect.NewRequest(&webv1.ListDashboardsRequest{}))
	if err != nil || len(list.Msg.GetDashboards()) == 0 {
		t.Fatalf("list = %v, %v", list, err)
	}
	for _, l := range list.Msg.GetDashboards() {
		if len(l.GetSpec().GetWidgets()) != 0 {
			t.Error("the list carries no widgets")
		}
	}
	if _, err := mia.dashboards.DeleteDashboard(ctx, connect.NewRequest(&webv1.DeleteDashboardRequest{DashboardId: d.GetId()})); err != nil {
		t.Errorf("the owner deletes: %v", err)
	}
}

// Export and import: the exported JSON reads back; documents that break the model, are too large
// or name nodes the caller can't view are rejected and nothing is stored.
func TestDashboards_ImportAndExport(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ann := login(t, s, "ann@example.org")
	ctx := context.Background()

	created, err := ann.dashboards.CreateDashboard(ctx, connect.NewRequest(&webv1.CreateDashboardRequest{Spec: chartSpecOf(nodeAl)}))
	if err != nil {
		t.Fatal(err)
	}
	exported, err := ann.dashboards.ExportDashboard(ctx, connect.NewRequest(&webv1.ExportDashboardRequest{DashboardId: created.Msg.GetDashboard().GetId()}))
	if err != nil {
		t.Fatal(err)
	}
	text := exported.Msg.GetJson()
	if !strings.Contains(text, `"WIDGET_KIND_TIMESERIES"`) || strings.Contains(text, created.Msg.GetDashboard().GetId()) || strings.Contains(text, annID) {
		t.Errorf("export = %s; want the model without ID and owner", text)
	}
	imported, err := ann.dashboards.ImportDashboard(ctx, connect.NewRequest(&webv1.ImportDashboardRequest{Json: text}))
	if err != nil {
		t.Fatalf("import of the export: %v", err)
	}
	if imported.Msg.GetDashboard().GetId() == created.Msg.GetDashboard().GetId() || imported.Msg.GetDashboard().GetSpec().GetWidgets()[0].GetId() != "cpu" {
		t.Errorf("imported = %v", imported.Msg.GetDashboard())
	}

	before := countDashboards(t)
	foreignNode := "0193f5a0-0000-7000-8000-0000000002ff"
	for name, tc := range map[string]struct {
		json string
		code connect.Code
	}{
		"not JSON":            {"{", connect.CodeInvalidArgument},
		"unknown field":       {`{"version":1,"title":"x","owner":"bob"}`, connect.CodeInvalidArgument},
		"unknown version":     {`{"version":2,"title":"x"}`, connect.CodeInvalidArgument},
		"too many widgets":    {manyWidgets(31), connect.CodeInvalidArgument},
		"overlapping widgets": {`{"version":1,"title":"x","widgets":[` + alertWidget("a", 0) + `,` + alertWidget("b", 0) + `]}`, connect.CodeInvalidArgument},
		"larger than 32 KiB":  {`{"version":1,"title":"x","description":"` + strings.Repeat("x", 33*1024) + `"}`, connect.CodeInvalidArgument},
		"node of nobody":      {strings.ReplaceAll(text, nodeAl, foreignNode), connect.CodeNotFound},
	} {
		_, err := ann.dashboards.ImportDashboard(ctx, connect.NewRequest(&webv1.ImportDashboardRequest{Json: tc.json}))
		if connect.CodeOf(err) != tc.code {
			t.Errorf("%s: %v, want %v", name, err, tc.code)
		}
	}
	if after := countDashboards(t); after != before {
		t.Errorf("rejected imports stored %d dashboards", after-before)
	}
	_, err = ann.dashboards.ImportDashboard(ctx, connect.NewRequest(&webv1.ImportDashboardRequest{Json: strings.ReplaceAll(text, nodeAl, foreignNode)}))
	if err == nil || !strings.Contains(err.Error(), `widget "cpu"`) {
		t.Errorf("the error names the widget: %v", err)
	}
}

func countDashboards(t *testing.T) int {
	t.Helper()
	var n int
	ownerQueryRow(t, "SELECT count(*) FROM dashboards WHERE org_id = '"+orgAlpha+"'", &n)
	return n
}

func alertWidget(id string, y int) string {
	return `{"id":"` + id + `","kind":"WIDGET_KIND_ALERT_LIST","title":"A","position":{"y":` + strconv.Itoa(y) + `,"w":1,"h":1},"query":{}}`
}

func manyWidgets(n int) string {
	parts := make([]string, 0, n)
	for i := range n {
		parts = append(parts, alertWidget("w"+strconv.Itoa(i), i))
	}
	return `{"version":1,"title":"x","widgets":[` + strings.Join(parts, ",") + `]}`
}
