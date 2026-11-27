// Package retention holds how long an organization keeps its logs and security events (M5-30):
// within the instance's limits (KONZEPT D12: logs 14 days, events a year) and the superadmin's
// retention cap of the organization (ADR 0029). 0 means the instance's default (logs 14 days,
// events SERMON_EVENT_RETENTION, 90 days unless set: V1-22).
package retention

import (
	"errors"
	"fmt"
	"strconv"
	"time"
)

// The instance's limits in days.
const (
	MinLogDays   = 1
	MaxLogDays   = 14 // Loki's retention (deploy/compose/loki.yaml)
	MinEventDays = 7  // the agent buffers events up to 7 days
	MaxEventDays = 365
)

const day = 24 * time.Hour

// ErrInvalid means a retention is outside the limits.
var ErrInvalid = errors.New("retention out of range")

// Settings of an organization; 0 is the instance's default.
type Settings struct {
	LogDays, EventDays uint32
}

// Validate checks the limits of the instance and the organization's cap (0: none). A cap only
// bounds what is set; the defaults stay (a lowered quota deletes nothing, ADR 0029).
func (s Settings) Validate(capDays uint32) error {
	check := func(name string, v, lo, hi uint32) error {
		if v == 0 {
			return nil
		}
		if v < lo || v > hi {
			return fmt.Errorf("%w: %s must be %d to %d days", ErrInvalid, name, lo, hi)
		}
		if capDays > 0 && v > capDays {
			return fmt.Errorf("%w: %s is above the organization's cap of %d days", ErrInvalid, name, capDays)
		}
		return nil
	}
	if err := check("logs", s.LogDays, MinLogDays, MaxLogDays); err != nil {
		return err
	}
	return check("events", s.EventDays, MinEventDays, MaxEventDays)
}

// Logs is how long logs are kept.
func (s Settings) Logs() time.Duration { return days(s.LogDays, MaxLogDays) }

// EventsOr is how long security events (and the alert history) are kept: the organization's
// period, or defaultDays (the instance's, SERMON_EVENT_RETENTION) without one.
func (s Settings) EventsOr(defaultDays uint32) time.Duration { return days(s.EventDays, defaultDays) }

func days(v, def uint32) time.Duration {
	if v == 0 {
		v = def
	}
	return time.Duration(v) * day
}

// String names the settings for the audit log.
func (s Settings) String() string {
	name := func(v uint32) string {
		if v == 0 {
			return "default"
		}
		return strconv.FormatUint(uint64(v), 10) + "d"
	}
	return "logs=" + name(s.LogDays) + ",events=" + name(s.EventDays)
}
