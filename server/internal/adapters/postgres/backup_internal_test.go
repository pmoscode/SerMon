package postgres

import (
	"strings"
	"testing"
)

func names(tables []table) string {
	out := make([]string, len(tables))
	for i, t := range tables {
		out[i] = t.name
	}
	return strings.Join(out, ",")
}

// The restore loads each table after the tables it references; equals by name.
func TestOrderByReferences(t *testing.T) {
	tables := []table{{name: "memberships"}, {name: "users"}, {name: "organizations"}, {name: "audit_log"}, {name: "nodes"}}
	refs := map[string][]string{
		"memberships": {"users", "organizations"},
		"audit_log":   {"organizations"},
		"nodes":       {"organizations"},
	}
	got, err := orderByReferences(tables, refs)
	if err != nil {
		t.Fatal(err)
	}
	if names(got) != "organizations,audit_log,nodes,users,memberships" {
		t.Errorf("order = %s", names(got))
	}
}

func TestOrderByReferences_Refuses(t *testing.T) {
	tables := []table{{name: "a"}, {name: "b"}}
	if _, err := orderByReferences(tables, map[string][]string{"a": {"b"}, "b": {"a"}}); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("a cycle: %v", err)
	}
	if _, err := orderByReferences(tables, map[string][]string{"a": {"sessions"}}); err == nil || !strings.Contains(err.Error(), "leaves out") {
		t.Errorf("a reference to an excluded table: %v", err)
	}
}
