// Package selfmonitor watches the server itself (M2-24, T37): ingest lag, dropped samples and the
// size of the database. Only superadmins hear of it – by mail on every change and in the UI – no
// organization sees it.
package selfmonitor

import (
	"context"
	"fmt"
	"log/slog"
	"math"
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
	// DroppedSecurityEvents: rare security events (logins, sudo, accounts, the system) dropped by
	// the rate limit of a node (M2-37); failed logins dropped under attack don't count.
	DroppedSecurityEvents Check = "dropped_security_events"
	DatabaseSize          Check = "database_size"
	// LogsUnavailable: the log store (Loki) fails, the ingest drops log entries (M3-07).
	LogsUnavailable Check = "logs_unavailable"
	// AgentCAExpiry: a trusted agent CA expires soon (V1-19); after that agents and server
	// refuse each other until the hosts are enrolled again.
	AgentCAExpiry Check = "agent_ca_expiry"
)

// checks is the fixed order of the checks.
var checks = []Check{IngestLag, DroppedSamples, DroppedSecurityEvents, DatabaseSize, LogsUnavailable, AgentCAExpiry}

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
	// CAWarning and CACritical: time left before a trusted agent CA expires. A rollover takes up to
	// 30 days (the agents' certificates of the old CA run out), so 90 days leave room for it.
	CAWarning  = 90 * 24 * time.Hour
	CACritical = 30 * 24 * time.Hour
)

// Problem is a check that is not OK. Value is the lag in seconds, the dropped samples or security
// events within DroppedWindow, the share of the database limit (0..1) or the days left before the
// agent CA expires (0 once it has).
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
	// DroppedRareEvents is the cumulative number of rare security events the ingest dropped.
	DroppedRareEvents() uint64
	// LogsUnavailable reports whether the log store counts as down.
	LogsUnavailable() bool
	DatabaseSize(ctx context.Context) (int64, error)
}

// AgentCA tells when the first of the trusted agent CAs expires: the earlier end of its root and
// intermediate certificate (agentca.Service). Zero: unknown, not checked.
type AgentCA interface {
	AgentCAExpiry() time.Time
}

// Admins lists the addresses of the superadmins.
type Admins interface {
	Superadmins(ctx context.Context) ([]string, error)
}

// Mailer sends plain text mail (smtp.Mailer).
type Mailer interface {
	SendText(ctx context.Context, to, subject, text string) error
}

// Deps are the collaborators; AgentCA (no check), Admins and Mailer (no mail) may be nil.
type Deps struct {
	Sources       Sources
	AgentCA       AgentCA
	Admins        Admins
	Mailer        Mailer
	DatabaseLimit int64 // bytes; 0: the size is not checked
	Now           func() time.Time
	Logger        *slog.Logger
}

type sample struct {
	at              time.Time
	dropped, events uint64
}

// Monitor runs the checks.
type Monitor struct {
	d       Deps
	mu      sync.Mutex
	state   map[Check]Problem
	history []sample
	// caExpires is the agent CA's expiry the last check saw (for the mail).
	caExpires time.Time
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
	for _, c := range checks {
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

	samples, events := m.droppedWithin(now)
	if samples > 0 {
		results[DroppedSamples] = Problem{Check: DroppedSamples, Level: Warning, Value: float64(samples)}
	}
	if events > 0 {
		results[DroppedSecurityEvents] = Problem{Check: DroppedSecurityEvents, Level: Warning, Value: float64(events)}
	}
	if m.d.Sources.LogsUnavailable() {
		results[LogsUnavailable] = Problem{Check: LogsUnavailable, Level: Warning}
	}

	if p, ok := m.caExpiry(now); ok {
		results[AgentCAExpiry] = p
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
	for _, c := range checks {
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

// caExpiry is the problem of the agent CA's expiry, if there is one.
func (m *Monitor) caExpiry(now time.Time) (Problem, bool) {
	if m.d.AgentCA == nil {
		return Problem{}, false
	}
	expires := m.d.AgentCA.AgentCAExpiry()
	left := expires.Sub(now)
	if expires.IsZero() || left > CAWarning {
		return Problem{}, false
	}
	p := Problem{Check: AgentCAExpiry, Level: Warning, Value: math.Max(0, left.Hours()/24)}
	if left <= CACritical {
		p.Level = Critical
	}
	m.mu.Lock()
	m.caExpires = expires
	m.mu.Unlock()
	return p, true
}

// droppedWithin remembers the counters and returns their growth within DroppedWindow (0 after a
// restart of the server, which resets them): dropped samples and rare security events.
func (m *Monitor) droppedWithin(now time.Time) (samples, events uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.history = append(m.history, sample{at: now, dropped: m.d.Sources.DroppedSamples(), events: m.d.Sources.DroppedRareEvents()})
	m.history = slices.DeleteFunc(m.history, func(s sample) bool { return now.Sub(s.at) > DroppedWindow })
	first, last := m.history[0], m.history[len(m.history)-1]
	return growth(first.dropped, last.dropped), growth(first.events, last.events)
}

func growth(first, last uint64) uint64 {
	if last < first {
		return 0
	}
	return last - first
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
	m.mu.Lock()
	caExpires := m.caExpires
	m.mu.Unlock()
	subject, text := message(p, caExpires)
	for _, addr := range to {
		if err := m.d.Mailer.SendText(ctx, addr, subject, text); err != nil {
			m.d.Logger.WarnContext(ctx, "self-monitoring: mail failed", "err", err)
		}
	}
}

// message is the mail of a change, in English (superadmins operate the instance; the UI shows the
// problems in their language); caExpires is the agent CA's expiry.
func message(p Problem, caExpires time.Time) (string, string) {
	what := map[Check]string{
		IngestLag:             "Agent data arrives late",
		DroppedSamples:        "The server dropped samples",
		DroppedSecurityEvents: "The server dropped security events",
		DatabaseSize:          "The database is filling up",
		LogsUnavailable:       "The server drops log entries",
		AgentCAExpiry:         "The agent CA expires soon",
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
	case DroppedSecurityEvents:
		detail = fmt.Sprintf("%.0f security events (logins, sudo, accounts or system events) were dropped within 15 minutes by the rate limit of a node – a node sends far more than usual. See sermon_server_ingest_dropped_security_events_by_kind_total in /metrics of the server.", p.Value)
	case DatabaseSize:
		detail = fmt.Sprintf("The database uses %.0f %% of SERMON_DATABASE_SIZE_LIMIT. Shorten the retention or add space.", p.Value*100)
	case LogsUnavailable:
		detail = "Loki (SERMON_LOKI_URL) is unreachable or fails: the agents' log entries are dropped, metrics, security events and alerts go on. Check the loki container (docker compose logs loki) and /readyz of the server; see sermon_server_ingest_dropped_log_entries_total in /metrics."
	case AgentCAExpiry:
		caDate := caExpires.UTC().Format(time.DateOnly)
		detail = fmt.Sprintf("A trusted agent CA expires on %s (%.0f days left). After that agents and server refuse each other and every host must be enrolled again. Start a rollover on the page Agent CA, and retire the previous CA there once its certificates are replaced (at most 30 days).", caDate, math.Floor(p.Value))
	}
	return fmt.Sprintf("SerMon: %s – %s", p.Level, what), fmt.Sprintf("%s (%s since %s).\n\n%s\n", what, p.Level, p.Since.UTC().Format(time.RFC3339), detail)
}
