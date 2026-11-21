package ruleset_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/pmoscode/SerMon/server/internal/app/alerting/ruleset"
)

func TestParseBundle_SplitsDocumentsAndKeepsTheirText(t *testing.T) {
	second := strings.Replace(cpuRule, "system.cpu.usage_high", "system.cpu.usage_very_high", 1)
	bundle := "# exported\n---\n" + cpuRule + "--- # the stricter one\n" + second
	docs := ruleset.ParseBundle([]byte(bundle))
	if len(docs) != 2 {
		t.Fatalf("%d documents: %+v", len(docs), docs)
	}
	if docs[0].Err != nil || docs[0].Def.Rule.ID != "system.cpu.usage_high" || docs[0].Line != 3 || docs[0].Source != cpuRule {
		t.Errorf("first %+v", docs[0])
	}
	if docs[1].Err != nil || docs[1].Def.Rule.ID != "system.cpu.usage_very_high" || docs[1].Line != 3+strings.Count(cpuRule, "\n")+1 {
		t.Errorf("second %+v", docs[1])
	}
}

// Errors name the line in the whole text, not in the document.
func TestParseBundle_ErrorsAtTheirLine(t *testing.T) {
	broken := strings.Replace(cpuRule, "  for: 5m\n", "  for: 5m\n  query: \"rate(x[5m])\"\n", 1)
	bundle := cpuRule + "---\n" + broken
	docs := ruleset.ParseBundle([]byte(bundle))
	if len(docs) != 2 || docs[0].Err != nil || docs[1].Err == nil {
		t.Fatalf("documents %+v", docs)
	}
	before, _, found := strings.Cut(cpuRule, "  for: 5m")
	if !found {
		t.Fatal("the fixture has no for")
	}
	line := strings.Count(cpuRule, "\n") + 2 + strings.Count(before, "\n") + 1
	if want := "line " + strconv.Itoa(line) + `: unknown field "query"`; !strings.Contains(docs[1].Err.Error(), want) {
		t.Errorf("error %v, want %q", docs[1].Err, want)
	}
	// A YAML syntax error is placed as well.
	docs = ruleset.ParseBundle([]byte(cpuRule + "---\napiVersion: [\n"))
	if len(docs) != 2 || docs[1].Err == nil || !strings.Contains(docs[1].Err.Error(), "line ") {
		t.Errorf("syntax error %+v", docs)
	}
	// Empty documents (a trailing separator) are skipped.
	if docs := ruleset.ParseBundle([]byte(cpuRule + "---\n\n")); len(docs) != 1 {
		t.Errorf("trailing separator: %d documents", len(docs))
	}
}

func TestYAML_RendersADocumentThatParsesAgain(t *testing.T) {
	def, err := ruleset.Parse([]byte(cpuRule))
	if err != nil {
		t.Fatal(err)
	}
	out, err := ruleset.YAML(def.Doc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "{") || !strings.HasPrefix(string(out), "apiVersion: sermon/v1\nkind: AlertRule\nmetadata:\n") {
		t.Errorf("not block style:\n%s", out)
	}
	again, err := ruleset.Parse(out)
	if err != nil || again.Rule.ID != def.Rule.ID {
		t.Fatalf("parse again: %v\n%s", err, out)
	}
	if !ruleset.Equal(def.Doc, again.Doc) {
		t.Error("the rendered document differs")
	}
}

// The default rules keep their file text for display.
func TestDefaults_KeepTheirSource(t *testing.T) {
	defs, err := ruleset.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range defs {
		if !strings.Contains(string(d.Source), "id: "+string(d.Rule.ID)) {
			t.Errorf("%s: source %q", d.Rule.ID, d.Source)
		}
	}
}
