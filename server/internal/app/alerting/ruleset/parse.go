// Package ruleset reads alert rules (KONZEPT §12.2, M2-04): YAML documents of the AlertRule
// message, checked by protovalidate and against the metric catalog, turned into the rules the
// engine evaluates. It ships the default rules (rules/, copied by `just gen-rules`) and merges
// them with the overrides of an organization.
package ruleset

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"buf.build/go/protovalidate"
	"github.com/google/uuid"
	"go.yaml.in/yaml/v3"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/pmoscode/SerMon/server/internal/app/alerting"
	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/metric"
	"github.com/pmoscode/SerMon/server/internal/domain/secevent"
	"github.com/pmoscode/SerMon/server/internal/domain/tag"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
)

// MaxDocumentBytes bounds a rule document (rules are imported by users, M2-17).
const MaxDocumentBytes = 64 << 10

// Definition is a parsed rule: the document (for display, export and overrides), the rule the
// engine evaluates and, where known, the YAML it came from (comments and layout kept).
type Definition struct {
	Doc    *webv1.AlertRule
	Rule   alerting.Rule
	Source []byte
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", alert.ErrInvalidRule, fmt.Sprintf(format, args...))
}

// Parse reads one rule from YAML (or JSON, which is YAML).
func Parse(data []byte) (Definition, error) {
	if len(data) > MaxDocumentBytes {
		return Definition{}, invalid("document too large (%d bytes, at most %d)", len(data), MaxDocumentBytes)
	}
	return parse(data)
}

func parse(data []byte) (Definition, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var root yaml.Node
	if err := decoder.Decode(&root); err != nil {
		return Definition{}, invalid("%v", err)
	}
	var more yaml.Node
	if err := decoder.Decode(&more); !errors.Is(err, io.EOF) {
		return Definition{}, invalid("a document holds exactly one rule")
	}
	doc := &webv1.AlertRule{}
	if len(root.Content) == 1 {
		if err := knownFields(root.Content[0], doc.ProtoReflect().Descriptor(), ""); err != nil {
			return Definition{}, err
		}
	}
	var tree any
	if err := root.Decode(&tree); err != nil {
		return Definition{}, invalid("%v", err)
	}
	asJSON, err := json.Marshal(tree)
	if err != nil {
		return Definition{}, invalid("yaml: only text keys are allowed: %v", err)
	}
	if err := protojson.Unmarshal(asJSON, doc); err != nil {
		// The position is one in the JSON, not in the YAML.
		return Definition{}, invalid("%s", jsonPosition.ReplaceAllString(err.Error(), ""))
	}
	return FromDocument(doc)
}

var jsonPosition = regexp.MustCompile(`\(line \d+:\d+\): `)

// knownFields reports the first key of the YAML that is no field of the message, with its line
// and path; protojson would only name the position in the converted JSON.
func knownFields(n *yaml.Node, md protoreflect.MessageDescriptor, at string) error {
	if n.Kind != yaml.MappingNode {
		return nil // type errors come from protojson
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, value := n.Content[i], n.Content[i+1]
		fd := md.Fields().ByJSONName(key.Value)
		if fd == nil {
			fd = md.Fields().ByName(protoreflect.Name(key.Value))
		}
		if fd == nil {
			where := ""
			if at != "" {
				where = " in " + at
			}
			return invalid("line %d: unknown field %q%s", key.Line, key.Value, where)
		}
		path := fd.JSONName()
		if at != "" {
			path = at + "." + path
		}
		var err error
		switch {
		case fd.IsMap():
			if mv := fd.MapValue(); mv.Message() != nil && value.Kind == yaml.MappingNode {
				for j := 1; j < len(value.Content) && err == nil; j += 2 {
					err = knownFields(value.Content[j], mv.Message(), path+"."+value.Content[j-1].Value)
				}
			}
		case fd.Message() != nil && fd.IsList() && value.Kind == yaml.SequenceNode:
			for _, item := range value.Content {
				if err = knownFields(item, fd.Message(), path); err != nil {
					break
				}
			}
		case fd.Message() != nil:
			err = knownFields(value, fd.Message(), path)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Marshal writes a rule document as JSON, which Parse reads back.
func Marshal(doc *webv1.AlertRule) ([]byte, error) {
	data, err := protojson.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("marshal rule: %w", err)
	}
	return data, nil
}

// FromDocument checks a rule document and builds the rule.
func FromDocument(doc *webv1.AlertRule) (Definition, error) {
	if err := protovalidate.Validate(doc); err != nil {
		return Definition{}, fmt.Errorf("%w: %w", alert.ErrInvalidRule, err)
	}
	id, err := alert.ParseRuleID(doc.GetMetadata().GetId())
	if err != nil {
		return Definition{}, err
	}
	spec := doc.GetSpec()
	typ, err := alert.ParseType(spec.GetType())
	if err != nil {
		return Definition{}, err
	}
	if err := onlyFieldsOf(typ, spec); err != nil {
		return Definition{}, err
	}
	thresholds, err := conditions(spec)
	if err != nil {
		return Definition{}, err
	}
	timing, err := timingOf(spec)
	if err != nil {
		return Definition{}, err
	}
	groups, err := groupsOf(spec.GetSelector())
	if err != nil {
		return Definition{}, err
	}
	evaluator, err := evaluatorOf(typ, spec, thresholds)
	if err != nil {
		return Definition{}, err
	}
	if err := checkUnits(typ, spec, thresholds); err != nil {
		return Definition{}, err
	}
	if err := evaluator.Validate(); err != nil {
		if !errors.Is(err, alert.ErrInvalidRule) {
			err = fmt.Errorf("%w: %w", alert.ErrInvalidRule, err)
		}
		return Definition{}, err
	}
	tags := tag.Tags(spec.GetSelector().GetTags())
	if err := tags.Validate(); err != nil {
		return Definition{}, invalid("selector.tags: %v", err)
	}
	if len(tags) == 0 {
		tags = nil
	}
	return Definition{Doc: doc, Rule: alerting.Rule{ID: id, Timing: timing, Groups: groups, Tags: tags, Evaluator: evaluator}}, nil
}

// Fields of the spec by rule type (YAML names in the errors). Event rules count instead of
// comparing with conditions.
var (
	commonFields    = []protoreflect.Name{"type", "for_duration", "flapping", "selector", "help"}
	conditionFields = []protoreflect.Name{"warning", "critical", "clear"}
	seriesFields    = append(slices.Clone(conditionFields), "metric", "labels", "dimensions", "exclude")
	typeFields      = map[alert.Type][]protoreflect.Name{
		alert.Threshold:  append(slices.Clone(seriesFields), "aggregate", "window", "relative_to", "complement"),
		alert.Rate:       append(slices.Clone(seriesFields), "window", "per", "relative_to"),
		alert.Absence:    conditionFields,
		alert.StateRule:  append(slices.Clone(seriesFields), "seen", "seen_window"),
		alert.Prediction: append(slices.Clone(seriesFields), "lookback"),
		alert.Event:      {"event", "match", "group_by", "count", "after", "notify_fields", "learning"},
		alert.Anomaly:    append(slices.Clone(conditionFields), "aggregate", "window"),
	}
)

func onlyFieldsOf(typ alert.Type, spec *webv1.AlertRuleSpec) error {
	allowed, ok := typeFields[typ]
	if !ok {
		return invalid("type %s is not supported yet", typ)
	}
	var err error
	spec.ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		if !slices.Contains(commonFields, fd.Name()) && !slices.Contains(allowed, fd.Name()) {
			err = invalid("%s does not apply to a %s rule", fd.JSONName(), typ)
			return false
		}
		return true
	})
	return err
}

func duration(name, s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, invalid("%s must be a duration like 90s, 10m or 24h, not %q", name, s)
	}
	return d, nil
}

func condition(c *webv1.AlertCondition) *alert.Condition {
	if c == nil {
		return nil
	}
	return &alert.Condition{Op: alert.Op(c.GetOp()), Value: c.GetValue()}
}

func conditions(spec *webv1.AlertRuleSpec) (alert.Thresholds, error) {
	for name, c := range map[string]*webv1.AlertCondition{"warning": spec.GetWarning(), "critical": spec.GetCritical(), "clear": spec.GetClear(), "seen": spec.GetSeen()} {
		if c != nil && c.GetOp() == "" {
			return alert.Thresholds{}, invalid("%s needs an op", name)
		}
	}
	return alert.Thresholds{Warning: condition(spec.GetWarning()), Critical: condition(spec.GetCritical()), Clear: condition(spec.GetClear())}, nil
}

func timingOf(spec *webv1.AlertRuleSpec) (alert.Timing, error) {
	timing := alert.Timing{Flapping: alert.DefaultFlapping}
	var err error
	if timing.For, err = duration("for", spec.GetForDuration()); err != nil {
		return alert.Timing{}, err
	}
	if f := spec.GetFlapping(); f != nil {
		window, err := duration("flapping.window", f.GetWindow())
		if err != nil {
			return alert.Timing{}, err
		}
		timing.Flapping = alert.Flapping{Changes: int(f.GetChanges()), Window: window}
	}
	if err := timing.Validate(); err != nil {
		return alert.Timing{}, err
	}
	return timing, nil
}

// groupsOf reads the selector: "*" (or nothing) for all groups, otherwise group IDs.
func groupsOf(s *webv1.AlertSelector) ([]tenant.GroupID, error) {
	names := s.GetGroups()
	if len(names) == 0 || (len(names) == 1 && names[0] == "*") {
		return nil, nil
	}
	groups := make([]tenant.GroupID, 0, len(names))
	for _, name := range names {
		if name == "*" {
			return nil, invalid("selector.groups: \"*\" (all groups) stands alone")
		}
		id, err := uuid.Parse(name)
		if err != nil {
			return nil, invalid("selector.groups takes \"*\" alone or group IDs, not %q", name)
		}
		groups = append(groups, tenant.GroupID(id))
	}
	return groups, nil
}

// Bounds of the exclusion patterns of one label.
const (
	maxPatterns     = 32
	maxPatternBytes = 256
)

func seriesOf(spec *webv1.AlertRuleSpec) (alerting.SeriesSpec, error) {
	s := alerting.SeriesSpec{Metric: spec.GetMetric(), Dimensions: spec.GetDimensions()}
	if len(spec.GetLabels()) > 0 {
		s.Labels = metrics.Labels(spec.GetLabels())
	}
	if len(spec.GetExclude()) > 0 {
		s.Exclude = make(map[string][]string, len(spec.GetExclude()))
		for name, list := range spec.GetExclude() {
			values := list.GetValues()
			if len(values) > maxPatterns {
				return alerting.SeriesSpec{}, invalid("exclude.%s takes at most %d patterns", name, maxPatterns)
			}
			patterns := make([]string, 0, len(values))
			for _, v := range values {
				p, ok := v.GetKind().(*structpb.Value_StringValue)
				if !ok || p.StringValue == "" || len(p.StringValue) > maxPatternBytes {
					return alerting.SeriesSpec{}, invalid("exclude.%s takes patterns of 1 to %d characters", name, maxPatternBytes)
				}
				patterns = append(patterns, p.StringValue)
			}
			s.Exclude[name] = patterns
		}
	}
	return s, nil
}

var aggregates = map[string]alerting.Aggregate{"avg": alerting.Avg, "max": alerting.Max, "min": alerting.Min, "last": alerting.Last, "increase": alerting.Increase}

type validEvaluator interface {
	alerting.Evaluator
	Validate() error
}

func evaluatorOf(typ alert.Type, spec *webv1.AlertRuleSpec, th alert.Thresholds) (validEvaluator, error) {
	window, err := duration("window", spec.GetWindow())
	if err != nil {
		return nil, err
	}
	series, err := seriesOf(spec)
	if err != nil {
		return nil, err
	}
	switch typ {
	case alert.Event:
		return eventOf(spec)
	case alert.Threshold:
		return alerting.ThresholdRule{
			Series: series, RelativeTo: spec.GetRelativeTo(), Complement: spec.GetComplement(),
			Aggregate: aggregates[spec.GetAggregate()], Window: window, Thresholds: th,
		}, nil
	case alert.Rate:
		per := map[string]time.Duration{"": 0, "second": time.Second, "minute": time.Minute}[spec.GetPer()]
		return alerting.RateRule{Series: series, Per: per, RelativeTo: spec.GetRelativeTo(), Window: window, Thresholds: th}, nil
	case alert.Absence:
		return alerting.AbsenceRule{Thresholds: th}, nil
	case alert.Anomaly:
		aggregate := alerting.Avg
		if spec.GetAggregate() != "" {
			aggregate = aggregates[spec.GetAggregate()]
		}
		return alerting.AnomalyRule{Aggregate: aggregate, Window: window, Thresholds: th}, nil
	case alert.StateRule:
		seenWindow, err := duration("seenWindow", spec.GetSeenWindow())
		if err != nil {
			return nil, err
		}
		if spec.GetSeen() == nil && seenWindow != 0 {
			return nil, invalid("seenWindow needs seen")
		}
		return alerting.StateRule{Series: series, Thresholds: th, Seen: condition(spec.GetSeen()), SeenWindow: seenWindow}, nil
	case alert.Prediction:
		lookback, err := duration("lookback", spec.GetLookback())
		if err != nil {
			return nil, err
		}
		return alerting.PredictionRule{Series: series, Lookback: lookback, Thresholds: th}, nil
	}
	return nil, invalid("type %s is not supported yet", typ)
}

func eventOf(spec *webv1.AlertRuleSpec) (validEvaluator, error) {
	count := spec.GetCount()
	window, err := duration("count.window", count.GetWindow())
	if err != nil {
		return nil, err
	}
	learning, err := duration("learning", spec.GetLearning())
	if err != nil {
		return nil, err
	}
	rule := alerting.EventRule{
		Kind: secevent.Kind(spec.GetEvent()), GroupBy: spec.GetGroupBy(), Window: window,
		Warning: int(count.GetWarning()), Critical: int(count.GetCritical()), NotifyFields: spec.GetNotifyFields(),
		Learning: learning,
	}
	if len(spec.GetMatch()) > 0 {
		rule.Match = spec.GetMatch()
	}
	if count.GetOf() == "nodes" {
		rule.Count = alerting.CountNodes
	}
	if a := spec.GetAfter(); a != nil {
		aw, err := duration("after.window", a.GetWindow())
		if err != nil {
			return nil, err
		}
		rule.After = &alerting.EventCondition{Kind: secevent.Kind(a.GetEvent()), Count: int(a.GetCount()), Window: aw}
	}
	return rule, nil
}

// checkUnits catches thresholds in the wrong unit: ratios (and relative values) are 0..1, not
// percent; seconds and hours are not negative.
func checkUnits(typ alert.Type, spec *webv1.AlertRuleSpec, th alert.Thresholds) error {
	lo, hi, unit := 0.0, 1.0, "a ratio (0..1, not percent)"
	def, _ := metric.Lookup(spec.GetMetric())
	switch {
	case spec.GetRelativeTo() != "", def.Unit == metric.UnitRatioPPM, def.Unit == metric.UnitBoolean, typ == alert.Anomaly:
	case typ == alert.Absence:
		hi, unit = maxFloat, "seconds (not negative)"
	case typ == alert.Prediction:
		hi, unit = maxFloat, "hours (not negative)"
	default:
		return nil
	}
	named := map[string]*alert.Condition{"warning": th.Warning, "critical": th.Critical, "clear": th.Clear, "seen": condition(spec.GetSeen())}
	for _, name := range []string{"warning", "critical", "clear", "seen"} {
		if c := named[name]; c != nil && (c.Value < lo || c.Value > hi) {
			return invalid("%s compares %s, %v is out of range", name, unit, c.Value)
		}
	}
	return nil
}

const maxFloat = 1e300

// Load reads every rule of fsys: <category>/<id>.yaml, the directory being the rule's category
// label. All errors are reported, each with its file.
func Load(fsys fs.FS) ([]Definition, error) {
	var defs []Definition
	var errs []error
	seen := map[alert.RuleID]string{}
	walk := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(name, ".yml") {
			errs = append(errs, fmt.Errorf("%s: %w", name, invalid("rule files end in .yaml")))
			return nil
		}
		if !strings.HasSuffix(name, ".yaml") {
			return nil
		}
		def, err := loadFile(fsys, name)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			return nil
		}
		if first, dup := seen[def.Rule.ID]; dup {
			errs = append(errs, fmt.Errorf("%s: %w", name, invalid("id %s is already used by %s", def.Rule.ID, first)))
			return nil
		}
		seen[def.Rule.ID] = name
		defs = append(defs, def)
		return nil
	})
	if walk != nil {
		return nil, fmt.Errorf("read rules: %w", walk)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return defs, nil
}

func loadFile(fsys fs.FS, name string) (Definition, error) {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return Definition{}, fmt.Errorf("read: %w", err)
	}
	def, err := Parse(data)
	if err != nil {
		return Definition{}, err
	}
	def.Source = data
	dir, file := path.Split(name)
	category := def.Doc.GetMetadata().GetLabels()["category"]
	if want := category + "/"; dir != want {
		return Definition{}, invalid("a rule of category %q belongs in %s", category, want)
	}
	if want := string(def.Rule.ID) + ".yaml"; file != want {
		return Definition{}, invalid("the file of rule %s is named %s", def.Rule.ID, want)
	}
	return def, nil
}
