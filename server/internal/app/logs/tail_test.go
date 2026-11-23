package logs_test

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/app/logs"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/logql"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// clock is a test clock the store script moves on.
type clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// tailStore holds lines like Loki: a query returns those in [from, to), newest first. After each
// call it runs the script of that call (new lines, a later time, an outage).
type tailStore struct {
	mu     sync.Mutex
	lines  []logs.StoredLine
	calls  []call
	err    error
	script func(call int, s *tailStore)
}

func (s *tailStore) add(lines ...logs.StoredLine) { s.lines = append(s.lines, lines...) }

func (s *tailStore) Lines(_ context.Context, o tenant.OrgID, q logql.Query, from, to time.Time, limit int, forward bool) ([]logs.StoredLine, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.calls)
	s.calls = append(s.calls, call{org: o, query: q.String(), from: from, to: to, limit: limit, forward: forward})
	defer func() {
		if s.script != nil {
			s.script(n, s)
		}
	}()
	if s.err != nil {
		return nil, s.err
	}
	var out []logs.StoredLine
	for _, l := range s.lines {
		if !l.At.Before(from) && l.At.Before(to) {
			out = append(out, l)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out[:min(len(out), limit)], nil
}

func (s *tailStore) Counts(context.Context, tenant.OrgID, logql.Query, time.Time, time.Time, time.Duration) ([]logs.CountSeries, error) {
	return nil, nil
}

func at(t0 time.Time, d time.Duration, message string) logs.StoredLine {
	l := line(web01, message)
	l.At = t0.Add(d)
	return l
}

// tail runs a tail until the script cancels it and returns the batches.
func tail(t *testing.T, store *tailStore, clk *clock, c access.Caller, f logs.Filter) ([]logs.TailBatch, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	script := store.script
	store.script = func(n int, s *tailStore) {
		if script == nil || !scriptGoesOn(script, n, s) {
			cancel()
		}
	}
	svc := logs.NewService(store, fakeDirectory{}, authz.NewPolicy(), logs.WithClock(clk.now), logs.WithTailTiming(time.Millisecond, logs.TailRecheck))
	var batches []logs.TailBatch
	err := svc.Tail(ctx, c, f, func(b logs.TailBatch) error {
		batches = append(batches, b)
		return nil
	}, nil)
	if errors.Is(err, context.Canceled) {
		err = nil
	}
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatal("the tail did not end")
	}
	return batches, err
}

// stop ends a script.
var errStop = errors.New("stop")

// scriptGoesOn runs a script step; a panic with errStop ends the tail.
func scriptGoesOn(script func(int, *tailStore), n int, s *tailStore) (goesOn bool) {
	defer func() {
		if r := recover(); r != nil {
			if r != errStop { //nolint:errorlint // the sentinel itself
				panic(r)
			}
			goesOn = false
		}
	}()
	script(n, s)
	return true
}

func messages(b logs.TailBatch) []string {
	out := make([]string, 0, len(b.Lines))
	for _, l := range b.Lines {
		out = append(out, l.Message)
	}
	return out
}

func TestTail_NewLinesArriveExactlyOnce(t *testing.T) {
	clk := &clock{at: now}
	store := &tailStore{}
	store.add(at(now, -10*time.Second, "before the start"))
	store.script = func(n int, s *tailStore) {
		switch n {
		case 0:
			s.add(at(now, time.Second, "one"))
		case 1:
			// A late line older than "one", the same text on another node and a second line with the
			// same time.
			other := at(now, 3*time.Second, "two")
			other.Labels = map[string]string{"node_id": web02.String(), "source": "journal", "unit": "nginx.service", "severity": "error"}
			s.add(at(now, 3*time.Second, "two"), other, at(now, 3*time.Second, "two again"), at(now, 500*time.Millisecond, "late"))
		case 2, 3:
		default:
			panic(errStop)
		}
		clk.advance(2 * time.Second)
	}
	batches, err := tail(t, store, clk, viewerOf(web), logs.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	var got [][]string
	for _, b := range batches {
		got = append(got, messages(b))
	}
	// Oldest first; lines of the same time in any order.
	want := [][]string{{}, {"one"}, {"late", "two", "two", "two again"}}
	if len(got) != len(want) {
		t.Fatalf("batches = %q, want %q", got, want)
	}
	for i := range want {
		sorted := slices.Clone(got[i])
		slices.Sort(sorted)
		if len(sorted) > 0 && got[i][0] != want[i][0] || !slices.Equal(sorted, sortedCopy(want[i])) {
			t.Errorf("batch %d = %q, want %q", i, got[i], want[i])
		}
	}
	if !slices.ContainsFunc(batches[2].Lines, func(l logs.Line) bool { return l.Hostname == "web-02" }) {
		t.Errorf("lines = %+v", batches[2].Lines)
	}
	for i, c := range store.calls {
		wantFrom := now
		if lookback := clk0(i).Add(-logs.TailLookback); lookback.After(now) {
			wantFrom = lookback
		}
		if c.forward || c.limit != logs.TailBatchLimit+1 || !c.from.Equal(wantFrom) || !c.to.Equal(clk0(i).Add(time.Second)) ||
			!strings.Contains(c.query, web01.String()) || strings.Contains(c.query, db01.String()) {
			t.Errorf("call %d = %+v", i, c)
		}
	}
}

func sortedCopy(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}

// clk0 is the clock of call i of a script that moves 2 s per call.
func clk0(i int) time.Time { return now.Add(time.Duration(i) * 2 * time.Second) }

// The window slides: what falls out of it is forgotten, but not sent again.
func TestTail_TheWindowSlides(t *testing.T) {
	clk := &clock{at: now}
	store := &tailStore{}
	store.script = func(n int, s *tailStore) {
		if n == 0 {
			s.add(at(now, time.Second, "one"))
		}
		if n > 25 {
			panic(errStop)
		}
		clk.advance(2 * time.Second)
	}
	batches, err := tail(t, store, clk, admin(), logs.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	sent := 0
	for _, b := range batches {
		sent += len(b.Lines)
	}
	last := store.calls[len(store.calls)-1]
	if sent != 1 || !last.from.Equal(clk0(len(store.calls)-1).Add(-logs.TailLookback)) {
		t.Errorf("sent %d lines, last window from %v", sent, last.from)
	}
}

func TestTail_AStartInThePastIsReadOnce(t *testing.T) {
	clk := &clock{at: now}
	store := &tailStore{}
	store.add(at(now, -4*time.Minute, "four minutes ago"), at(now, -10*time.Minute, "too old"))
	store.script = func(n int, _ *tailStore) {
		if n > 1 {
			panic(errStop)
		}
		clk.advance(2 * time.Second)
	}
	batches, err := tail(t, store, clk, admin(), logs.Filter{From: now.Add(-5 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 1 || !slices.Equal(messages(batches[0]), []string{"four minutes ago"}) {
		t.Errorf("batches = %+v", batches)
	}
	if !store.calls[0].from.Equal(now.Add(-5*time.Minute)) || !store.calls[1].from.Equal(now.Add(2*time.Second-logs.TailLookback)) {
		t.Errorf("windows from %v, %v", store.calls[0].from, store.calls[1].from)
	}
	for _, since := range []time.Time{now.Add(-logs.MaxTailSince - time.Second), now.Add(2 * time.Minute)} {
		if _, err := tail(t, &tailStore{}, &clock{at: now}, admin(), logs.Filter{From: since}); !errors.Is(err, logs.ErrInvalid) {
			t.Errorf("since %v: %v, want ErrInvalid", since, err)
		}
	}
}

func TestTail_ThrottlesToTheNewestLines(t *testing.T) {
	clk := &clock{at: now}
	store := &tailStore{}
	store.script = func(n int, s *tailStore) {
		switch n {
		case 0:
			for i := range 500 {
				s.add(at(now, time.Second+time.Duration(i)*time.Millisecond, "burst "+strconv.Itoa(i)))
			}
		case 1: // a round without new lines would reach the newest skipped line
		case 2:
			for i := range 10 {
				s.add(at(now, 3*time.Second+time.Duration(i)*time.Millisecond, "after "+strconv.Itoa(i)))
			}
		case 3:
		default:
			panic(errStop)
		}
		clk.advance(2 * time.Second)
	}
	batches, err := tail(t, store, clk, admin(), logs.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 3 {
		t.Fatalf("batches = %d", len(batches))
	}
	burst := batches[1]
	if !burst.Throttled || len(burst.Lines) != logs.TailBatchLimit || burst.Lines[0].Message != "burst 300" || burst.Lines[199].Message != "burst 499" {
		t.Errorf("burst: throttled %v, %d lines from %q", burst.Throttled, len(burst.Lines), burst.Lines[0].Message)
	}
	// The skipped older lines don't come later.
	after := batches[2]
	if after.Throttled || len(after.Lines) != 10 || after.Lines[0].Message != "after 0" {
		t.Errorf("after: throttled %v, %q", after.Throttled, messages(after))
	}
	if logs.TailBatchLimit != logs.TailRate*int(logs.TailInterval/time.Second) {
		t.Errorf("TailBatchLimit = %d", logs.TailBatchLimit)
	}
}

func TestTail_ReportsAnOutageAndGoesOn(t *testing.T) {
	clk := &clock{at: now}
	store := &tailStore{}
	store.script = func(n int, s *tailStore) {
		switch n {
		case 0:
			s.err = logs.ErrUnavailable
		case 2:
			s.err = nil
			s.add(at(now, 5*time.Second, "back"))
		case 1, 3:
		default:
			panic(errStop)
		}
		clk.advance(2 * time.Second)
	}
	batches, err := tail(t, store, clk, admin(), logs.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 3 || batches[0].Unavailable || !batches[1].Unavailable || len(batches[1].Lines) != 0 ||
		batches[2].Unavailable || !slices.Equal(messages(batches[2]), []string{"back"}) {
		t.Errorf("batches = %+v", batches)
	}
	broken := &tailStore{script: func(_ int, s *tailStore) { s.err = errors.New("bad answer") }}
	if _, err := tail(t, broken, &clock{at: now}, admin(), logs.Filter{}); err == nil || errors.Is(err, logs.ErrUnavailable) {
		t.Errorf("another error ends the tail: %v", err)
	}
}

func TestTail_SendsAHeartbeat(t *testing.T) {
	clk := &clock{at: now}
	store := &tailStore{script: func(n int, _ *tailStore) {
		if n >= 20 { // 40 s
			panic(errStop)
		}
		clk.advance(2 * time.Second)
	}}
	batches, err := tail(t, store, clk, admin(), logs.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 3 { // at 0, 16 and 32 s
		t.Errorf("%d batches, want the first and one every %v", len(batches), logs.TailHeartbeat)
	}
}

func TestTail_EndsAfterItsDuration(t *testing.T) {
	clk := &clock{at: now}
	store := &tailStore{script: func(n int, _ *tailStore) {
		if n == 1 {
			clk.advance(logs.TailDuration)
		}
		if n > 3 {
			panic(errStop)
		}
	}}
	svc := logs.NewService(store, fakeDirectory{}, authz.NewPolicy(), logs.WithClock(clk.now), logs.WithTailTiming(time.Millisecond, logs.TailRecheck))
	err := svc.Tail(t.Context(), admin(), logs.Filter{}, func(logs.TailBatch) error { return nil }, nil)
	if err != nil || len(store.calls) != 2 {
		t.Errorf("Tail() = %v after %d rounds, want a normal end after 2", err, len(store.calls))
	}
}

// movingDirectory moves web-01 to the group db after the first listing.
type movingDirectory struct {
	mu    sync.Mutex
	calls int
}

func (d *movingDirectory) List(ctx context.Context, scope tenant.Scope) ([]fleet.Node, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	out, _ := fakeDirectory{}.List(ctx, scope)
	if d.calls > 1 {
		out = slices.DeleteFunc(out, func(n fleet.Node) bool { return n.ID == web01 })
	}
	return out, nil
}

func TestTail_ChecksRightsAndNodesAgain(t *testing.T) {
	clk := &clock{at: now}
	store := &tailStore{script: func(n int, _ *tailStore) {
		if n > 20 {
			panic(errStop)
		}
		clk.advance(2 * time.Second)
	}}
	dir := &movingDirectory{}
	rechecks := 0
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	script := store.script
	store.script = func(n int, s *tailStore) {
		if !scriptGoesOn(script, n, s) {
			cancel()
		}
	}
	svc := logs.NewService(store, dir, authz.NewPolicy(), logs.WithClock(clk.now), logs.WithTailTiming(time.Millisecond, logs.TailRecheck))
	err := svc.Tail(ctx, viewerOf(web), logs.Filter{}, func(logs.TailBatch) error { return nil }, func() error {
		rechecks++
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// 42 s: rechecks at 30 s; the node moved away is no longer asked for.
	if rechecks != 1 {
		t.Errorf("rechecks = %d, want 1", rechecks)
	}
	first, last := store.calls[0].query, store.calls[len(store.calls)-1].query
	if !strings.Contains(first, web01.String()) || strings.Contains(last, web01.String()) || !strings.Contains(last, web02.String()) {
		t.Errorf("first %s, last %s", first, last)
	}

	// A failed recheck (logout) ends the tail with its error.
	logout := errors.New("session ended")
	store = &tailStore{script: func(int, *tailStore) { clk.advance(10 * time.Second) }}
	svc = logs.NewService(store, fakeDirectory{}, authz.NewPolicy(), logs.WithClock(clk.now), logs.WithTailTiming(time.Millisecond, logs.TailRecheck))
	if err := svc.Tail(t.Context(), viewerOf(web), logs.Filter{}, func(logs.TailBatch) error { return nil }, func() error { return logout }); !errors.Is(err, logout) {
		t.Errorf("failed recheck: %v", err)
	}
}

func TestTail_BoundsAndRights(t *testing.T) {
	clk := &clock{at: now}
	sent := 0
	send := func(logs.TailBatch) error { sent++; return nil }
	svc := logs.NewService(&tailStore{}, fakeDirectory{}, authz.NewPolicy(), logs.WithClock(clk.now))
	if err := svc.Tail(t.Context(), admin(), logs.Filter{Text: strings.Repeat("x", logql.MaxTextBytes+1)}, send, nil); !errors.Is(err, logs.ErrInvalid) {
		t.Errorf("long text: %v", err)
	}
	if err := svc.Tail(t.Context(), caller(), logs.Filter{}, send, nil); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("no binding: %v", err)
	}
	hidden := logs.NewService(&tailStore{}, fakeDirectory{}, authz.NewPolicy(authz.WithViewerLogsDisabled(org)), logs.WithClock(clk.now))
	if err := hidden.Tail(t.Context(), viewerOf(web), logs.Filter{}, send, nil); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("viewer with logs off: %v", err)
	}
	none := logs.NewService(nil, fakeDirectory{}, authz.NewPolicy(), logs.WithClock(clk.now))
	if err := none.Tail(t.Context(), admin(), logs.Filter{}, send, nil); !errors.Is(err, logs.ErrUnavailable) {
		t.Errorf("no Loki: %v", err)
	}
	if sent != 0 {
		t.Errorf("%d batches before a refusal", sent)
	}
}

// Without a visible node the tail stays open (like an empty list) but never asks the store.
func TestTail_WithoutVisibleNodesAsksNothing(t *testing.T) {
	clk := &clock{at: now}
	store := &tailStore{}
	ctx, cancel := context.WithCancel(t.Context())
	svc := logs.NewService(store, fakeDirectory{}, authz.NewPolicy(), logs.WithClock(clk.now), logs.WithTailTiming(time.Millisecond, logs.TailRecheck))
	batches := 0
	err := svc.Tail(ctx, viewerOf(web), logs.Filter{Nodes: []identity.NodeID{alien}}, func(b logs.TailBatch) error {
		batches++
		if len(b.Lines) > 0 {
			t.Error("lines without visible nodes")
		}
		clk.advance(20 * time.Second)
		if batches == 3 {
			cancel()
		}
		return nil
	}, nil)
	if !errors.Is(err, context.Canceled) || len(store.calls) != 0 || batches != 3 {
		t.Errorf("Tail() = %v, %d store calls, %d batches", err, len(store.calls), batches)
	}
}
