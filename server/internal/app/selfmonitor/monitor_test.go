package selfmonitor_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/selfmonitor"
)

var t0 = time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

type fakeSources struct {
	lag     time.Duration
	dropped uint64
	events  uint64
	size    int64
	sizeErr error
}

func (f *fakeSources) IngestLag() time.Duration  { return f.lag }
func (f *fakeSources) DroppedSamples() uint64    { return f.dropped }
func (f *fakeSources) DroppedRareEvents() uint64 { return f.events }
func (f *fakeSources) DatabaseSize(context.Context) (int64, error) {
	return f.size, f.sizeErr
}

type mail struct{ to, subject, text string }

type fakeMail struct {
	admins []string
	sent   []mail
}

func (f *fakeMail) Superadmins(context.Context) ([]string, error) { return f.admins, nil }
func (f *fakeMail) SendText(_ context.Context, to, subject, text string) error {
	f.sent = append(f.sent, mail{to, subject, text})
	return nil
}

func monitor(src *fakeSources, mailer *fakeMail, now *time.Time, limit int64) *selfmonitor.Monitor {
	return selfmonitor.New(selfmonitor.Deps{
		Sources: src, Admins: mailer, Mailer: mailer, DatabaseLimit: limit,
		Now: func() time.Time { return *now },
	})
}

func levels(m *selfmonitor.Monitor) map[selfmonitor.Check]selfmonitor.Level {
	out := map[selfmonitor.Check]selfmonitor.Level{}
	for _, p := range m.Problems() {
		out[p.Check] = p.Level
	}
	return out
}

// Each check moves between ok, warning and critical; superadmins get one mail per change, also
// when it ends. Nothing is sent while all stays the same.
func TestMonitor_MailsSuperadminsOnEveryChange(t *testing.T) {
	now := t0
	src := &fakeSources{lag: 5 * time.Second, size: 10 << 30}
	mailer := &fakeMail{admins: []string{"alice@example.org", "bob@example.org"}}
	m := monitor(src, mailer, &now, 100<<30)
	ctx := context.Background()

	m.Check(ctx)
	if len(m.Problems()) != 0 || len(mailer.sent) != 0 {
		t.Fatalf("healthy: problems %v, mails %v", m.Problems(), mailer.sent)
	}

	src.lag, src.size = 2*time.Minute, 85<<30
	now = now.Add(time.Minute)
	m.Check(ctx)
	got := levels(m)
	if got[selfmonitor.IngestLag] != selfmonitor.Warning || got[selfmonitor.DatabaseSize] != selfmonitor.Warning {
		t.Fatalf("problems = %v", m.Problems())
	}
	if len(mailer.sent) != 4 || mailer.sent[0].to != "alice@example.org" || !strings.Contains(mailer.sent[0].subject, "SerMon") {
		t.Fatalf("mails = %v, want two problems to two superadmins", mailer.sent)
	}
	now = now.Add(time.Minute)
	m.Check(ctx)
	if len(mailer.sent) != 4 {
		t.Errorf("unchanged state mailed again: %d", len(mailer.sent))
	}

	src.lag, src.size = 15*time.Minute, 95<<30
	now = now.Add(time.Minute)
	m.Check(ctx)
	if got := levels(m); got[selfmonitor.IngestLag] != selfmonitor.Critical || got[selfmonitor.DatabaseSize] != selfmonitor.Critical {
		t.Errorf("problems = %v", m.Problems())
	}

	src.lag, src.size = time.Second, 10<<30
	now = now.Add(time.Minute)
	sent := len(mailer.sent)
	m.Check(ctx)
	if len(m.Problems()) != 0 || len(mailer.sent) != sent+4 || !strings.Contains(mailer.sent[len(mailer.sent)-1].subject, "resolved") {
		t.Errorf("recovery: problems %v, mails %v", m.Problems(), mailer.sent[sent:])
	}
}

// Dropped samples count within 15 minutes: an increase warns, the warning ends once no more
// were dropped for 15 minutes.
func TestMonitor_DroppedSamplesWarnWhileTheyIncrease(t *testing.T) {
	now := t0
	src := &fakeSources{dropped: 100}
	mailer := &fakeMail{admins: []string{"alice@example.org"}}
	m := monitor(src, mailer, &now, 0)
	ctx := context.Background()
	m.Check(ctx)
	if len(m.Problems()) != 0 {
		t.Fatalf("a counter that was already high is no problem: %v", m.Problems())
	}
	src.dropped = 130
	now = now.Add(time.Minute)
	m.Check(ctx)
	p := m.Problems()
	if len(p) != 1 || p[0].Check != selfmonitor.DroppedSamples || p[0].Level != selfmonitor.Warning || p[0].Value != 30 {
		t.Fatalf("problems = %+v", p)
	}
	for range 15 {
		now = now.Add(time.Minute)
		m.Check(ctx)
	}
	if len(m.Problems()) != 0 {
		t.Errorf("15 quiet minutes: %v", m.Problems())
	}
}

// Dropped rare security events (a successful login, sudo, an account) hide what matters: they
// warn like dropped samples, on their own (M2-37). Dropped failed logins don't – an attack.
func TestMonitor_DroppedRareEventsWarn(t *testing.T) {
	now := t0
	src := &fakeSources{events: 5}
	mailer := &fakeMail{admins: []string{"alice@example.org"}}
	m := monitor(src, mailer, &now, 0)
	ctx := context.Background()
	m.Check(ctx)
	src.events = 7
	now = now.Add(time.Minute)
	m.Check(ctx)
	p := m.Problems()
	if len(p) != 1 || p[0].Check != selfmonitor.DroppedSecurityEvents || p[0].Level != selfmonitor.Warning || p[0].Value != 2 {
		t.Fatalf("problems = %+v", p)
	}
	if len(mailer.sent) != 1 || !strings.Contains(mailer.sent[0].text, "2 security events") {
		t.Errorf("mails = %+v", mailer.sent)
	}
	for range 15 {
		now = now.Add(time.Minute)
		m.Check(ctx)
	}
	if len(m.Problems()) != 0 {
		t.Errorf("15 quiet minutes: %v", m.Problems())
	}
}

// Without a limit the database size is not checked; a failed size query is no alert storm.
func TestMonitor_DatabaseSizeOnlyWithALimit(t *testing.T) {
	now := t0
	src := &fakeSources{size: 1 << 40}
	mailer := &fakeMail{admins: []string{"alice@example.org"}}
	m := monitor(src, mailer, &now, 0)
	m.Check(context.Background())
	if len(m.Problems()) != 0 {
		t.Errorf("no limit: %v", m.Problems())
	}
	src.sizeErr = errors.New("connection refused")
	m = monitor(src, mailer, &now, 100)
	m.Check(context.Background())
	if len(m.Problems()) != 0 || len(mailer.sent) != 0 {
		t.Errorf("failed size query: %v, %v", m.Problems(), mailer.sent)
	}
}

// Without SMTP the problems still show in the UI.
func TestMonitor_WorksWithoutMail(t *testing.T) {
	now := t0
	m := selfmonitor.New(selfmonitor.Deps{Sources: &fakeSources{lag: time.Hour}, Now: func() time.Time { return now }})
	m.Check(context.Background())
	if len(m.Problems()) != 1 {
		t.Errorf("problems = %v", m.Problems())
	}
}
