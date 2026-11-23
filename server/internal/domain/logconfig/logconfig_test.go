package logconfig_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/pmoscode/SerMon/server/internal/domain/logconfig"
)

func TestDefault_SendsEverythingFromTheJournal(t *testing.T) {
	d := logconfig.Default()
	if !d.Enabled || d.MinSeverity != logconfig.SeverityUnspecified || len(d.ExcludeUnits) != 0 || len(d.Files) != 0 {
		t.Errorf("Default() = %+v, want logs on without filters (KONZEPT §10.1)", d)
	}
	if err := d.Validate(); err != nil {
		t.Errorf("Default().Validate() = %v", err)
	}
}

func TestSettings_Validate(t *testing.T) {
	valid := []logconfig.Settings{
		{Enabled: true, MinSeverity: logconfig.SeverityWarning},
		{ExcludeUnits: []string{"cron.service", "user@1000.service", `systemd-fsck@dev-disk-by\x2duuid.service`, "sshd"}},
		{Files: []logconfig.FileGlob{
			{Pattern: "/var/log/nginx/*.log"},
			{Pattern: "/srv/app/logs/app-?.log", MultilineStart: `^\d{4}-\d{2}-\d{2} `},
		}},
		{MinSeverity: logconfig.SeverityDebug},
	}
	for _, s := range valid {
		if err := s.Validate(); err != nil {
			t.Errorf("%+v: %v", s, err)
		}
	}

	many := func(n int, f func(int) string) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = f(i)
		}
		return out
	}
	files := func(globs ...logconfig.FileGlob) logconfig.Settings { return logconfig.Settings{Files: globs} }
	invalid := map[string]logconfig.Settings{
		"severity 9":        {MinSeverity: 9},
		"empty unit":        {ExcludeUnits: []string{""}},
		"unit with space":   {ExcludeUnits: []string{"a b"}},
		"unit injection":    {ExcludeUnits: []string{`x"}|~".*`}},
		"long unit":         {ExcludeUnits: []string{strings.Repeat("u", 257)}},
		"too many units":    {ExcludeUnits: many(65, func(i int) string { return "u" + strings.Repeat("x", i) })},
		"duplicate unit":    {ExcludeUnits: []string{"cron.service", "cron.service"}},
		"relative glob":     files(logconfig.FileGlob{Pattern: "var/log/*.log"}),
		"root only":         files(logconfig.FileGlob{Pattern: "/"}),
		"parent segment":    files(logconfig.FileGlob{Pattern: "/var/log/../../etc/shadow"}),
		"parent at the end": files(logconfig.FileGlob{Pattern: "/var/log/.."}),
		"nul byte":          files(logconfig.FileGlob{Pattern: "/var/log/a\x00b"}),
		"bad glob":          files(logconfig.FileGlob{Pattern: "/var/log/[a.log"}),
		// The agent knows * and ? only (M3-05).
		"character class":    files(logconfig.FileGlob{Pattern: "/var/log/[ab].log"}),
		"escape":             files(logconfig.FileGlob{Pattern: `/var/log/a\*.log`}),
		"dot segment":        files(logconfig.FileGlob{Pattern: "/var/log/./a.log"}),
		"empty segment":      files(logconfig.FileGlob{Pattern: "/var/log//a.log"}),
		"long glob":          files(logconfig.FileGlob{Pattern: "/" + strings.Repeat("g", 1024)}),
		"bad multiline":      files(logconfig.FileGlob{Pattern: "/var/log/a.log", MultilineStart: "(unclosed"}),
		"long multiline":     files(logconfig.FileGlob{Pattern: "/var/log/a.log", MultilineStart: strings.Repeat("a", 1025)}),
		"too many files":     {Files: make([]logconfig.FileGlob, 17)},
		"duplicate patterns": files(logconfig.FileGlob{Pattern: "/var/log/a.log"}, logconfig.FileGlob{Pattern: "/var/log/a.log"}),
	}
	for i := range invalid["too many files"].Files {
		invalid["too many files"].Files[i] = logconfig.FileGlob{Pattern: "/var/log/" + strings.Repeat("f", i+1)}
	}
	for name, s := range invalid {
		err := s.Validate()
		if !errors.Is(err, logconfig.ErrInvalid) {
			t.Errorf("%s: Validate() = %v, want ErrInvalid", name, err)
		}
	}
}

func TestValidateRules(t *testing.T) {
	if err := logconfig.ValidateRules(nil); err != nil {
		t.Errorf("no rules: %v", err)
	}
	ok := []logconfig.Rule{{Pattern: `customer-\d{6}`}, {Pattern: `(?i)pin\s*(?P<secret>\d{4})`}, {Pattern: `(?<secret>x)`}}
	if err := logconfig.ValidateRules(ok); err != nil {
		t.Errorf("valid rules: %v", err)
	}
	rules := func(patterns ...string) []logconfig.Rule {
		out := make([]logconfig.Rule, len(patterns))
		for i, p := range patterns {
			out[i] = logconfig.Rule{Pattern: p}
		}
		return out
	}
	tooMany := make([]string, 33)
	for i := range tooMany {
		tooMany[i] = "a"
	}
	for name, tc := range map[string]struct {
		rules []logconfig.Rule
		index int
	}{
		"empty":           {rules("a", ""), 1},
		"long":            {rules(strings.Repeat("a", 1025)), 0},
		"syntax":          {rules("ok", "(unclosed"), 1},
		"backreference":   {rules(`(a)\1`), 0},
		"look-ahead":      {rules(`(?=x)`), 0},
		"too many":        {rules(tooMany...), -1},
		"invalid utf8":    {rules("\xff"), 0},
		"huge repetition": {rules(`a{1001}`), 0},
	} {
		err := logconfig.ValidateRules(tc.rules)
		var invalid *logconfig.InvalidError
		if !errors.Is(err, logconfig.ErrInvalid) || !errors.As(err, &invalid) {
			t.Errorf("%s: ValidateRules() = %v, want an InvalidError", name, err)
			continue
		}
		if invalid.Index != tc.index {
			t.Errorf("%s: index %d, want %d (%v)", name, invalid.Index, tc.index, err)
		}
	}
}

func TestSeverity_FollowsTheProtocol(t *testing.T) {
	if logconfig.SeverityEmergency != 1 || logconfig.SeverityDebug != 8 {
		t.Errorf("severities = %d..%d, want 1..8 like LogSeverity", logconfig.SeverityEmergency, logconfig.SeverityDebug)
	}
}
