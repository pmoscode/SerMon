package routing

import (
	"context"
	"fmt"
	"maps"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/alerting/ruleset"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/metric"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// definitions is what RulesOf needs of ruleset.Source.
type definitions interface {
	Definitions(ctx context.Context, org tenant.OrgID) ([]ruleset.Effective, error)
}

type sourceRules struct{ source definitions }

// RulesOf reads the rules of an organization from its rule source.
func RulesOf(source definitions) Rules { return sourceRules{source: source} }

// RuleInfos implements Rules.
func (s sourceRules) RuleInfos(ctx context.Context, org tenant.OrgID) (map[alert.RuleID]RuleInfo, error) {
	defs, err := s.source.Definitions(ctx, org)
	if err != nil {
		return nil, fmt.Errorf("rule definitions: %w", err)
	}
	out := make(map[alert.RuleID]RuleInfo, len(defs))
	for _, d := range defs {
		meta := d.Doc.GetMetadata()
		out[d.Rule.ID] = RuleInfo{
			Name:   notify.Text{De: meta.GetName().GetDe(), En: meta.GetName().GetEn()},
			Labels: maps.Clone(meta.GetLabels()),
			Unit:   unitOf(d.Definition),
		}
	}
	return out, nil
}

// unitOf is the unit of the value a rule reports: what its type computes, or its metric's unit.
func unitOf(d ruleset.Definition) notify.Unit {
	spec := d.Doc.GetSpec()
	switch spec.GetType() {
	case "event":
		return notify.UnitCount
	case "absence":
		return notify.UnitSeconds
	case "prediction":
		return notify.UnitHours
	case "rate":
		switch {
		case spec.GetRelativeTo() != "":
			return notify.UnitRatio
		case spec.GetPer() == "minute":
			return notify.UnitPerMinute
		}
		return notify.UnitPerSecond
	}
	def, ok := metric.Lookup(spec.GetMetric())
	if !ok {
		return ""
	}
	return map[metric.Unit]notify.Unit{
		metric.UnitRatioPPM: notify.UnitRatio, metric.UnitBytes: notify.UnitBytes, metric.UnitSeconds: notify.UnitSeconds,
		metric.UnitCount: notify.UnitCount, metric.UnitPerSecond: notify.UnitPerSecond, metric.UnitBytesPerSecond: notify.UnitBytesPerSecond,
	}[def.Unit]
}

// memberLister lists the members of an organization (access store).
type memberLister interface {
	ListMembers(ctx context.Context, scope tenant.Scope) ([]access.Member, error)
}

type memberNames struct{ members memberLister }

// UsersOf names users by the organization's members.
func UsersOf(members memberLister) Users { return memberNames{members: members} }

// DisplayNames implements Users: the display name, or the e-mail address without one.
func (m memberNames) DisplayNames(ctx context.Context, scope tenant.Scope) (map[identity.UserID]string, error) {
	list, err := m.members.ListMembers(ctx, scope)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	out := make(map[identity.UserID]string, len(list))
	for _, u := range list {
		out[u.User] = u.DisplayName
		if u.DisplayName == "" {
			out[u.User] = u.Email
		}
	}
	return out, nil
}
