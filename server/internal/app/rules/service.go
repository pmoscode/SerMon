// Package rules holds the use cases of the rule editor (KONZEPT §12.2, M2-17): the rules of an
// organization as they apply (shipped, changed or its own, on or off) with their YAML, import
// of one or more documents with a dry run that shows the changes, switching rules on and off,
// restoring the shipped rule and export. Users write typed rule documents, never free queries;
// the ruleset loader checks every document.
package rules

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/alerting/ruleset"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
)

// MaxImportDocuments bounds the rules of one import.
const MaxImportDocuments = 100

// MaxOwnRules bounds an organization's own rules (M2-38): they are evaluated next to every other
// organization's in the same rounds. Changed shipped rules don't count.
const MaxOwnRules = 200

// Store keeps the overrides of an organization (postgres.AlertRuleStore).
type Store interface {
	Overrides(ctx context.Context, org tenant.OrgID) ([]ruleset.Override, error)
	// Apply stores overrides with one audit entry, all or none.
	Apply(ctx context.Context, scope tenant.Scope, put []ruleset.Override, by identity.UserID, audit access.AuditEntry) error
	// Reset deletes the override of a rule; access.ErrNotFound if there is none.
	Reset(ctx context.Context, scope tenant.Scope, rule alert.RuleID, audit access.AuditEntry) error
}

// Deps are the collaborators of Service.
type Deps struct {
	Defaults []ruleset.Definition
	Store    Store
	Policy   authz.Policy
	Now      func() time.Time
}

// Service serves the rule editor.
type Service struct{ d Deps }

// NewService returns the service.
func NewService(d Deps) *Service { return &Service{d: d} }

// Origin is where the rule of an organization comes from.
type Origin int

// Origins.
const (
	Default Origin = iota + 1 // shipped with SerMon
	Changed                   // shipped, replaced by the organization's document
	Own                       // the organization's own rule
)

// Rule is a rule as it applies to an organization.
type Rule struct {
	ID      alert.RuleID
	Doc     *webv1.AlertRule // nil for an own rule whose document no longer loads
	Type    string
	Origin  Origin
	Enabled bool
	// YAML is the document as written; DefaultYAML the shipped one of a changed rule.
	YAML        string
	DefaultYAML string
	// Problem says why a stored document no longer loads (the shipped rule applies then).
	Problem string
}

// Action is what an import does with a rule.
type Action int

// Actions.
const (
	Create Action = iota + 1
	Update
	Unchanged
)

// Change is one rule of an import with the text before and after.
type Change struct {
	Rule          alert.RuleID
	Action        Action
	Before, After string
}

// Problem is an error of an import: the document (1-based), the line it starts on and the
// loader's message (which names the line of the error).
type Problem struct {
	Document int
	Line     int
	Message  string
}

// Result is the outcome of an import.
type Result struct {
	Changes  []Change
	Problems []Problem
	Applied  bool
}

func (s *Service) viewer(c access.Caller) error {
	scope, err := s.d.Policy.ScopeForAction(c.Principal, authz.ViewNodes)
	if err != nil || (!scope.AllGroups() && len(scope.Groups()) == 0) {
		return access.ErrForbidden
	}
	return nil
}

// editor authorizes a change: admins with ManageAlertRules; writing needs a fresh step-up,
// because switching rules off can hide security alerts.
func (s *Service) editor(c access.Caller, write bool) (tenant.Scope, error) {
	p := c.Principal
	if !s.d.Policy.Allowed(p, authz.ManageAlertRules, authz.InOrg(p.Org)) {
		return tenant.Scope{}, access.ErrForbidden
	}
	if write && s.d.Now().Sub(c.StepUpAt) > access.StepUpMaxAge {
		return tenant.Scope{}, access.ErrStepUpRequired
	}
	scope, err := tenant.ForAllGroups(p.Org)
	if err != nil {
		return tenant.Scope{}, access.ErrForbidden
	}
	return scope, nil
}

// state is the rules of an organization with the overrides behind them.
type state struct {
	rules     []Rule
	overrides map[alert.RuleID]ruleset.Override
}

func text(def ruleset.Definition) string {
	if len(def.Source) > 0 {
		return string(def.Source)
	}
	if out, err := ruleset.YAML(def.Doc); err == nil {
		return string(out)
	}
	return ""
}

func (s *Service) state(ctx context.Context, org tenant.OrgID) (state, error) {
	list, err := s.d.Store.Overrides(ctx, org)
	if err != nil {
		return state{}, fmt.Errorf("rule overrides: %w", err)
	}
	st := state{overrides: make(map[alert.RuleID]ruleset.Override, len(list))}
	for _, o := range list {
		st.overrides[o.Rule] = o
	}
	load := func(o ruleset.Override) (ruleset.Definition, error) {
		def, err := ruleset.Parse(o.Document)
		if err == nil && def.Rule.ID != o.Rule {
			err = fmt.Errorf("the document is rule %s", def.Rule.ID)
		}
		def.Source = o.Source
		return def, err
	}
	for _, d := range s.d.Defaults {
		r := Rule{ID: d.Rule.ID, Doc: d.Doc, Type: d.Doc.GetSpec().GetType(), Origin: Default, Enabled: true, YAML: text(d)}
		if o, ok := st.overrides[d.Rule.ID]; ok {
			r.Enabled = !o.Disabled
			if o.Document != nil {
				if def, err := load(o); err != nil {
					r.Problem = err.Error()
				} else {
					r.Doc, r.Origin, r.YAML, r.DefaultYAML = def.Doc, Changed, text(def), text(d)
				}
			}
		}
		st.rules = append(st.rules, r)
	}
	var own []Rule
	for id, o := range st.overrides {
		if slices.ContainsFunc(s.d.Defaults, func(d ruleset.Definition) bool { return d.Rule.ID == id }) || o.Document == nil {
			continue
		}
		r := Rule{ID: id, Origin: Own, Enabled: !o.Disabled, YAML: string(o.Source)}
		if def, err := load(o); err != nil {
			r.Problem = err.Error()
		} else {
			r.Doc, r.Type, r.YAML = def.Doc, def.Doc.GetSpec().GetType(), text(def)
		}
		own = append(own, r)
	}
	slices.SortFunc(own, func(a, b Rule) int { return strings.Compare(string(a.ID), string(b.ID)) })
	st.rules = append(st.rules, own...)
	return st, nil
}

// own counts the organization's own rules.
func (st state) own() int {
	n := 0
	for _, r := range st.rules {
		if r.Origin == Own {
			n++
		}
	}
	return n
}

// newOwn counts the rules an import adds that are neither shipped nor already the organization's.
func newOwn(st state, put []ruleset.Override, defaults []ruleset.Definition) int {
	n := 0
	for _, o := range put {
		_, known := st.find(o.Rule)
		shipped := slices.ContainsFunc(defaults, func(d ruleset.Definition) bool { return d.Rule.ID == o.Rule })
		if !known && !shipped {
			n++
		}
	}
	return n
}

func (st state) find(id alert.RuleID) (Rule, bool) {
	i := slices.IndexFunc(st.rules, func(r Rule) bool { return r.ID == id })
	if i < 0 {
		return Rule{}, false
	}
	return st.rules[i], true
}

// List returns the rules of the caller's organization: the shipped ones in their order, then
// its own by ID. Every member may read them.
func (s *Service) List(ctx context.Context, c access.Caller) ([]Rule, error) {
	if err := s.viewer(c); err != nil {
		return nil, err
	}
	st, err := s.state(ctx, c.Principal.Org)
	if err != nil {
		return nil, err
	}
	return st.rules, nil
}

// Import checks one or more rule documents and, unless dryRun, stores them – all or none. The
// result lists what changes and every problem; documents equal to the current rule stay as
// they are, and a rule keeps being on or off.
func (s *Service) Import(ctx context.Context, c access.Caller, data []byte, dryRun bool) (Result, error) {
	scope, err := s.editor(c, !dryRun)
	if err != nil {
		return Result{}, err
	}
	st, err := s.state(ctx, c.Principal.Org)
	if err != nil {
		return Result{}, err
	}
	docs := ruleset.ParseBundle(data)
	var res Result
	switch {
	case len(docs) == 0:
		res.Problems = append(res.Problems, Problem{Document: 1, Line: 1, Message: "no rule found"})
	case len(docs) > MaxImportDocuments:
		res.Problems = append(res.Problems, Problem{Document: MaxImportDocuments + 1, Line: docs[MaxImportDocuments].Line, Message: fmt.Sprintf("at most %d rules per import", MaxImportDocuments)})
	}
	seen := map[alert.RuleID]int{}
	var put []ruleset.Override
	for i, d := range docs {
		if d.Err != nil {
			res.Problems = append(res.Problems, Problem{Document: i + 1, Line: d.Line, Message: d.Err.Error()})
			continue
		}
		id := d.Def.Rule.ID
		if first, dup := seen[id]; dup {
			res.Problems = append(res.Problems, Problem{Document: i + 1, Line: d.Line, Message: fmt.Sprintf("rule %s is in the import twice (first in document %d)", id, first)})
			continue
		}
		seen[id] = i + 1
		ch := Change{Rule: id, Action: Create, After: d.Source}
		current, known := st.find(id)
		if known {
			ch.Before, ch.Action = current.YAML, Update
			if current.Doc != nil && ruleset.Equal(current.Doc, d.Def.Doc) {
				ch.Action = Unchanged
			}
		}
		res.Changes = append(res.Changes, ch)
		if ch.Action == Unchanged {
			continue
		}
		doc, err := ruleset.Marshal(d.Def.Doc)
		if err != nil {
			return Result{}, fmt.Errorf("rule %s: %w", id, err)
		}
		put = append(put, ruleset.Override{Rule: id, Document: doc, Source: []byte(d.Source), Disabled: st.overrides[id].Disabled})
	}
	if own := st.own() + newOwn(st, put, s.d.Defaults); own > MaxOwnRules {
		res.Problems = append(res.Problems, Problem{Document: 1, Line: 1, Message: fmt.Sprintf("an organization has at most %d rules of its own (%d after this import)", MaxOwnRules, own)})
	}
	if dryRun || len(res.Problems) > 0 || len(put) == 0 {
		return res, nil
	}
	ids := make([]string, len(put))
	for i, o := range put {
		ids[i] = string(o.Rule)
	}
	audit := access.AuditEntry{
		Actor: c.Principal.User, Action: "alert_rule.import", Target: "alert_rules",
		Details: map[string]string{"rules": strings.Join(ids, ",")},
	}
	if err := s.d.Store.Apply(ctx, scope, put, c.Principal.User, audit); err != nil {
		return Result{}, fmt.Errorf("store rules: %w", err)
	}
	res.Applied = true
	return res, nil
}

func ruleAudit(c access.Caller, action string, id alert.RuleID) access.AuditEntry {
	return access.AuditEntry{Actor: c.Principal.User, Action: "alert_rule." + action, Target: "alert_rule:" + string(id)}
}

// SetEnabled switches a rule on or off; its document stays.
func (s *Service) SetEnabled(ctx context.Context, c access.Caller, id alert.RuleID, enabled bool) error {
	scope, err := s.editor(c, true)
	if err != nil {
		return err
	}
	st, err := s.state(ctx, c.Principal.Org)
	if err != nil {
		return err
	}
	if _, ok := st.find(id); !ok {
		return access.ErrNotFound
	}
	o, had := st.overrides[id]
	o.Rule, o.Disabled = id, !enabled
	action := map[bool]string{true: "enable", false: "disable"}[enabled]
	if o.Document == nil && !o.Disabled {
		// Back to the shipped rule as it is.
		if !had {
			return nil
		}
		if err := s.d.Store.Reset(ctx, scope, id, ruleAudit(c, action, id)); err != nil && !errors.Is(err, access.ErrNotFound) {
			return fmt.Errorf("enable rule: %w", err)
		}
		return nil
	}
	if err := s.d.Store.Apply(ctx, scope, []ruleset.Override{o}, c.Principal.User, ruleAudit(c, action, id)); err != nil {
		return fmt.Errorf("switch rule: %w", err)
	}
	return nil
}

// Reset restores the shipped rule (or deletes a rule of the organization's own).
func (s *Service) Reset(ctx context.Context, c access.Caller, id alert.RuleID) error {
	scope, err := s.editor(c, true)
	if err != nil {
		return err
	}
	if err := s.d.Store.Reset(ctx, scope, id, ruleAudit(c, "reset", id)); err != nil {
		return fmt.Errorf("reset rule: %w", err)
	}
	return nil
}

// Export writes the rules as one YAML text (documents separated by "---"): all rules, or only
// the changed and own ones. Whether a rule is on or off is not part of a document.
func (s *Service) Export(ctx context.Context, c access.Caller, all bool) (string, error) {
	if err := s.viewer(c); err != nil {
		return "", err
	}
	st, err := s.state(ctx, c.Principal.Org)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# SerMon alert rules, exported %s\n", s.d.Now().UTC().Format(time.RFC3339))
	for _, r := range st.rules {
		if r.Doc == nil || (!all && r.Origin == Default) {
			continue
		}
		b.WriteString("---\n")
		b.WriteString(r.YAML)
		if !strings.HasSuffix(r.YAML, "\n") {
			b.WriteByte('\n')
		}
	}
	return b.String(), nil
}
