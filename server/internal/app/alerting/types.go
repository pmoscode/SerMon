package alerting

import (
	"context"
	"fmt"
	"math"
	"path"
	"slices"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/metric"
)

// Rule types (KONZEPT §12.2, M2-03): threshold, rate, absence, state and prediction. Each is an
// Evaluator over the metrics of the organization's nodes; none takes a free query. Ratios are
// compared as 0..1 (the catalog stores them in ppm).

// maxWindow bounds windows and lookbacks.
const maxWindow = 7 * 24 * time.Hour

// SeriesSpec selects one series per node and dimension: the metric, a label filter, the labels
// that split instances (dimensions) and excluded label values (glob patterns, e.g. /dev/*).
// Filter and dimensions together must name every label of the metric.
type SeriesSpec struct {
	Metric     string
	Labels     metrics.Labels
	Dimensions []string
	Exclude    map[string][]string
}

func (s SeriesSpec) validate() (metric.Definition, error) {
	def, ok := metric.Lookup(s.Metric)
	if !ok {
		return metric.Definition{}, fmt.Errorf("%w: unknown metric %q", alert.ErrInvalidRule, s.Metric)
	}
	covered := map[string]bool{}
	for name := range s.Labels {
		covered[name] = true
	}
	for _, name := range s.Dimensions {
		if covered[name] {
			return metric.Definition{}, fmt.Errorf("%w: label %q is both filter and dimension", alert.ErrInvalidRule, name)
		}
		covered[name] = true
	}
	for name := range s.Exclude {
		if !slices.Contains(s.Dimensions, name) {
			return metric.Definition{}, fmt.Errorf("%w: exclusions apply to dimensions, %q is none", alert.ErrInvalidRule, name)
		}
		for _, pattern := range s.Exclude[name] {
			if _, err := path.Match(pattern, ""); err != nil {
				return metric.Definition{}, fmt.Errorf("%w: bad pattern %q", alert.ErrInvalidRule, pattern)
			}
		}
	}
	for name := range covered {
		if !slices.Contains(def.Labels, name) {
			return metric.Definition{}, fmt.Errorf("%w: %s has no label %q", alert.ErrInvalidRule, s.Metric, name)
		}
	}
	if len(covered) != len(def.Labels) {
		return metric.Definition{}, fmt.Errorf("%w: %s has the labels %v; filter or split by each of them", alert.ErrInvalidRule, s.Metric, def.Labels)
	}
	return def, nil
}

// dimension returns the instance's dimension key, false if the series is excluded.
func (s SeriesSpec) dimension(labels metrics.Labels) (string, bool) {
	if len(s.Dimensions) == 0 {
		return "", true
	}
	dims := metrics.Labels{}
	for _, name := range s.Dimensions {
		value := labels[name]
		for _, pattern := range s.Exclude[name] {
			if ok, _ := path.Match(pattern, value); ok {
				return "", false
			}
		}
		dims[name] = value
	}
	return dims.Key(), true
}

// series is one instance's points.
type series struct {
	node      identity.NodeID
	dimension string
	points    []metrics.Point
}

// query reads the spec's series of all nodes between from and now (in chunks of MaxNodes),
// with values scaled from ppm to ratios.
func (s SeriesSpec) query(ctx context.Context, env Env, from time.Time) ([]series, error) {
	def, ok := metric.Lookup(s.Metric)
	if !ok {
		return nil, fmt.Errorf("%w: unknown metric %q", alert.ErrInvalidRule, s.Metric)
	}
	scale := 1.0
	if def.Unit == metric.UnitRatioPPM {
		scale = 1e-6
	}
	var out []series
	for nodes := range slices.Chunk(env.Nodes, metrics.MaxNodes) {
		ids := make([]identity.NodeID, len(nodes))
		for i, n := range nodes {
			ids[i] = n.ID
		}
		found, err := env.Metrics.Query(ctx, env.Scope, metrics.Query{Metric: s.Metric, Nodes: ids, Labels: s.Labels, From: from, To: env.Now, Step: stepFor(env.Now.Sub(from))})
		if err != nil {
			return nil, fmt.Errorf("query %s: %w", s.Metric, err)
		}
		for _, f := range found {
			dim, ok := s.dimension(f.Labels)
			if !ok || len(f.Points) == 0 {
				continue
			}
			points := make([]metrics.Point, len(f.Points))
			for i, p := range f.Points {
				points[i] = metrics.Point{Time: p.Time, Avg: p.Avg * scale, Min: p.Min * scale, Max: p.Max * scale}
			}
			out = append(out, series{node: f.Node, dimension: dim, points: points})
		}
	}
	return out, nil
}

// Windows longer than coarseSpan are read with a step of about coarsePoints points, in whole
// minutes: from the 1-min aggregate instead of the raw table, as rules run every Interval.
const (
	coarseSpan   = time.Hour
	coarsePoints = 288
)

func stepFor(span time.Duration) time.Duration {
	if span <= coarseSpan {
		return 0
	}
	return max(time.Minute, (span / coarsePoints).Truncate(time.Minute))
}

func validateWindow(name string, w time.Duration) error {
	if w <= 0 || w > maxWindow {
		return fmt.Errorf("%w: %s must be between 0 and %v", alert.ErrInvalidRule, name, maxWindow)
	}
	return nil
}

// Aggregate reduces the points of a window to one value.
type Aggregate int

// Aggregates (KONZEPT §12.2 also names sum; a sum over time buckets depends on their size and is
// not offered).
const (
	Avg Aggregate = iota + 1
	Max
	Min
	Last
)

func (a Aggregate) of(points []metrics.Point) float64 {
	switch a {
	case Max:
		v := math.Inf(-1)
		for _, p := range points {
			v = math.Max(v, p.Max)
		}
		return v
	case Min:
		v := math.Inf(1)
		for _, p := range points {
			v = math.Min(v, p.Min)
		}
		return v
	case Last:
		return points[len(points)-1].Avg
	case Avg:
		fallthrough
	default:
		var sum float64
		for _, p := range points {
			sum += p.Avg
		}
		return sum / float64(len(points))
	}
}

// ThresholdRule compares an aggregate of a metric over a window with the thresholds; with
// RelativeTo the aggregate of a second metric with the same labels and unit divides it (inodes
// used / total).
type ThresholdRule struct {
	Series     SeriesSpec
	RelativeTo string
	Aggregate  Aggregate
	Window     time.Duration
	Thresholds alert.Thresholds
}

// relative checks a metric to divide by: same labels, same unit as def.
func relative(name string, def metric.Definition, ok func(metric.Unit) bool) error {
	rel, found := metric.Lookup(name)
	if !found || !slices.Equal(rel.Labels, def.Labels) || !ok(rel.Unit) {
		return fmt.Errorf("%w: relative_to %q must be a metric with the labels and kind of %s", alert.ErrInvalidRule, name, def.Name)
	}
	return nil
}

// value is the reduced value of one instance.
type value struct {
	node      identity.NodeID
	dimension string
	v         float64
}

func reduce(found []series, aggregate Aggregate, factor float64) []value {
	out := make([]value, len(found))
	for i, s := range found {
		out[i] = value{node: s.node, dimension: s.dimension, v: aggregate.of(s.points) * factor}
	}
	return out
}

// divideBy divides each value by the matching total; values without a total drop out, as do
// values over a zero total – unless both are zero (no traffic, no errors).
func divideBy(values, totals []value) []value {
	base := make(map[string]float64, len(totals))
	for _, t := range totals {
		base[t.node.String()+"|"+t.dimension] = t.v
	}
	out := make([]value, 0, len(values))
	for _, v := range values {
		total, ok := base[v.node.String()+"|"+v.dimension]
		switch {
		case !ok:
		case total == 0 && v.v == 0:
			out = append(out, value{node: v.node, dimension: v.dimension, v: 0})
		case total == 0:
		default:
			out = append(out, value{node: v.node, dimension: v.dimension, v: v.v / total})
		}
	}
	return out
}

func results(values []value, th alert.Thresholds) []Result {
	out := make([]Result, len(values))
	for i, v := range values {
		out[i] = Result{Node: v.node, Dimension: v.dimension, Observation: th.Evaluate(v.v)}
	}
	return out
}

// relativeSeries reads the series of the relative metric with the spec's filter and dimensions.
func (s SeriesSpec) relativeSeries(ctx context.Context, env Env, metricName string, from time.Time) ([]series, error) {
	spec := s
	spec.Metric = metricName
	return spec.query(ctx, env, from)
}

// Validate checks the rule against the metric catalog.
func (r ThresholdRule) Validate() error {
	def, err := r.Series.validate()
	if err != nil {
		return err
	}
	if r.RelativeTo != "" {
		if err := relative(r.RelativeTo, def, func(u metric.Unit) bool { return u == def.Unit }); err != nil {
			return err
		}
	}
	if r.Aggregate < Avg || r.Aggregate > Last {
		return fmt.Errorf("%w: aggregate must be avg, max, min or last", alert.ErrInvalidRule)
	}
	if err := validateWindow("window", r.Window); err != nil {
		return err
	}
	return r.Thresholds.Validate()
}

// Evaluate implements Evaluator.
func (r ThresholdRule) Evaluate(ctx context.Context, env Env) ([]Result, error) {
	from := env.Now.Add(-r.Window)
	found, err := r.Series.query(ctx, env, from)
	if err != nil {
		return nil, err
	}
	values := reduce(found, r.Aggregate, 1)
	if r.RelativeTo != "" {
		totals, err := r.Series.relativeSeries(ctx, env, r.RelativeTo, from)
		if err != nil {
			return nil, err
		}
		values = divideBy(values, reduce(totals, r.Aggregate, 1))
	}
	return results(values, r.Thresholds), nil
}

// RateRule compares the average of a per-second metric over a window, per second or per minute
// (Per), or relative to another metric with the same labels (errors per packet).
type RateRule struct {
	Series     SeriesSpec
	Per        time.Duration // 0 or a second: per second; a minute: per minute
	RelativeTo string
	Window     time.Duration
	Thresholds alert.Thresholds
}

func isRate(u metric.Unit) bool {
	return u == metric.UnitPerSecond || u == metric.UnitBytesPerSecond || u == metric.UnitBitsPerSecond || u == metric.UnitPagesPerSecond
}

// Validate checks the rule against the metric catalog.
func (r RateRule) Validate() error {
	def, err := r.Series.validate()
	if err != nil {
		return err
	}
	if !isRate(def.Unit) {
		return fmt.Errorf("%w: %s is no rate", alert.ErrInvalidRule, def.Name)
	}
	if r.Per != 0 && r.Per != time.Second && r.Per != time.Minute {
		return fmt.Errorf("%w: per must be a second or a minute", alert.ErrInvalidRule)
	}
	if r.RelativeTo != "" {
		if err := relative(r.RelativeTo, def, isRate); err != nil {
			return err
		}
		if r.Per != 0 {
			return fmt.Errorf("%w: per does not apply to a rate relative to another", alert.ErrInvalidRule)
		}
	}
	if err := validateWindow("window", r.Window); err != nil {
		return err
	}
	return r.Thresholds.Validate()
}

// Evaluate implements Evaluator.
func (r RateRule) Evaluate(ctx context.Context, env Env) ([]Result, error) {
	from := env.Now.Add(-r.Window)
	found, err := r.Series.query(ctx, env, from)
	if err != nil {
		return nil, err
	}
	factor := 1.0
	if r.Per == time.Minute {
		factor = 60
	}
	values := reduce(found, Avg, factor)
	if r.RelativeTo != "" {
		totals, err := r.Series.relativeSeries(ctx, env, r.RelativeTo, from)
		if err != nil {
			return nil, err
		}
		values = divideBy(reduce(found, Avg, 1), reduce(totals, Avg, 1))
	}
	return results(values, r.Thresholds), nil
}

// AbsenceRule compares the seconds since a node's last contact with the thresholds (node offline).
// A node that never connected yields nothing.
type AbsenceRule struct {
	Thresholds alert.Thresholds
}

// Validate checks the thresholds.
func (r AbsenceRule) Validate() error {
	return r.Thresholds.Validate()
}

// Evaluate implements Evaluator.
func (r AbsenceRule) Evaluate(_ context.Context, env Env) ([]Result, error) {
	out := make([]Result, 0, len(env.Nodes))
	for _, n := range env.Nodes {
		if n.LastSeenAt == nil {
			continue
		}
		age := math.Max(0, env.Now.Sub(*n.LastSeenAt).Seconds())
		out = append(out, Result{Node: n.ID, Observation: r.Thresholds.Evaluate(math.Round(age))})
	}
	return out, nil
}

// stateLookback is how far back a state rule reads the latest value.
const stateLookback = 5 * time.Minute

// StateRule compares the latest value of a discrete metric (0/1 and the like) with the thresholds.
// With Seen, an instance only counts if the condition held at some point within SeenWindow
// (interface down that was up before; unused ports never alert).
type StateRule struct {
	Series     SeriesSpec
	Thresholds alert.Thresholds
	Seen       *alert.Condition
	SeenWindow time.Duration
}

// Validate checks the rule against the metric catalog.
func (r StateRule) Validate() error {
	if _, err := r.Series.validate(); err != nil {
		return err
	}
	if r.Seen != nil {
		if err := validateWindow("seen window", r.SeenWindow); err != nil {
			return err
		}
	}
	return r.Thresholds.Validate()
}

// Evaluate implements Evaluator.
func (r StateRule) Evaluate(ctx context.Context, env Env) ([]Result, error) {
	found, err := r.Series.query(ctx, env, env.Now.Add(-stateLookback))
	if err != nil {
		return nil, err
	}
	// What was seen before comes from a coarse history; the current points count as well.
	before := map[string]bool{}
	if r.Seen != nil {
		history, err := r.Series.query(ctx, env, env.Now.Add(-r.SeenWindow))
		if err != nil {
			return nil, err
		}
		for _, h := range history {
			if seen(*r.Seen, h.points) {
				before[h.node.String()+"|"+h.dimension] = true
			}
		}
	}
	out := make([]Result, 0, len(found))
	for _, s := range found {
		last := s.points[len(s.points)-1]
		if r.Seen != nil && !before[s.node.String()+"|"+s.dimension] && !seen(*r.Seen, s.points) {
			out = append(out, Result{Node: s.node, Dimension: s.dimension, Observation: alert.Observation{Level: alert.LevelOK, Cleared: true, Value: last.Avg}})
			continue
		}
		out = append(out, Result{Node: s.node, Dimension: s.dimension, Observation: r.Thresholds.Evaluate(last.Avg)})
	}
	return out, nil
}

func seen(c alert.Condition, points []metrics.Point) bool {
	for _, p := range points {
		if c.Holds(p.Max) || c.Holds(p.Min) {
			return true
		}
	}
	return false
}

// Trend needs at least this many points over this span; the forecast looks this far ahead.
const (
	minTrendPoints  = 6
	minTrendSpan    = time.Hour
	forecastHorizon = 30 * 24 * time.Hour
)

// PredictionRule compares the hours until a falling metric reaches zero (linear trend over
// Lookback, e.g. available bytes of a file system) with the thresholds. A flat or rising trend
// is no risk; too little history yields no data.
type PredictionRule struct {
	Series     SeriesSpec
	Lookback   time.Duration
	Thresholds alert.Thresholds
}

// Validate checks the rule against the metric catalog.
func (r PredictionRule) Validate() error {
	if _, err := r.Series.validate(); err != nil {
		return err
	}
	if r.Lookback < 2*minTrendSpan || r.Lookback > maxWindow {
		return fmt.Errorf("%w: lookback must be between %v and %v", alert.ErrInvalidRule, 2*minTrendSpan, maxWindow)
	}
	return r.Thresholds.Validate()
}

// Evaluate implements Evaluator.
func (r PredictionRule) Evaluate(ctx context.Context, env Env) ([]Result, error) {
	found, err := r.Series.query(ctx, env, env.Now.Add(-r.Lookback))
	if err != nil {
		return nil, err
	}
	out := make([]Result, 0, len(found))
	for _, s := range found {
		if len(s.points) < minTrendPoints || s.points[len(s.points)-1].Time.Sub(s.points[0].Time) < minTrendSpan {
			out = append(out, Result{Node: s.node, Dimension: s.dimension, Observation: alert.Observation{NoData: true}})
			continue
		}
		hours := forecastHorizon.Hours() // beyond the horizon: no risk
		if at, ok := fleet.FullAt(s.points, env.Now, forecastHorizon); ok {
			hours = at.Sub(env.Now).Hours()
		}
		out = append(out, Result{Node: s.node, Dimension: s.dimension, Observation: r.Thresholds.Evaluate(math.Round(hours*10) / 10)})
	}
	return out, nil
}
