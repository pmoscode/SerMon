package module_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/domain/module"
)

const nginx = `{"type":"object","additionalProperties":false,"required":["url"],"properties":{
"url":{"type":"string","format":"uri","maxLength":512,"default":"http://127.0.0.1/stub_status"},
"port":{"type":"integer","minimum":1,"maximum":65535},
"password":{"type":"string","writeOnly":true,"maxLength":64}}}`

func catalog(t *testing.T) *module.Catalog {
	t.Helper()
	c, err := module.NewCatalog(map[string]string{"nginx": nginx})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// The agent's schemas (copied by just gen) are in the subset; simulate is among them.
func TestBuiltin_HasTheAgentsSchemas(t *testing.T) {
	c, err := module.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	s, ok := c.Lookup("simulate")
	if !ok || strings.Join(s.Secrets(), ",") != "token" || !strings.Contains(s.Raw(), `"behavior"`) {
		t.Fatalf("simulate: %v %v", s, ok)
	}
	if all := c.All(); len(all) == 0 || all[0].ID > all[len(all)-1].ID {
		t.Errorf("All() = %v", all)
	}
}

func TestNewCatalog_RefusesBadSchemasAndIDs(t *testing.T) {
	if _, err := module.NewCatalog(map[string]string{"Nginx": nginx}); err == nil {
		t.Error("bad id accepted")
	}
	if _, err := module.NewCatalog(map[string]string{"nginx": `{"type":"object"}`}); err == nil {
		t.Error("bad schema accepted")
	}
}

func TestCatalog_Check(t *testing.T) {
	c := catalog(t)
	got, err := c.Check(module.Setting{Module: "nginx", Enabled: true, Interval: 30 * time.Second, Config: ` {"url": "http://127.0.0.1:8080/"} `}, nil)
	if err != nil || got.Config != `{"url":"http://127.0.0.1:8080/"}` || !got.Enabled || got.Interval != 30*time.Second {
		t.Fatalf("Check() = %+v, %v", got, err)
	}
	for _, tc := range []struct {
		setting module.Setting
		why     string
	}{
		{module.Setting{Module: "redis"}, "unknown"},
		{module.Setting{Module: "nginx", Config: `{}`}, "url: required"},
		{module.Setting{Module: "nginx", Config: `{"url":"http://x/","password":"pw"}`}, "credential"},
		{module.Setting{Module: "nginx", Config: `{"url":"http://x/","port":0}`}, "port"},
		{module.Setting{Module: "nginx", Config: `{"url":"http://x/"}`, Interval: 9 * time.Second}, "interval"},
		{module.Setting{Module: "nginx", Config: `{"url":"http://x/"}`, Interval: time.Hour + time.Second}, "interval"},
		{module.Setting{Module: "nginx", Config: `{"url":"http://x/"}`, Interval: 10*time.Second + time.Millisecond}, "interval"},
		{module.Setting{Module: "nginx", Config: `{"url":"http://x/"` + strings.Repeat(" ", module.MaxConfigBytes) + `}`}, "bytes"},
	} {
		_, err := c.Check(tc.setting, nil)
		if err == nil || !strings.Contains(err.Error(), tc.why) {
			t.Errorf("%+v: %v, want %q", tc.setting, err, tc.why)
		}
		if tc.why != "unknown" && !errors.Is(err, module.ErrInvalid) {
			t.Errorf("%+v: %v is not ErrInvalid", tc.setting, err)
		}
	}
	if _, err := c.Check(module.Setting{Module: "redis"}, nil); !errors.Is(err, module.ErrUnknown) {
		t.Errorf("unknown: %v", err)
	}
	for _, edge := range []time.Duration{0, 10 * time.Second, time.Hour} {
		if _, err := c.Check(module.Setting{Module: "nginx", Config: `{"url":"http://x/"}`, Interval: edge}, nil); err != nil {
			t.Errorf("interval %s: %v", edge, err)
		}
	}
}

func TestHealthReport_Normalized(t *testing.T) {
	c := catalog(t)
	long := strings.Repeat("ä", 200)
	r := module.HealthReport{Modules: []module.Health{
		{Module: "nginx", State: module.StateError, Reason: "refused\u202e" + long},
		{Module: "nginx", State: module.StateOK},
		{Module: "redis", State: module.StateOK},
		{Module: "nginx", State: module.State(99)},
	}}.Normalized(c)
	if len(r.Modules) != 1 || r.Modules[0].State != module.StateError {
		t.Fatalf("%+v", r)
	}
	if reason := r.Modules[0].Reason; len(reason) > module.MaxReasonBytes || strings.ContainsRune(reason, '\u202e') || !strings.HasPrefix(reason, "refused") {
		t.Errorf("reason %q", reason)
	}
	if module.State(0).Valid() || module.State(8).Valid() || !module.StateVetoed.Valid() {
		t.Error("Valid")
	}
}

func TestHints_Normalized(t *testing.T) {
	c := catalog(t)
	h := module.Hints{Hints: []module.Hint{
		{Module: "nginx", Reasons: []string{"process:nginx", "port:80", "port:80", "bogus", "port:80\n"}, Suggested: `{"port": 8080}`},
		{Module: "nginx", Reasons: []string{"process:nginx"}},
		{Module: "redis", Reasons: []string{"process:redis"}},
	}}.Normalized(c)
	if len(h.Hints) != 1 || strings.Join(h.Hints[0].Reasons, ",") != "process:nginx,port:80" || h.Hints[0].Suggested != `{"port":8080}` {
		t.Fatalf("%+v", h)
	}
	for _, bad := range []string{`{"port":0}`, `{"password":"x"}`, `[1]`, `{"port":` + strings.Repeat("1", module.MaxSuggestionBytes) + `}`} {
		h := module.Hints{Hints: []module.Hint{{Module: "nginx", Reasons: []string{"port:80"}, Suggested: bad}}}.Normalized(c)
		if len(h.Hints) != 1 || h.Hints[0].Suggested != "" {
			t.Errorf("%s: %+v", bad, h)
		}
	}
	if h := (module.Hints{Hints: []module.Hint{{Module: "nginx", Reasons: []string{"bogus"}}}}).Normalized(c); len(h.Hints) != 0 {
		t.Errorf("a hint without a valid reason: %+v", h)
	}
}

func TestValidID(t *testing.T) {
	for id, want := range map[string]bool{"nginx": true, "postgresql": true, "a_1": true, "": false, "1a": false, "Nginx": false, strings.Repeat("a", 33): false} {
		if module.ValidID(id) != want {
			t.Errorf("%q", id)
		}
	}
}

func TestForAgent(t *testing.T) {
	got := module.ForAgent([]module.Setting{{Module: "nginx"}, {Module: "redis"}, {Module: "simulate"}}, []string{"simulate", "nginx"})
	if len(got) != 2 || got[0].Module != "nginx" || got[1].Module != "simulate" {
		t.Errorf("%+v", got)
	}
	if got := module.ForAgent([]module.Setting{{Module: "nginx"}}, nil); len(got) != 0 {
		t.Errorf("%+v", got)
	}
}
