package routing_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/app/routing"
	"github.com/pmoscode/SerMon/server/internal/domain/tag"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	groupA = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000a1"))
	groupB = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000b1"))
	chanA  = notify.ChannelID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000c1"))
	chanB  = notify.ChannelID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000c2"))
	chanC  = notify.ChannelID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000c3"))
)

func TestMatchers_Table(t *testing.T) {
	cpu := routing.Target{Rule: "system.cpu.usage_high", Labels: map[string]string{"category": "cpu"}, Group: groupA, Severity: notify.Critical, Tags: tag.Tags{"env": "prod", "role": "web"}}
	ssh := routing.Target{Rule: "security.ssh.bruteforce", Labels: map[string]string{"category": "security"}, Group: groupB, Severity: notify.Warning}
	for _, tc := range []struct {
		name     string
		m        routing.Matchers
		cpu, ssh bool
	}{
		{"empty matches all", routing.Matchers{}, true, true},
		{"severity", routing.Matchers{Severities: []notify.Severity{notify.Critical}}, true, false},
		{"both severities", routing.Matchers{Severities: []notify.Severity{notify.Warning, notify.Critical}}, true, true},
		{"group", routing.Matchers{Groups: []tenant.GroupID{groupB}}, false, true},
		{"exact rule", routing.Matchers{Rules: []string{"system.cpu.usage_high"}}, true, false},
		{"rule glob", routing.Matchers{Rules: []string{"security.*"}}, false, true},
		{"glob in the middle", routing.Matchers{Rules: []string{"*.ssh.*"}}, false, true},
		{"glob does not match a prefix only", routing.Matchers{Rules: []string{"security"}}, false, false},
		{"one of several rules", routing.Matchers{Rules: []string{"system.memory.*", "system.cpu.*"}}, true, false},
		{"label", routing.Matchers{Labels: map[string]string{"category": "security"}}, false, true},
		{"label missing on the rule", routing.Matchers{Labels: map[string]string{"team": "ops"}}, false, false},
		// Tags of the node (M2-29): every pair must be on the alert's node.
		{"tag", routing.Matchers{Tags: tag.Tags{"env": "prod"}}, true, false},
		{"two tags", routing.Matchers{Tags: tag.Tags{"env": "prod", "role": "web"}}, true, false},
		{"tag with another value", routing.Matchers{Tags: tag.Tags{"env": "staging"}}, false, false},
		{"tag and label", routing.Matchers{Tags: tag.Tags{"env": "prod"}, Labels: map[string]string{"category": "security"}}, false, false},
		{"all must match", routing.Matchers{Severities: []notify.Severity{notify.Warning}, Groups: []tenant.GroupID{groupA}}, false, false},
		{"all match", routing.Matchers{Severities: []notify.Severity{notify.Critical}, Groups: []tenant.GroupID{groupA}, Rules: []string{"system.*"}, Labels: map[string]string{"category": "cpu"}}, true, false},
	} {
		if got := tc.m.Match(cpu); got != tc.cpu {
			t.Errorf("%s: cpu %v, want %v", tc.name, got, tc.cpu)
		}
		if got := tc.m.Match(ssh); got != tc.ssh {
			t.Errorf("%s: ssh %v, want %v", tc.name, got, tc.ssh)
		}
	}
}

func valid() routing.Policy {
	return routing.Policy{Name: " Ops ", Enabled: true, Channels: []notify.ChannelID{chanA}, Grouping: routing.GroupByNode}
}

func TestPolicy_Validate(t *testing.T) {
	p := valid()
	if err := p.Validate(); err != nil || p.Name != "Ops" {
		t.Fatalf("valid policy: %v, name %q", err, p.Name)
	}
	bad := map[string]func(*routing.Policy){
		"no name":           func(p *routing.Policy) { p.Name = "  " },
		"long name":         func(p *routing.Policy) { p.Name = strings.Repeat("x", 101) },
		"control in name":   func(p *routing.Policy) { p.Name = "a\nb" },
		"grouping":          func(p *routing.Policy) { p.Grouping = "rule" },
		"no channel":        func(p *routing.Policy) { p.Channels = nil },
		"duplicate channel": func(p *routing.Policy) { p.Channels = []notify.ChannelID{chanA, chanA} },
		"repeat too short":  func(p *routing.Policy) { p.Repeat = time.Minute },
		"repeat too long":   func(p *routing.Policy) { p.Repeat = 8 * 24 * time.Hour },
		"escalation too early": func(p *routing.Policy) {
			p.Escalation = &routing.Escalation{After: time.Second, Channels: []notify.ChannelID{chanB}}
		},
		"escalation too late": func(p *routing.Policy) {
			p.Escalation = &routing.Escalation{After: 25 * time.Hour, Channels: []notify.ChannelID{chanB}}
		},
		"escalation without target": func(p *routing.Policy) { p.Escalation = &routing.Escalation{After: 15 * time.Minute} },
		"severity":                  func(p *routing.Policy) { p.Match.Severities = []notify.Severity{"info"} },
		"severity twice":            func(p *routing.Policy) { p.Match.Severities = []notify.Severity{notify.Warning, notify.Warning} },
		"rule pattern":              func(p *routing.Policy) { p.Match.Rules = []string{"Security.*"} },
		"rule pattern class":        func(p *routing.Policy) { p.Match.Rules = []string{"system.[a-z]*"} },
		"label key":                 func(p *routing.Policy) { p.Match.Labels = map[string]string{"Category": "x"} },
		"empty label value":         func(p *routing.Policy) { p.Match.Labels = map[string]string{"category": ""} },
		"tag key":                   func(p *routing.Policy) { p.Match.Tags = tag.Tags{"Env": "prod"} },
		"tag value":                 func(p *routing.Policy) { p.Match.Tags = tag.Tags{"env": "<prod>"} },
		"many groups": func(p *routing.Policy) {
			for range 21 {
				p.Match.Groups = append(p.Match.Groups, tenant.GroupID(uuid.New()))
			}
		},
	}
	for name, change := range bad {
		p := valid()
		change(&p)
		if err := p.Validate(); !errors.Is(err, routing.ErrInvalidPolicy) {
			t.Errorf("%s: %v", name, err)
		}
	}
	p = valid()
	p.Repeat, p.Escalation = 4*time.Hour, &routing.Escalation{After: 15 * time.Minute, Channels: []notify.ChannelID{chanB, chanC}}
	if err := p.Validate(); err != nil {
		t.Errorf("repeat and escalation: %v", err)
	}
}
