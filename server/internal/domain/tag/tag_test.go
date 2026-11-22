package tag_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/pmoscode/SerMon/server/internal/domain/tag"
)

func TestValidate(t *testing.T) {
	many := tag.Tags{}
	for i := range tag.MaxTags + 1 {
		many["k"+strings.Repeat("x", i)] = "v"
	}
	cases := []struct {
		name string
		tags tag.Tags
		ok   bool
	}{
		{"none", nil, true},
		{"env and role", tag.Tags{"env": "prod", "role": "db"}, true},
		{"value with dots, dashes, colons and slashes", tag.Tags{"site": "fra-1.dc:rack/7"}, true},
		{"key with digits, _ and -", tag.Tags{"team_2-a": "ops"}, true},
		{"uppercase value", tag.Tags{"owner": "OpsTeam"}, true},
		{"empty value", tag.Tags{"env": ""}, false},
		{"uppercase key", tag.Tags{"Env": "prod"}, false},
		{"key starts with a digit", tag.Tags{"1env": "prod"}, false},
		{"key too long", tag.Tags{strings.Repeat("k", tag.MaxKeyLen+1): "v"}, false},
		{"value too long", tag.Tags{"env": strings.Repeat("v", tag.MaxValueLen+1)}, false},
		{"space in value", tag.Tags{"env": "pro d"}, false},
		{"markup in value", tag.Tags{"env": "<b>"}, false},
		{"control character", tag.Tags{"env": "prod\n"}, false},
		{"too many", many, false},
	}
	for _, c := range cases {
		err := c.tags.Validate()
		if (err == nil) != c.ok {
			t.Errorf("%s: Validate() = %v, want ok=%v", c.name, err, c.ok)
		}
		if err != nil && !errors.Is(err, tag.ErrInvalid) {
			t.Errorf("%s: error %v is not ErrInvalid", c.name, err)
		}
	}
}

func TestValidate_NamesNoValue(t *testing.T) {
	err := tag.Tags{"env": "secret<value>"}.Validate()
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("Validate() = %v, want an error without the value", err)
	}
}

// The matcher table (M2-29): a selector matches when every pair is on the node; an empty one
// matches every node.
func TestSelects(t *testing.T) {
	node := tag.Tags{"env": "prod", "role": "db"}
	cases := []struct {
		name     string
		selector tag.Tags
		tags     tag.Tags
		want     bool
	}{
		{"empty selector, tagged node", nil, node, true},
		{"empty selector, untagged node", tag.Tags{}, nil, true},
		{"one pair present", tag.Tags{"env": "prod"}, node, true},
		{"all pairs present", tag.Tags{"env": "prod", "role": "db"}, node, true},
		{"other value", tag.Tags{"env": "staging"}, node, false},
		{"missing key", tag.Tags{"site": "fra"}, node, false},
		{"one of two missing", tag.Tags{"env": "prod", "site": "fra"}, node, false},
		{"untagged node", tag.Tags{"env": "prod"}, nil, false},
		{"case matters", tag.Tags{"env": "Prod"}, node, false},
	}
	for _, c := range cases {
		if got := c.selector.Selects(c.tags); got != c.want {
			t.Errorf("%s: Selects = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestString_IsSorted(t *testing.T) {
	tags := tag.Tags{"role": "db", "env": "prod"}
	if got := tags.String(); got != "env=prod,role=db" {
		t.Fatalf("String() = %q", got)
	}
	if got := (tag.Tags{}).String(); got != "" {
		t.Fatalf("empty String() = %q", got)
	}
}
