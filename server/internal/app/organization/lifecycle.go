package organization

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/invitation"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Deletion grace (D29, ADR 0029): an organization scheduled for deletion is deleted after it;
// operators set it with SERMON_ORG_DELETION_GRACE, but not below MinDeletionGrace.
const (
	DefaultDeletionGrace = 30 * 24 * time.Hour
	MinDeletionGrace     = 7 * 24 * time.Hour
	// ReminderBefore is when the admins are reminded of the deletion.
	ReminderBefore = 7 * 24 * time.Hour
	// MaintainInterval is how often reminders and due deletions are handled.
	MaintainInterval = time.Hour
)

var (
	// ErrNameMismatch means the confirmation does not match the organization's name.
	ErrNameMismatch = errors.New("type the name of the organization to confirm")
	// ErrNotLocked means the action needs a locked organization (unlock, delete, audit export).
	ErrNotLocked = errors.New("the organization is not locked")
	// ErrGraceTooShort means the configured deletion grace is below MinDeletionGrace.
	ErrGraceTooShort = fmt.Errorf("the deletion grace must be at least %v", MinDeletionGrace)
)

// State is the lock of an organization.
type State struct {
	Name          string
	LockedAt      *time.Time
	DeletionDueAt *time.Time
}

// ExportedEntry is one entry of an organization's audit log with its place in the hash chain.
type ExportedEntry struct {
	Seq      int64
	At       time.Time
	Actor    *identity.UserID
	Action   string
	Target   string
	Details  json.RawMessage
	PrevHash []byte
	Hash     []byte
}

// Reminder is an organization whose admins are reminded of its deletion.
type Reminder struct {
	Org   tenant.OrgID
	Name  string
	DueAt time.Time
}

// LifecycleStore persists locks and deletions. Every change writes its entry into the
// organization's audit log (while it exists) and into the instance's, in the same transaction.
type LifecycleStore interface {
	// State returns name and lock (access.ErrNotFound for an unknown organization).
	State(ctx context.Context, org tenant.OrgID) (State, error)
	// SetLock sets lock and deletion date (nil: none); a lock ends the organization's sessions.
	SetLock(ctx context.Context, org tenant.OrgID, lockedAt, dueAt *time.Time, audit access.AuditEntry) error
	// AdminEmails returns the addresses of the admins of all groups.
	AdminEmails(ctx context.Context, org tenant.OrgID) ([]string, error)
	// ExportAudit returns the whole audit log, oldest first, whether its chain holds, and
	// records the export by the superadmin in the instance's log.
	ExportAudit(ctx context.Context, org tenant.OrgID, by identity.UserID) ([]ExportedEntry, bool, error)
	// Purge deletes a locked organization that is due, with the accounts that have no other
	// membership, and records it in the instance's log; it returns the deleted accounts.
	Purge(ctx context.Context, org tenant.OrgID, now time.Time, by *identity.UserID, name string) (int64, error)
	// ClaimReminders returns the scheduled organizations due before the time that were not
	// reminded yet, and marks them – each organization once, across server instances.
	ClaimReminders(ctx context.Context, before time.Time) ([]Reminder, error)
	// DueForDeletion returns the locked organizations whose deletion is due.
	DueForDeletion(ctx context.Context, now time.Time) ([]tenant.OrgID, error)
}

// AgentSessions ends the agent sessions of an organization on this server at once; other
// instances end theirs at their next certificate check.
type AgentSessions interface {
	TerminateOrganization(org tenant.OrgID)
}

// Lifecycle locks, schedules, lifts and deletes organizations (M2-33, D29, ADR 0029): only
// superadmins, each with a fresh step-up and the organization's name typed as confirmation.
type Lifecycle struct {
	store  LifecycleStore
	policy authz.Policy
	now    func() time.Time
	grace  time.Duration
	mailer invitation.Mailer
	agents AgentSessions
	logs   LogEraser
	logger *slog.Logger
}

// LogEraser deletes the logs of an organization (Loki, M3-10); an error leaves the organization
// due, the next run tries again.
type LogEraser interface {
	EraseOrganization(ctx context.Context, org tenant.OrgID) error
}

// LifecycleOption configures a Lifecycle.
type LifecycleOption func(*Lifecycle)

// WithLifecycleMailer mails the admins of the organization (lock, schedule, lift, reminder).
func WithLifecycleMailer(mailer invitation.Mailer) LifecycleOption {
	return func(l *Lifecycle) { l.mailer = mailer }
}

// WithAgentSessions ends the organization's agent sessions on a lock.
func WithAgentSessions(agents AgentSessions) LifecycleOption {
	return func(l *Lifecycle) { l.agents = agents }
}

// WithLogs deletes an organization's logs before its master data (M3-10).
func WithLogs(eraser LogEraser) LifecycleOption {
	return func(l *Lifecycle) { l.logs = eraser }
}

// WithLogger logs mails that could not be sent and the deletions of the maintenance.
func WithLogger(logger *slog.Logger) LifecycleOption {
	return func(l *Lifecycle) { l.logger = logger }
}

// NewLifecycle returns the use case; grace is the deletion grace (ErrGraceTooShort below the
// minimum).
func NewLifecycle(store LifecycleStore, policy authz.Policy, now func() time.Time, grace time.Duration, options ...LifecycleOption) (*Lifecycle, error) {
	if grace < MinDeletionGrace {
		return nil, ErrGraceTooShort
	}
	l := &Lifecycle{store: store, policy: policy, now: now, grace: grace, logger: slog.New(slog.DiscardHandler)}
	for _, o := range options {
		o(l)
	}
	return l, nil
}

// confirm authorizes the superadmin with a fresh step-up and checks the typed name.
func (l *Lifecycle) confirm(ctx context.Context, c access.Caller, org tenant.OrgID, typed string) (State, error) {
	if !l.policy.AllowedInstance(c.Principal, authz.ManageOrganizations) {
		return State{}, fmt.Errorf("%w: %w", access.ErrForbidden, errNoSuperadmin)
	}
	if l.now().Sub(c.StepUpAt) > access.StepUpMaxAge {
		return State{}, access.ErrStepUpRequired
	}
	st, err := l.store.State(ctx, org)
	if err != nil {
		return State{}, fmt.Errorf("load organization: %w", err)
	}
	if strings.TrimSpace(typed) != st.Name {
		return State{}, ErrNameMismatch
	}
	return st, nil
}

func entry(c access.Caller, action string, org tenant.OrgID, details map[string]string) access.AuditEntry {
	return access.AuditEntry{Actor: c.Principal.User, Action: action, Target: "org:" + org.String(), Details: details}
}

// Lock locks the organization without a deadline: no logins, agents, enrollments or
// invitations until a superadmin lifts it. A scheduled deletion stays scheduled.
func (l *Lifecycle) Lock(ctx context.Context, c access.Caller, org tenant.OrgID, typed string) error {
	st, err := l.confirm(ctx, c, org, typed)
	if err != nil {
		return err
	}
	if st.LockedAt != nil {
		return nil
	}
	now := l.now()
	if err := l.store.SetLock(ctx, org, &now, nil, entry(c, "org.lock", org, map[string]string{"name": st.Name})); err != nil {
		return fmt.Errorf("lock organization: %w", err)
	}
	l.locked(org)
	l.mail(ctx, org, lockedMail(st.Name))
	return nil
}

// ScheduleDeletion locks the organization and deletes it after the grace.
func (l *Lifecycle) ScheduleDeletion(ctx context.Context, c access.Caller, org tenant.OrgID, typed string) (time.Time, error) {
	st, err := l.confirm(ctx, c, org, typed)
	if err != nil {
		return time.Time{}, err
	}
	now := l.now()
	locked, due := now, now.Add(l.grace)
	if st.LockedAt != nil {
		locked = *st.LockedAt
	}
	details := map[string]string{"name": st.Name, "deletion_due_at": due.UTC().Format(time.RFC3339)}
	if err := l.store.SetLock(ctx, org, &locked, &due, entry(c, "org.schedule_deletion", org, details)); err != nil {
		return time.Time{}, fmt.Errorf("schedule deletion: %w", err)
	}
	l.locked(org)
	l.mail(ctx, org, scheduledMail(st.Name, due))
	return due, nil
}

// Unlock lifts the lock and a scheduled deletion: logins and agents work again, without a new
// enrollment.
func (l *Lifecycle) Unlock(ctx context.Context, c access.Caller, org tenant.OrgID, typed string) error {
	st, err := l.confirm(ctx, c, org, typed)
	if err != nil {
		return err
	}
	if st.LockedAt == nil {
		return ErrNotLocked
	}
	if err := l.store.SetLock(ctx, org, nil, nil, entry(c, "org.unlock", org, map[string]string{"name": st.Name})); err != nil {
		return fmt.Errorf("unlock organization: %w", err)
	}
	l.mail(ctx, org, unlockedMail(st.Name))
	return nil
}

// DeleteNow deletes a locked organization at once (e.g. on its own request) and returns the
// number of accounts deleted with it.
func (l *Lifecycle) DeleteNow(ctx context.Context, c access.Caller, org tenant.OrgID, typed string) (int64, error) {
	st, err := l.confirm(ctx, c, org, typed)
	if err != nil {
		return 0, err
	}
	if st.LockedAt == nil {
		return 0, ErrNotLocked
	}
	now := l.now()
	details := map[string]string{"name": st.Name, "deletion_due_at": now.UTC().Format(time.RFC3339)}
	if err := l.store.SetLock(ctx, org, st.LockedAt, &now, entry(c, "org.schedule_deletion", org, details)); err != nil {
		return 0, fmt.Errorf("delete organization: %w", err)
	}
	by := c.Principal.User
	users, err := l.purge(ctx, org, now, &by, st.Name)
	if err != nil {
		return 0, fmt.Errorf("delete organization: %w", err)
	}
	return users, nil
}

// purge deletes the logs, then the master data: the master data keep the organization due, so a
// failure of the log store is tried again by the next run.
func (l *Lifecycle) purge(ctx context.Context, org tenant.OrgID, at time.Time, by *identity.UserID, name string) (int64, error) {
	if l.logs != nil {
		if err := l.logs.EraseOrganization(ctx, org); err != nil {
			return 0, fmt.Errorf("delete the logs: %w", err)
		}
	}
	users, err := l.store.Purge(ctx, org, at, by, name)
	if err != nil {
		return 0, fmt.Errorf("delete the data: %w", err)
	}
	return users, nil
}

// ExportAudit returns the organization's audit log as a file of JSON lines with the hash chain
// (the superadmin sees tenant data here – only while the organization is locked, recorded in
// the instance's log).
func (l *Lifecycle) ExportAudit(ctx context.Context, c access.Caller, org tenant.OrgID) (name string, file []byte, valid bool, err error) {
	if !l.policy.AllowedInstance(c.Principal, authz.ManageOrganizations) {
		return "", nil, false, fmt.Errorf("%w: %w", access.ErrForbidden, errNoSuperadmin)
	}
	if l.now().Sub(c.StepUpAt) > access.StepUpMaxAge {
		return "", nil, false, access.ErrStepUpRequired
	}
	st, err := l.store.State(ctx, org)
	if err != nil {
		return "", nil, false, fmt.Errorf("load organization: %w", err)
	}
	if st.LockedAt == nil {
		return "", nil, false, ErrNotLocked
	}
	entries, valid, err := l.store.ExportAudit(ctx, org, c.Principal.User)
	if err != nil {
		return "", nil, false, fmt.Errorf("export audit log: %w", err)
	}
	file, err = auditFile(org, st.Name, l.now(), entries, valid)
	if err != nil {
		return "", nil, false, err
	}
	return "audit-" + org.String() + ".jsonl", file, valid, nil
}

// exportLine is one line of the audit export.
type exportLine struct {
	Seq      int64  `json:"seq"`
	At       string `json:"at"`
	Actor    string `json:"actor,omitempty"`
	Action   string `json:"action"`
	Target   string `json:"target"`
	Details  string `json:"details"` // the stored text, verbatim: it is part of the hash
	PrevHash string `json:"prev_hash"`
	Hash     string `json:"hash"`
}

// exportHeader is the first line: what the file is and how to check the chain.
type exportHeader struct {
	Organization string `json:"organization"`
	Name         string `json:"name"`
	ExportedAt   string `json:"exported_at"`
	Entries      int    `json:"entries"`
	ChainValid   bool   `json:"chain_valid"`
	Hash         string `json:"hash"`
}

// HashRule documents the chain in the export: hash = SHA-256(prev_hash || fields), the fields
// joined by the unit separator U+001F.
const HashRule = "sha256(prev_hash || org US seq US at US actor US action US target US details), US = U+001F, at in UTC with microseconds, details as the string in the file"

func auditFile(org tenant.OrgID, name string, now time.Time, entries []ExportedEntry, valid bool) ([]byte, error) {
	var b strings.Builder
	header, err := json.Marshal(exportHeader{
		Organization: org.String(), Name: name, ExportedAt: now.UTC().Format(time.RFC3339), Entries: len(entries), ChainValid: valid, Hash: HashRule,
	})
	if err != nil {
		return nil, fmt.Errorf("encode audit export: %w", err)
	}
	b.Write(header)
	b.WriteByte('\n')
	for _, e := range entries {
		line := exportLine{
			Seq: e.Seq, At: e.At.UTC().Format("2006-01-02T15:04:05.000000Z"), Action: e.Action, Target: e.Target,
			Details: string(e.Details), PrevHash: hex.EncodeToString(e.PrevHash), Hash: hex.EncodeToString(e.Hash),
		}
		if e.Actor != nil {
			line.Actor = e.Actor.String()
		}
		out, err := json.Marshal(line)
		if err != nil {
			return nil, fmt.Errorf("encode audit export: %w", err)
		}
		b.Write(out)
		b.WriteByte('\n')
	}
	return []byte(b.String()), nil
}

// Maintain sends the reminders 7 days before a deletion and deletes the organizations that are
// due; it runs every MaintainInterval (Run) and returns the first error after trying all.
func (l *Lifecycle) Maintain(ctx context.Context) error {
	now := l.now()
	var first error
	reminders, err := l.store.ClaimReminders(ctx, now.Add(ReminderBefore))
	if err != nil {
		first = fmt.Errorf("claim reminders: %w", err)
	}
	for _, r := range reminders {
		l.mail(ctx, r.Org, reminderMail(r.Name, r.DueAt))
	}
	due, err := l.store.DueForDeletion(ctx, now)
	if err != nil && first == nil {
		first = fmt.Errorf("due deletions: %w", err)
	}
	for _, org := range due {
		st, err := l.store.State(ctx, org)
		if err == nil {
			var users int64
			users, err = l.purge(ctx, org, now, nil, st.Name)
			if err == nil {
				l.logger.InfoContext(ctx, "organization deleted after its grace", "org", org.String(), "accounts", users)
				continue
			}
		}
		l.logger.ErrorContext(ctx, "organization deletion failed", "org", org.String(), "error", err)
		if first == nil {
			first = fmt.Errorf("delete organization: %w", err)
		}
	}
	return first
}

// Run calls Maintain every MaintainInterval until ctx ends.
func (l *Lifecycle) Run(ctx context.Context) error {
	ticker := time.NewTicker(MaintainInterval)
	defer ticker.Stop()
	for {
		if err := l.Maintain(ctx); err != nil {
			l.logger.WarnContext(ctx, "organization maintenance", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (l *Lifecycle) locked(org tenant.OrgID) {
	if l.agents != nil {
		l.agents.TerminateOrganization(org)
	}
}

// mail tells the admins of all groups; a failure is logged, the change stands.
func (l *Lifecycle) mail(ctx context.Context, org tenant.OrgID, m mailText) {
	if l.mailer == nil {
		return
	}
	to, err := l.store.AdminEmails(ctx, org)
	if err != nil {
		l.logger.WarnContext(ctx, "cannot load the admins for the mail", "org", org.String(), "error", err)
		return
	}
	for _, addr := range to {
		if err := l.mailer.SendText(ctx, addr, m.subject, m.text); err != nil {
			l.logger.WarnContext(ctx, "cannot mail an admin of the organization", "org", org.String(), "error", err)
		}
	}
}

type mailText struct{ subject, text string }

func dateOf(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") }

func lockedMail(name string) mailText {
	return mailText{
		subject: "SerMon: „" + name + "“ ist gesperrt / \"" + name + "\" is locked",
		text: "Der Superadmin hat die Organisation „" + name + "“ gesperrt. Niemand kann sich anmelden, die Agents senden nicht\n" +
			"mehr und puffern lokal. Die Daten bleiben erhalten; nur der Superadmin kann die Sperre aufheben.\n\n" +
			"The superadmin locked the organization \"" + name + "\". Nobody can sign in, the agents stop sending and buffer\n" +
			"locally. The data stays; only the superadmin can lift the lock.\n",
	}
}

func scheduledMail(name string, due time.Time) mailText {
	return mailText{
		subject: "SerMon: „" + name + "“ wird gelöscht / \"" + name + "\" will be deleted",
		text: "Der Superadmin hat die Organisation „" + name + "“ gesperrt und zum Löschen vorgemerkt. Am " + dateOf(due) + "\n" +
			"werden alle ihre Daten und die Konten ohne weitere Organisation endgültig gelöscht. Bis dahin kann der\n" +
			"Superadmin die Sperre aufheben.\n\n" +
			"The superadmin locked the organization \"" + name + "\" and scheduled it for deletion. On " + dateOf(due) + "\n" +
			"all its data and the accounts without another organization are deleted for good. Until then the superadmin\n" +
			"can lift the lock.\n",
	}
}

func unlockedMail(name string) mailText {
	return mailText{
		subject: "SerMon: „" + name + "“ ist wieder freigegeben / \"" + name + "\" is unlocked",
		text: "Der Superadmin hat die Sperre der Organisation „" + name + "“ aufgehoben. Anmelden geht wieder, die Agents\n" +
			"senden ihren Puffer nach.\n\n" +
			"The superadmin lifted the lock of the organization \"" + name + "\". Signing in works again, the agents send\n" +
			"their buffer.\n",
	}
}

func reminderMail(name string, due time.Time) mailText {
	return mailText{
		subject: "SerMon: „" + name + "“ wird in 7 Tagen gelöscht / \"" + name + "\" is deleted in 7 days",
		text: "Erinnerung: Die Organisation „" + name + "“ wird am " + dateOf(due) + " endgültig gelöscht. Wende dich an den\n" +
			"Superadmin, wenn das nicht geschehen soll.\n\n" +
			"Reminder: the organization \"" + name + "\" is deleted for good on " + dateOf(due) + ". Contact the superadmin\n" +
			"if that should not happen.\n",
	}
}
