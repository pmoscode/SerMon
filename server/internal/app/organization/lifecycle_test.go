package organization_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/organization"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// lifecycleStore keeps the lock of one organization like the Postgres store.
type lifecycleStore struct {
	state    organization.State
	audits   []access.AuditEntry
	purged   []string // "org by"
	reminded bool
	entries  []organization.ExportedEntry
	exported []identity.UserID
}

func (s *lifecycleStore) State(_ context.Context, org tenant.OrgID) (organization.State, error) {
	if org != orgA || s.state.Name == "" {
		return organization.State{}, access.ErrNotFound
	}
	return s.state, nil
}

func (s *lifecycleStore) SetLock(_ context.Context, _ tenant.OrgID, locked, due *time.Time, a access.AuditEntry) error {
	s.state.LockedAt, s.state.DeletionDueAt = locked, due
	s.audits = append(s.audits, a)
	return nil
}

func (s *lifecycleStore) AdminEmails(context.Context, tenant.OrgID) ([]string, error) {
	return []string{"ann@example.org", "bob@example.org"}, nil
}

func (s *lifecycleStore) ExportAudit(_ context.Context, _ tenant.OrgID, by identity.UserID) ([]organization.ExportedEntry, bool, error) {
	s.exported = append(s.exported, by)
	return s.entries, true, nil
}

func (s *lifecycleStore) Purge(_ context.Context, org tenant.OrgID, at time.Time, by *identity.UserID, name string) (int64, error) {
	if s.state.LockedAt == nil || s.state.DeletionDueAt == nil || s.state.DeletionDueAt.After(at) {
		return 0, errors.New("not due")
	}
	actor := "system"
	if by != nil {
		actor = by.String()
	}
	s.purged = append(s.purged, org.String()+" "+actor+" "+name)
	s.state = organization.State{}
	return 2, nil
}

func (s *lifecycleStore) ClaimReminders(_ context.Context, before time.Time) ([]organization.Reminder, error) {
	if s.reminded || s.state.DeletionDueAt == nil || s.state.DeletionDueAt.After(before) {
		return nil, nil
	}
	s.reminded = true
	return []organization.Reminder{{Org: orgA, Name: s.state.Name, DueAt: *s.state.DeletionDueAt}}, nil
}

func (s *lifecycleStore) DueForDeletion(_ context.Context, at time.Time) ([]tenant.OrgID, error) {
	if s.state.LockedAt != nil && s.state.DeletionDueAt != nil && !s.state.DeletionDueAt.After(at) {
		return []tenant.OrgID{orgA}, nil
	}
	return nil, nil
}

type mails struct{ sent []string }

func (m *mails) SendText(_ context.Context, to, subject, _ string) error {
	m.sent = append(m.sent, to+": "+subject)
	return nil
}

type agents struct{ ended []tenant.OrgID }

func (a *agents) TerminateOrganization(org tenant.OrgID) { a.ended = append(a.ended, org) }

type world struct {
	store  *lifecycleStore
	mails  *mails
	agents *agents
	clock  *time.Time
	l      *organization.Lifecycle
}

func newWorld(t *testing.T) *world {
	t.Helper()
	clock := now
	w := &world{store: &lifecycleStore{state: organization.State{Name: "Alpha AG"}}, mails: &mails{}, agents: &agents{}, clock: &clock}
	l, err := organization.NewLifecycle(w.store, authz.NewPolicy(), func() time.Time { return *w.clock }, organization.DefaultDeletionGrace,
		organization.WithLifecycleMailer(w.mails), organization.WithAgentSessions(w.agents))
	if err != nil {
		t.Fatal(err)
	}
	w.l = l
	return w
}

func superadminAt(w *world) access.Caller {
	return access.Caller{Principal: authz.Principal{User: super, Superadmin: true}, StepUpAt: *w.clock}
}

// Only superadmins, with a fresh step-up and the organization's name typed (any case); nothing
// changes otherwise.
func TestLifecycle_NeedsSuperadminStepUpAndTheName(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	orgAdmin := access.Caller{Principal: authz.Principal{User: super, Org: orgA, Bindings: []authz.Binding{{AllGroups: true, Role: role.Admin}}}, StepUpAt: now}
	stale := superadminAt(w)
	stale.StepUpAt = now.Add(-time.Hour)
	for name, c := range map[string]struct {
		caller access.Caller
		typed  string
		want   error
	}{
		"org admin":     {orgAdmin, "Alpha AG", access.ErrForbidden},
		"stale step-up": {stale, "Alpha AG", access.ErrStepUpRequired},
		"wrong name":    {superadminAt(w), "Alpha", organization.ErrNameMismatch},
		"other case":    {superadminAt(w), "alpha ag", organization.ErrNameMismatch},
	} {
		if err := w.l.Lock(ctx, c.caller, orgA, c.typed); !errors.Is(err, c.want) {
			t.Errorf("Lock %s: %v, want %v", name, err, c.want)
		}
		if _, err := w.l.ScheduleDeletion(ctx, c.caller, orgA, c.typed); !errors.Is(err, c.want) {
			t.Errorf("ScheduleDeletion %s: %v, want %v", name, err, c.want)
		}
		if _, err := w.l.DeleteNow(ctx, c.caller, orgA, c.typed); !errors.Is(err, c.want) {
			t.Errorf("DeleteNow %s: %v, want %v", name, err, c.want)
		}
	}
	if w.store.state.LockedAt != nil || len(w.store.audits) != 0 || len(w.mails.sent) != 0 {
		t.Errorf("changed: %+v, %v, %v", w.store.state, w.store.audits, w.mails.sent)
	}
	if _, err := organization.NewLifecycle(w.store, authz.NewPolicy(), time.Now, organization.MinDeletionGrace-time.Hour); !errors.Is(err, organization.ErrGraceTooShort) {
		t.Errorf("grace below 7 days: %v", err)
	}
}

// Lock: at once, agents of this server ended, admins mailed, audited; lifting mails again;
// unlocking an active organization is refused.
func TestLifecycle_LockAndUnlock(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	if err := w.l.Unlock(ctx, superadminAt(w), orgA, "Alpha AG"); !errors.Is(err, organization.ErrNotLocked) {
		t.Errorf("unlock active: %v", err)
	}
	if err := w.l.Lock(ctx, superadminAt(w), orgA, " Alpha AG "); err != nil {
		t.Fatal(err)
	}
	if w.store.state.LockedAt == nil || !w.store.state.LockedAt.Equal(now) || w.store.state.DeletionDueAt != nil {
		t.Errorf("state %+v", w.store.state)
	}
	if len(w.agents.ended) != 1 || w.store.audits[0].Action != "org.lock" || len(w.mails.sent) != 2 || !strings.Contains(w.mails.sent[0], "gesperrt") {
		t.Errorf("agents %v, audits %v, mails %v", w.agents.ended, w.store.audits, w.mails.sent)
	}
	if err := w.l.Lock(ctx, superadminAt(w), orgA, "Alpha AG"); err != nil || len(w.store.audits) != 1 {
		t.Errorf("locking again: %v, audits %v", err, w.store.audits)
	}
	if err := w.l.Unlock(ctx, superadminAt(w), orgA, "Alpha AG"); err != nil {
		t.Fatal(err)
	}
	if w.store.state.LockedAt != nil || w.store.audits[1].Action != "org.unlock" || !strings.Contains(w.mails.sent[2], "freigegeben") {
		t.Errorf("after unlock: %+v %v %v", w.store.state, w.store.audits, w.mails.sent)
	}
}

// Scheduling locks and sets the date after the grace; 7 days before, the admins are reminded once;
// at the date the maintenance deletes it.
func TestLifecycle_ScheduleRemindAndDelete(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	due, err := w.l.ScheduleDeletion(ctx, superadminAt(w), orgA, "Alpha AG")
	if err != nil {
		t.Fatal(err)
	}
	if !due.Equal(now.Add(30*24*time.Hour)) || w.store.state.LockedAt == nil || len(w.agents.ended) != 1 {
		t.Errorf("due %v, state %+v", due, w.store.state)
	}
	if a := w.store.audits[0]; a.Action != "org.schedule_deletion" || a.Details["deletion_due_at"] != due.UTC().Format(time.RFC3339) {
		t.Errorf("audit %+v", a)
	}
	if !strings.Contains(w.mails.sent[0], "gelöscht") {
		t.Errorf("mails %v", w.mails.sent)
	}

	*w.clock = now.Add(22 * 24 * time.Hour) // 8 days before
	if err := w.l.Maintain(ctx); err != nil || len(w.mails.sent) != 2 || len(w.store.purged) != 0 {
		t.Fatalf("8 days before: %v, mails %v, purged %v", err, w.mails.sent, w.store.purged)
	}
	*w.clock = now.Add(24 * 24 * time.Hour) // 6 days before
	_ = w.l.Maintain(ctx)
	_ = w.l.Maintain(ctx)
	if len(w.mails.sent) != 4 || !strings.Contains(w.mails.sent[2], "7 Tagen") {
		t.Errorf("reminder once to both admins: %v", w.mails.sent)
	}
	*w.clock = due
	if err := w.l.Maintain(ctx); err != nil || len(w.store.purged) != 1 || w.store.purged[0] != orgA.String()+" system Alpha AG" {
		t.Errorf("at the date: %v, purged %v", err, w.store.purged)
	}
}

// Deleting at once needs a lock; the deletion is the superadmin's.
func TestLifecycle_DeleteNow(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	if _, err := w.l.DeleteNow(ctx, superadminAt(w), orgA, "Alpha AG"); !errors.Is(err, organization.ErrNotLocked) {
		t.Errorf("delete an active organization: %v", err)
	}
	if err := w.l.Lock(ctx, superadminAt(w), orgA, "Alpha AG"); err != nil {
		t.Fatal(err)
	}
	// M3-28: lines up to 5 min ahead would outlive a deletion right after the lock.
	if _, err := w.l.DeleteNow(ctx, superadminAt(w), orgA, "Alpha AG"); !errors.Is(err, organization.ErrLockTooRecent) || len(w.store.purged) != 0 {
		t.Errorf("right after the lock: %v", err)
	}
	*w.clock = w.clock.Add(organization.MinLockBeforeDelete)
	users, err := w.l.DeleteNow(ctx, superadminAt(w), orgA, "Alpha AG")
	if err != nil || users != 2 || w.store.purged[0] != orgA.String()+" "+super.String()+" Alpha AG" {
		t.Errorf("DeleteNow = %d, %v; purged %v", users, err, w.store.purged)
	}
}

// The export: only while locked, recorded with the superadmin, a header and one line per entry
// with the hashes in hex.
func TestLifecycle_ExportAudit(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	actor := identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000a1"))
	w.store.entries = []organization.ExportedEntry{
		{Seq: 1, At: now, Actor: &actor, Action: "org.create", Target: "org:x", Details: json.RawMessage(`{"name": "Alpha AG"}`), PrevHash: []byte{}, Hash: []byte{0xab}},
		{Seq: 2, At: now.Add(time.Second), Action: "org.lock", Target: "org:x", Details: json.RawMessage(`{}`), PrevHash: []byte{0xab}, Hash: []byte{0xcd}},
	}
	if _, _, _, err := w.l.ExportAudit(ctx, superadminAt(w), orgA); !errors.Is(err, organization.ErrNotLocked) {
		t.Errorf("export while active: %v", err)
	}
	if err := w.l.Lock(ctx, superadminAt(w), orgA, "Alpha AG"); err != nil {
		t.Fatal(err)
	}
	name, file, valid, err := w.l.ExportAudit(ctx, superadminAt(w), orgA)
	if err != nil || !valid || name != "audit-"+orgA.String()+".jsonl" || len(w.store.exported) != 1 || w.store.exported[0] != super {
		t.Fatalf("ExportAudit = %q, %v, %v; exported by %v", name, valid, err, w.store.exported)
	}
	lines := strings.Split(strings.TrimSpace(string(file)), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines %v", lines)
	}
	var header map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil || header["entries"] != float64(2) || header["chain_valid"] != true || header["name"] != "Alpha AG" {
		t.Errorf("header %v, %v", header, err)
	}
	var second map[string]any
	if err := json.Unmarshal([]byte(lines[2]), &second); err != nil || second["prev_hash"] != "ab" || second["hash"] != "cd" || second["actor"] != nil {
		t.Errorf("second %v, %v", second, err)
	}
	// The details stay as stored (jsonb prints spaces); compacted, the hash would not match.
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &first); err != nil || first["details"] != `{"name": "Alpha AG"}` {
		t.Errorf("details of the first line %v, %v", first["details"], err)
	}
	if !strings.Contains(lines[1], `"actor":"`+actor.String()+`"`) || !strings.Contains(lines[1], `"at":"2026-11-19T12:00:00.000000Z"`) {
		t.Errorf("first %s", lines[1])
	}
}

// eraser stands for Loki: it fails while down and records the organizations it erased.
type eraser struct {
	down   bool
	erased []tenant.OrgID
}

func (e *eraser) EraseOrganization(_ context.Context, org tenant.OrgID) error {
	if e.down {
		return errors.New("loki unreachable")
	}
	e.erased = append(e.erased, org)
	return nil
}

// M3-10: the logs go before the master data; while the log store fails, the organization stays
// due and the next run tries again (the master data are what makes it due).
func TestLifecycle_ErasesTheLogsFirstAndRetries(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	logs := &eraser{down: true}
	l, err := organization.NewLifecycle(w.store, authz.NewPolicy(), func() time.Time { return *w.clock }, organization.DefaultDeletionGrace,
		organization.WithLogs(logs))
	if err != nil {
		t.Fatal(err)
	}
	due, err := l.ScheduleDeletion(ctx, superadminAt(w), orgA, "Alpha AG")
	if err != nil {
		t.Fatal(err)
	}
	*w.clock = due
	if err := l.Maintain(ctx); err == nil || len(w.store.purged) != 0 {
		t.Fatalf("log store down: %v, purged %v", err, w.store.purged)
	}
	logs.down = false
	if err := l.Maintain(ctx); err != nil || len(w.store.purged) != 1 || len(logs.erased) != 1 || logs.erased[0] != orgA {
		t.Errorf("retry: %v, purged %v, erased %v", err, w.store.purged, logs.erased)
	}

	// Deleting at once: the same order; a failure leaves the organization due for the next run.
	w2 := newWorld(t)
	logs2 := &eraser{down: true}
	l2, _ := organization.NewLifecycle(w2.store, authz.NewPolicy(), func() time.Time { return *w2.clock }, organization.DefaultDeletionGrace,
		organization.WithLogs(logs2))
	if err := l2.Lock(ctx, superadminAt(w2), orgA, "Alpha AG"); err != nil {
		t.Fatal(err)
	}
	*w2.clock = w2.clock.Add(organization.MinLockBeforeDelete)
	if _, err := l2.DeleteNow(ctx, superadminAt(w2), orgA, "Alpha AG"); err == nil || len(w2.store.purged) != 0 {
		t.Errorf("DeleteNow with the log store down: %v, purged %v", err, w2.store.purged)
	}
	if w2.store.state.DeletionDueAt == nil {
		t.Error("the organization must stay due")
	}
	logs2.down = false
	if err := l2.Maintain(ctx); err != nil || len(w2.store.purged) != 1 {
		t.Errorf("next run: %v, purged %v", err, w2.store.purged)
	}
}
