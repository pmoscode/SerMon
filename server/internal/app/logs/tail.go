package logs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
)

// The live tail (M3-09, ADR 0032): rounds of queries over a sliding window.
const (
	// TailInterval is the time between two rounds.
	TailInterval = 2 * time.Second
	// TailLookback is the window of a round: lines arrive this late at most (bundling in the agent,
	// the way through server and Loki).
	TailLookback = 30 * time.Second
	// TailRate is the number of lines per second a tail sends at most.
	TailRate = 100
	// TailBatchLimit is the number of lines of a round.
	TailBatchLimit = TailRate * int(TailInterval/time.Second)
	// TailRecheck is how often a tail checks the session and the visible nodes again.
	TailRecheck = 30 * time.Second
	// TailHeartbeat is the longest time without a message.
	TailHeartbeat = 15 * time.Second
	// TailDuration ends a tail; the client opens a new one.
	TailDuration = 15 * time.Minute
	// MaxTailSince is how far back a tail may start.
	MaxTailSince = 5 * time.Minute
)

// TailBatch is a message of a tail.
type TailBatch struct {
	// Lines are the new lines, oldest first. A late line may be older than one of an earlier batch.
	Lines []Line
	// Throttled: more lines arrived than TailRate allows; the older ones were skipped.
	Throttled bool
	// Unavailable: the log store is not reachable; the tail goes on.
	Unavailable bool
}

// Tail sends new lines of the filter until ctx ends or TailDuration is over. f.From is the start
// (zero: now, at most MaxTailSince ago); f.To is not used. The first batch goes out at once, then
// batches with new lines, a change of the store's availability or a heartbeat. Every TailRecheck,
// recheck (the caller's session) runs and the visible nodes are resolved again; an error of either
// ends the tail.
func (s *Service) Tail(ctx context.Context, c access.Caller, f Filter, send func(TailBatch) error, recheck func() error) error {
	start := s.now()
	since := f.From
	if since.IsZero() {
		since = start
	}
	if since.Before(start.Add(-MaxTailSince)) || since.After(start.Add(time.Minute)) {
		return invalid("a tail starts at most %v ago", MaxTailSince)
	}
	q, org, hosts, err := s.prepare(ctx, c, f)
	if err != nil {
		return err
	}
	ticker := time.NewTicker(s.tailInterval)
	defer ticker.Stop()
	w := window{seen: map[string]time.Time{}}
	checked, sentAt := start, start
	first, read, down := true, false, false
	for {
		now := s.now()
		if now.Sub(start) >= TailDuration {
			return nil
		}
		if now.Sub(checked) >= s.tailRecheck {
			checked = now
			if recheck != nil {
				if err := recheck(); err != nil {
					return err
				}
			}
			if q, org, hosts, err = s.prepare(ctx, c, f); err != nil {
				return err
			}
		}
		var batch TailBatch
		if hosts != nil {
			from := since
			if lookback := now.Add(-TailLookback); read && lookback.After(since) {
				from = lookback
			}
			stored, err := s.store.Lines(ctx, org, q.Query, from, now.Add(time.Second), TailBatchLimit+1, false)
			switch {
			case errors.Is(err, ErrUnavailable):
				batch.Unavailable = true
			case err != nil:
				return fmt.Errorf("tail logs: %w", err)
			default:
				read = true
				batch.Lines, batch.Throttled = w.fresh(stored, from, hosts)
			}
		}
		if first || len(batch.Lines) > 0 || batch.Throttled || batch.Unavailable != down || now.Sub(sentAt) >= TailHeartbeat {
			if err := send(batch); err != nil {
				return err
			}
			first, down, sentAt = false, batch.Unavailable, now
		}
		select {
		case <-ctx.Done():
			return ctx.Err() //nolint:wrapcheck // cancellation by the client
		case <-ticker.C:
		}
	}
}

// window remembers the lines a tail sent while they are in its window.
type window struct {
	seen map[string]time.Time
	// floor: lines up to this time were skipped by the throttle and stay skipped.
	floor time.Time
}

// fresh returns the lines of a round not sent yet, oldest first, at most TailBatchLimit, and
// whether lines were skipped. stored is newest first with up to TailBatchLimit+1 lines.
func (w *window) fresh(stored []StoredLine, from time.Time, hosts map[identity.NodeID]string) ([]Line, bool) {
	for key, at := range w.seen {
		if at.Before(from) {
			delete(w.seen, key)
		}
	}
	var fresh []StoredLine
	for _, l := range stored {
		if _, sent := w.seen[keyOf(l)]; sent || !l.At.After(w.floor) {
			continue
		}
		fresh = append(fresh, l)
	}
	// All of a full round are new: the window holds more new lines than a round may send.
	throttled := len(stored) > TailBatchLimit && len(fresh) == len(stored)
	if throttled {
		fresh = fresh[:TailBatchLimit]
		w.floor = stored[len(stored)-1].At
	}
	slices.Reverse(fresh)
	lines := make([]Line, 0, len(fresh))
	for _, l := range fresh {
		w.seen[keyOf(l)] = l.At
		lines = append(lines, toLine(l, hosts))
	}
	return lines, throttled
}

// keyOf identifies a line: its stream, time and text.
func keyOf(l StoredLine) string {
	return l.Labels["node_id"] + "\x1f" + l.Labels["source"] + "\x1f" + l.Labels["unit"] + "\x1f" + l.Labels["severity"] +
		"\x1f" + strconv.FormatInt(l.At.UnixNano(), 10) + "\x1f" + l.Line
}
