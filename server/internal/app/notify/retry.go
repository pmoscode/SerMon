package notify

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
)

// Retries: a failed delivery is tried again after 30 s, doubling up to MaxBackoff, at most
// MaxAttempts times in all.
const (
	MaxAttempts   = 6
	firstBackoff  = 30 * time.Second
	MaxBackoff    = 10 * time.Minute
	MaxRetryAfter = time.Hour
	// defaultRetryAfter applies to a 429 without a usable Retry-After.
	defaultRetryAfter = time.Minute
)

// ErrChannelGone means the channel of a delivery was deleted or disabled.
var ErrChannelGone = errors.New("the channel was deleted or disabled")

type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

// Permanent marks an error that retrying does not fix (wrong URL, rejected credentials).
func Permanent(err error) error { return permanentError{err: err} }

// IsPermanent reports whether retrying the delivery is pointless.
func IsPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p) || errors.Is(err, ErrChannelGone) || errors.Is(err, context.Canceled) ||
		errors.Is(err, safehttp.ErrBlocked) || errors.Is(err, safehttp.ErrInsecureScheme) ||
		errors.Is(err, safehttp.ErrInvalidURL) || errors.Is(err, safehttp.ErrTooManyRedirects) ||
		errors.Is(err, safehttp.ErrResponseTooLarge)
}

// RateLimitedError means the service asks to wait (HTTP 429).
type RateLimitedError struct {
	RetryAfter time.Duration
}

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("rate limited, retry after %v", e.RetryAfter)
}

// NextAttempt returns when to try again after attempt (1-based) failed with err; false if the
// delivery failed for good.
func NextAttempt(attempt int, err error, now time.Time) (time.Time, bool) {
	if attempt >= MaxAttempts || IsPermanent(err) {
		return time.Time{}, false
	}
	var rl *RateLimitedError
	if errors.As(err, &rl) && rl.RetryAfter > 0 {
		return now.Add(min(rl.RetryAfter, MaxRetryAfter)), true
	}
	wait := MaxBackoff
	if attempt < 16 { // no overflow of the shift
		wait = min(firstBackoff<<(attempt-1), MaxBackoff)
	}
	return now.Add(wait), true
}

// HTTPStatus turns the status of a channel's HTTP response into an error: nil for 2xx, a
// RateLimitedError for 429, a retryable error for 408 and 5xx, a permanent one otherwise.
func HTTPStatus(code int, retryAfter string) error {
	switch {
	case code >= 200 && code < 300:
		return nil
	case code == http.StatusTooManyRequests:
		wait := defaultRetryAfter
		if s, err := strconv.Atoi(retryAfter); err == nil && s >= 0 {
			wait = time.Duration(s) * time.Second
		} else if at, err := http.ParseTime(retryAfter); err == nil {
			wait = max(time.Until(at), 0)
		}
		return &RateLimitedError{RetryAfter: wait}
	case code == http.StatusRequestTimeout || code >= 500:
		return fmt.Errorf("HTTP %d", code)
	}
	return Permanent(fmt.Errorf("HTTP %d", code))
}
