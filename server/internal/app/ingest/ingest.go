// Package ingest turns the agents' batches into stored metrics (KONZEPT §5, M1-24).
//
// Per agent session (one node):
//   - 1 s samples go to the ring buffer and are aggregated into 10 s windows (avg/min/max).
//   - A window is complete once the agent sent data of a later window (its samples arrive in time
//     order). Complete windows are written periodically, together with the highest batch sequence
//     number whose windows are all written – in one transaction (metrics.Store.Write).
//   - Only then the batches are acknowledged. Open windows are never written: if the session
//     ends, the agent sends their batches again. A batch is therefore stored exactly once, also
//     across reconnects and server restarts (seq ≤ persisted seq ⇒ duplicate, acknowledged at once).
//
// Organization and node come from the agent certificate (passed to Open), never from a batch.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/app/secevents"
	"github.com/pmoscode/SerMon/server/internal/app/updates"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/metric"
	"github.com/pmoscode/SerMon/server/internal/domain/secevent"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// OfflineAfter is how long a node may stay silent before it counts as offline (KONZEPT §5).
const OfflineAfter = 60 * time.Second

// Online reports whether a node last seen at lastSeen is online at now.
func Online(lastSeen *time.Time, now time.Time) bool {
	return lastSeen != nil && now.Sub(*lastSeen) < OfflineAfter
}

// HostFacts are the static properties from the agent's Hello.
type HostFacts struct {
	Hostname      string
	OSID          string
	OSVersion     string
	KernelRelease string
	Architecture  string // "amd64" or "arm64"
	CPUCount      uint32
	MemoryBytes   uint64
	// SecuritySource is where the agent reads security events (M2-31).
	SecuritySource secevent.Source
}

// Limits are the ingest quotas of the node's organization (M2-32); 0 is the instance's value.
type Limits struct {
	SamplesPerSecond uint32
	SeriesPerNode    uint32
}

// Connection is what the node records say about a connecting node.
type Connection struct {
	Group  tenant.GroupID
	Limits Limits
}

// Nodes is the port to the node records.
type Nodes interface {
	// Connected stores the Hello (host facts, agent version, last contact) and returns the node's
	// group and the quotas of its organization; any error ends the session.
	Connected(ctx context.Context, org tenant.OrgID, node identity.NodeID, facts HostFacts, agentVersion string, at time.Time) (Connection, error)
	// Seen records the last contact.
	Seen(ctx context.Context, org tenant.OrgID, node identity.NodeID, at time.Time) error
}

// Config holds the ingest limits.
type Config struct {
	// FlushInterval is how often complete windows are written.
	FlushInterval time.Duration
	// FlushPending writes early once this many batches wait for their windows.
	FlushPending int
	// SampleRate and SampleBurst form the per-node token bucket (samples per second).
	SampleRate, SampleBurst float64
	// ThrottleSlack is how far an agent may go below zero before its samples are dropped: the
	// batches already in flight when it gets Throttle (the agent keeps up to 64, each with up to
	// SeriesLimit samples). An agent that ignores Throttle goes beyond it.
	ThrottleSlack float64
	// ThrottleInterval is the batch interval the agent is asked for when over quota.
	ThrottleInterval time.Duration
	// SeriesLimit bounds the distinct series per node (KONZEPT §8), counted across sessions: the
	// series stored in the last ActiveSeriesWindow count, so a reconnect gains nothing (M1-36).
	SeriesLimit int
	// ActiveSeriesWindow is how long a series without data still counts towards the limits.
	ActiveSeriesWindow time.Duration
	// OrgSeriesLimit bounds the active series of an organization. The count comes from the store
	// at most once per OrgCountInterval (after a server restart all agents reconnect at once) and
	// grows with every new series admitted in between; other server instances count on their own.
	OrgSeriesLimit int
	// OrgCountInterval is how long a counted number of organization series is reused.
	OrgCountInterval time.Duration
	// SeenInterval is how often the last contact is written at most.
	SeenInterval time.Duration
	// MaxFuture and MaxAge bound the collection time of samples relative to the server clock.
	MaxFuture, MaxAge time.Duration
	// EventRate and EventBurst form the security event bucket of a node (events per second) for the
	// noisy kinds (secevent.Kind.Noisy). It belongs to the node, not the session: a reconnect
	// gains nothing.
	EventRate, EventBurst float64
	// RareEventRate and RareEventBurst form the node's bucket for all other kinds, so a flood of
	// failed logins can't crowd out the successful one that follows (M2-37).
	RareEventRate, RareEventBurst float64
	// EventMaxAge bounds how old an event may be (MaxFuture applies too); the partitions start a
	// month back (M2-07).
	EventMaxAge time.Duration
	// LogBytesRate and LogBytesBurst form a node's bucket for log bytes (the messages), across
	// reconnects; OrgLogBytesRate and OrgLogBytesBurst the organization's (M3-07).
	LogBytesRate, LogBytesBurst       float64
	OrgLogBytesRate, OrgLogBytesBurst float64
	// LogMaxAge bounds how old a log entry may be (MaxFuture applies too), as Loki does.
	LogMaxAge time.Duration
	// LogRetryFor is how long log batches are retried while the store fails; the acknowledgement
	// waits for them that long at most, then they are dropped (ADR 0031).
	LogRetryFor time.Duration
	// LogDownFor is how long log batches are dropped without a try after the store failed for
	// LogRetryFor.
	LogDownFor time.Duration
	// LogWriteTimeout bounds one write to the store.
	LogWriteTimeout time.Duration
	// UpdateReportRate and UpdateReportBurst form a node's bucket for update reports (reports per
	// second), across reconnects; OrgUpdateReportRate and OrgUpdateReportBurst the
	// organization's (M4-09). Reports beyond them are acknowledged and dropped.
	UpdateReportRate, UpdateReportBurst       float64
	OrgUpdateReportRate, OrgUpdateReportBurst float64
	// UpdateMaxAge bounds how old a report may be (MaxFuture applies too).
	UpdateMaxAge time.Duration
}

// DefaultConfig returns the default limits: about 60× the normal rate of a node.
func DefaultConfig() Config {
	return Config{
		FlushInterval: 2 * time.Second, FlushPending: 32,
		SampleRate: 10_000, SampleBurst: 50_000, ThrottleInterval: 5 * time.Second,
		ThrottleSlack: 64 * 2000,
		SeriesLimit:   2000, ActiveSeriesWindow: 48 * time.Hour, OrgSeriesLimit: 250_000, OrgCountInterval: time.Minute,
		SeenInterval: 30 * time.Second,
		MaxFuture:    5 * time.Minute, MaxAge: 24 * time.Hour,
		// A busy brute force is a few attempts per second with three events each.
		EventRate: 50, EventBurst: 5000, EventMaxAge: secevent.MaxAge,
		// Logins, sudo and accounts come by hand: a few per minute; a script still fits.
		RareEventRate: 5, RareEventBurst: 500,
		// Twice what the agent sends at most (512 KiB/s, 5 MiB burst); the organization within
		// Loki's limit per tenant (4 MB/s, burst 16 MB, deploy/compose/loki.yaml).
		LogBytesRate: 1 << 20, LogBytesBurst: 8 << 20,
		OrgLogBytesRate: 3 << 20, OrgLogBytesBurst: 12 << 20,
		LogMaxAge:   7 * 24 * time.Hour,
		LogRetryFor: 30 * time.Second, LogDownFor: 30 * time.Second, LogWriteTimeout: 10 * time.Second,
		// The agent reports every 6 hours, after the package manager ran and on request; 12 an
		// hour leave room for a busy admin. An organization of 10,000 nodes sends ~1,700 an hour.
		UpdateReportRate: 12.0 / 3600, UpdateReportBurst: 12,
		OrgUpdateReportRate: 20_000.0 / 3600, OrgUpdateReportBurst: 20_000,
		UpdateMaxAge: 30 * 24 * time.Hour,
	}
}

// Stats are counters for monitoring the ingest.
type Stats struct {
	Batches, Samples, Duplicates, Late, Rejected, Unknown, DroppedSeries, WriteErrors atomic.Uint64
	// Security events: stored, dropped by the node's bucket, rejected (time, validation), and
	// batches that were stored before (resent after a reconnect).
	Events, EventsDropped, EventsRejected, EventDuplicates atomic.Uint64
	// Log entries: stored, and dropped by reason (M3-07).
	LogEntries, LogsDroppedRate, LogsDroppedAge, LogsDroppedRejected, LogsDroppedUnavailable, LogsDroppedNoStore atomic.Uint64
	// Update reports (M4-09): stored, dropped by the buckets, rejected (time, validation, no
	// store), and older than the stored one.
	UpdateReports, UpdateReportsDropped, UpdateReportsRejected, UpdateReportsStale atomic.Uint64
	// LagMillis is the delay of the latest batch (server time − collection time).
	LagMillis atomic.Int64
	// droppedKinds counts the events dropped by the buckets per kind (index in secevent.Kinds).
	droppedKinds [maxKinds]atomic.Uint64
}

// maxKinds bounds the kinds counted per kind (there are 14).
const maxKinds = 32

var kindIndex = func() map[secevent.Kind]int {
	out := map[secevent.Kind]int{}
	for i, k := range secevent.Kinds() {
		if i < maxKinds {
			out[k] = i
		}
	}
	return out
}()

func (s *Stats) dropEvent(k secevent.Kind) {
	s.EventsDropped.Add(1)
	if i, ok := kindIndex[k]; ok {
		s.droppedKinds[i].Add(1)
	}
}

// DroppedEvents returns the events dropped by the buckets per kind, every kind included.
func (s *Stats) DroppedEvents() map[secevent.Kind]uint64 {
	out := make(map[secevent.Kind]uint64, len(kindIndex))
	for k, i := range kindIndex {
		out[k] = s.droppedKinds[i].Load()
	}
	return out
}

// DroppedRareEvents sums the dropped events of the kinds that are not noisy: losing them hides
// what matters (self-monitoring warns).
func (s *Stats) DroppedRareEvents() uint64 {
	var n uint64
	for k, i := range kindIndex {
		if !k.Noisy() {
			n += s.droppedKinds[i].Load()
		}
	}
	return n
}

// Service opens ingest sessions.
type Service struct {
	store  metrics.Store
	ring   *metrics.Ring
	nodes  Nodes
	config Config
	now    func() time.Time
	logger *slog.Logger
	stats  Stats

	unknownMu sync.Mutex
	unknown   map[string]struct{}

	orgMu     sync.Mutex
	orgCounts map[tenant.OrgID]orgCount

	events      secevents.Writer
	bucketMu    sync.Mutex
	eventBucket map[identity.NodeID]*eventBuckets

	updates          updates.Writer
	nodeUpdateBucket map[identity.NodeID]*bucket
	orgUpdateBucket  map[tenant.OrgID]*bucket

	logs           LogWriter
	logMu          sync.Mutex
	nodeLogBuckets map[identity.NodeID]*bucket
	orgLogBuckets  map[tenant.OrgID]*bucket
	units          map[identity.NodeID]map[string]struct{}
	logsDownUntil  time.Time
}

// eventBuckets are a node's buckets: the noisy kinds and the rest.
type eventBuckets struct{ noisy, rare bucket }

// bucket is a token bucket.
type bucket struct {
	tokens   float64
	refilled time.Time
}

// maxEventBuckets bounds the buckets kept; beyond it they are forgotten (and start full).
const maxEventBuckets = 100_000

// SetEventWriter stores security events through w; without one they are acknowledged and
// dropped (a server without a database).
func (s *Service) SetEventWriter(w secevents.Writer) { s.events = w }

// SetUpdateWriter stores update reports through w (M4-09); without one they are acknowledged and
// dropped.
func (s *Service) SetUpdateWriter(w updates.Writer) { s.updates = w }

// admitUpdate spends a report of the node's and the organization's buckets; false if either is
// empty (then neither is charged).
func (s *Service) admitUpdate(org tenant.OrgID, node identity.NodeID, now time.Time) bool {
	s.bucketMu.Lock()
	defer s.bucketMu.Unlock()
	nb := bucketOf(s.nodeUpdateBucket, node, s.config.UpdateReportBurst, now)
	ob := bucketOf(s.orgUpdateBucket, org, s.config.OrgUpdateReportBurst, now)
	nb.refill(now, s.config.UpdateReportRate, s.config.UpdateReportBurst)
	ob.refill(now, s.config.OrgUpdateReportRate, s.config.OrgUpdateReportBurst)
	if nb.tokens < 1 || ob.tokens < 1 {
		return false
	}
	nb.take()
	ob.take()
	return true
}

// bucketOf returns the bucket of key, a full one if it is new; beyond maxEventBuckets the map is
// forgotten (and its buckets start full).
func bucketOf[K comparable](buckets map[K]*bucket, key K, burst float64, now time.Time) *bucket {
	b, ok := buckets[key]
	if !ok {
		if len(buckets) >= maxEventBuckets {
			clear(buckets)
		}
		b = &bucket{tokens: burst, refilled: now}
		buckets[key] = b
	}
	return b
}

// refill adds what accrued since the last refill, up to burst.
func (b *bucket) refill(now time.Time, rate, burst float64) {
	b.tokens = math.Min(burst, b.tokens+now.Sub(b.refilled).Seconds()*rate)
	b.refilled = later(b.refilled, now)
}

// take spends a token if there is one.
func (b *bucket) take() bool {
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// admitEvents keeps the events the node's buckets allow, in order: noisy kinds from one bucket,
// the rest from another (M2-37); the dropped ones are counted per kind. It returns how many
// were dropped.
func (s *Service) admitEvents(node identity.NodeID, events []secevent.Event, now time.Time) ([]secevent.Event, int) {
	s.bucketMu.Lock()
	defer s.bucketMu.Unlock()
	b, ok := s.eventBucket[node]
	if !ok {
		if len(s.eventBucket) >= maxEventBuckets {
			clear(s.eventBucket)
		}
		b = &eventBuckets{
			noisy: bucket{tokens: s.config.EventBurst, refilled: now},
			rare:  bucket{tokens: s.config.RareEventBurst, refilled: now},
		}
		s.eventBucket[node] = b
	}
	b.noisy.refill(now, s.config.EventRate, s.config.EventBurst)
	b.rare.refill(now, s.config.RareEventRate, s.config.RareEventBurst)
	kept, dropped := events[:0], 0
	for _, e := range events {
		from := &b.rare
		if e.Kind.Noisy() {
			from = &b.noisy
		}
		if !from.take() {
			s.stats.dropEvent(e.Kind)
			dropped++
			continue
		}
		kept = append(kept, e)
	}
	return kept, dropped
}

type orgCount struct {
	series int
	at     time.Time
}

// NewService returns the ingest service.
func NewService(store metrics.Store, ring *metrics.Ring, nodes Nodes, config Config, now func() time.Time, logger *slog.Logger) *Service {
	return &Service{
		eventBucket: map[identity.NodeID]*eventBuckets{}, nodeLogBuckets: map[identity.NodeID]*bucket{},
		orgLogBuckets: map[tenant.OrgID]*bucket{}, nodeUpdateBucket: map[identity.NodeID]*bucket{}, orgUpdateBucket: map[tenant.OrgID]*bucket{}, units: map[identity.NodeID]map[string]struct{}{}, store: store, ring: ring, nodes: nodes, config: config, now: now, logger: logger, unknown: map[string]struct{}{}, orgCounts: map[tenant.OrgID]orgCount{},
	}
}

// maxReportedUnknown bounds the metric names remembered for "logged once".
const maxReportedUnknown = 100

// reportUnknown reports whether an unknown metric should be logged (first time, bounded).
func (s *Service) reportUnknown(name string) bool {
	s.unknownMu.Lock()
	defer s.unknownMu.Unlock()
	if _, done := s.unknown[name]; done || len(s.unknown) >= maxReportedUnknown {
		return false
	}
	s.unknown[name] = struct{}{}
	return true
}

// Stats returns the counters.
func (s *Service) Stats() *Stats { return &s.stats }

// Config returns the limits.
func (s *Service) Config() Config { return s.config }

// Sample is one value of a batch.
type Sample struct {
	Metric string
	Labels metrics.Labels
	Value  float64
}

// Batch is one buffered batch of an agent: samples of a tick, or security events.
type Batch struct {
	Seq         uint64
	CollectedAt time.Time
	Samples     []Sample
	Events      []secevent.Event
	// Logs are the entries of a log batch (M3-07).
	Logs []LogEntry
	// Update is the report of an update batch (M4-09).
	Update *updates.Report
	// Unhandled is a batch the server takes but doesn't use yet (M4-05: command results until
	// M4-11): acknowledged in order, nothing stored.
	Unhandled bool
}

// Response tells the transport what to send back.
type Response struct {
	// Ack is a sequence number to acknowledge (0: nothing).
	Ack uint64
	// Throttle, if set, is a new minimum batch interval for the agent (0 lifts it).
	Throttle *time.Duration
}

type windowKey struct {
	metric string
	labels string
	start  time.Time
}

type window struct {
	labels        metrics.Labels
	sum, min, max float64
	count         int
}

type pendingBatch struct {
	seq uint64
	end time.Time // end of the batch's window
}

// Session is the ingest state of one connected node; not safe for concurrent use (the
// transport's session loop owns it).
type Session struct {
	service   *Service
	org       tenant.OrgID
	group     tenant.GroupID
	node      identity.NodeID
	persisted uint64
	received  uint64
	windows   map[windowKey]*window
	pending   []pendingBatch
	watermark time.Time // latest collection time seen
	written   time.Time // windows ending at or before this are written
	series    map[string]struct{}
	// rate, burst and seriesLimit are the instance's limits or the organization's quotas.
	rate, burst float64
	seriesLimit int
	tokens      float64
	refilled    time.Time
	throttled   bool
	// eventsDropped: the flood warning was logged in this session.
	eventsDropped bool
	lastSeen      time.Time
	events        []secevents.Batch // waiting for the next flush
	update        *updates.Report   // the newest report waiting for the next flush
	logs          []logBatch        // waiting for the next flush
	logsFailing   time.Time         // since when the store fails for the waiting logs
}

// ErrOpen means the session could not be prepared (database unavailable); the agent retries.
var ErrOpen = errors.New("open ingest session")

// Open starts the ingest for a node that sent its Hello. org and node come from the certificate.
func (s *Service) Open(ctx context.Context, org tenant.OrgID, node identity.NodeID, facts HostFacts, agentVersion string) (*Session, error) {
	now := s.now()
	conn, err := s.nodes.Connected(ctx, org, node, facts, agentVersion, now)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrOpen, err)
	}
	persisted, err := s.store.PersistedSeq(ctx, org, node)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrOpen, err)
	}
	since := now.Add(-s.config.ActiveSeriesWindow)
	known, err := s.store.NodeSeries(ctx, org, node, since)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrOpen, err)
	}
	if err := s.refreshOrgCount(ctx, org, now, since); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrOpen, err)
	}
	series := make(map[string]struct{}, len(known))
	for _, k := range known {
		series[seriesID(k.Metric, k.Labels)] = struct{}{}
	}
	rate, burst, seriesLimit := s.config.SampleRate, s.config.SampleBurst, s.config.SeriesLimit
	if q := conn.Limits.SamplesPerSecond; q > 0 {
		// The burst keeps the instance's ratio to the rate.
		rate, burst = float64(q), float64(q)*s.config.SampleBurst/s.config.SampleRate
	}
	if q := conn.Limits.SeriesPerNode; q > 0 {
		seriesLimit = int(q)
	}
	return &Session{
		service: s, org: org, group: conn.Group, node: node, persisted: persisted, received: persisted,
		windows: map[windowKey]*window{}, series: series, rate: rate, burst: burst, seriesLimit: seriesLimit,
		tokens: burst, refilled: now, lastSeen: now,
	}, nil
}

// refreshOrgCount counts the active series of the organization in the store unless that
// happened less than OrgCountInterval ago.
func (s *Service) refreshOrgCount(ctx context.Context, org tenant.OrgID, now, since time.Time) error {
	s.orgMu.Lock()
	defer s.orgMu.Unlock()
	if c, ok := s.orgCounts[org]; ok && now.Sub(c.at) < s.config.OrgCountInterval {
		return nil
	}
	n, err := s.store.OrgSeriesCount(ctx, org, since)
	if err != nil {
		return err //nolint:wrapcheck // wrapped by Open
	}
	if len(s.orgCounts) > maxCachedOrgCounts {
		clear(s.orgCounts)
	}
	s.orgCounts[org] = orgCount{series: n, at: now}
	return nil
}

// admitOrgSeries counts a new series of the organization; false if its limit is reached.
func (s *Service) admitOrgSeries(org tenant.OrgID) bool {
	s.orgMu.Lock()
	defer s.orgMu.Unlock()
	c := s.orgCounts[org]
	if c.series >= s.config.OrgSeriesLimit {
		return false
	}
	c.series++
	s.orgCounts[org] = c
	return true
}

// maxCachedOrgCounts bounds the cache of orgSeries.
const maxCachedOrgCounts = 10_000

func seriesID(metric string, labels metrics.Labels) string { return metric + "\x1f" + labels.Key() }

// Persisted returns the highest stored sequence number.
func (s *Session) Persisted() uint64 { return s.persisted }

// Accept takes a batch. Duplicates are acknowledged at once; new data waits for Flush.
func (s *Session) Accept(ctx context.Context, batch Batch) Response {
	stats := &s.service.stats
	now := s.service.now()
	s.seen(ctx, now)
	if batch.Seq <= s.received {
		stats.Duplicates.Add(1)
		if batch.Seq <= s.persisted {
			return Response{Ack: s.persisted}
		}
		return Response{} // already in progress in this session
	}
	s.received = batch.Seq
	stats.Batches.Add(1)
	if len(batch.Events) > 0 {
		s.acceptEvents(batch, now)
		return Response{}
	}
	if len(batch.Logs) > 0 {
		s.acceptLogs(batch, now)
		return Response{}
	}
	if batch.Update != nil {
		s.acceptUpdate(*batch.Update, now)
		s.pending = append(s.pending, pendingBatch{seq: batch.Seq, end: s.written})
		return Response{}
	}
	if batch.Unhandled {
		s.pending = append(s.pending, pendingBatch{seq: batch.Seq, end: s.written})
		return Response{}
	}
	stats.LagMillis.Store(now.Sub(batch.CollectedAt).Milliseconds())
	response := Response{Throttle: s.quota(now, len(batch.Samples))}
	config := s.service.config
	if s.tokens < -config.ThrottleSlack {
		// The agent ignores Throttle beyond the batches in flight: drop the samples
		// but acknowledge the batch, so that an honest agent doesn't resend it forever.
		stats.Rejected.Add(uint64(len(batch.Samples)))
		s.pending = append(s.pending, pendingBatch{seq: batch.Seq, end: s.written})
		return response
	}

	at := batch.CollectedAt
	if at.After(now.Add(config.MaxFuture)) || at.Before(now.Add(-config.MaxAge)) {
		// A wrong agent clock must not close windows early or write outside the retention.
		stats.Rejected.Add(uint64(len(batch.Samples)))
		s.pending = append(s.pending, pendingBatch{seq: batch.Seq, end: s.written})
		return response
	}
	start := at.Truncate(metrics.Window)
	end := start.Add(metrics.Window)
	s.pending = append(s.pending, pendingBatch{seq: batch.Seq, end: end})
	if !end.After(s.written) {
		stats.Late.Add(uint64(len(batch.Samples)))
		return response
	}
	if at.After(s.watermark) {
		s.watermark = at
	}
	for _, sample := range batch.Samples {
		if math.IsNaN(sample.Value) || math.IsInf(sample.Value, 0) || !s.known(sample) || !s.admitSeries(sample) {
			continue
		}
		stats.Samples.Add(1)
		s.service.ring.Add(s.org, s.group, s.node, sample.Metric, sample.Labels, at, sample.Value)
		key := windowKey{metric: sample.Metric, labels: sample.Labels.Key(), start: start}
		w, ok := s.windows[key]
		if !ok {
			w = &window{labels: sample.Labels, min: sample.Value, max: sample.Value}
			s.windows[key] = w
		}
		w.sum += sample.Value
		w.count++
		w.min = min(w.min, sample.Value)
		w.max = max(w.max, sample.Value)
	}
	return response
}

// acceptEvents keeps the valid events within the time window and the node's bucket for the next
// flush; the batch is acknowledged in order once they are stored.
func (s *Session) acceptEvents(batch Batch, now time.Time) {
	stats := &s.service.stats
	config := s.service.config
	s.pending = append(s.pending, pendingBatch{seq: batch.Seq, end: s.written})
	if s.service.events == nil {
		stats.EventsRejected.Add(uint64(len(batch.Events)))
		return
	}
	kept := make([]secevent.Event, 0, len(batch.Events))
	for _, e := range batch.Events {
		e.Node = s.node // from the certificate, never from the agent
		e = e.Normalized()
		if e.OccurredAt.After(now.Add(config.MaxFuture)) || e.OccurredAt.Before(now.Add(-config.EventMaxAge)) || e.Validate() != nil {
			stats.EventsRejected.Add(1)
			continue
		}
		kept = append(kept, e)
	}
	kept, dropped := s.service.admitEvents(s.node, kept, now)
	if dropped > 0 {
		if !s.eventsDropped {
			s.eventsDropped = true
			s.service.logger.Warn("security event flood, dropping events", "node", s.node.String(), "dropped", dropped)
		}
	}
	// The batch is written even if nothing is left: its sequence number marks it as stored.
	s.events = append(s.events, secevents.Batch{Seq: batch.Seq, Events: kept})
}

// Due reports whether enough batches wait for an early flush; security events flush at once.
func (s *Session) Due() bool {
	return len(s.pending) >= s.service.config.FlushPending || len(s.events) > 0 || len(s.logs) > 0 || s.update != nil
}

// acceptUpdate keeps a valid report within the time window and the buckets for the next flush,
// the newest one if several arrive in between; its batch is acknowledged in order like the others.
func (s *Session) acceptUpdate(r updates.Report, now time.Time) {
	stats := &s.service.stats
	config := s.service.config
	if s.service.updates == nil || !r.Valid() ||
		r.CollectedAt.After(now.Add(config.MaxFuture)) || r.CollectedAt.Before(now.Add(-config.UpdateMaxAge)) {
		stats.UpdateReportsRejected.Add(1)
		return
	}
	if s.update != nil && !r.CollectedAt.After(s.update.CollectedAt) {
		stats.UpdateReportsStale.Add(1)
		return
	}
	if !s.service.admitUpdate(s.org, s.node, now) {
		if stats.UpdateReportsDropped.Add(1) == 1 {
			s.service.logger.Warn("update reports over the limit, dropping", "node", s.node.String())
		}
		return
	}
	normalized := r.Normalized()
	s.update = &normalized
}

// Flush writes the complete windows and returns the sequence number to acknowledge (0: none).
// On a write error nothing is acknowledged and everything is kept for the next attempt.
func (s *Session) Flush(ctx context.Context) (uint64, error) {
	// Events first: their batches are acknowledged with the sequence below, and a resend after
	// a failed acknowledgement is skipped by the store.
	if len(s.events) > 0 {
		written, err := s.service.events.Write(ctx, s.org, s.node, s.events)
		if err != nil {
			s.service.stats.WriteErrors.Add(1)
			return 0, fmt.Errorf("flush security events: %w", err)
		}
		for _, b := range written {
			s.service.stats.Events.Add(uint64(len(b.Events)))
		}
		s.service.stats.EventDuplicates.Add(uint64(max(len(s.events)-len(written), 0)))
		s.events = nil
	}
	// The update report next, like the events: a resend is older or as old and changes nothing.
	if s.update != nil {
		stored, err := s.service.updates.Write(ctx, s.org, s.node, *s.update, s.service.now())
		if err != nil {
			s.service.stats.WriteErrors.Add(1)
			return 0, fmt.Errorf("flush update report: %w", err)
		}
		if stored {
			s.service.stats.UpdateReports.Add(1)
		} else {
			s.service.stats.UpdateReportsStale.Add(1)
		}
		s.update = nil
	}
	// Logs next: while the store fails they wait, and so does the acknowledgement of the batches
	// from theirs on – at most LogRetryFor (ADR 0031).
	holdFrom := s.flushLogs(ctx)
	complete := s.watermark.Truncate(metrics.Window)
	ack := s.persisted
	ackable := 0
	for _, batch := range s.pending {
		if batch.end.After(complete) || (holdFrom != 0 && batch.seq >= holdFrom) {
			break
		}
		ack = batch.seq
		ackable++
	}
	var aggregates []metrics.Aggregate
	var keys []windowKey
	for key, w := range s.windows {
		if key.start.Add(metrics.Window).After(complete) {
			continue
		}
		aggregates = append(aggregates, metrics.Aggregate{
			Node: s.node, Metric: key.metric, Labels: w.labels, Start: key.start,
			Avg: w.sum / float64(w.count), Min: w.min, Max: w.max,
		})
		keys = append(keys, key)
	}
	if ack == s.persisted && len(aggregates) == 0 {
		return 0, nil
	}
	if err := s.service.store.Write(ctx, s.org, aggregates, map[identity.NodeID]uint64{s.node: ack}); err != nil {
		s.service.stats.WriteErrors.Add(1)
		return 0, fmt.Errorf("flush metrics: %w", err)
	}
	for _, key := range keys {
		delete(s.windows, key)
	}
	s.pending = s.pending[ackable:]
	s.written = later(s.written, complete)
	advanced := ack > s.persisted
	s.persisted = ack
	if !advanced {
		return 0, nil
	}
	return ack, nil
}

// Close writes what is complete; open windows are left to the agent's resend.
func (s *Session) Close(ctx context.Context) {
	if _, err := s.Flush(ctx); err != nil {
		s.service.logger.WarnContext(ctx, "final metric flush failed", "node", s.node.String(), "error", err)
	}
}

// Heartbeat records liveness.
func (s *Session) Heartbeat(ctx context.Context) { s.seen(ctx, s.service.now()) }

func (s *Session) seen(ctx context.Context, now time.Time) {
	if now.Sub(s.lastSeen) < s.service.config.SeenInterval {
		return
	}
	if err := s.service.nodes.Seen(ctx, s.org, s.node, now); err != nil {
		s.service.logger.WarnContext(ctx, "record node contact failed", "node", s.node.String(), "error", err)
		return
	}
	s.lastSeen = now
}

// known reports whether the metric is in the catalog with exactly its labels (KONZEPT §8: no
// unbounded labels, no metrics the UI can't describe). Unknown ones are counted and logged once.
func (s *Session) known(sample Sample) bool {
	definition, ok := metric.Lookup(sample.Metric)
	if ok && definition.AcceptsLabels(sample.Labels) {
		return true
	}
	s.service.stats.Unknown.Add(1)
	if s.service.reportUnknown(sample.Metric) {
		s.service.logger.Warn("unknown metric or labels from agent, dropped", "node", s.node.String(), "metric", sample.Metric)
	}
	return false
}

// admitSeries applies the series limits of the node and the organization; known series always
// pass.
func (s *Session) admitSeries(sample Sample) bool {
	key := seriesID(sample.Metric, sample.Labels)
	if _, ok := s.series[key]; ok {
		return true
	}
	config := s.service.config
	if len(s.series) >= s.seriesLimit || !s.service.admitOrgSeries(s.org) {
		if s.service.stats.DroppedSeries.Add(1) == 1 {
			s.service.logger.Warn("series limit reached, dropping new series", "node", s.node.String(),
				"limit", s.seriesLimit, "org_limit", config.OrgSeriesLimit)
		}
		return false
	}
	s.series[key] = struct{}{}
	return true
}

// quota charges the samples to the token bucket and returns a throttle change, if any.
func (s *Session) quota(now time.Time, samples int) *time.Duration {
	config := s.service.config
	elapsed := now.Sub(s.refilled).Seconds()
	s.refilled = now
	// The floor keeps a flooding agent recoverable: once it slows down, it is admitted again
	// after at most a burst's worth of refill.
	s.tokens = max(min(s.burst, s.tokens+elapsed*s.rate)-float64(samples), -config.ThrottleSlack-s.burst)
	switch {
	case !s.throttled && s.tokens < 0:
		s.throttled = true
		s.service.logger.Warn("agent over its sample quota, throttling", "node", s.node.String())
		interval := config.ThrottleInterval
		return &interval
	case s.throttled && s.tokens > s.burst/2:
		s.throttled = false
		var lifted time.Duration
		return &lifted
	default:
		return nil
	}
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
