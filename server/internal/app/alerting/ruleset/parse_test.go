package ruleset_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/alerting"
	"github.com/pmoscode/SerMon/server/internal/app/alerting/ruleset"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/secevent"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// A CPU rule on the idle time (KONZEPT §12.2 compares its complement, the use).
const cpuRule = `apiVersion: sermon/v1
kind: AlertRule
metadata:
  id: system.cpu.usage_high
  name: { de: "Hohe CPU-Auslastung", en: "High CPU usage" }
  labels: { category: cpu }
spec:
  type: threshold
  metric: sermon_cpu_utilization_ratio
  labels: { mode: idle }
  aggregate: avg
  window: 10m
  warning: { op: "<", value: 0.15 }
  critical: { op: "<", value: 0.05 }
  clear: { op: ">", value: 0.20 }
  for: 5m
  selector: { groups: [ "*" ] }
  help:
    de: "Die CPU war über 10 Minuten im Mittel stark ausgelastet."
    en: "The CPU was busy on average over 10 minutes."
`

func TestParse_BuildsTheRule(t *testing.T) {
	def, err := ruleset.Parse([]byte(cpuRule))
	if err != nil {
		t.Fatal(err)
	}
	rule := def.Rule
	if rule.ID != "system.cpu.usage_high" || rule.Timing.For != 5*time.Minute || rule.Timing.Flapping != alert.DefaultFlapping || len(rule.Groups) != 0 {
		t.Errorf("rule = %+v", rule)
	}
	th, ok := rule.Evaluator.(alerting.ThresholdRule)
	if !ok {
		t.Fatalf("evaluator = %T, want a threshold rule", rule.Evaluator)
	}
	if th.Series.Metric != "sermon_cpu_utilization_ratio" || th.Series.Labels["mode"] != "idle" || th.Aggregate != alerting.Avg ||
		th.Window != 10*time.Minute || th.Thresholds.Warning.Op != alert.Below || th.Thresholds.Critical.Value != 0.05 || th.Thresholds.Clear.Value != 0.20 {
		t.Errorf("threshold rule = %+v", th)
	}
	if def.Doc.GetMetadata().GetName().GetEn() != "High CPU usage" || def.Doc.GetSpec().GetHelp().GetDe() == "" {
		t.Errorf("document = %v", def.Doc)
	}
}

// complement compares 1 − a ratio: the CPU's use from its idle time, with thresholds on the use.
func TestParse_Complement(t *testing.T) {
	use := strings.NewReplacer(
		"labels: { mode: idle }", "labels: { mode: idle }\n  complement: true",
		`{ op: "<", value: 0.15 }`, `{ op: ">", value: 0.85 }`,
		`{ op: "<", value: 0.05 }`, `{ op: ">", value: 0.95 }`,
		`{ op: ">", value: 0.20 }`, `{ op: "<", value: 0.80 }`,
	).Replace(cpuRule)
	def, err := ruleset.Parse([]byte(use))
	if err != nil {
		t.Fatal(err)
	}
	if th, ok := def.Rule.Evaluator.(alerting.ThresholdRule); !ok || !th.Complement || th.Thresholds.Critical.Op != alert.Above {
		t.Errorf("evaluator = %+v", def.Rule.Evaluator)
	}
	for name, tc := range map[string]struct {
		replace []string
		hint    string
	}{
		"of a rate":     {[]string{"type: threshold", "type: rate", "  aggregate: avg\n", ""}, "complement"},
		"of bytes":      {[]string{"sermon_cpu_utilization_ratio", "sermon_memory_available_bytes", "labels: { mode: idle }\n  ", ""}, "complement"},
		"in percent":    {[]string{`{ op: ">", value: 0.85 }`, `{ op: ">", value: 85 }`}, "0..1"},
		"not a boolean": {[]string{"complement: true", "complement: yes please"}, "complement"},
	} {
		t.Run(name, func(t *testing.T) {
			doc := strings.NewReplacer(tc.replace...).Replace(use)
			if _, err := ruleset.Parse([]byte(doc)); !errors.Is(err, alert.ErrInvalidRule) || !strings.Contains(err.Error(), tc.hint) {
				t.Errorf("Parse() = %v, want an invalid rule about %q", err, tc.hint)
			}
		})
	}
}

func TestParse_EachTypeAndItsOptions(t *testing.T) {
	group := uuid.MustParse("0193f5a0-0000-7000-8000-0000000000c1")
	def, err := ruleset.Parse([]byte(strings.NewReplacer(
		"type: threshold", "type: rate",
		"sermon_cpu_utilization_ratio", "sermon_network_receive_errors_per_second",
		"labels: { mode: idle }", "dimensions: [interface]\n  exclude: { interface: [\"wg*\"] }\n  relativeTo: sermon_network_receive_packets_per_second",
		"  aggregate: avg\n", "",
		`{ op: "<", value: 0.15 }`, `{ op: ">", value: 0.001 }`,
		`{ op: "<", value: 0.05 }`, `{ op: ">", value: 0.01 }`,
		`{ op: ">", value: 0.20 }`, `{ op: "<", value: 0.0005 }`,
		`groups: [ "*" ]`, `groups: [ "`+group.String()+`" ]`,
		"for: 5m", "for: 5m\n  flapping: { changes: 3, window: 1h }",
	).Replace(cpuRule)))
	if err != nil {
		t.Fatal(err)
	}
	rate, ok := def.Rule.Evaluator.(alerting.RateRule)
	if !ok || rate.RelativeTo != "sermon_network_receive_packets_per_second" || rate.Series.Exclude["interface"][0] != "wg*" ||
		rate.Series.Dimensions[0] != "interface" || rate.Window != 10*time.Minute {
		t.Errorf("rate rule = %+v", def.Rule.Evaluator)
	}
	if def.Rule.Timing.Flapping != (alert.Flapping{Changes: 3, Window: time.Hour}) || len(def.Rule.Groups) != 1 || def.Rule.Groups[0] != tenant.GroupID(group) {
		t.Errorf("rule = %+v", def.Rule)
	}
}

func TestParse_RejectsInvalidRules(t *testing.T) {
	for name, tc := range map[string]struct{ old, new, hint string }{
		"unknown field":         {"window: 10m", "window: 10m\n  windwo: 5m", `line 13: unknown field "windwo" in spec`},
		"unknown nested field":  {"value: 0.15 }", "value: 0.15, level: 2 }", `line 13: unknown field "level" in spec.warning`},
		"unknown top field":     {"kind: AlertRule", "kind: AlertRule\nstatus: {}", `line 3: unknown field "status"`},
		"wrong value type":      {"value: 0.15 }", `value: "low" }`, "value"},
		"wrong api version":     {"sermon/v1", "sermon/v2", "api_version"},
		"invalid id":            {"system.cpu.usage_high", "CPU high", "id"},
		"missing help":          {"    en: \"The CPU was busy on average over 10 minutes.\"\n", "", "help.en"},
		"unknown type":          {"type: threshold", "type: query", "type"},
		"ratio in percent":      {`{ op: "<", value: 0.15 }`, `{ op: "<", value: 15 }`, "0..1"},
		"negative ratio":        {`{ op: ">", value: 0.20 }`, `{ op: ">", value: -0.2 }`, "0..1"},
		"field of another type": {"window: 10m", "window: 10m\n  lookback: 24h", "lookback"},
		"bad duration":          {"window: 10m", "window: 10 minutes", "window"},
		"negative duration":     {"for: 5m", "for: -5m", "for"},
		"unknown metric":        {"sermon_cpu_utilization_ratio", "sermon_cpu_usage", "sermon_cpu_usage"},
		"label not covered":     {"labels: { mode: idle }", "labels: {}", "mode"},
		"no aggregate":          {"  aggregate: avg\n", "", "aggregate"},
		"mixed selector":        {`groups: [ "*" ]`, `groups: [ "*", "0193f5a0-0000-7000-8000-0000000000c1" ]`, "selector"},
		"group no uuid":         {`groups: [ "*" ]`, `groups: [ "web" ]`, "selector"},
		"tag key":               {`groups: [ "*" ]`, `groups: [ "*" ], tags: { Env: prod }`, "tags"},
		"tag value":             {`groups: [ "*" ]`, `groups: [ "*" ], tags: { env: "<prod>" }`, "tags"},
		"thresholds disagree":   {`{ op: "<", value: 0.05 }`, `{ op: ">", value: 0.95 }`, "direction"},
		"two documents":         {"apiVersion: sermon/v1", "apiVersion: sermon/v1\nkind: AlertRule\n---\napiVersion: sermon/v1", "one rule"},
		"not yaml":              {"spec:", "spec: [", "yaml"},
		"non-string keys":       {"labels: { mode: idle }", "labels: { 1: idle }", ""},
		"exclude no string":     {"labels: { mode: idle }", "labels: { mode: idle }\n  exclude: { mode: [ 1 ] }", "exclude.mode"},
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(cpuRule, tc.old) {
				t.Fatalf("%q not in the example", tc.old)
			}
			_, err := ruleset.Parse([]byte(strings.Replace(cpuRule, tc.old, tc.new, 1)))
			if !errors.Is(err, alert.ErrInvalidRule) {
				t.Fatalf("Parse() = %v, want an invalid rule", err)
			}
			if !strings.Contains(err.Error(), tc.hint) {
				t.Errorf("error %q does not point to %q", err, tc.hint)
			}
		})
	}
}

// The selector names tags (M2-29): the rule applies only to nodes that carry all of them.
func TestParse_SelectorWithTags(t *testing.T) {
	doc := strings.Replace(cpuRule, `selector: { groups: [ "*" ] }`, `selector: { tags: { env: prod, role: db } }`, 1)
	def, err := ruleset.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if got := def.Rule.Tags; len(got) != 2 || got["env"] != "prod" || got["role"] != "db" || len(def.Rule.Groups) != 0 {
		t.Errorf("rule tags %v, groups %v", got, def.Rule.Groups)
	}
}

func TestParse_RejectsOversizedDocuments(t *testing.T) {
	big := strings.Replace(cpuRule, "en: \"The CPU", "en: \"The CPU"+strings.Repeat(" ", ruleset.MaxDocumentBytes), 1)
	if _, err := ruleset.Parse([]byte(big)); !errors.Is(err, alert.ErrInvalidRule) || !strings.Contains(err.Error(), "large") {
		t.Errorf("Parse() = %v, want too large", err)
	}
}

// What a rule stores (overrides, M2-17) is read back the same.
func TestMarshal_RoundTrips(t *testing.T) {
	def, err := ruleset.Parse([]byte(cpuRule))
	if err != nil {
		t.Fatal(err)
	}
	data, err := ruleset.Marshal(def.Doc)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ruleset.Parse(data)
	if err != nil || again.Rule.ID != def.Rule.ID || again.Rule.Timing != def.Rule.Timing {
		t.Errorf("round trip = %+v, %v", again.Rule, err)
	}
}

func TestLoad_ChecksFileNamesAndDuplicates(t *testing.T) {
	ok := fstest.MapFS{
		"cpu/system.cpu.usage_high.yaml": {Data: []byte(cpuRule)},
		"README.md":                      {Data: []byte("# rules")},
	}
	defs, err := ruleset.Load(ok)
	if err != nil || len(defs) != 1 {
		t.Fatalf("Load() = %d rules, %v", len(defs), err)
	}
	for name, fsys := range map[string]fstest.MapFS{
		"file name is not the id":       {"cpu/cpu_high.yaml": {Data: []byte(cpuRule)}},
		"directory is not the category": {"memory/system.cpu.usage_high.yaml": {Data: []byte(cpuRule)}},
		"duplicate id": {
			"cpu/system.cpu.usage_high.yaml":    {Data: []byte(cpuRule)},
			"memory/system.cpu.usage_high.yaml": {Data: []byte(strings.Replace(cpuRule, "category: cpu", "category: memory", 1))},
		},
		"nested directory": {"cpu/more/system.cpu.usage_high.yaml": {Data: []byte(cpuRule)}},
		"invalid rule":     {"cpu/system.cpu.usage_high.yaml": {Data: []byte(strings.Replace(cpuRule, "10m", "ten", 1))}},
		"yml":              {"cpu/system.cpu.usage_high.yml": {Data: []byte(cpuRule)}},
	} {
		_, err := ruleset.Load(fsys)
		if !errors.Is(err, alert.ErrInvalidRule) {
			t.Errorf("%s: Load() = %v, want an invalid rule", name, err)
			continue
		}
		for file := range fsys {
			if !strings.Contains(err.Error(), file) {
				t.Errorf("%s: error %q does not name %s", name, err, file)
			}
		}
	}
}

// The event example of KONZEPT §12.2, with help.
const bruteforceRule = `apiVersion: sermon/v1
kind: AlertRule
metadata:
  id: security.ssh.bruteforce
  name: { de: "SSH-Brute-Force", en: "SSH brute force" }
  labels: { category: security }
spec:
  type: event
  event: ssh.login.failed
  groupBy: [ ip ]
  count: { window: 5m, warning: 10, critical: 50 }
  notifyFields: [ ts, user, ip, port ]
  help:
    de: "Viele fehlgeschlagene SSH-Anmeldungen von einer Adresse."
    en: "Many failed SSH logins from one address."
`

func TestParse_EventRule(t *testing.T) {
	def, err := ruleset.Parse([]byte(bruteforceRule))
	if err != nil {
		t.Fatal(err)
	}
	ev, ok := def.Rule.Evaluator.(alerting.EventRule)
	if !ok {
		t.Fatalf("evaluator = %T, want an event rule", def.Rule.Evaluator)
	}
	if ev.Kind != secevent.SSHLoginFailed || !slices.Equal(ev.GroupBy, []string{"ip"}) || ev.Window != 5*time.Minute ||
		ev.Warning != 10 || ev.Critical != 50 || ev.Count != alerting.CountEvents || ev.After != nil ||
		!slices.Equal(ev.NotifyFields, []string{"ts", "user", "ip", "port"}) {
		t.Errorf("event rule = %+v", ev)
	}

	def, err = ruleset.Parse([]byte(strings.NewReplacer(
		"event: ssh.login.failed", "event: ssh.login.success\n  match: { user: root }\n  after: { event: ssh.login.failed, count: 5, window: 1h }",
		"window: 5m, warning: 10, critical: 50", "of: nodes, window: 15m, critical: 3",
		"notifyFields:", "learning: 168h\n  notifyFields:",
	).Replace(bruteforceRule)))
	if err != nil {
		t.Fatal(err)
	}
	ev = def.Rule.Evaluator.(alerting.EventRule) //nolint:forcetypeassert // checked above
	if ev.Kind != secevent.SSHLoginSucceeded || ev.Match["user"] != "root" || ev.Count != alerting.CountNodes || ev.Warning != 0 || ev.Critical != 3 ||
		ev.After == nil || *ev.After != (alerting.EventCondition{Kind: secevent.SSHLoginFailed, Count: 5, Window: time.Hour}) ||
		ev.Learning != 7*24*time.Hour {
		t.Errorf("event rule with options = %+v", ev)
	}
}

func TestParse_RejectsInvalidEventRules(t *testing.T) {
	for name, tc := range map[string]struct{ old, new, hint string }{
		"unknown event":        {"event: ssh.login.failed", "event: ssh.login.guessed", "ssh.login.guessed"},
		"no event":             {"  event: ssh.login.failed\n", "", "event"},
		"no count":             {"  count: { window: 5m, warning: 10, critical: 50 }\n", "", "count"},
		"no window":            {"window: 5m, ", "", "window"},
		"no level":             {"warning: 10, critical: 50", "warning: 0", "warning or critical"},
		"group by another":     {"groupBy: [ ip ]", "groupBy: [ port ]", "group_by"},
		"group by missing":     {"event: ssh.login.failed", "event: system.oom_kill", "groupBy"},
		"notify a missing":     {"notifyFields: [ ts, user, ip, port ]", "notifyFields: [ uid ]", "uid"},
		"threshold field":      {"notifyFields:", "warning: { op: \">\", value: 1 }\n  notifyFields:", "warning"},
		"metric field":         {"notifyFields:", "metric: sermon_cpu_utilization_ratio\n  notifyFields:", "metric"},
		"event on threshold":   {"type: event", "type: threshold", "event"},
		"bad after":            {"notifyFields:", "after: { event: ssh.login.failed, count: 0, window: 1h }\n  notifyFields:", "count"},
		"bad after window":     {"notifyFields:", "after: { event: ssh.login.failed, count: 3, window: soon }\n  notifyFields:", "after.window"},
		"counting nodes alone": {"groupBy: [ ip ]\n", "", ""},
		"learning in days":     {"notifyFields:", "learning: 7d\n  notifyFields:", "learning"},
		"learning too long":    {"notifyFields:", "learning: 1000h\n  notifyFields:", "learning"},
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(bruteforceRule, tc.old) {
				t.Fatalf("%q not in the example", tc.old)
			}
			doc := strings.Replace(bruteforceRule, tc.old, tc.new, 1)
			if name == "counting nodes alone" {
				doc = strings.Replace(doc, "window: 5m", "of: nodes, window: 5m", 1)
			}
			_, err := ruleset.Parse([]byte(doc))
			if !errors.Is(err, alert.ErrInvalidRule) {
				t.Fatalf("Parse() = %v, want an invalid rule", err)
			}
			if !strings.Contains(err.Error(), tc.hint) {
				t.Errorf("error %q does not point to %q", err, tc.hint)
			}
		})
	}
}
