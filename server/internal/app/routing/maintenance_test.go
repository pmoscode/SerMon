package routing_test

import (
	"errors"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/routing"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

func berlin(t *testing.T, s string) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	at, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

// Sunday 03:00 for 2 h in Berlin, like the update policies' window (KONZEPT §14.2).
func sundayNight() routing.Schedule {
	return routing.Schedule{Days: []time.Weekday{time.Sunday}, Start: 3 * time.Hour, Duration: 2 * time.Hour, TimeZone: "Europe/Berlin"}
}

func TestSchedule_ActiveInItsTimeZone(t *testing.T) {
	s := sundayNight()
	for at, active := range map[string]bool{
		"2026-11-22 02:59": false, // Sunday
		"2026-11-22 03:00": true,
		"2026-11-22 04:59": true,
		"2026-11-22 05:00": false,
		"2026-11-21 03:30": false, // Saturday
		"2026-11-29 03:30": true,  // next Sunday
	} {
		if got := s.Active(berlin(t, at)); got != active {
			t.Errorf("%s: %v, want %v", at, got, active)
		}
	}
	// The same instant in UTC: 02:30 UTC is 03:30 in Berlin (winter time).
	if !s.Active(time.Date(2026, 11, 22, 2, 30, 0, 0, time.UTC)) {
		t.Error("UTC instant inside the window")
	}
}

func TestSchedule_AcrossMidnightAndDaylightSaving(t *testing.T) {
	late := routing.Schedule{Days: []time.Weekday{time.Saturday}, Start: 23 * time.Hour, Duration: 3 * time.Hour, TimeZone: "Europe/Berlin"}
	for at, active := range map[string]bool{
		"2026-11-21 23:30": true,  // Saturday
		"2026-11-22 01:59": true,  // Sunday morning, still Saturday's window
		"2026-11-22 02:00": false, // over
		"2026-11-22 23:30": false, // Sunday is not a window day
	} {
		if got := late.Active(berlin(t, at)); got != active {
			t.Errorf("%s: %v, want %v", at, got, active)
		}
	}
	// 29 March 2026: clocks jump from 02:00 to 03:00 in Berlin; the window still starts at 03:00
	// local time and lasts 2 h.
	s := sundayNight()
	if !s.Active(berlin(t, "2026-03-29 03:00")) || !s.Active(berlin(t, "2026-03-29 04:59")) || s.Active(berlin(t, "2026-03-29 05:00")) {
		t.Error("window on the day of the change to summer time")
	}
}

func TestSchedule_Validate(t *testing.T) {
	if err := sundayNight().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*routing.Schedule){
		"no day":         func(s *routing.Schedule) { s.Days = nil },
		"day twice":      func(s *routing.Schedule) { s.Days = []time.Weekday{time.Monday, time.Monday} },
		"unknown day":    func(s *routing.Schedule) { s.Days = []time.Weekday{7} },
		"start too late": func(s *routing.Schedule) { s.Start = 24 * time.Hour },
		"odd start":      func(s *routing.Schedule) { s.Start = 3*time.Hour + 30*time.Second },
		"too short":      func(s *routing.Schedule) { s.Duration = 10 * time.Minute },
		"too long":       func(s *routing.Schedule) { s.Duration = 25 * time.Hour },
		"odd duration":   func(s *routing.Schedule) { s.Duration = time.Hour + time.Second },
		"unknown zone":   func(s *routing.Schedule) { s.TimeZone = "Mars/Olympus" },
		"no zone":        func(s *routing.Schedule) { s.TimeZone = "" },
		"local zone":     func(s *routing.Schedule) { s.TimeZone = "Local" },
		"path as zone":   func(s *routing.Schedule) { s.TimeZone = "../../etc/passwd" },
	} {
		s := sundayNight()
		change(&s)
		if err := s.Validate(); !errors.Is(err, routing.ErrInvalidWindow) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestMaintenanceWindow_MutesRebootAndOfflineInItsGroups(t *testing.T) {
	w := routing.MaintenanceWindow{Name: "Patches", Enabled: true, Groups: []tenant.GroupID{groupA}, Schedule: sundayNight()}
	inside := berlin(t, "2026-11-22 03:30")
	for _, tc := range []struct {
		target routing.Target
		muted  bool
	}{
		{routing.Target{Rule: "system.node.offline", Group: groupA}, true},
		{routing.Target{Rule: "system.node.rebooted", Group: groupA}, true},
		{routing.Target{Rule: "system.cpu.usage_high", Group: groupA}, false},
		{routing.Target{Rule: "system.node.offline", Group: groupB}, false},
	} {
		if got := w.Mutes(tc.target, inside); got != tc.muted {
			t.Errorf("%+v: %v", tc.target, got)
		}
	}
	if w.Mutes(routing.Target{Rule: "system.node.offline", Group: groupA}, berlin(t, "2026-11-22 05:30")) {
		t.Error("outside the window")
	}
	w.Enabled = false
	if w.Mutes(routing.Target{Rule: "system.node.offline", Group: groupA}, inside) {
		t.Error("a disabled window mutes")
	}
	all := routing.MaintenanceWindow{Name: "All", Enabled: true, Schedule: sundayNight()}
	if !all.Mutes(routing.Target{Rule: "system.node.offline", Group: groupB}, inside) {
		t.Error("a window without groups applies to all groups")
	}
	if err := (&routing.MaintenanceWindow{Name: " ", Schedule: sundayNight()}).Validate(); !errors.Is(err, routing.ErrInvalidWindow) {
		t.Errorf("window without a name: %v", err)
	}
}
