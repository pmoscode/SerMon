package logql_test

import (
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/logql"
)

// parsed is what an expression of the builder consists of.
type parsed struct {
	matchers map[string]string // label → value (unquoted)
	ops      map[string]string // label → operator
	filterOp string            // |= or |~, empty without line filter
	filter   string            // unquoted
}

// parse reads `{l=~"v", …} |= "t"` strictly: every value must be a complete Go string literal
// and nothing may follow. A builder output an input could break fails here.
func parse(t *testing.T, expr string) parsed {
	t.Helper()
	p := parsed{matchers: map[string]string{}, ops: map[string]string{}}
	rest, ok := strings.CutPrefix(expr, "{")
	if !ok {
		t.Fatalf("no selector: %s", expr)
	}
	label := regexp.MustCompile(`^([a-z_]+)(=~|=)`)
	for {
		m := label.FindStringSubmatch(rest)
		if m == nil {
			t.Fatalf("no matcher at %q in %s", rest, expr)
		}
		rest = rest[len(m[0]):]
		lit, err := strconv.QuotedPrefix(rest)
		if err != nil {
			t.Fatalf("value of %s is no literal: %q", m[1], rest)
		}
		value, _ := strconv.Unquote(lit)
		if _, dup := p.matchers[m[1]]; dup {
			t.Fatalf("label %s twice in %s", m[1], expr)
		}
		p.matchers[m[1]], p.ops[m[1]] = value, m[2]
		rest = rest[len(lit):]
		if after, ok := strings.CutPrefix(rest, ", "); ok {
			rest = after
			continue
		}
		if after, ok := strings.CutPrefix(rest, "}"); ok {
			rest = after
			break
		}
		t.Fatalf("matcher not closed at %q in %s", rest, expr)
	}
	if rest == "" {
		return p
	}
	for _, op := range []string{" |= ", " |~ "} {
		if after, ok := strings.CutPrefix(rest, op); ok {
			lit, err := strconv.QuotedPrefix(after)
			if err != nil || lit != after {
				t.Fatalf("line filter is not exactly one literal: %q", after)
			}
			p.filterOp = strings.TrimSpace(op)
			p.filter, _ = strconv.Unquote(lit)
			return p
		}
	}
	t.Fatalf("unexpected rest %q in %s", rest, expr)
	return p
}

var (
	nodeA = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001"))
	nodeB = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000002"))
)

func TestLines_BuildsTheSelectorAndTheLineFilter(t *testing.T) {
	q, err := logql.Lines(logql.Filter{
		Nodes: []identity.NodeID{nodeB, nodeA}, Sources: []logql.Source{logql.SourceJournal},
		Units: []string{"nginx.service", "cron.service"}, Severities: []logql.Severity{logql.SeverityError, logql.SeverityCritical},
		Text: "timed out",
	})
	if err != nil {
		t.Fatal(err)
	}
	p := parse(t, q.String())
	want := map[string]string{
		"node_id":  nodeA.String() + "|" + nodeB.String(),
		"source":   "journal",
		"unit":     `cron\.service|nginx\.service`,
		"severity": "critical|error",
	}
	for k, v := range want {
		if p.matchers[k] != v || p.ops[k] != "=~" {
			t.Errorf("%s %s %q, want =~ %q (%s)", k, p.ops[k], p.matchers[k], v, q)
		}
	}
	if p.filterOp != "|=" || p.filter != "timed out" {
		t.Errorf("filter %s %q", p.filterOp, p.filter)
	}

	// Only the nodes, no text: no further matchers, no line filter.
	q, _ = logql.Lines(logql.Filter{Nodes: []identity.NodeID{nodeA}})
	if p := parse(t, q.String()); len(p.matchers) != 1 || p.filterOp != "" {
		t.Errorf("minimal = %s", q)
	}
	// Case-insensitive: a regex of the escaped text.
	q, _ = logql.Lines(logql.Filter{Nodes: []identity.NodeID{nodeA}, Text: "Error (x).*", IgnoreCase: true})
	if p := parse(t, q.String()); p.filterOp != "|~" || p.filter != `(?i)Error \(x\)\.\*` {
		t.Errorf("ignore case = %s → %q", q, p.filter)
	}
}

func TestLines_RejectsWhatIsNotAllowed(t *testing.T) {
	for name, f := range map[string]logql.Filter{
		"no nodes":         {},
		"unknown source":   {Nodes: []identity.NodeID{nodeA}, Sources: []logql.Source{"docker"}},
		"unknown severity": {Nodes: []identity.NodeID{nodeA}, Severities: []logql.Severity{"loud"}},
		"long text":        {Nodes: []identity.NodeID{nodeA}, Text: strings.Repeat("x", logql.MaxTextBytes+1)},
		"empty unit":       {Nodes: []identity.NodeID{nodeA}, Units: []string{""}},
		"too many units":   {Nodes: []identity.NodeID{nodeA}, Units: slices.Repeat([]string{"u"}, logql.MaxUnits+1)},
		"invalid utf8":     {Nodes: []identity.NodeID{nodeA}, Text: "\xff"},
	} {
		if _, err := logql.Lines(f); !errors.Is(err, logql.ErrInvalid) && !errors.Is(err, logql.ErrNoNodes) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestCountBySeverity_WrapsTheLinesInAMetricQuery(t *testing.T) {
	q, err := logql.CountBySeverity(logql.Filter{Nodes: []identity.NodeID{nodeA}, Text: "x"}, 90*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	inner, _ := logql.Lines(logql.Filter{Nodes: []identity.NodeID{nodeA}, Text: "x"})
	if want := "sum by (severity) (count_over_time(" + inner.String() + " [90s]))"; q.String() != want {
		t.Errorf("CountBySeverity = %s, want %s", q, want)
	}
	for _, step := range []time.Duration{0, 500 * time.Millisecond, 25 * time.Hour} {
		if _, err := logql.CountBySeverity(logql.Filter{Nodes: []identity.NodeID{nodeA}}, step); !errors.Is(err, logql.ErrInvalid) {
			t.Errorf("step %v: %v", step, err)
		}
	}
}

// Whatever the user types – quotes, backslashes, braces, pipes, line breaks, regex syntax – stays
// inside its literal: the structure is the same and the literal unquotes to the input.
func FuzzLines(f *testing.F) {
	for _, seed := range []string{`a"} or {x=~".+`, `\`, "line\nbreak", `|= "x"`, "(?i).*", "\u202e", "`", "{}", ""} {
		f.Add(seed, seed, false)
		f.Add(seed, "nginx.service", true)
	}
	f.Fuzz(func(t *testing.T, text, unit string, ignoreCase bool) {
		filter := logql.Filter{Nodes: []identity.NodeID{nodeA}, Units: []string{unit}, Text: text, IgnoreCase: ignoreCase}
		q, err := logql.Lines(filter)
		if err != nil {
			return // rejected input (length, UTF-8, empty unit)
		}
		p := parse(t, q.String())
		if p.matchers["node_id"] != nodeA.String() || p.matchers["unit"] != regexp.QuoteMeta(unit) || len(p.matchers) != 2 {
			t.Fatalf("matchers %v for unit %q", p.matchers, unit)
		}
		want := text
		if ignoreCase {
			want = "(?i)" + regexp.QuoteMeta(text)
		}
		if text != "" && p.filter != want {
			t.Fatalf("filter %q, want %q", p.filter, want)
		}
	})
}
