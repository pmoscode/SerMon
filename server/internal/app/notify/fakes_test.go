package notify_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	"github.com/pmoscode/SerMon/server/internal/secrets"
)

var (
	orgA = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
	orgB = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000b"))
	ann  = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000a1"))
)

const secretToken = "tok-very-secret-4711"

// fakeNotifier is a channel type with a URL, a secret token and a priority; it records what it
// sends and fails with the scripted errors first.
type fakeNotifier struct {
	mu        sync.Mutex
	sent      []sent
	errs      []error
	validated []notify.Config
	reject    error
}

type sent struct {
	config  notify.Config
	message notify.Message
}

func (f *fakeNotifier) Type() notify.ChannelType { return "fake" }

func (f *fakeNotifier) Fields() []notify.Field {
	return []notify.Field{
		{Name: "url", Kind: notify.FieldURL, Required: true},
		{Name: "token", Kind: notify.FieldText, Secret: true, Required: true},
		{Name: "priority", Kind: notify.FieldChoice, Choices: []string{"low", "high"}},
	}
}

func (f *fakeNotifier) Validate(c notify.Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.validated = append(f.validated, c)
	return f.reject
}

func (f *fakeNotifier) Send(_ context.Context, c notify.Config, m notify.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sent{config: c, message: m})
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		return err
	}
	return nil
}

func (f *fakeNotifier) messages() []sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sent)
}

// memStore keeps channels and deliveries per organization, like the tables behind RLS.
type memStore struct {
	mu         sync.Mutex
	channels   map[tenant.OrgID]map[notify.ChannelID]notify.StoredChannel
	deliveries map[tenant.OrgID][]notify.Delivery
	audits     []access.AuditEntry
	nextID     int64
}

func newMemStore() *memStore {
	return &memStore{channels: map[tenant.OrgID]map[notify.ChannelID]notify.StoredChannel{}, deliveries: map[tenant.OrgID][]notify.Delivery{}}
}

func mustScope(scope tenant.Scope) {
	if !scope.Valid() || !scope.AllGroups() {
		panic("notification data needs the scope of a whole organization")
	}
}

func (m *memStore) CreateChannel(_ context.Context, scope tenant.Scope, c notify.StoredChannel, a access.AuditEntry) error {
	mustScope(scope)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.channels[scope.OrgID()] == nil {
		m.channels[scope.OrgID()] = map[notify.ChannelID]notify.StoredChannel{}
	}
	m.channels[scope.OrgID()][c.ID] = c
	m.audits = append(m.audits, a)
	return nil
}

func (m *memStore) UpdateChannel(_ context.Context, scope tenant.Scope, c notify.StoredChannel, a access.AuditEntry) error {
	mustScope(scope)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.channels[scope.OrgID()][c.ID]; !ok {
		return access.ErrNotFound
	}
	m.channels[scope.OrgID()][c.ID] = c
	m.audits = append(m.audits, a)
	return nil
}

func (m *memStore) DeleteChannel(_ context.Context, scope tenant.Scope, id notify.ChannelID, a access.AuditEntry) error {
	mustScope(scope)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.channels[scope.OrgID()][id]; !ok {
		return access.ErrNotFound
	}
	delete(m.channels[scope.OrgID()], id)
	m.audits = append(m.audits, a)
	return nil
}

func (m *memStore) Channels(_ context.Context, scope tenant.Scope) ([]notify.StoredChannel, error) {
	mustScope(scope)
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []notify.StoredChannel
	for _, c := range m.channels[scope.OrgID()] {
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b notify.StoredChannel) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return out, nil
}

func (m *memStore) Channel(_ context.Context, scope tenant.Scope, id notify.ChannelID) (notify.StoredChannel, error) {
	mustScope(scope)
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.channels[scope.OrgID()][id]
	if !ok {
		return notify.StoredChannel{}, access.ErrNotFound
	}
	return c, nil
}

func (m *memStore) Enqueue(_ context.Context, scope tenant.Scope, ds []notify.Delivery) error {
	mustScope(scope)
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range ds {
		m.nextID++
		d.ID = m.nextID
		m.deliveries[scope.OrgID()] = append(m.deliveries[scope.OrgID()], d)
	}
	return nil
}

func (m *memStore) Deliveries(_ context.Context, scope tenant.Scope, f notify.DeliveryFilter) ([]notify.Delivery, error) {
	mustScope(scope)
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []notify.Delivery
	for i := len(m.deliveries[scope.OrgID()]) - 1; i >= 0; i-- {
		d := m.deliveries[scope.OrgID()][i]
		if (f.Channel != nil && d.Channel != *f.Channel) || (f.Before != 0 && d.ID >= f.Before) {
			continue
		}
		out = append(out, d)
		if len(out) == f.Limit {
			break
		}
	}
	return out, nil
}

func (m *memStore) Organizations(context.Context) ([]tenant.OrgID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []tenant.OrgID
	for org := range m.deliveries {
		out = append(out, org)
	}
	return out, nil
}

func (m *memStore) Claim(_ context.Context, scope tenant.Scope, now, until time.Time, limit int) ([]notify.Delivery, error) {
	mustScope(scope)
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []notify.Delivery
	list := m.deliveries[scope.OrgID()]
	for i := range list {
		d := &list[i]
		due := (d.Status == notify.StatusPending && !d.NextAttemptAt.After(now)) || (d.Status == notify.StatusSending && d.ClaimedUntil.Before(now))
		if !due || len(out) == limit {
			continue
		}
		d.Status, d.ClaimedUntil = notify.StatusSending, until
		d.Attempts++
		out = append(out, *d)
	}
	return out, nil
}

func (m *memStore) Finish(_ context.Context, scope tenant.Scope, d notify.Delivery, claim time.Time) error {
	mustScope(scope)
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.deliveries[scope.OrgID()] {
		stored := &m.deliveries[scope.OrgID()][i]
		if stored.ID == d.ID {
			if stored.Status != notify.StatusSending || !stored.ClaimedUntil.Equal(claim) {
				return notify.ErrClaimLost
			}
			*stored = d
			return nil
		}
	}
	return errors.New("no such delivery")
}

func (m *memStore) Purge(_ context.Context, scope tenant.Scope, before time.Time) (int, error) {
	mustScope(scope)
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.deliveries[scope.OrgID()][:0]
	n := 0
	for _, d := range m.deliveries[scope.OrgID()] {
		if d.CreatedAt.Before(before) && (d.Status == notify.StatusSent || d.Status == notify.StatusFailed) {
			n++
			continue
		}
		kept = append(kept, d)
	}
	m.deliveries[scope.OrgID()] = kept
	return n, nil
}

func (m *memStore) all(org tenant.OrgID) []notify.Delivery {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.deliveries[org])
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func box(t *testing.T) *secrets.Box {
	t.Helper()
	line, err := secrets.GenerateKeyLine(1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := secrets.ParseKeys(line)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func admin(org tenant.OrgID, stepUp time.Time) access.Caller {
	return access.Caller{
		Principal: authz.Principal{User: ann, Org: org, Bindings: []authz.Binding{{AllGroups: true, Role: role.Admin}}},
		StepUpAt:  stepUp,
	}
}

func operator(org tenant.OrgID, stepUp time.Time) access.Caller {
	return access.Caller{
		Principal: authz.Principal{User: ann, Org: org, Bindings: []authz.Binding{{AllGroups: true, Role: role.Operator}}},
		StepUpAt:  stepUp,
	}
}

// logs captures what the package logs.
type logs struct{ buf bytes.Buffer }

func (l *logs) logger() *slog.Logger { return slog.New(slog.NewTextHandler(&l.buf, nil)) }
