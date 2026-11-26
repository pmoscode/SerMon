package webapi

import (
	"context"
	"sync"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/kiosk"
)

// Limits of the displays' reads (T115, M5-33).
const (
	// KioskReadsPerToken is how many widgets of one display read at once; the others wait. It is
	// the session's share of the log queries, so a display alone never fills it.
	KioskReadsPerToken = 4
	// kioskReadWait is how long a widget waits for a slot before it fails.
	kioskReadWait = 10 * time.Second
)

// KioskReads bounds the reads of the kiosk displays running at once (kiosk.Admission). A display
// loads all its widgets together: per token KioskReadsPerToken read, the others wait up to
// kioskReadWait. A log widget also takes a slot of the log queries the LogService counts – the
// token as the session, its creator as the user, the organization – and fails at once when
// those are full, as a query in the UI does.
type KioskReads struct {
	logs *streamLimiter
	wait time.Duration

	mu     sync.Mutex
	tokens map[kiosk.TokenID]*tokenReads
}

type tokenReads struct {
	slots chan struct{}
	users int
}

var _ kiosk.Admission = (*KioskReads)(nil)

// NewKioskReads returns the limits; queries are the log queries shared with the LogService.
func NewKioskReads(queries *LogQueries) *KioskReads {
	return &KioskReads{logs: queries.orNew(), wait: kioskReadWait, tokens: map[kiosk.TokenID]*tokenReads{}}
}

// Admit implements kiosk.Admission.
func (r *KioskReads) Admit(ctx context.Context, d kiosk.Display, logs bool) (func(), error) {
	t := r.enter(d.Token)
	waitCtx, cancel := context.WithTimeout(ctx, r.wait)
	defer cancel()
	select {
	case t.slots <- struct{}{}:
	case <-waitCtx.Done():
		r.leave(d.Token)
		return nil, kiosk.ErrBusy
	}
	k := streamKey{session: "kiosk:" + d.Token.String(), user: d.Creator.String(), org: d.Org.String()}
	if logs && !r.logs.acquire(k) {
		<-t.slots
		r.leave(d.Token)
		return nil, kiosk.ErrBusy
	}
	return func() {
		if logs {
			r.logs.release(k)
		}
		<-t.slots
		r.leave(d.Token)
	}, nil
}

func (r *KioskReads) enter(token kiosk.TokenID) *tokenReads {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tokens[token]
	if !ok {
		t = &tokenReads{slots: make(chan struct{}, KioskReadsPerToken)}
		r.tokens[token] = t
	}
	t.users++
	return t
}

func (r *KioskReads) leave(token kiosk.TokenID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t := r.tokens[token]; t != nil {
		if t.users--; t.users <= 0 {
			delete(r.tokens, token)
		}
	}
}

// tracked returns the number of tokens with reads running or waiting.
func (r *KioskReads) tracked() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.tokens)
}
