package teams_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/pmoscode/SerMon/server/internal/adapters/notify/notifytest"
	"github.com/pmoscode/SerMon/server/internal/adapters/notify/teams"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
)

func config(url string) notify.Config { return notify.Config{Secrets: map[string]string{"url": url}} }

func TestTeams_PostsAnAdaptiveCard(t *testing.T) {
	srv := notifytest.NewServer(t)
	srv.Status = http.StatusAccepted
	n := teams.New(notifytest.LocalClient(t))
	if err := n.Send(context.Background(), config(srv.URL), notifytest.Security(notify.German)); err != nil {
		t.Fatal(err)
	}
	body, _ := srv.Last(t)
	notifytest.Golden(t, "security.de.json", notifytest.Indent(t, body))
	if err := n.Send(context.Background(), config(srv.URL), notifytest.Resolved(notify.English)); err != nil {
		t.Fatal(err)
	}
	body, _ = srv.Last(t)
	notifytest.Golden(t, "resolved.en.json", notifytest.Indent(t, body))
}

// Adaptive Cards render Markdown in TextBlock and FactSet; values from the hosts go only into
// TextRun, which is plain text.
func TestTeams_KeepsHostileValuesInTextRuns(t *testing.T) {
	srv := notifytest.NewServer(t)
	if err := teams.New(notifytest.LocalClient(t)).Send(context.Background(), config(srv.URL), notifytest.Security(notify.English)); err != nil {
		t.Fatal(err)
	}
	body, _ := srv.Last(t)
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	var found bool
	var visit func(any)
	visit = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if text, ok := x["text"].(string); ok && strings.Contains(text, notifytest.Hostile) {
				found = true
				if x["type"] != "TextRun" {
					t.Errorf("the hostile value is in a %v", x["type"])
				}
			}
			for k, child := range x {
				if k == "type" && (child == "TextBlock" || child == "FactSet") {
					t.Errorf("the card uses %v, which renders Markdown", child)
				}
				visit(child)
			}
		case []any:
			for _, child := range x {
				visit(child)
			}
		}
	}
	visit(v)
	if !found {
		t.Error("the user name is missing")
	}
}

func TestTeams_Validates(t *testing.T) {
	n := teams.New(notifytest.LocalClient(t))
	for url, ok := range map[string]bool{
		"https://prod-01.westeurope.logic.azure.com/workflows/x/triggers/manual/paths/invoke?sig=y": true,
		"http://prod-01.westeurope.logic.azure.com/workflows/x":                                     false,
	} {
		if err := n.Validate(config(url)); (err == nil) != ok {
			t.Errorf("%s: %v", url, err)
		}
	}
	if f := n.Fields(); n.Type() != "teams" || len(f) != 1 || !f[0].Secret {
		t.Errorf("fields %+v", f)
	}
	srv := notifytest.NewServer(t)
	if err := teams.New(safehttp.New(safehttp.Options{})).Send(context.Background(), config(srv.URL), notifytest.Resolved(notify.English)); !errors.Is(err, safehttp.ErrBlocked) {
		t.Errorf("internal target: %v", err)
	}
}
