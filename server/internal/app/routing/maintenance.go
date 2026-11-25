package routing

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// WindowID identifies a maintenance window.
type WindowID uuid.UUID

// String returns the UUID.
func (id WindowID) String() string { return uuid.UUID(id).String() }

// MaintenanceRules are muted during maintenance: the reboots and outages it causes.
var MaintenanceRules = []alert.RuleID{"system.node.offline", "system.node.rebooted"}

// Limits of a maintenance window.
const (
	MinWindow  = 15 * time.Minute
	MaxWindow  = 24 * time.Hour
	MaxWindows = 50
)

// ErrInvalidWindow means a maintenance window is malformed.
var ErrInvalidWindow = errors.New("invalid maintenance window")

func invalidWindow(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidWindow, fmt.Sprintf(format, args...))
}

// Schedule is a weekly window in a time zone, like the window of an update policy (KONZEPT
// §14.2): it begins at Start (local time) on each of Days and lasts Duration.
type Schedule struct {
	Days     []time.Weekday
	Start    time.Duration // since midnight
	Duration time.Duration
	TimeZone string // IANA name, e.g. Europe/Berlin
}

// zoneName is an IANA name (letters, digits, _, +, - and /) or UTC; no relative paths.
var zoneName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+-]*(/[A-Za-z0-9_+-]+)*$`)

var zones sync.Map // name → *time.Location

func location(name string) (*time.Location, error) {
	if l, ok := zones.Load(name); ok {
		return l.(*time.Location), nil //nolint:forcetypeassert // only *time.Location is stored
	}
	if !zoneName.MatchString(name) || name == "Local" || len(name) > 64 {
		return nil, invalidWindow("time zone must be an IANA name such as Europe/Berlin")
	}
	l, err := time.LoadLocation(name)
	if err != nil {
		return nil, invalidWindow("unknown time zone")
	}
	zones.Store(name, l)
	return l, nil
}

// Validate checks the schedule.
func (s Schedule) Validate() error {
	if _, err := location(s.TimeZone); err != nil {
		return err
	}
	switch {
	case len(s.Days) == 0 || len(s.Days) > 7 || hasDuplicates(s.Days) || slices.ContainsFunc(s.Days, func(d time.Weekday) bool { return d < time.Sunday || d > time.Saturday }):
		return invalidWindow("a window needs 1 to 7 different weekdays")
	case s.Start < 0 || s.Start >= 24*time.Hour || s.Start%time.Minute != 0:
		return invalidWindow("the start is a time of day in minutes")
	case s.Duration < MinWindow || s.Duration > MaxWindow || s.Duration%time.Minute != 0:
		return invalidWindow("a window lasts %v to %v in minutes", MinWindow, MaxWindow)
	}
	return nil
}

// Active reports whether t lies in a window: one that began today or yesterday (local time).
func (s Schedule) Active(t time.Time) bool {
	_, _, ok := s.Current(t)
	return ok
}

// Current returns the window t lies in, the one that began latest if two overlap: its start and
// end. The start is the local time of day on the day of the window; on the day the clocks go
// forward a start in the gap moves on by the gap (time.Date), and the window lasts Duration of
// real time.
func (s Schedule) Current(t time.Time) (start, end time.Time, ok bool) {
	loc, err := location(s.TimeZone)
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	local := t.In(loc)
	for back := range 2 {
		day := time.Date(local.Year(), local.Month(), local.Day()-back, 0, 0, 0, 0, loc)
		if !slices.Contains(s.Days, day.Weekday()) {
			continue
		}
		minutes := int(s.Start / time.Minute)
		start := time.Date(day.Year(), day.Month(), day.Day(), minutes/60, minutes%60, 0, 0, loc)
		if !t.Before(start) && t.Before(start.Add(s.Duration)) {
			return start, start.Add(s.Duration), true
		}
	}
	return time.Time{}, time.Time{}, false
}

// Next returns the start of the next window that begins after t (within two weeks).
func (s Schedule) Next(t time.Time) (time.Time, bool) {
	loc, err := location(s.TimeZone)
	if err != nil || len(s.Days) == 0 {
		return time.Time{}, false
	}
	local := t.In(loc)
	minutes := int(s.Start / time.Minute)
	for ahead := range 15 {
		day := time.Date(local.Year(), local.Month(), local.Day()+ahead, minutes/60, minutes%60, 0, 0, loc)
		if slices.Contains(s.Days, day.Weekday()) && day.After(t) {
			return day, true
		}
	}
	return time.Time{}, false
}

// MaintenanceWindow mutes the reboot and offline alerts of its groups (all groups if none)
// while its schedule is active.
type MaintenanceWindow struct {
	ID        WindowID
	Name      string
	Enabled   bool
	Groups    []tenant.GroupID
	Schedule  Schedule
	CreatedAt time.Time
	UpdatedAt time.Time
	CreatedBy identity.UserID
}

// Validate checks the window on its own; whether its groups exist is the service's business.
func (w *MaintenanceWindow) Validate() error {
	w.Name = strings.TrimSpace(w.Name)
	switch {
	case w.Name == "" || utf8.RuneCountInString(w.Name) > maxName || strings.IndexFunc(w.Name, unicode.IsControl) >= 0:
		return invalidWindow("the name needs 1 to %d characters without control characters", maxName)
	case len(w.Groups) > MaxMatchValues || hasDuplicates(w.Groups):
		return invalidWindow("at most %d different groups", MaxMatchValues)
	}
	return w.Schedule.Validate()
}

// Mutes reports whether the window mutes the target at t.
func (w MaintenanceWindow) Mutes(target Target, t time.Time) bool {
	return w.Enabled && slices.Contains(MaintenanceRules, target.Rule) &&
		(len(w.Groups) == 0 || slices.Contains(w.Groups, target.Group)) && w.Schedule.Active(t)
}
