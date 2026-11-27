package retention_test

import (
	"errors"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/domain/retention"
)

func TestSettings_Validate(t *testing.T) {
	cases := map[string]struct {
		s   retention.Settings
		cap uint32
		ok  bool
	}{
		"defaults":                 {retention.Settings{}, 0, true},
		"shortest":                 {retention.Settings{LogDays: 1, EventDays: 7}, 0, true},
		"longest":                  {retention.Settings{LogDays: 14, EventDays: 365}, 0, true},
		"logs beyond the instance": {retention.Settings{LogDays: 15}, 0, false},
		"events too short":         {retention.Settings{EventDays: 6}, 0, false},
		"events beyond a year":     {retention.Settings{EventDays: 366}, 0, false},
		"within the cap":           {retention.Settings{LogDays: 14, EventDays: 90}, 90, true},
		"events above the cap":     {retention.Settings{EventDays: 91}, 90, false},
		"logs above the cap":       {retention.Settings{LogDays: 10}, 7, false},
		"default events and a cap": {retention.Settings{LogDays: 3}, 30, true},
	}
	for name, tc := range cases {
		err := tc.s.Validate(tc.cap)
		if (err == nil) != tc.ok || (err != nil && !errors.Is(err, retention.ErrInvalid)) {
			t.Errorf("%s: Validate = %v, want ok %v", name, err, tc.ok)
		}
	}
}

func TestSettings_Effective(t *testing.T) {
	if got := (retention.Settings{}).Logs(); got != 14*24*time.Hour {
		t.Errorf("default logs = %v", got)
	}
	if got := (retention.Settings{}).EventsOr(90); got != 90*24*time.Hour {
		t.Errorf("default events = %v", got)
	}
	if got := (retention.Settings{LogDays: 3, EventDays: 30}); got.Logs() != 72*time.Hour || got.EventsOr(90) != 30*24*time.Hour {
		t.Errorf("set = %v, %v", got.Logs(), got.EventsOr(90))
	}
}

func TestSettings_String(t *testing.T) {
	if got := (retention.Settings{LogDays: 3}).String(); got != "logs=3d,events=default" {
		t.Errorf("String = %q", got)
	}
}
