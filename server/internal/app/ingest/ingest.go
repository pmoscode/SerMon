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
}

// Nodes is the port to the node records.
type Nodes interface {
	// Connected stores the Hello (host facts, agent version, last contact) and returns the node's
	// group; metrics.ErrInvalidQuery-like errors are not expected, any error ends the session.
	Connected(ctx context.Context, org tenant.OrgID, node identity.NodeID, facts HostFacts, agentVersion string, at time.Time) (tenant.GroupID, error)
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
	// EventRate and EventBurst form the security event bucket of a node (events per second). It
	// belongs to the node, not the session: a reconnect gains nothing.
	EventRate, EventBurst float64
	// EventMaxAge bounds how old an event may be (MaxFuture applies too); the partitions start a
	// month back (M2-07).
	EventMaxAge time.Duration
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
	}
}

// Stats are counters for monitoring the ingest.
type Stats struct {
	Batches, Samples, Duplicates, Late, Rejected, Unknown, DroppedSeries, WriteErrors atomic.Uint64
	// Security events: stored, dropped by the node's bucket, rejected (time, validation), and
	// batches that were stored before (resent after a reconnect).
	Events, EventsDropped, EventsRejected, EventDuplicates atomic.Uint64
	// LagMillis is the delay of the latest batch (server time − collection time).
	LagMillis atomic.Int64
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
	eventBucket map[identity.NodeID]*bucket
}

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

// admitEvents takes up to n events from the node's bucket and returns how many may pass.
func (s *Service) admitEvents(node identity.NodeID, n int, now time.Time) int {
	s.bucketMu.Lock()
	defer s.bucketMu.Unlock()
	b, ok := s.eventBucket[node]
	if !ok {
		if len(s.eventBucket) >= maxEventBuckets {
			clear(s.eventBucket)
		}
		b = &bucket{tokens: s.config.EventBurst, refilled: now}
		s.eventBucket[node] = b
	}
	b.tokens = math.Min(s.config.EventBurst, b.tokens+now.Sub(b.refilled).Seconds()*s.config.EventRate)
	b.refilled = later(b.refilled, now)
	admitted := min(n, int(math.Max(b.tokens, 0)))
	b.tokens -= float64(admitted)
	return admitted
}

type orgCount struct {
	series int
	at     time.Time
}

// NewService returns the ingest service.
func NewService(store metrics.Store, ring *metrics.Ring, nodes Nodes, config Config, now func() time.Time, logger *slog.Logger) *Service {
	return &Service{eventBucket: map[identity.NodeID]*bucket{}, store: store, ring: ring, nodes: nodes, config: config, now: now, logger: logger, unknown: map[string]struct{}{}, orgCounts: map[tenant.OrgID]orgCount{}}
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
	tokens    float64
	refilled  time.Time
	throttled bool
	// eventsDropped: the flood warning was logged in this session.
	eventsDropped bool
	lastSeen      time.Time
	events        []secevents.Batch // waiting for the next flush
}

// ErrOpen means the session could not be prepared (database unavailable); the agent retries.
var ErrOpen = errors.New("open ingest session")

// Open starts the ingest for a node that sent its Hello. org and node come from the certificate.
func (s *Service) Open(ctx context.Context, org tenant.OrgID, node identity.NodeID, facts HostFacts, agentVersion string) (*Session, error) {
	now := s.now()
	group, err := s.nodes.Connected(ctx, org, node, facts, agentVersion, now)
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
	return &Session{
		service: s, org: org, group: group, node: node, persisted: persisted, received: persisted,
		windows: map[windowKey]*window{}, series: series,
		tokens: s.config.SampleBurst, refilled: now, lastSeen: now,
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
	admitted := s.service.admitEvents(s.node, len(kept), now)
	if dropped := len(kept) - admitted; dropped > 0 {
		stats.EventsDropped.Add(uint64(dropped))
		if !s.eventsDropped {
			s.eventsDropped = true
			s.service.logger.Warn("security event flood, dropping events", "node", s.node.String(), "dropped", dropped)
		}
	}
	// The batch is written even if nothing is left: its sequence number marks it as stored.
	s.events = append(s.events, secevents.Batch{Seq: batch.Seq, Events: kept[:admitted]})
}

// Due reports whether enough batches wait for an early flush; security events flush at once.
func (s *Session) Due() bool {
	return len(s.pending) >= s.service.config.FlushPending || len(s.events) > 0
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
	complete := s.watermark.Truncate(metrics.Window)
	ack := s.persisted
	ackable := 0
	for _, batch := range s.pending {
		if batch.end.After(complete) {
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
	if len(s.series) >= config.SeriesLimit || !s.service.admitOrgSeries(s.org) {
		if s.service.stats.DroppedSeries.Add(1) == 1 {
			s.service.logger.Warn("series limit reached, dropping new series", "node", s.node.String(),
				"limit", config.SeriesLimit, "org_limit", config.OrgSeriesLimit)
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
	s.tokens = max(min(config.SampleBurst, s.tokens+elapsed*config.SampleRate)-float64(samples), -config.ThrottleSlack-config.SampleBurst)
	switch {
	case !s.throttled && s.tokens < 0:
		s.throttled = true
		s.service.logger.Warn("agent over its sample quota, throttling", "node", s.node.String())
		interval := config.ThrottleInterval
		return &interval
	case s.throttled && s.tokens > config.SampleBurst/2:
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
