package notify_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
)

func TestRetry_BacksOffUntilTheLastAttempt(t *testing.T) {
	transient := errors.New("connection reset")
	var waits []time.Duration
	for attempt := 1; attempt <= notify.MaxAttempts; attempt++ {
		next, retry := notify.NextAttempt(attempt, transient, t0)
		if attempt == notify.MaxAttempts {
			if retry {
				t.Errorf("attempt %d retried, want the last one final", attempt)
			}
			break
		}
		if !retry {
			t.Fatalf("attempt %d: no retry", attempt)
		}
		waits = append(waits, next.Sub(t0))
	}
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute}
	if fmt.Sprint(waits) != fmt.Sprint(want) {
		t.Errorf("waits = %v, want %v", waits, want)
	}
	if next, _ := notify.NextAttempt(20, transient, t0); next.Sub(t0) > notify.MaxBackoff {
		t.Errorf("backoff beyond %v", notify.MaxBackoff)
	}
}

func TestRetry_NoRetryForPermanentErrors(t *testing.T) {
	for _, err := range []error{
		notify.Permanent(errors.New("HTTP 404")),
		fmt.Errorf("POST hooks.example: %w", safehttp.ErrBlocked),
		fmt.Errorf("POST hooks.example: %w", safehttp.ErrInsecureScheme),
		fmt.Errorf("POST hooks.example: %w", safehttp.ErrInvalidURL),
		notify.ErrChannelGone,
		context.Canceled,
	} {
		if _, retry := notify.NextAttempt(1, err, t0); retry {
			t.Errorf("%v retried", err)
		}
	}
}

func TestRetry_HonoursRetryAfter(t *testing.T) {
	next, retry := notify.NextAttempt(1, &notify.RateLimitedError{RetryAfter: 5 * time.Minute}, t0)
	if !retry || next.Sub(t0) != 5*time.Minute {
		t.Errorf("rate limited: %v, %v", next.Sub(t0), retry)
	}
	// An absurd Retry-After is capped.
	if next, _ := notify.NextAttempt(1, &notify.RateLimitedError{RetryAfter: 48 * time.Hour}, t0); next.Sub(t0) != notify.MaxRetryAfter {
		t.Errorf("capped to %v", next.Sub(t0))
	}
}

func TestHTTPStatus_ClassifiesResponses(t *testing.T) {
	for code, want := range map[int]string{200: "ok", 204: "ok", 400: "permanent", 401: "permanent", 404: "permanent", 429: "rate", 408: "retry", 500: "retry", 503: "retry", 302: "permanent"} {
		err := notify.HTTPStatus(code, "30")
		var rl *notify.RateLimitedError
		got := "retry"
		switch {
		case err == nil:
			got = "ok"
		case errors.As(err, &rl):
			got = "rate"
			if rl.RetryAfter != 30*time.Second {
				t.Errorf("429 retry after %v", rl.RetryAfter)
			}
		case notify.IsPermanent(err):
			got = "permanent"
		}
		if got != want {
			t.Errorf("HTTP %d: %s, want %s", code, got, want)
		}
	}
}
