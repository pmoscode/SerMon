package ruleset

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/pmoscode/SerMon/server/internal/app/alerting"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Override is an organization's change to the rules: a default rule switched off or replaced by
// its own document, or a rule of its own. The shipped default stays as it is.
type Override struct {
	Rule     alert.RuleID
	Disabled bool
	// Document is the rule as JSON (Marshal); nil keeps the default.
	Document []byte
	// Source is the YAML the document was written as (shown by the editor); may be empty.
	Source []byte
}

// OverrideStore reads the overrides of an organization.
type OverrideStore interface {
	Overrides(ctx context.Context, org tenant.OrgID) ([]Override, error)
}

// Effective is a rule as it applies to an organization.
type Effective struct {
	Definition
	Default    bool // shipped with SerMon
	Overridden bool // the organization replaced the document
	Disabled   bool
}

// Source merges the default rules with the overrides of each organization (alerting.RuleSource).
type Source struct {
	defaults []Definition
	store    OverrideStore
	logger   *slog.Logger
}

var _ alerting.RuleSource = (*Source)(nil)

// NewSource returns the source over the default rules.
func NewSource(defaults []Definition, store OverrideStore, logger *slog.Logger) *Source {
	return &Source{defaults: defaults, store: store, logger: logger}
}

// Definitions returns the organization's rules, the disabled ones included: the defaults in their
// order, then its own rules by ID. An override that no longer loads is logged and ignored – the
// default applies, a rule of its own is left out.
func (s *Source) Definitions(ctx context.Context, org tenant.OrgID) ([]Effective, error) {
	list, err := s.store.Overrides(ctx, org)
	if err != nil {
		return nil, fmt.Errorf("rule overrides: %w", err)
	}
	byRule := make(map[alert.RuleID]Override, len(list))
	for _, o := range list {
		byRule[o.Rule] = o
	}
	out := make([]Effective, 0, len(s.defaults)+len(list))
	for _, def := range s.defaults {
		e := Effective{Definition: def, Default: true}
		if o, ok := byRule[def.Rule.ID]; ok {
			delete(byRule, def.Rule.ID)
			e.Disabled = o.Disabled
			if o.Document != nil {
				if own, ok := s.load(ctx, org, o); ok {
					e.Definition, e.Overridden = own, true
				}
			}
		}
		out = append(out, e)
	}
	own := make([]Effective, 0, len(byRule))
	for _, o := range byRule {
		if o.Document == nil {
			continue
		}
		if def, ok := s.load(ctx, org, o); ok {
			own = append(own, Effective{Definition: def, Disabled: o.Disabled})
		}
	}
	slices.SortFunc(own, func(a, b Effective) int { return compare(a.Rule.ID, b.Rule.ID) })
	return append(out, own...), nil
}

func compare(a, b alert.RuleID) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func (s *Source) load(ctx context.Context, org tenant.OrgID, o Override) (Definition, bool) {
	def, err := Parse(o.Document)
	if err == nil && def.Rule.ID != o.Rule {
		err = invalid("the document is rule %s", def.Rule.ID)
	}
	if err != nil {
		s.logger.ErrorContext(ctx, "alert rule override ignored", "org", org.String(), "rule", string(o.Rule), "error", err.Error())
		return Definition{}, false
	}
	return def, true
}

// Rules implements alerting.RuleSource: the enabled rules.
func (s *Source) Rules(ctx context.Context, org tenant.OrgID) ([]alerting.Rule, error) {
	defs, err := s.Definitions(ctx, org)
	if err != nil {
		return nil, err
	}
	rules := make([]alerting.Rule, 0, len(defs))
	for _, d := range defs {
		if !d.Disabled {
			rules = append(rules, d.Rule)
		}
	}
	return rules, nil
}
