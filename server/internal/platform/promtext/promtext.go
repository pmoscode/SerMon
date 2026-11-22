// Package promtext writes metrics in the Prometheus text exposition format 0.0.4 (ADR 0011).
// Values are read from functions at scrape time; there is no global registry.
package promtext

import (
	"bufio"
	"fmt"
	"io"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// ContentType of the exposition format.
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

var (
	namePattern  = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
	labelPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
)

type metric struct {
	name, help, kind string
	labels           string // rendered {…} or empty
	value            func() float64
	// family replaces labels and value: one sample per label value.
	family func() []sample
}

type sample struct {
	labels string
	value  float64
}

// Registry holds the metrics of one endpoint.
type Registry struct {
	metrics []metric
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{} }

// Counter registers a monotonically increasing value. It panics on an invalid name – a
// programming error caught by the tests.
func (r *Registry) Counter(name, help string, value func() float64) {
	r.add(metric{name: name, help: help, kind: "counter", value: value})
}

// CounterFamily registers one counter per value of a label, written sorted by value; it panics on
// an invalid metric or label name. Label values must be bounded (no host or user data).
func (r *Registry) CounterFamily(name, help, label string, values func() map[string]float64) {
	if !labelPattern.MatchString(label) || strings.HasPrefix(label, "__") {
		panic(fmt.Sprintf("promtext: invalid label name %q", label))
	}
	r.add(metric{name: name, help: help, kind: "counter", family: func() []sample {
		v := values()
		keys := slices.Sorted(maps.Keys(v))
		out := make([]sample, len(keys))
		for i, k := range keys {
			out[i] = sample{labels: "{" + label + `="` + escapeLabel(k) + `"}`, value: v[k]}
		}
		return out
	}})
}

// Gauge registers a value that goes up and down; it panics on an invalid name.
func (r *Registry) Gauge(name, help string, value func() float64) {
	r.add(metric{name: name, help: help, kind: "gauge", value: value})
}

// Info registers a gauge with the value 1 carrying information as labels (e.g. the version).
func (r *Registry) Info(name, help string, labels map[string]string) {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		if !labelPattern.MatchString(k) || strings.HasPrefix(k, "__") {
			panic(fmt.Sprintf("promtext: invalid label name %q", k))
		}
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + `="` + escapeLabel(labels[k]) + `"`
	}
	r.add(metric{name: name, help: help, kind: "gauge", labels: "{" + strings.Join(parts, ",") + "}", value: func() float64 { return 1 }})
}

func (r *Registry) add(m metric) {
	if !namePattern.MatchString(m.name) {
		panic(fmt.Sprintf("promtext: invalid metric name %q", m.name))
	}
	if slices.ContainsFunc(r.metrics, func(o metric) bool { return o.name == m.name }) {
		panic(fmt.Sprintf("promtext: metric %q registered twice", m.name))
	}
	r.metrics = append(r.metrics, m)
	slices.SortFunc(r.metrics, func(a, b metric) int { return strings.Compare(a.name, b.name) })
}

// WriteTo writes all metrics, sorted by name.
func (r *Registry) WriteTo(w io.Writer) (int64, error) {
	counter := &countingWriter{w: w}
	buf := bufio.NewWriter(counter)
	for _, m := range r.metrics {
		_, _ = fmt.Fprintf(buf, "# HELP %s %s\n# TYPE %s %s\n", m.name, escapeHelp(m.help), m.name, m.kind)
		var samples []sample
		if m.family != nil {
			samples = m.family()
		} else {
			samples = []sample{{labels: m.labels, value: m.value()}}
		}
		for _, x := range samples {
			_, _ = fmt.Fprintf(buf, "%s%s %s\n", m.name, x.labels, formatValue(x.value))
		}
	}
	if err := buf.Flush(); err != nil {
		return counter.n, fmt.Errorf("write metrics: %w", err)
	}
	return counter.n, nil
}

// Handler serves GET (and HEAD) /metrics.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/metrics" {
			http.NotFound(w, req)
			return
		}
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", ContentType)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if req.Method == http.MethodHead {
			return
		}
		_, _ = r.WriteTo(w) // the client went away; nothing to report
	})
}

func formatValue(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}

func escapeHelp(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(s)
}

func escapeLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`).Replace(s)
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err //nolint:wrapcheck // plain pass-through
}
