package logql_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/logql"
)

func TestSelect_NamesExactlyTheNodes(t *testing.T) {
	a := identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001"))
	b := identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000002"))
	q, err := logql.Select([]identity.NodeID{b, a, b})
	if err != nil {
		t.Fatal(err)
	}
	want := `{node_id=~"0193f5a0-0000-7000-8000-000000000001|0193f5a0-0000-7000-8000-000000000002"}`
	if q.String() != want {
		t.Errorf("String() = %s, want %s (sorted, no duplicates)", q, want)
	}
	if _, err := logql.Select(nil); !errors.Is(err, logql.ErrNoNodes) {
		t.Errorf("no nodes: %v, want ErrNoNodes (never a selector for everything)", err)
	}
	if (logql.Query{}).String() != "" {
		t.Error("the zero query must be empty, not match everything")
	}
}

// Label values and line filters are Go string literals in LogQL (Loki unquotes them with
// strconv): quotes, backslashes and line breaks of the input stay inside the literal.
func TestQuote_KeepsHostileInputInsideTheLiteral(t *testing.T) {
	for in, want := range map[string]string{
		`plain`:            `"plain"`,
		`a"} or {x=~".+`:   `"a\"} or {x=~\".+"`,
		`back\slash`:       `"back\\slash"`,
		"line\nbreak":      `"line\nbreak"`,
		"\x00nul":          `"\x00nul"`,
		"`backtick`":       "\"`backtick`\"",
		"ünicode | pipe":   `"ünicode | pipe"`,
		"\u202ebidi\u202c": `"\u202ebidi\u202c"`,
	} {
		if got := logql.Quote(in); got != want {
			t.Errorf("Quote(%q) = %s, want %s", in, got, want)
		}
	}
}
