package routing_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/app/routing"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tag"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

func silence() routing.Silence {
	return routing.Silence{
		Match: routing.Matchers{Groups: []tenant.GroupID{groupA}}, StartsAt: t0, EndsAt: t0.Add(2 * time.Hour), Comment: " Kernel update ",
	}
}

func TestSilence_BeginsAndEnds(t *testing.T) {
	s := silence()
	for at, active := range map[time.Time]bool{
		t0.Add(-time.Second): false, t0: true, t0.Add(time.Hour): true, t0.Add(2*time.Hour - time.Second): true, t0.Add(2 * time.Hour): false,
	} {
		if got := s.ActiveAt(at); got != active {
			t.Errorf("at %v: %v, want %v", at.Sub(t0), got, active)
		}
	}
}

func TestSilence_SelectorMutesOnlyWhatItNames(t *testing.T) {
	webCPU := routing.Target{Rule: "system.cpu.usage_high", Labels: map[string]string{"category": "cpu"}, Group: groupA, Node: web, Severity: notify.Warning, Tags: tag.Tags{"env": "prod"}}
	dbSSH := routing.Target{Rule: "security.ssh.bruteforce", Labels: map[string]string{"category": "security"}, Group: groupB, Node: db, Severity: notify.Critical}
	otherWeb := webCPU
	otherWeb.Node = identity.NodeID(uuid.New())
	for _, tc := range []struct {
		name               string
		s                  routing.Silence
		web, db, otherNode bool
	}{
		{"group", routing.Silence{Match: routing.Matchers{Groups: []tenant.GroupID{groupA}}}, true, false, true},
		{"one node", routing.Silence{Match: routing.Matchers{Groups: []tenant.GroupID{groupA}}, Nodes: []identity.NodeID{web}}, true, false, false},
		{"rule pattern", routing.Silence{Match: routing.Matchers{Rules: []string{"security.*"}}}, false, true, false},
		{"label and severity", routing.Silence{Match: routing.Matchers{Labels: map[string]string{"category": "cpu"}, Severities: []notify.Severity{notify.Critical}}}, false, false, false},
		{"tag of the node (M2-29)", routing.Silence{Match: routing.Matchers{Tags: tag.Tags{"env": "prod"}}}, true, false, true},
		{"rule on a node", routing.Silence{Match: routing.Matchers{Groups: []tenant.GroupID{groupA}, Rules: []string{"system.cpu.*"}}, Nodes: []identity.NodeID{web}}, true, false, false},
	} {
		if got := tc.s.Mutes(webCPU); got != tc.web {
			t.Errorf("%s: web %v", tc.name, got)
		}
		if got := tc.s.Mutes(dbSSH); got != tc.db {
			t.Errorf("%s: db %v", tc.name, got)
		}
		if got := tc.s.Mutes(otherWeb); got != tc.otherNode {
			t.Errorf("%s: other node %v", tc.name, got)
		}
	}
}

func TestSilence_Validate(t *testing.T) {
	s := silence()
	if err := s.Validate(t0); err != nil || s.Comment != "Kernel update" {
		t.Fatalf("valid: %v, comment %q", err, s.Comment)
	}
	for name, change := range map[string]func(*routing.Silence){
		"matches everything": func(s *routing.Silence) { s.Match = routing.Matchers{} },
		"severity only":      func(s *routing.Silence) { s.Match = routing.Matchers{Severities: []notify.Severity{notify.Warning}} },
		"nodes without groups": func(s *routing.Silence) {
			s.Match.Groups, s.Match.Rules, s.Nodes = nil, []string{"system.*"}, []identity.NodeID{web}
		},
		"duplicate node":      func(s *routing.Silence) { s.Nodes = []identity.NodeID{web, web} },
		"ends before start":   func(s *routing.Silence) { s.EndsAt = s.StartsAt },
		"already over":        func(s *routing.Silence) { s.StartsAt, s.EndsAt = t0.Add(-2*time.Hour), t0.Add(-time.Minute) },
		"longer than 30 days": func(s *routing.Silence) { s.EndsAt = s.StartsAt.Add(30*24*time.Hour + time.Minute) },
		"no comment":          func(s *routing.Silence) { s.Comment = " " },
		"long comment":        func(s *routing.Silence) { s.Comment = strings.Repeat("x", 501) },
		"control in comment":  func(s *routing.Silence) { s.Comment = "a\x1bb" },
		"invalid matcher":     func(s *routing.Silence) { s.Match.Rules = []string{"CPU"} },
		"many nodes": func(s *routing.Silence) {
			for range 51 {
				s.Nodes = append(s.Nodes, identity.NodeID(uuid.New()))
			}
		},
	} {
		s := silence()
		change(&s)
		if err := s.Validate(t0); !errors.Is(err, routing.ErrInvalidSilence) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Tags alone are a selector (M2-29): env=staging names something.
	s = silence()
	s.Match = routing.Matchers{Tags: tag.Tags{"env": "staging"}}
	if err := s.Validate(t0); err != nil {
		t.Errorf("tags alone: %v", err)
	}
	// A silence may have begun already, and its comment may span lines.
	s = silence()
	s.StartsAt, s.Comment = t0.Add(-time.Hour), "Kernel update\nsee ticket 42"
	if err := s.Validate(t0); err != nil {
		t.Errorf("begun silence: %v", err)
	}
}
