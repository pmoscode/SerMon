package dashboard_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/domain/dashboard"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	nodeA  = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000a1"))
	nodeB  = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000a2"))
	groupX = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000aa"))
)

func chart(id string, x, y, w, h int) dashboard.Widget {
	return dashboard.Widget{
		ID: id, Kind: dashboard.KindTimeseries, Title: "CPU " + id,
		Position: dashboard.Position{X: x, Y: y, W: w, H: h},
		Query:    dashboard.Query{Metric: "sermon_cpu_utilization_ratio", Target: dashboard.Target{Nodes: []identity.NodeID{nodeA}}},
	}
}

func valid() dashboard.Spec {
	return dashboard.Spec{
		Version: 1, Title: "Web", TimeRange: dashboard.Preset1h,
		Variables: []dashboard.Variable{{Name: "node", Kind: dashboard.VariableNode, Default: uuid.UUID(nodeB)}},
		Widgets: []dashboard.Widget{
			chart("cpu", 0, 0, 6, 4),
			{
				ID: "load", Kind: dashboard.KindStat, Title: "Load", Position: dashboard.Position{X: 6, Y: 0, W: 6, H: 4},
				Query: dashboard.Query{Metric: "sermon_load_average", Labels: map[string]string{"period": "1m"}, Target: dashboard.Target{Variable: "node"}},
			},
			{
				ID: "logs", Kind: dashboard.KindLogStream, Title: "Logs", Position: dashboard.Position{X: 0, Y: 4, W: 12, H: 4},
				Query: dashboard.Query{Target: dashboard.Target{Group: &groupX}, Text: "error", MinSeverity: 4},
			},
			{ID: "alerts", Kind: dashboard.KindAlertList, Title: "Alerts", Position: dashboard.Position{X: 0, Y: 8, W: 4, H: 3}},
		},
	}
}

func TestSpec_ValidAcceptsEveryKindOfWidget(t *testing.T) {
	s := valid()
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

func TestSpec_ValidateRejects(t *testing.T) {
	tests := []struct {
		name   string
		change func(s *dashboard.Spec)
		want   string // part of the message
	}{
		{"unknown version", func(s *dashboard.Spec) { s.Version = 2 }, "version"},
		{"empty title", func(s *dashboard.Spec) { s.Title = "  " }, "title"},
		{"title with control characters", func(s *dashboard.Spec) { s.Title = "a\u202eb" }, "title"},
		{"long description", func(s *dashboard.Spec) { s.Description = strings.Repeat("x", 2001) }, "description"},
		{"description with control characters", func(s *dashboard.Spec) { s.Description = "a\x07b" }, "description"},
		{"too many widgets", func(s *dashboard.Spec) {
			s.Widgets = nil
			for i := range 31 {
				s.Widgets = append(s.Widgets, chart("w"+strings.Repeat("x", i), 0, i*3%99, 1, 1))
			}
		}, "at most 30 widgets"},
		{"duplicate widget id", func(s *dashboard.Spec) { s.Widgets[1].ID = "cpu" }, `widget "cpu"`},
		{"bad widget id", func(s *dashboard.Spec) { s.Widgets[0].ID = "CPU!" }, "widget id"},
		{"widget title", func(s *dashboard.Spec) { s.Widgets[0].Title = "" }, `widget "cpu": title`},
		{"unknown kind", func(s *dashboard.Spec) { s.Widgets[0].Kind = 0 }, `widget "cpu": kind`},
		{"beyond the columns", func(s *dashboard.Spec) { s.Widgets[0].Position = dashboard.Position{X: 8, Y: 20, W: 6, H: 2} }, `widget "cpu": position`},
		{"beyond the rows", func(s *dashboard.Spec) { s.Widgets[0].Position = dashboard.Position{X: 0, Y: 95, W: 6, H: 6} }, `widget "cpu": position`},
		{"zero width", func(s *dashboard.Spec) { s.Widgets[0].Position.W = 0 }, `widget "cpu": position`},
		{"too high", func(s *dashboard.Spec) { s.Widgets[0].Position.H = 13 }, `widget "cpu": position`},
		{"negative", func(s *dashboard.Spec) { s.Widgets[0].Position.X = -1 }, `widget "cpu": position`},
		{"overlap", func(s *dashboard.Spec) { s.Widgets[1].Position.X = 5 }, `widget "load" overlaps "cpu"`},
		{"unknown metric", func(s *dashboard.Spec) { s.Widgets[0].Query.Metric = "sermon_nope_total" }, `widget "cpu": unknown metric`},
		{"metric missing", func(s *dashboard.Spec) { s.Widgets[0].Query.Metric = "" }, `widget "cpu": unknown metric`},
		{"unknown label", func(s *dashboard.Spec) { s.Widgets[1].Query.Labels = map[string]string{"device": "sda"} }, `widget "load": label`},
		{"too many labels", func(s *dashboard.Spec) {
			s.Widgets[1].Query.Labels = map[string]string{"a": "", "b": "", "c": "", "d": "", "e": "", "f": "", "g": "", "h": "", "i": ""}
		}, `widget "load": label`},
		{"metric widget without target", func(s *dashboard.Spec) { s.Widgets[0].Query.Target = dashboard.Target{} }, `widget "cpu": target`},
		{"two targets", func(s *dashboard.Spec) { s.Widgets[0].Query.Target.Variable = "node" }, `widget "cpu": target`},
		{"too many nodes", func(s *dashboard.Spec) {
			for range 20 {
				s.Widgets[0].Query.Target.Nodes = append(s.Widgets[0].Query.Target.Nodes, identity.NodeID(uuid.New()))
			}
		}, `widget "cpu": at most 20 nodes`},
		{"duplicate nodes", func(s *dashboard.Spec) {
			s.Widgets[0].Query.Target.Nodes = []identity.NodeID{nodeA, nodeA}
		}, `widget "cpu": at most 20 nodes`},
		{"unknown variable", func(s *dashboard.Spec) { s.Widgets[1].Query.Target.Variable = "host" }, `widget "load": unknown variable`},
		{"stat of several nodes", func(s *dashboard.Spec) {
			s.Widgets[1].Query.Target = dashboard.Target{Nodes: []identity.NodeID{nodeA, nodeB}}
		}, `widget "load": one node`},
		{"stat of a group", func(s *dashboard.Spec) { s.Widgets[1].Query.Target = dashboard.Target{Group: &groupX} }, `widget "load": one node`},
		{"stat of a group variable", func(s *dashboard.Spec) {
			s.Variables = append(s.Variables, dashboard.Variable{Name: "group", Kind: dashboard.VariableGroup})
			s.Widgets[1].Query.Target = dashboard.Target{Variable: "group"}
		}, `widget "load": one node`},
		{"log stream with a metric", func(s *dashboard.Spec) { s.Widgets[2].Query.Metric = "sermon_cpu_utilization_ratio" }, `widget "logs": metric`},
		{"log stream without target", func(s *dashboard.Spec) { s.Widgets[2].Query.Target = dashboard.Target{} }, `widget "logs": target`},
		{"log stream with long text", func(s *dashboard.Spec) { s.Widgets[2].Query.Text = strings.Repeat("x", 257) }, `widget "logs": text`},
		{"log severity out of range", func(s *dashboard.Spec) { s.Widgets[2].Query.MinSeverity = 9 }, `widget "logs": severity`},
		{"text on a chart", func(s *dashboard.Spec) { s.Widgets[0].Query.Text = "x" }, `widget "cpu": text`},
		{"alert list with a metric", func(s *dashboard.Spec) { s.Widgets[3].Query.Metric = "sermon_cpu_utilization_ratio" }, `widget "alerts": metric`},
		{"too many variables", func(s *dashboard.Spec) {
			for _, n := range []string{"a", "b", "c", "d"} {
				s.Variables = append(s.Variables, dashboard.Variable{Name: n, Kind: dashboard.VariableNode})
			}
		}, "at most 4 variables"},
		{"duplicate variable", func(s *dashboard.Spec) {
			s.Variables = append(s.Variables, dashboard.Variable{Name: "node", Kind: dashboard.VariableGroup})
		}, `variable "node"`},
		{"bad variable name", func(s *dashboard.Spec) { s.Variables[0].Name = "Node" }, "variable name"},
		{"unknown variable kind", func(s *dashboard.Spec) { s.Variables[0].Kind = 0 }, `variable "node": kind`},
		{"unknown time range", func(s *dashboard.Spec) { s.TimeRange = 99 }, "time range"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := valid()
			tt.change(&s)
			err := s.Validate()
			if !errors.Is(err, dashboard.ErrInvalid) {
				t.Fatalf("Validate() = %v, want ErrInvalid", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Validate() = %q, want it to name %q", err, tt.want)
			}
		})
	}
}

func TestSpec_ValidateTrimsTheTitles(t *testing.T) {
	s := valid()
	s.Title = "  Web  "
	s.Widgets[0].Title = " CPU "
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if s.Title != "Web" || s.Widgets[0].Title != "CPU" {
		t.Errorf("titles = %q, %q", s.Title, s.Widgets[0].Title)
	}
}

func TestSpec_ReferencesNameEveryNodeAndGroup(t *testing.T) {
	s := valid()
	s.Variables = append(s.Variables, dashboard.Variable{Name: "group", Kind: dashboard.VariableGroup, Default: uuid.UUID(groupX)})
	nodes, groups := s.References()
	if len(nodes) != 2 || nodes[0] != nodeA || nodes[1] != nodeB {
		t.Errorf("nodes = %v, want the fixed node and the default of the variable", nodes)
	}
	if len(groups) != 1 || groups[0] != groupX {
		t.Errorf("groups = %v, want the group once (widget and default)", groups)
	}
}

func TestSpec_ReferencesOfAWidgetNameItsOwnNodes(t *testing.T) {
	s := valid()
	nodes, groups := s.Widgets[0].References()
	if len(nodes) != 1 || nodes[0] != nodeA || len(groups) != 0 {
		t.Errorf("References() = %v, %v", nodes, groups)
	}
}

func TestPreset_Duration(t *testing.T) {
	if got := dashboard.Preset24h.Duration().Hours(); got != 24 {
		t.Errorf("24h = %v hours", got)
	}
	if got := dashboard.PresetUnspecified.Duration().Hours(); got != 1 {
		t.Errorf("unspecified = %v hours, want the last hour", got)
	}
}

// The store keeps the document as JSON: IDs as text, every field back as it was.
func TestSpec_JSONRoundTrip(t *testing.T) {
	s := valid()
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"nodes":["`+uuid.UUID(nodeA).String()+`"]`) || !strings.Contains(string(data), `"group":"`+uuid.UUID(groupX).String()+`"`) {
		t.Errorf("JSON = %s, want the IDs as text", data)
	}
	var back dashboard.Spec
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, s) {
		t.Errorf("round trip = %+v, want %+v", back, s)
	}
}
