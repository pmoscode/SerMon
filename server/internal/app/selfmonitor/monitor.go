// Package selfmonitor watches the server itself (M2-24, T37): ingest lag, dropped samples and the
// size of the database. Only superadmins hear of it – by mail on every change and in the UI – no
// organization sees it.
package selfmonitor

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"
)

// Check names a check.
type Check string

// The checks.
const (
	IngestLag      Check = "ingest_lag"
	DroppedSamples Check = "dropped_samples"
	DatabaseSize   Check = "database_size"
)

// Level is the state of a check.
type Level int

// Levels.
const (
	OK Level = iota
	Warning
	Critical
)

func (l Level) String() string {
	switch l {
	case Warning:
		return "warning"
	case Critical:
		return "critical"
	case OK:
		return "ok"
	default:
		return "ok"
	}
}

// Thresholds of the checks.
const (
	LagWarning       = time.Minute
	LagCritical      = 10 * time.Minute
	DroppedWindow    = 15 * time.Minute
	SizeWarningPart  = 0.8
	SizeCriticalPart = 0.9
)

// Problem is a check that is not OK. Value is the lag in seconds, the dropped samples within
// DroppedWindow or the share of the database limit (0..1).
type Problem struct {
	Check Check
	Level Level
	Since time.Time
	Value float64
}

// Sources are the measurements.
type Sources interface {
	IngestLag() time.Duration
	// DroppedSamples is the cumulative number of samples the ingest dropped since the start.
	DroppedSamples() uint64
	DatabaseSize(ctx context.Context) (int64, error)
}

// Admins lists the addresses of the superadmins.
type Admins interface {
	Superadmins(ctx context.Context) ([]string, error)
}

// Mailer sends plain text mail (smtp.Mailer).
type Mailer interface {
	SendText(ctx context.Context, to, subject, text string) error
}

// Deps are the collaborators; Admins and Mailer may be nil (no mail).
type Deps struct {
	Sources       Sources
	Admins        Admins
	Mailer        Mailer
	DatabaseLimit int64 // bytes; 0: the size is not checked
	Now           func() time.Time
	Logger        *slog.Logger
}

type sample struct {
	at      time.Time
	dropped uint64
}

// Monitor runs the checks.
type Monitor struct {
	d       Deps
	mu      sync.Mutex
	state   map[Check]Problem
	history []sample
}

// New returns a monitor.
func New(d Deps) *Monitor {
	if d.Logger == nil {
		d.Logger = slog.New(slog.DiscardHandler)
	}
	return &Monitor{d: d, state: map[Check]Problem{}}
}

// Problems returns the checks that are not OK, in a fixed order.
func (m *Monitor) Problems() []Problem {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Problem, 0, len(m.state))
	for _, c := range []Check{IngestLag, DroppedSamples, DatabaseSize} {
		if p, ok := m.state[c]; ok {
			out = append(out, p)
		}
	}
	return out
}

// Run checks every interval until ctx ends.
func (m *Monitor) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		m.Check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Check runs every check once and mails the superadmins about changes.
func (m *Monitor) Check(ctx context.Context) {
	now := m.d.Now()
	results := map[Check]Problem{}

	lag := m.d.Sources.IngestLag()
	switch {
	case lag >= LagCritical:
		results[IngestLag] = Problem{Check: IngestLag, Level: Critical, Value: lag.Seconds()}
	case lag >= LagWarning:
		results[IngestLag] = Problem{Check: IngestLag, Level: Warning, Value: lag.Seconds()}
	}

	if grown := m.droppedWithin(now); grown > 0 {
		results[DroppedSamples] = Problem{Check: DroppedSamples, Level: Warning, Value: float64(grown)}
	}

	keepSize := false
	if limit := m.d.DatabaseLimit; limit > 0 {
		size, err := m.d.Sources.DatabaseSize(ctx)
		if err != nil {
			// Unknown is no change: keep the previous state, don't flap.
			m.d.Logger.WarnContext(ctx, "self-monitoring: database size unknown", "err", err)
			keepSize = true
		} else {
			part := float64(size) / float64(limit)
			switch {
			case part >= SizeCriticalPart:
				results[DatabaseSize] = Problem{Check: DatabaseSize, Level: Critical, Value: part}
			case part >= SizeWarningPart:
				results[DatabaseSize] = Problem{Check: DatabaseSize, Level: Warning, Value: part}
			}
		}
	}

	m.mu.Lock()
	var changes []Problem
	for _, c := range []Check{IngestLag, DroppedSamples, DatabaseSize} {
		if c == DatabaseSize && keepSize {
			continue
		}
		before, was := m.state[c]
		now, is := results[c]
		switch {
		case is && (!was || before.Level != now.Level):
			now.Since = m.d.Now()
			m.state[c] = now
			changes = append(changes, now)
		case is:
			now.Since = before.Since
			m.state[c] = now
		case was:
			delete(m.state, c)
			changes = append(changes, Problem{Check: c, Level: OK, Since: m.d.Now()})
		}
	}
	m.mu.Unlock()
	for _, p := range changes {
		m.mail(ctx, p)
	}
}

// droppedWithin remembers the counter and returns its growth within DroppedWindow (0 after a
// restart of the server, which resets it).
func (m *Monitor) droppedWithin(now time.Time) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.history = append(m.history, sample{at: now, dropped: m.d.Sources.DroppedSamples()})
	m.history = slices.DeleteFunc(m.history, func(s sample) bool { return now.Sub(s.at) > DroppedWindow })
	first, last := m.history[0], m.history[len(m.history)-1]
	if last.dropped < first.dropped {
		return 0
	}
	return last.dropped - first.dropped
}

func (m *Monitor) mail(ctx context.Context, p Problem) {
	if m.d.Admins == nil || m.d.Mailer == nil {
		return
	}
	to, err := m.d.Admins.Superadmins(ctx)
	if err != nil {
		m.d.Logger.WarnContext(ctx, "self-monitoring: superadmins unknown", "err", err)
		return
	}
	subject, text := message(p)
	for _, addr := range to {
		if err := m.d.Mailer.SendText(ctx, addr, subject, text); err != nil {
			m.d.Logger.WarnContext(ctx, "self-monitoring: mail failed", "err", err)
		}
	}
}

// message is the mail of a change, in English (superadmins operate the instance; the UI shows the
// problems in their language).
func message(p Problem) (string, string) {
	what := map[Check]string{
		IngestLag:      "Agent data arrives late",
		DroppedSamples: "The server dropped samples",
		DatabaseSize:   "The database is filling up",
	}[p.Check]
	if p.Level == OK {
		return fmt.Sprintf("SerMon: resolved – %s", what), fmt.Sprintf("%s: back to normal since %s.\n", what, p.Since.UTC().Format(time.RFC3339))
	}
	var detail string
	switch p.Check {
	case IngestLag:
		detail = fmt.Sprintf("The latest batch was %.0f s old when it arrived. Check the load of the server and the database.", p.Value)
	case DroppedSamples:
		detail = fmt.Sprintf("%.0f samples were dropped within 15 minutes (series limit, implausible times or late windows). See /metrics of the server.", p.Value)
	case DatabaseSize:
		detail = fmt.Sprintf("The database uses %.0f %% of SERMON_DATABASE_SIZE_LIMIT. Shorten the retention or add space.", p.Value*100)
	}
	return fmt.Sprintf("SerMon: %s – %s", p.Level, what), fmt.Sprintf("%s (%s since %s).\n\n%s\n", what, p.Level, p.Since.UTC().Format(time.RFC3339), detail)
}
