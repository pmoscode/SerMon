package ratelimit_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/platform/ratelimit"
)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func TestLimiter_AllowsUpToTheLimitPerWindow(t *testing.T) {
	c := &clock{now: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
	l := ratelimit.New(3, time.Minute, c.Now)

	for i := range 3 {
		if !l.Allow("10.0.0.1") {
			t.Fatalf("attempt %d must be allowed", i+1)
		}
	}
	if l.Allow("10.0.0.1") {
		t.Error("4th attempt within the window must be denied")
	}
	if !l.Allow("10.0.0.2") {
		t.Error("other keys are limited independently")
	}
}

func TestLimiter_RecoversAfterTheWindow(t *testing.T) {
	c := &clock{now: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
	l := ratelimit.New(2, time.Minute, c.Now)
	l.Allow("k")
	l.Allow("k")

	c.now = c.now.Add(30 * time.Second)
	if l.Allow("k") {
		t.Error("still within the sliding window: must be denied")
	}
	c.now = c.now.Add(31 * time.Second)
	if !l.Allow("k") {
		t.Error("after the window has passed the key must be allowed again")
	}
}

func TestLimiter_DeniedAttemptsDoNotExtendTheBlock(t *testing.T) {
	c := &clock{now: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
	l := ratelimit.New(1, time.Minute, c.Now)
	l.Allow("k")
	for range 100 {
		c.now = c.now.Add(time.Second / 2)
		l.Allow("k")
	}
	c.now = c.now.Add(11 * time.Second) // 61 s after the only allowed attempt
	if !l.Allow("k") {
		t.Error("denied attempts must not count against the window")
	}
}

func TestLimiter_ForgetsIdleKeys(t *testing.T) {
	c := &clock{now: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
	l := ratelimit.New(1, time.Minute, c.Now)
	for i := range 10_000 {
		l.Allow(fmt.Sprintf("10.0.%d.%d", i/256, i%256))
	}
	c.now = c.now.Add(2 * time.Minute)
	l.Allow("trigger-cleanup")

	if n := l.Len(); n > 1 {
		t.Errorf("%d keys tracked after the window, want idle keys to be removed", n)
	}
}

func TestNew_RejectsInvalidConfiguration(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("New with limit 0 must panic (programming error)")
		}
	}()
	ratelimit.New(0, time.Minute, time.Now)
}

// An IPv6 client usually controls a whole /64: per address it would get 2^64 budgets.
func TestLimiter_CountsIPv6PerSlash64(t *testing.T) {
	c := &clock{now: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
	l := ratelimit.New(2, time.Minute, c.Now)

	if !l.Allow("2001:db8:1:2::1") || !l.Allow("2001:db8:1:2::ffff") {
		t.Fatal("the first two attempts of the /64 are allowed")
	}
	if l.Allow("2001:db8:1:2:abcd::7") {
		t.Error("a third address of the same /64 shares the budget")
	}
	if !l.Allow("2001:db8:1:3::1") || !l.Allow("192.0.2.1") || !l.Allow("::ffff:192.0.2.2") {
		t.Error("other /64 networks and IPv4 (also mapped) are limited on their own")
	}
	if !l.Allow("user-0193f5a0") {
		t.Error("keys that are no IP stay unchanged")
	}
}
