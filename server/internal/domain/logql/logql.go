// Package logql builds LogQL from typed parts (ADR 0031, KONZEPT §10.4): users never write LogQL,
// every value is quoted, and a query always names its nodes. Pure, without I/O. The filters of
// the log explorer follow with M3-08.
package logql

import (
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
)

// ErrNoNodes is returned for a query without nodes: it would read every node of the tenant.
var ErrNoNodes = errors.New("a log query needs at least one node")

// Query is a LogQL expression built by this package; the zero value is empty.
type Query struct{ expr string }

// String returns the LogQL text.
func (q Query) String() string { return q.expr }

// Select is the stream selector for the nodes, sorted and without duplicates.
func Select(nodes []identity.NodeID) (Query, error) {
	if len(nodes) == 0 {
		return Query{}, ErrNoNodes
	}
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.String())
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	// UUIDs hold no regex metacharacters; the regex is anchored by Loki.
	return Query{expr: "{node_id=~" + Quote(strings.Join(ids, "|")) + "}"}, nil
}

// MustSelect is Select of one node.
func MustSelect(node identity.NodeID) Query {
	q, _ := Select([]identity.NodeID{node}) // one node: never ErrNoNodes
	return q
}

// Everything selects every stream of an organization (every SerMon stream has a node_id), for
// deleting it (M3-10). Never for reading: queries name the visible nodes.
func Everything() Query { return Query{expr: `{node_id=~".+"}`} }

// Quote returns s as a LogQL string literal. LogQL strings follow Go's syntax (Loki unquotes
// them with strconv), so strconv.Quote keeps any input inside the literal.
func Quote(s string) string { return strconv.Quote(s) }
