package ingest

import (
	"context"
	"errors"
	"time"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// LogSource is where the agent read a log entry.
type LogSource uint8

// Log sources.
const (
	LogSourceJournal LogSource = iota + 1
	LogSourceFile
	// LogSourceContainer is a container's output (module docker_socket, M5-21).
	LogSourceContainer
)

// LogEntry is one log entry of a node (KONZEPT §10.2, agent.proto LogEntry). Every text comes
// from the host and is shown as text only.
type LogEntry struct {
	At         time.Time
	Source     LogSource
	Unit       string
	File       string
	Severity   uint8 // syslog priority + 1; 0 unknown
	Message    string
	Truncated  bool
	Identifier string
	PID        uint32
	Process    string
	UID        *uint32
	BootID     string
	Transport  string
	Facility   *uint8
}

// LogWriter stores log entries (Loki, ADR 0031). Organization and node come from the session.
type LogWriter interface {
	Write(ctx context.Context, org tenant.OrgID, node identity.NodeID, entries []LogEntry) error
}

// LogCount is how many lines of a batch Loki stored in a minute with a source and severity
// (V1-24, ADR 0044): the lines at Minute − 1 min < at ≤ Minute, as Loki counts a step.
type LogCount struct {
	Seq      uint64
	Minute   time.Time
	Source   LogSource
	Severity uint8 // as LogEntry, MaxSeverity at most; beyond it 0 (unknown) as in Loki
	Lines    uint64
}

// MaxSeverity is the highest severity of an entry (debug); the store calls higher ones unknown.
const MaxSeverity = 8

// LogCounter stores the counts of the lines Loki stored, for the log histogram over long ranges
// (V1-24). Counts of a batch whose sequence number the node's counts already reached are skipped:
// a batch resent after a lost acknowledgement is counted once, as Loki keeps it once.
type LogCounter interface {
	CountLogs(ctx context.Context, org tenant.OrgID, node identity.NodeID, counts []LogCount) error
}

// SetLogCounter counts the lines stored in Loki through c (V1-24); without one nothing is counted.
func (s *Service) SetLogCounter(c LogCounter) { s.counter = c }

// countsOf counts the entries of the batches per batch, minute, source and severity.
func countsOf(batches []logBatch) []LogCount {
	var out []LogCount
	for _, b := range batches {
		out = append(out, Counts(b.seq, b.entries)...)
	}
	return out
}

// Counts counts the entries of the batch seq per minute, source and severity: the minute ends at
// or after the entry (Loki counts a step (t − step, t]), severities beyond MaxSeverity are unknown
// as in Loki's labels.
func Counts(seq uint64, entries []LogEntry) []LogCount {
	type key struct {
		minute   int64
		source   LogSource
		severity uint8
	}
	index := map[key]int{}
	var out []LogCount
	for _, e := range entries {
		severity := e.Severity
		if severity > MaxSeverity {
			severity = 0
		}
		minute := e.At.Truncate(time.Minute)
		if minute.Before(e.At) {
			minute = minute.Add(time.Minute)
		}
		k := key{minute.UnixNano(), e.Source, severity}
		i, ok := index[k]
		if !ok {
			i = len(out)
			index[k] = i
			out = append(out, LogCount{Seq: seq, Minute: minute.UTC(), Source: e.Source, Severity: severity})
		}
		out[i].Lines++
	}
	return out
}

// flushCounts writes the waiting counts. It returns the sequence number of the first batch whose
// counts still wait (0: none): while the store fails the acknowledgement waits for at most
// LogRetryFor, then the counts are given up. The lines stay in Loki either way.
func (s *Session) flushCounts(ctx context.Context) uint64 {
	if len(s.counts) == 0 {
		return 0
	}
	svc := s.service
	now := svc.now()
	err := svc.counter.CountLogs(ctx, s.org, s.node, s.counts)
	if err == nil {
		s.counts, s.countsFailing = nil, time.Time{}
		return 0
	}
	svc.stats.WriteErrors.Add(1)
	if s.countsFailing.IsZero() {
		s.countsFailing = now
	}
	if now.Sub(s.countsFailing) < svc.config.LogRetryFor {
		first := s.counts[0].Seq
		for _, c := range s.counts {
			first = min(first, c.Seq)
		}
		return first
	}
	var lines uint64
	for _, c := range s.counts {
		lines += c.Lines
	}
	svc.stats.LogCountsDropped.Add(lines)
	svc.logger.WarnContext(ctx, "log counts could not be stored, the histogram misses them", "node", s.node.String(), "lines", lines, "error", err)
	s.counts, s.countsFailing = nil, time.Time{}
	return 0
}

// ErrLogsRejected marks entries the store refuses for good; they are dropped, not retried. Any
// other error counts as an outage, except ErrLogsThrottled.
var ErrLogsRejected = errors.New("log entries rejected")

// ErrLogsThrottled means the store refused the organization's entries for its limits (Loki 429):
// retried for LogRetryFor and then dropped for this node only. Loki counts as up, the other
// organizations write on (M3-28).
var ErrLogsThrottled = errors.New("log entries throttled")

// entryOverhead is charged per entry on top of its bytes: a flood of tiny entries costs too.
const entryOverhead = 16

// entryCost is what an entry costs in the buckets: everything the store keeps of it, not only
// the message (M3-28).
func entryCost(e LogEntry) float64 {
	return float64(len(e.Message) + len(e.Unit) + len(e.File) + len(e.Identifier) + len(e.Process) +
		len(e.BootID) + len(e.Transport) + entryOverhead)
}

// MaxUnitsPerNode bounds the distinct units of a node: beyond it entries go to OtherUnit, so a
// host can't grow the store's streams without bound.
const MaxUnitsPerNode = 64

// OtherUnit is the unit of entries beyond MaxUnitsPerNode.
const OtherUnit = "other"

// maxLogNodes bounds the per-node state kept for logs; beyond it it is forgotten.
const maxLogNodes = 100_000

type logBatch struct {
	seq     uint64
	entries []LogEntry
}

// SetLogWriter stores log entries through w; without one they are acknowledged and dropped (no
// Loki configured).
func (s *Service) SetLogWriter(w LogWriter) { s.logs = w }

// LogsUnavailable reports whether the log store counts as down (for the self-monitoring).
func (s *Service) LogsUnavailable() bool {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	return s.now().Before(s.logsDownUntil)
}

func (s *Service) logsDown(now time.Time) bool {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	return now.Before(s.logsDownUntil)
}

// markLogsDown drops log batches without a try for LogDownFor.
func (s *Service) markLogsDown(now time.Time) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	s.logsDownUntil = now.Add(s.config.LogDownFor)
}

// admitLogs keeps the entries the node's and the organization's buckets allow and bounds the
// node's units.
func (s *Service) admitLogs(org tenant.OrgID, node identity.NodeID, entries []LogEntry, now time.Time) []LogEntry {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	nodeBucket := logBucketFor(s.nodeLogBuckets, node, s.config.LogBytesBurst, now)
	orgBucket := logBucketFor(s.orgLogBuckets, org, s.config.OrgLogBytesBurst, now)
	nodeBucket.refill(now, s.config.LogBytesRate, s.config.LogBytesBurst)
	orgBucket.refill(now, s.config.OrgLogBytesRate, s.config.OrgLogBytesBurst)
	units, ok := s.units[node]
	if !ok {
		if len(s.units) >= maxLogNodes {
			clear(s.units)
		}
		units = map[string]struct{}{}
		s.units[node] = units
	}
	kept := entries[:0]
	for _, e := range entries {
		size := entryCost(e)
		if nodeBucket.tokens < size || orgBucket.tokens < size {
			s.stats.LogsDroppedRate.Add(1)
			continue
		}
		nodeBucket.tokens -= size
		orgBucket.tokens -= size
		if _, known := units[e.Unit]; !known {
			if len(units) < MaxUnitsPerNode {
				units[e.Unit] = struct{}{}
			} else {
				e.Unit = OtherUnit
			}
		}
		kept = append(kept, e)
	}
	return kept
}

func logBucketFor[K comparable](buckets map[K]*bucket, key K, burst float64, now time.Time) *bucket {
	b, ok := buckets[key]
	if !ok {
		if len(buckets) >= maxLogNodes {
			clear(buckets)
		}
		b = &bucket{tokens: burst, refilled: now}
		buckets[key] = b
	}
	return b
}

// acceptLogs keeps the entries within the time window and the buckets for the next flush; the
// batch is acknowledged in order once they are stored (or given up).
func (s *Session) acceptLogs(batch Batch, now time.Time) {
	stats := &s.service.stats
	config := s.service.config
	s.pending = append(s.pending, pendingBatch{seq: batch.Seq, end: s.written})
	if s.service.logs == nil {
		stats.LogsDroppedNoStore.Add(uint64(len(batch.Logs)))
		return
	}
	inWindow := make([]LogEntry, 0, len(batch.Logs))
	for _, e := range batch.Logs {
		if e.At.After(now.Add(config.MaxFuture)) || e.At.Before(now.Add(-config.LogMaxAge)) {
			stats.LogsDroppedAge.Add(1)
			continue
		}
		inWindow = append(inWindow, e)
	}
	kept := s.service.admitLogs(s.org, s.node, inWindow, now)
	if len(kept) > 0 {
		s.logs = append(s.logs, logBatch{seq: batch.Seq, entries: kept})
	}
}

// flushLogs writes the waiting log batches. It returns the sequence number of the first batch
// that still waits (0: none): the acknowledgement stops before it.
func (s *Session) flushLogs(ctx context.Context) uint64 {
	if len(s.logs) == 0 {
		return 0
	}
	svc := s.service
	stats := &svc.stats
	now := svc.now()
	var count uint64
	for _, b := range s.logs {
		count += uint64(len(b.entries))
	}
	drop := func(counter interface{ Add(uint64) uint64 }) uint64 {
		counter.Add(count)
		s.logs, s.logsFailing = nil, time.Time{}
		return 0
	}
	if svc.logsDown(now) {
		return drop(&stats.LogsDroppedUnavailable)
	}
	entries := make([]LogEntry, 0, len(s.logs)*len(s.logs[0].entries))
	for _, b := range s.logs {
		entries = append(entries, b.entries...)
	}
	write, cancel := context.WithTimeout(ctx, svc.config.LogWriteTimeout)
	defer cancel()
	err := svc.logs.Write(write, s.org, s.node, entries)
	switch {
	case err == nil:
		stats.LogEntries.Add(count)
		if svc.counter != nil {
			s.counts = append(s.counts, countsOf(s.logs)...)
		}
		s.logs, s.logsFailing = nil, time.Time{}
		return 0
	case errors.Is(err, ErrLogsRejected):
		svc.logger.WarnContext(ctx, "log entries rejected by the store, dropped", "node", s.node.String(), "entries", count, "error", err)
		return drop(&stats.LogsDroppedRejected)
	}
	if s.logsFailing.IsZero() {
		s.logsFailing = now
	}
	if now.Sub(s.logsFailing) < svc.config.LogRetryFor {
		return s.logs[0].seq
	}
	if errors.Is(err, ErrLogsThrottled) {
		// The organization's limit at the store: its entries go, nobody else's.
		svc.logger.WarnContext(ctx, "log store throttles the organization, dropping log entries", "node", s.node.String(), "entries", count, "error", err)
		return drop(&stats.LogsDroppedRate)
	}
	svc.logger.WarnContext(ctx, "log store unavailable, dropping log entries", "node", s.node.String(), "entries", count, "error", err)
	svc.markLogsDown(now)
	return drop(&stats.LogsDroppedUnavailable)
}
