package updates_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/routing"
	"github.com/pmoscode/SerMon/server/internal/app/updates"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	"github.com/pmoscode/SerMon/server/internal/domain/update"
)

const konzept = `kind: UpdatePolicy
spec:
  mode: security-auto
  window: { days: [ sun ], start: "03:00", duration: 2h, timezone: "Europe/Berlin" }
  autoReboot: true
  rollout: { maxParallel: 1, stopOnFailure: true, healthCheck: 10m }
`

func sunday() *routing.Schedule {
	return &routing.Schedule{Days: []time.Weekday{time.Sunday}, Start: 3 * time.Hour, Duration: 2 * time.Hour, TimeZone: "Europe/Berlin"}
}

// The example of KONZEPT §14.2 reads, writes and reads again to the same policy.
func TestParsePolicy_TheExampleOfTheConceptRoundTrips(t *testing.T) {
	p, err := updates.ParsePolicy(groupX, []byte(konzept))
	if err != nil {
		t.Fatal(err)
	}
	want := updates.Policy{
		Group: groupX, Mode: updates.ModeSecurityAuto, Window: sunday(), AutoReboot: true,
		AgentUpdate: updates.AgentManual, Channel: update.ChannelStable,
		Rollout: updates.Rollout{MaxParallel: 1, StopOnFailure: true, HealthCheck: 10 * time.Minute},
	}
	if !equal(p, want) {
		t.Fatalf("parsed %+v (%+v)", p, p.Window)
	}
	out := p.YAML()
	for _, part := range []string{"kind: UpdatePolicy", "agentUpdate: manual", "channel: stable", "mode: security-auto", "days: [sun]", `start: "03:00"`, "duration: 2h", "timezone: Europe/Berlin", "healthCheck: 10m", "maxParallel: 1"} {
		if !strings.Contains(string(out), part) {
			t.Errorf("export lacks %q:\n%s", part, out)
		}
	}
	again, err := updates.ParsePolicy(groupX, out)
	if err != nil || !equal(again, p) {
		t.Errorf("round trip = %+v, %v\n%s", again, err, out)
	}
	// Without a window, report only; several days, minutes.
	other := updates.Policy{
		Group: groupX, Mode: updates.ModeAllAuto, AutoReboot: false, AgentUpdate: updates.AgentAuto, Channel: update.ChannelBeta,
		Window:  &routing.Schedule{Days: []time.Weekday{time.Monday, time.Saturday}, Start: 22*time.Hour + 30*time.Minute, Duration: 90 * time.Minute, TimeZone: "UTC"},
		Rollout: updates.Rollout{MaxParallel: 50, HealthCheck: 2 * time.Hour},
	}
	for _, p := range []updates.Policy{updates.DefaultPolicy(groupX), other} {
		back, err := updates.ParsePolicy(groupX, p.YAML())
		if err != nil || !equal(back, p) {
			t.Errorf("round trip of %+v = %+v, %v\n%s", p, back, err, p.YAML())
		}
	}
}

func equal(a, b updates.Policy) bool {
	if (a.Window == nil) != (b.Window == nil) {
		return false
	}
	if a.Window != nil {
		wa, wb := *a.Window, *b.Window
		if wa.Start != wb.Start || wa.Duration != wb.Duration || wa.TimeZone != wb.TimeZone || len(wa.Days) != len(wb.Days) {
			return false
		}
		for i := range wa.Days {
			if wa.Days[i] != wb.Days[i] {
				return false
			}
		}
	}
	a.Window, b.Window = nil, nil
	return a == b
}

// M4-31: what an import leaves out keeps the default – stopOnFailure too, which stops a rollout
// at its first failure; written as false it stays false.
func TestParsePolicy_AnOmittedStopOnFailureStopsTheRollout(t *testing.T) {
	without := strings.Replace(konzept, " stopOnFailure: true,", "", 1)
	if without == konzept {
		t.Fatal("the example has no stopOnFailure")
	}
	p, err := updates.ParsePolicy(groupX, []byte(without))
	if err != nil || !p.Rollout.StopOnFailure {
		t.Errorf("omitted: %+v, %v", p.Rollout, err)
	}
	p, err = updates.ParsePolicy(groupX, []byte(strings.Replace(konzept, "stopOnFailure: true", "stopOnFailure: false", 1)))
	if err != nil || p.Rollout.StopOnFailure {
		t.Errorf("false: %+v, %v", p.Rollout, err)
	}
}

func TestParsePolicy_RefusesWhatIsNotAPolicy(t *testing.T) {
	for name, doc := range map[string]string{
		"unknown field":    strings.Replace(konzept, "autoReboot", "autoreboot", 1),
		"kind":             strings.Replace(konzept, "UpdatePolicy", "AlertRule", 1),
		"mode":             strings.Replace(konzept, "security-auto", "nightly", 1),
		"day":              strings.Replace(konzept, "[ sun ]", "[ sunday ]", 1),
		"start":            strings.Replace(konzept, `"03:00"`, `"3am"`, 1),
		"start 24:00":      strings.Replace(konzept, `"03:00"`, `"24:00"`, 1),
		"start 03:60":      strings.Replace(konzept, `"03:00"`, `"03:60"`, 1),
		"start 3:00":       strings.Replace(konzept, `"03:00"`, `"3:00"`, 1),
		"duration":         strings.Replace(konzept, "duration: 2h", "duration: two", 1),
		"short window":     strings.Replace(konzept, "duration: 2h", "duration: 10m", 1),
		"time zone":        strings.Replace(konzept, "Europe/Berlin", "Mars/Olympus", 1),
		"local zone":       strings.Replace(konzept, "Europe/Berlin", "Local", 1),
		"path zone":        strings.Replace(konzept, "Europe/Berlin", "../../etc/passwd", 1),
		"parallel 0":       strings.Replace(konzept, "maxParallel: 1", "maxParallel: -1", 1),
		"parallel 51":      strings.Replace(konzept, "maxParallel: 1", "maxParallel: 51", 1),
		"health 4m":        strings.Replace(konzept, "healthCheck: 10m", "healthCheck: 4m", 1),
		"health 3h":        strings.Replace(konzept, "healthCheck: 10m", "healthCheck: 3h", 1),
		"health seconds":   strings.Replace(konzept, "healthCheck: 10m", "healthCheck: 10m30s", 1),
		"auto no window":   "kind: UpdatePolicy\nspec:\n  mode: all-auto\n",
		"reboot no window": "kind: UpdatePolicy\nspec:\n  mode: report\n  autoReboot: true\n",
		"agent update":     strings.Replace(konzept, "autoReboot: true", "autoReboot: true\n  agentUpdate: always", 1),
		"channel":          strings.Replace(konzept, "autoReboot: true", "autoReboot: true\n  channel: nightly", 1),
		"agent no window":  "kind: UpdatePolicy\nspec:\n  mode: report\n  agentUpdate: auto\n",
		"two documents":    konzept + "---\n" + konzept,
		"not yaml":         "kind: [",
		"too large":        konzept + "#" + strings.Repeat("x", updates.MaxPolicyBytes),
	} {
		if _, err := updates.ParsePolicy(groupX, []byte(doc)); !errors.Is(err, updates.ErrInvalidPolicy) {
			t.Errorf("%s: %v, want invalid", name, err)
		}
	}
}

// Alpine has no security metadata (KONZEPT O6).
func TestModeFor_AlpineInSecurityAutoOnlyReports(t *testing.T) {
	p := updates.Policy{Mode: updates.ModeSecurityAuto}
	if m, note := p.ModeFor(update.Apk); m != updates.ModeReport || !strings.Contains(note, "all-auto") {
		t.Errorf("apk: %v %q", m, note)
	}
	if m, note := p.ModeFor(update.Dnf); m != updates.ModeSecurityAuto || note != "" {
		t.Errorf("dnf: %v %q", m, note)
	}
	if m, _ := (updates.Policy{Mode: updates.ModeAllAuto}).ModeFor(update.Apk); m != updates.ModeAllAuto {
		t.Errorf("all-auto on apk: %v", m)
	}
}

// policyStore keeps one group, groupX.
type policyStore struct {
	stored *updates.Policy
	audit  []access.AuditEntry
	scope  tenant.Scope
}

func (s *policyStore) Policy(_ context.Context, scope tenant.Scope, group tenant.GroupID) (updates.Policy, bool, error) {
	s.scope = scope
	if group != groupX || !scope.AllowsGroup(groupX) {
		return updates.Policy{}, false, nil
	}
	if s.stored != nil {
		return *s.stored, true, nil
	}
	return updates.DefaultPolicy(group), true, nil
}

func (s *policyStore) Policies(ctx context.Context, scope tenant.Scope) ([]updates.NamedPolicy, error) {
	p, ok, _ := s.Policy(ctx, scope, groupX)
	if !ok {
		return []updates.NamedPolicy{}, nil
	}
	return []updates.NamedPolicy{{Policy: p, GroupName: "web"}}, nil
}

func (s *policyStore) PutPolicy(_ context.Context, _ tenant.Scope, group tenant.GroupID, p *updates.Policy, audit access.AuditEntry) error {
	if group != groupX {
		return access.ErrNotFound
	}
	s.stored = p
	s.audit = append(s.audit, audit)
	return nil
}

func fresh(c access.Caller) access.Caller {
	c.StepUpAt = t0
	return c
}

func TestPolicyService_AdminsChangeWithAStepUpAndViewersRead(t *testing.T) {
	store := &policyStore{}
	svc := updates.NewPolicyService(store, authz.NewPolicy(), func() time.Time { return t0.Add(time.Minute) })
	ctx := context.Background()
	p, err := updates.ParsePolicy(groupX, []byte(konzept))
	if err != nil {
		t.Fatal(err)
	}
	// Only an admin of all groups, only with a fresh step-up.
	for name, c := range map[string]access.Caller{
		"operator":     fresh(caller(role.Operator, true)),
		"group admin":  fresh(caller(role.Admin, false)),
		"no step-up":   caller(role.Admin, true),
		"aged step-up": func() access.Caller { c := caller(role.Admin, true); c.StepUpAt = t0.Add(-time.Hour); return c }(),
	} {
		_, err := svc.Set(ctx, c, p)
		if !errors.Is(err, access.ErrForbidden) && !errors.Is(err, access.ErrStepUpRequired) {
			t.Errorf("%s: Set() = %v", name, err)
		}
		if _, err := svc.Import(ctx, c, groupX, []byte(konzept)); err == nil {
			t.Errorf("%s: Import() succeeded", name)
		}
		if err := svc.Reset(ctx, c, groupX); err == nil {
			t.Errorf("%s: Reset() succeeded", name)
		}
	}
	if store.stored != nil || len(store.audit) != 0 {
		t.Fatal("stored without the right")
	}
	admin := fresh(caller(role.Admin, true))
	set, err := svc.Set(ctx, admin, p)
	if err != nil {
		t.Fatal(err)
	}
	if set.UpdatedBy != user || !set.UpdatedAt.Equal(t0.Add(time.Minute)) {
		t.Errorf("set = %+v", set)
	}
	a := store.audit[0]
	if a.Action != "update_policy.update" || a.Target != "group:"+groupX.String() || a.Details["mode"] != "security-auto" ||
		a.Details["auto_reboot"] != "true" || !strings.Contains(a.Details["window"], "Europe/Berlin") || a.Actor != user {
		t.Errorf("audit = %+v", a)
	}
	// A viewer of the group reads it; a group it can't view is not found.
	got, err := svc.Get(ctx, caller(role.Viewer, false), groupX)
	if err != nil || got.Mode != updates.ModeSecurityAuto {
		t.Errorf("Get() = %+v, %v", got, err)
	}
	if _, err := svc.Get(ctx, caller(role.Viewer, false), tenant.GroupID(org)); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("Get(other group) = %v", err)
	}
	if list, err := svc.List(ctx, caller(role.Viewer, false)); err != nil || len(list) != 1 || list[0].GroupName != "web" {
		t.Errorf("List() = %+v, %v", list, err)
	}
	none := access.Caller{Principal: authz.Principal{User: user, Org: org}}
	if _, err := svc.Get(ctx, none, groupX); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("Get() without a role = %v", err)
	}
	if _, err := svc.List(ctx, none); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("List() without a role = %v", err)
	}
	// Invalid, imported, reset.
	bad := p
	bad.Rollout.MaxParallel = 0
	if _, err := svc.Set(ctx, admin, bad); !errors.Is(err, updates.ErrInvalidPolicy) {
		t.Errorf("Set(invalid) = %v", err)
	}
	if _, err := svc.Import(ctx, admin, groupX, []byte("kind: UpdatePolicy\nspec:\n  mode: report\n")); err != nil || store.stored.Mode != updates.ModeReport {
		t.Errorf("Import() = %v, stored %+v", err, store.stored)
	}
	if _, err := svc.Import(ctx, admin, groupX, []byte("nope")); !errors.Is(err, updates.ErrInvalidPolicy) {
		t.Errorf("Import(invalid) = %v", err)
	}
	if err := svc.Reset(ctx, admin, groupX); err != nil || store.stored != nil || store.audit[len(store.audit)-1].Action != "update_policy.reset" {
		t.Errorf("Reset() = %v, %+v", err, store.audit)
	}
	if err := svc.Reset(ctx, admin, tenant.GroupID(org)); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("Reset(unknown group) = %v", err)
	}
}
