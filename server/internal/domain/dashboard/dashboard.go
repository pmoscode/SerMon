// Package dashboard is the model of the custom dashboards (KONZEPT §16, ADR 0040): a grid of 12
// columns with widgets, each with one query of a metric, log lines or alerts, and variables the
// viewer chooses. Validate checks everything the store and the UI rely on; messages name the
// widget or variable, never a value.
package dashboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/metric"
	"github.com/pmoscode/SerMon/server/internal/domain/naming"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Limits of a dashboard (ADR 0040).
const (
	Version        = 1
	Columns        = 12
	Rows           = 100
	MaxHeight      = 12
	MaxWidgets     = 30
	MaxVariables   = 4
	MaxNodes       = 20
	MaxLabels      = 8
	MaxTitle       = 200
	MaxDescription = 2000
	MaxText        = 256
	maxLabelValue  = 256
	maxLogSeverity = 8
	// MaxDocumentBytes bounds the document as JSON: the table keeps 64 KiB of its text, which
	// writes a blank after every ':' and ',' (M5-33).
	MaxDocumentBytes = 48 << 10
)

// ErrInvalid means a document breaks the model.
var ErrInvalid = errors.New("invalid dashboard")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Kind is what a widget shows.
type Kind uint8

// Kinds of widgets.
const (
	KindTimeseries Kind = iota + 1
	KindGauge
	KindStat
	KindTable
	KindLogStream
	KindAlertList
)

func (k Kind) valid() bool { return k >= KindTimeseries && k <= KindAlertList }

// metric reports whether the widget shows a metric.
func (k Kind) metric() bool { return k >= KindTimeseries && k <= KindTable }

// VariableKind is what a variable selects.
type VariableKind uint8

// Kinds of variables.
const (
	VariableNode VariableKind = iota + 1
	VariableGroup
)

// Preset is the default time range.
type Preset uint8

// Presets (KONZEPT §17).
const (
	PresetUnspecified Preset = iota
	Preset15m
	Preset1h
	Preset6h
	Preset24h
	Preset7d
	Preset30d
)

var presetDurations = map[Preset]time.Duration{
	PresetUnspecified: time.Hour, Preset15m: 15 * time.Minute, Preset1h: time.Hour, Preset6h: 6 * time.Hour,
	Preset24h: 24 * time.Hour, Preset7d: 7 * 24 * time.Hour, Preset30d: 30 * 24 * time.Hour,
}

// Duration is the span of the preset; unspecified is the last hour.
func (p Preset) Duration() time.Duration {
	if d, ok := presetDurations[p]; ok {
		return d
	}
	return time.Hour
}

// Position is the place of a widget in cells.
type Position struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

func (p Position) valid() bool {
	return p.X >= 0 && p.Y >= 0 && p.W >= 1 && p.H >= 1 && p.H <= MaxHeight && p.X+p.W <= Columns && p.Y+p.H <= Rows
}

func (p Position) overlaps(o Position) bool {
	return p.X < o.X+o.W && o.X < p.X+p.W && p.Y < o.Y+o.H && o.Y < p.Y+p.H
}

// Target names the nodes of a query: fixed nodes, a group or a variable (at most one).
type Target struct {
	Nodes    []identity.NodeID `json:"nodes,omitempty"`
	Group    *tenant.GroupID   `json:"group,omitempty"`
	Variable string            `json:"variable,omitempty"`
}

// targetJSON is the stored form of a target: IDs as text.
type targetJSON struct {
	Nodes    []uuid.UUID `json:"nodes,omitempty"`
	Group    *uuid.UUID  `json:"group,omitempty"`
	Variable string      `json:"variable,omitempty"`
}

// MarshalJSON writes the IDs as text.
func (t Target) MarshalJSON() ([]byte, error) {
	out := targetJSON{Variable: t.Variable}
	for _, n := range t.Nodes {
		out.Nodes = append(out.Nodes, uuid.UUID(n))
	}
	if t.Group != nil {
		g := uuid.UUID(*t.Group)
		out.Group = &g
	}
	return json.Marshal(out) //nolint:wrapcheck // plain values
}

// UnmarshalJSON reads what MarshalJSON writes.
func (t *Target) UnmarshalJSON(data []byte) error {
	var in targetJSON
	if err := json.Unmarshal(data, &in); err != nil {
		return err //nolint:wrapcheck // the decoder's message
	}
	*t = Target{Variable: in.Variable}
	for _, n := range in.Nodes {
		t.Nodes = append(t.Nodes, identity.NodeID(n))
	}
	if in.Group != nil {
		g := tenant.GroupID(*in.Group)
		t.Group = &g
	}
	return nil
}

func (t Target) count() int {
	n := 0
	if len(t.Nodes) > 0 {
		n++
	}
	if t.Group != nil {
		n++
	}
	if t.Variable != "" {
		n++
	}
	return n
}

// Query is the one query of a widget.
type Query struct {
	Metric string            `json:"metric,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
	Target Target            `json:"target"`
	// Text is part of a log line (log stream), as typed.
	Text string `json:"text,omitempty"`
	// MinSeverity is the least severe log severity shown, 1 (emergency) to 8 (debug); 0 all.
	MinSeverity int `json:"minSeverity,omitempty"`
}

// Widget is one tile.
type Widget struct {
	ID       string   `json:"id"`
	Kind     Kind     `json:"kind"`
	Title    string   `json:"title"`
	Position Position `json:"position"`
	Query    Query    `json:"query"`
}

// Variable is a choice of the viewer.
type Variable struct {
	Name    string       `json:"name"`
	Kind    VariableKind `json:"kind"`
	Default uuid.UUID    `json:"default"` // zero: none
}

// Spec is the document of a dashboard.
type Spec struct {
	Version     int        `json:"version"`
	Title       string     `json:"title"`
	Description string     `json:"description,omitempty"`
	TimeRange   Preset     `json:"timeRange"`
	Variables   []Variable `json:"variables,omitempty"`
	Widgets     []Widget   `json:"widgets,omitempty"`
}

var (
	widgetID     = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	variableName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	labelName    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
)

// Validate checks the document and trims its titles.
func (s *Spec) Validate() error {
	if s.Version != Version {
		return invalid("unknown version %d", s.Version)
	}
	title, ok := naming.Clean(s.Title, MaxTitle)
	if !ok {
		return invalid("the title needs 1 to %d visible characters", MaxTitle)
	}
	s.Title = title
	if !validText(s.Description, MaxDescription) {
		return invalid("the description has at most %d characters without control characters", MaxDescription)
	}
	if _, ok := presetDurations[s.TimeRange]; !ok {
		return invalid("unknown time range")
	}
	variables, err := s.validateVariables()
	if err != nil {
		return err
	}
	if len(s.Widgets) > MaxWidgets {
		return invalid("at most %d widgets", MaxWidgets)
	}
	seen := make(map[string]bool, len(s.Widgets))
	for i := range s.Widgets {
		w := &s.Widgets[i]
		if !widgetID.MatchString(w.ID) {
			return invalid("widget id %d: 1 to 32 of a-z, 0-9, _ and -", i+1)
		}
		if seen[w.ID] {
			return invalid("widget %q is there twice", w.ID)
		}
		seen[w.ID] = true
		if err := w.validate(variables); err != nil {
			return err
		}
		for _, other := range s.Widgets[:i] {
			if w.Position.overlaps(other.Position) {
				return invalid("widget %q overlaps %q", w.ID, other.ID)
			}
		}
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return invalid("not representable as JSON")
	}
	if len(raw) > MaxDocumentBytes {
		return invalid("the document is too large: at most %d KiB as JSON", MaxDocumentBytes>>10)
	}
	return nil
}

func (s *Spec) validateVariables() (map[string]VariableKind, error) {
	if len(s.Variables) > MaxVariables {
		return nil, invalid("at most %d variables", MaxVariables)
	}
	kinds := make(map[string]VariableKind, len(s.Variables))
	for i, v := range s.Variables {
		if !variableName.MatchString(v.Name) {
			return nil, invalid("variable name %d: a-z first, then a-z, 0-9 and _ (at most 32)", i+1)
		}
		if _, dup := kinds[v.Name]; dup {
			return nil, invalid("variable %q is there twice", v.Name)
		}
		if v.Kind != VariableNode && v.Kind != VariableGroup {
			return nil, invalid("variable %q: kind unknown", v.Name)
		}
		kinds[v.Name] = v.Kind
	}
	return kinds, nil
}

func (w *Widget) validate(variables map[string]VariableKind) error {
	title, ok := naming.Clean(w.Title, MaxTitle)
	if !ok {
		return invalid("widget %q: title needs 1 to %d visible characters", w.ID, MaxTitle)
	}
	w.Title = title
	if !w.Kind.valid() {
		return invalid("widget %q: kind unknown", w.ID)
	}
	if !w.Position.valid() {
		return invalid("widget %q: position outside the grid of %d columns and %d rows (height at most %d)", w.ID, Columns, Rows, MaxHeight)
	}
	if err := w.validateTarget(variables); err != nil {
		return err
	}
	q := w.Query
	switch {
	case w.Kind.metric():
		return w.validateMetric()
	case q.Metric != "" || len(q.Labels) > 0:
		return invalid("widget %q: metric and labels only for metric widgets", w.ID)
	case w.Kind == KindAlertList && (q.Text != "" || q.MinSeverity != 0):
		return invalid("widget %q: text and severity only for log streams", w.ID)
	case len(q.Text) > MaxText || !validText(q.Text, MaxText) || strings.ContainsRune(q.Text, '\n'):
		return invalid("widget %q: text has at most %d bytes on one line", w.ID, MaxText)
	case q.MinSeverity < 0 || q.MinSeverity > maxLogSeverity:
		return invalid("widget %q: severity unknown", w.ID)
	}
	return nil
}

func (w *Widget) validateTarget(variables map[string]VariableKind) error {
	t := w.Query.Target
	switch {
	case t.count() > 1:
		return invalid("widget %q: target names nodes, a group or a variable, not several", w.ID)
	case t.count() == 0 && w.Kind != KindAlertList:
		return invalid("widget %q: target needs nodes, a group or a variable", w.ID)
	case len(t.Nodes) > MaxNodes || hasDuplicates(t.Nodes):
		return invalid("widget %q: at most %d nodes, each once", w.ID, MaxNodes)
	}
	kind, known := variables[t.Variable]
	if t.Variable != "" && !known {
		return invalid("widget %q: unknown variable", w.ID)
	}
	single := len(t.Nodes) == 1 || (t.Variable != "" && kind == VariableNode)
	if (w.Kind == KindGauge || w.Kind == KindStat) && !single {
		return invalid("widget %q: one node for a gauge or stat (a node or a node variable)", w.ID)
	}
	return nil
}

func (w *Widget) validateMetric() error {
	q := w.Query
	d, ok := metric.Lookup(q.Metric)
	if !ok {
		return invalid("widget %q: unknown metric", w.ID)
	}
	if len(q.Labels) > MaxLabels {
		return invalid("widget %q: label filters at most %d", w.ID, MaxLabels)
	}
	for name, value := range q.Labels {
		if !labelName.MatchString(name) || !slices.Contains(d.Labels, name) || len(value) > maxLabelValue {
			return invalid("widget %q: label filter not of the metric", w.ID)
		}
	}
	if q.Text != "" || q.MinSeverity != 0 {
		return invalid("widget %q: text and severity only for log streams", w.ID)
	}
	return nil
}

// References returns the nodes and groups the widget names (without variables).
func (w Widget) References() ([]identity.NodeID, []tenant.GroupID) {
	var groups []tenant.GroupID
	if w.Query.Target.Group != nil {
		groups = append(groups, *w.Query.Target.Group)
	}
	return slices.Clone(w.Query.Target.Nodes), groups
}

// References returns every node and group the document names (widgets and defaults of the
// variables), each once, in the order of the document.
func (s Spec) References() ([]identity.NodeID, []tenant.GroupID) {
	var nodes []identity.NodeID
	var groups []tenant.GroupID
	for _, w := range s.Widgets {
		n, g := w.References()
		nodes = appendNew(nodes, n...)
		groups = appendNew(groups, g...)
	}
	for _, v := range s.Variables {
		switch {
		case v.Default == uuid.Nil:
		case v.Kind == VariableNode:
			nodes = appendNew(nodes, identity.NodeID(v.Default))
		case v.Kind == VariableGroup:
			groups = appendNew(groups, tenant.GroupID(v.Default))
		}
	}
	return nodes, groups
}

// Widget returns the widget with the ID.
func (s Spec) Widget(id string) (Widget, bool) {
	i := slices.IndexFunc(s.Widgets, func(w Widget) bool { return w.ID == id })
	if i < 0 {
		return Widget{}, false
	}
	return s.Widgets[i], true
}

func appendNew[T comparable](list []T, items ...T) []T {
	for _, item := range items {
		if !slices.Contains(list, item) {
			list = append(list, item)
		}
	}
	return list
}

func hasDuplicates[T comparable](list []T) bool {
	seen := make(map[T]bool, len(list))
	for _, item := range list {
		if seen[item] {
			return true
		}
		seen[item] = true
	}
	return false
}

// validText reports whether s has at most maxRunes characters and no control or invisible
// characters except line breaks.
func validText(s string, maxRunes int) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= maxRunes &&
		!strings.ContainsFunc(s, func(r rune) bool { return r != '\n' && (unicode.IsControl(r) || unicode.Is(unicode.Cf, r)) })
}
