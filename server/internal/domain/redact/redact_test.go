package redact_test

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/pmoscode/SerMon/server/internal/domain/redact"
)

type sharedCase struct {
	Name   string   `json:"name"`
	Rules  []string `json:"rules"`
	Input  string   `json:"input"`
	Output string   `json:"output"`
}

// The cases the agent's Rust redactor gives the same way (testdata/redaction, M3-12): what the
// UI's test shows is what the agent does.
func TestRedact_TheSharedCasesHold(t *testing.T) {
	raw, err := os.ReadFile("../../../../testdata/redaction/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Cases []sharedCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Cases) < 30 {
		t.Fatalf("%d cases", len(doc.Cases))
	}
	plain := func(s string) string { return strings.ReplaceAll(s, "{{}}", "") }
	for _, c := range doc.Cases {
		rules := make([]string, 0, len(c.Rules))
		for _, r := range c.Rules {
			rules = append(rules, plain(r))
		}
		r, err := redact.New(rules)
		if err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		if got := r.Redact(plain(c.Input)); got != plain(c.Output) {
			t.Errorf("%s: %q, want %q", c.Name, got, plain(c.Output))
		}
	}
}

func TestNew_RejectsRulesThatCannotBeUsed(t *testing.T) {
	for name, rules := range map[string][]string{
		"too many":  make([]string, redact.MaxRules+1),
		"empty":     {"a", ""},
		"too long":  {strings.Repeat("a", redact.MaxPatternBytes+1)},
		"unclosed":  {"ok", "(unclosed"},
		"backref":   {`(a)\1`},
		"lookahead": {`(?=x)`},
	} {
		var invalid *redact.RuleError
		if _, err := redact.New(rules); !errors.As(err, &invalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	_, err := redact.New([]string{"ok", "(unclosed"})
	var invalid *redact.RuleError
	if errors.As(err, &invalid) && invalid.Index != 1 {
		t.Errorf("index = %d, want 1", invalid.Index)
	}
}

func FuzzRedact(f *testing.F) {
	for _, seed := range []string{"password=x", "a\x00b", "-----BEGIN PRIVATE KEY-----", strings.Repeat("A", 100), "id=42"} {
		f.Add(seed)
	}
	r, _ := redact.New([]string{`id=(?P<secret>\d+)?`, `x*`})
	f.Fuzz(func(_ *testing.T, text string) {
		_ = r.Redact(text)
	})
}
