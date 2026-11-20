package alert_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/domain/alert"
)

func TestRuleID_FollowsTheNamingScheme(t *testing.T) {
	for _, id := range []string{"system.cpu.usage_high", "security.ssh.bruteforce", "system.disk.fill_prediction_24h"} {
		if _, err := alert.ParseRuleID(id); err != nil {
			t.Errorf("ParseRuleID(%q) = %v", id, err)
		}
	}
	for _, id := range []string{"", "cpu", "system.cpu", "System.cpu.high", "system..high", "system.cpu.high!", "a.b.c.d.e", strings.Repeat("a", 60) + ".b.c"} {
		if _, err := alert.ParseRuleID(id); !errors.Is(err, alert.ErrInvalidRule) {
			t.Errorf("ParseRuleID(%q) = %v, want ErrInvalidRule", id, err)
		}
	}
}

func TestRuleType_KnowsTheTypesOfKonzept(t *testing.T) {
	for _, name := range []string{"threshold", "rate", "event", "absence", "state", "prediction", "anomaly"} {
		ty, err := alert.ParseType(name)
		if err != nil || ty.String() != name {
			t.Errorf("ParseType(%q) = %v, %v", name, ty, err)
		}
	}
	if _, err := alert.ParseType("query"); !errors.Is(err, alert.ErrInvalidRule) {
		t.Errorf("free queries are no rule type: %v", err)
	}
}

func TestTiming_Validate(t *testing.T) {
	if err := (alert.Timing{For: 5 * time.Minute, Flapping: alert.DefaultFlapping}).Validate(); err != nil {
		t.Fatal(err)
	}
	for name, timing := range map[string]alert.Timing{
		"negative for":       {For: -time.Second, Flapping: alert.DefaultFlapping},
		"for beyond a day":   {For: 25 * time.Hour, Flapping: alert.DefaultFlapping},
		"no flapping limit":  {Flapping: alert.Flapping{Window: time.Minute}},
		"no flapping window": {Flapping: alert.Flapping{Changes: 3}},
	} {
		if err := timing.Validate(); !errors.Is(err, alert.ErrInvalidRule) {
			t.Errorf("%s: Validate() = %v, want ErrInvalidRule", name, err)
		}
	}
}
