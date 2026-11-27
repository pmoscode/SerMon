package main

import (
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/pmoscode/SerMon/server/internal/adapters/pki"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	"github.com/pmoscode/SerMon/server/internal/app/enrollment"
	"github.com/pmoscode/SerMon/server/internal/app/rotation"
	"github.com/pmoscode/SerMon/server/internal/app/selfmonitor"
	"github.com/pmoscode/SerMon/server/internal/config"
	"github.com/pmoscode/SerMon/server/internal/domain/retention"
)

// V1-09: the admin guide (docs/admin) names everything an operator sets – every SERMON_* variable
// the server reads, every key of [local] in agent.toml and of helper.toml, every option of
// install.sh – with the defaults taken from the code, in German and English; every link in it
// resolves. A new variable or veto fails here until the guide explains it.

var repoRoot = filepath.Join("..", "..", "..")

// adminPages reads the pages of one language of the admin guide, file name → text.
func adminPages(t *testing.T, lang string) map[string]string {
	t.Helper()
	dir := filepath.Join(repoRoot, "docs", "admin", lang)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	pages := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		pages[e.Name()] = string(raw)
	}
	if len(pages) == 0 {
		t.Fatalf("docs/admin/%s: no pages", lang)
	}
	return pages
}

// flat joins texts without line breaks and table padding.
func flat(texts ...string) string {
	return strings.Join(strings.Fields(strings.Join(texts, "\n")), " ")
}

func allPages(pages map[string]string) string {
	texts := make([]string, 0, len(pages))
	for _, text := range pages {
		texts = append(texts, text)
	}
	return flat(texts...)
}

func readRepo(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// serverVariables are the SERMON_* names in the server's configuration code (not in tests);
// X_FILE counts as X, every variable has that form (SERMON_GEOIP_DB_FILE is a name of its own).
func serverVariables(t *testing.T) []string {
	t.Helper()
	literal := regexp.MustCompile(`"(SERMON_[A-Z0-9_]+)"`)
	seen := map[string]bool{}
	for _, dir := range []string{filepath.Join("..", "..", "internal", "config"), "."} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range literal.FindAllStringSubmatch(string(raw), -1) {
				seen[m[1]] = true
			}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		if base, ok := strings.CutSuffix(name, "_FILE"); ok && seen[base] {
			continue
		}
		names = append(names, name)
	}
	slices.Sort(names)
	if len(names) < 40 {
		t.Fatalf("found only %d variables – did the configuration move?", len(names))
	}
	return names
}

// structFields are the field names of `struct <name> {` in Rust source, by the pattern of a line.
func structFields(t *testing.T, source, name string, field *regexp.Regexp) []string {
	t.Helper()
	start := strings.Index(source, "struct "+name+" {")
	if start < 0 {
		t.Fatalf("struct %s not found", name)
	}
	body, _, found := strings.Cut(source[start:], "\n}")
	if !found {
		t.Fatalf("struct %s: no end found", name)
	}
	var fields []string
	for _, m := range field.FindAllStringSubmatch(body, -1) {
		fields = append(fields, m[1])
	}
	if len(fields) == 0 {
		t.Fatalf("struct %s: no fields found", name)
	}
	return fields
}

// installOptions are the options in the usage text of install.sh.
func installOptions(t *testing.T) []string {
	t.Helper()
	script := readRepo(t, "packaging/install.sh")
	start := strings.Index(script, "usage() {")
	if start < 0 {
		t.Fatal("install.sh: no usage text")
	}
	usage, _, found := strings.Cut(script[start:], "\nUSAGE\n")
	if !found {
		t.Fatal("install.sh: no end of the usage text")
	}
	seen := map[string]bool{}
	for _, m := range regexp.MustCompile(`--[a-z][a-z-]+`).FindAllString(usage, -1) {
		seen[m] = true
	}
	options := make([]string, 0, len(seen))
	for o := range seen {
		options = append(options, o)
	}
	slices.Sort(options)
	return options
}

func TestAdminDocs_NameEveryVariableVetoAndOption(t *testing.T) {
	variables := serverVariables(t)
	agentKeys := structFields(t, readRepo(t, "agent/crates/sermon-agent/src/config.rs"), "RawLocal",
		regexp.MustCompile(`(?m)^\s+(\w+): Option<`))
	helperKeys := structFields(t, readRepo(t, "agent/crates/sermon-helper/src/config.rs"), "Config",
		regexp.MustCompile(`(?m)^\s+pub (\w+): bool,`))
	options := installOptions(t)
	for _, lang := range helpLanguages {
		pages := adminPages(t, lang)
		all := allPages(pages)
		for _, name := range variables {
			if !strings.Contains(all, "`"+name+"`") {
				t.Errorf("docs/admin/%s: server variable `%s` is not explained", lang, name)
			}
		}
		hardening := flat(pages["hardening.md"])
		for _, key := range slices.Concat(agentKeys, helperKeys, options) {
			if !strings.Contains(hardening, "`"+key+"`") {
				t.Errorf("docs/admin/%s/hardening.md: `%s` is missing", lang, key)
			}
		}
	}
}

func TestAdminDocs_DefaultsMatchTheCode(t *testing.T) {
	defaults, err := config.Load(config.Source{
		LookupEnv: func(string) (string, bool) { return "", false },
		ReadFile:  func(string) ([]byte, error) { return nil, os.ErrNotExist },
	})
	if err != nil {
		t.Fatal(err)
	}
	h := func(d time.Duration) string { return fmt.Sprintf("`%dh`", int(d/time.Hour)) }
	days := func(d time.Duration) int { return int(d / (24 * time.Hour)) }
	hours := func(d time.Duration) int { return int(d / time.Hour) }
	minutes := func(d time.Duration) int { return int(d / time.Minute) }
	percent := func(part float64) int { return int(math.Round(part * 100)) }
	// The reference table: the same cells in both languages.
	cells := []string{
		"| `SERMON_MODE` | `" + string(defaults.Mode) + "` |",
		"| `SERMON_HTTP_ADDR` | `" + defaults.HTTPAddr + "` |",
		"| `SERMON_LOG_LEVEL` | `" + strings.ToLower(defaults.LogLevel.String()) + "` |",
		"| `SERMON_LOG_FORMAT` | `" + string(defaults.LogFormat) + "` |",
		"| `SERMON_SHUTDOWN_TIMEOUT` | `" + defaults.ShutdownTimeout.String() + "` |",
		"| `SERMON_ORG_DELETION_GRACE` | " + h(defaults.OrgDeletionGrace) + " |",
		"| `SERMON_NODE_DELETION_GRACE` | " + h(defaults.NodeDeletionGrace) + " |",
		"| `SERMON_ACCOUNT_DELETION_GRACE` | " + h(defaults.AccountGrace) + " |",
		"| `SERMON_AUDIT_RETENTION` | " + h(defaults.AuditRetention) + " |",
		"| `SERMON_EVENT_RETENTION` | " + h(defaults.EventRetention) + " |",
	}
	// Each fact in both languages: the value from the code, and every occurrence of the phrase on
	// any page with that value (a stale copy elsewhere fails too).
	facts := []struct {
		de, en string
		values []int
	}{
		{
			"gültig %d Minuten bis %d Tage, ab Werk %d Stunde, %d bis %d Nutzungen, ab Werk %d",
			"valid for %d minutes to %d days, default %d hour, %d to %d uses, default %d",
			[]int{minutes(enrollment.MinTTL), days(enrollment.MaxTTL), hours(enrollment.DefaultTTL), 1, enrollment.MaxUses, 1},
		},
		{"Agent-Zertifikate gelten %d Tage", "Agent certificates are valid for %d days", []int{days(pki.AgentCertificateLifetime)}},
		{"das alte gilt noch %d Stunde", "the old one stays valid for %d hour", []int{hours(rotation.Grace)}},
		{
			"Server-Zertifikat der Agent-API gilt %d Tage", "server certificate of the agent API is valid for %d days",
			[]int{days(pki.ServerCertificateLifetime)},
		},
		{
			"Warnung ab %d s, kritisch ab %d min", "warning from %d s, critical from %d min",
			[]int{int(selfmonitor.LagWarning / time.Second), minutes(selfmonitor.LagCritical)},
		},
		{
			"Warnung ab %d %%, kritisch ab %d %%", "warning from %d %%, critical from %d %%",
			[]int{percent(selfmonitor.SizeWarningPart), percent(selfmonitor.SizeCriticalPart)},
		},
		{
			"Warnung ab %d Tagen vor dem Ablauf, kritisch ab %d Tagen", "warning from %d days before the expiry, critical from %d days",
			[]int{days(selfmonitor.CAWarning), days(selfmonitor.CACritical)},
		},
		{
			"die Intermediate-CA, die alle Zertifikate ausstellt, %d Jahre", "the intermediate CA that issues all certificates for %d years",
			[]int{days(pki.IntermediateLifetime) / 365},
		},
		{"wenn innerhalb von %d min welche dazukommen", "if any are added within %d min", []int{minutes(selfmonitor.DroppedWindow)}},
		{
			"Logs %d bis %d Tage, Security-Events und Verlauf der Alerts %d bis %d Tage",
			"logs %d to %d days, security events and alert history %d to %d days",
			[]int{retention.MinLogDays, retention.MaxLogDays, retention.MinEventDays, retention.MaxEventDays},
		},
		{
			"Security-Events und Verlauf der Alerts ab Werk %d Tage", "security events and alert history %d days by default",
			[]int{days(defaults.EventRetention)},
		},
		{"gilt %d Minuten", "holds for %d minutes", []int{minutes(access.StepUpMaxAge)}},
		{
			"nach %d Minuten ohne Aktivität, spätestens nach %d Stunden",
			"after %d minutes without activity, at the latest after %d hours",
			[]int{minutes(session.IdleTimeout), hours(session.AbsoluteTimeout)},
		},
	}
	for _, lang := range helpLanguages {
		pages := adminPages(t, lang)
		reference := flat(pages["configuration.md"])
		for _, cell := range cells {
			if !strings.Contains(reference, cell) {
				t.Errorf("docs/admin/%s/configuration.md: missing %q", lang, cell)
			}
		}
		all := allPages(pages)
		for _, f := range facts {
			format := f.de
			if lang == "en" {
				format = f.en
			}
			checkFact(t, "docs/admin/"+lang, all, format, f.values)
		}
	}
}

// checkFact requires format with values in text, and the values in every occurrence of format.
func checkFact(t *testing.T, where, text, format string, values []int) {
	t.Helper()
	args := make([]any, len(values))
	want := make([]string, len(values))
	for i, v := range values {
		args[i], want[i] = v, strconv.Itoa(v)
	}
	if !strings.Contains(text, fmt.Sprintf(format, args...)) {
		t.Errorf("%s: missing %q", where, fmt.Sprintf(format, args...))
	}
	pattern := strings.ReplaceAll(strings.ReplaceAll(regexp.QuoteMeta(format), "%d", `(\d+)`), "%%", "%")
	for _, m := range regexp.MustCompile(pattern).FindAllStringSubmatch(text, -1) {
		if !slices.Equal(m[1:], want) {
			t.Errorf("%s: %q, but the code says %v", where, m[0], want)
		}
	}
}

// githubAnchor is the anchor GitHub gives a heading (as web/scripts/help.js).
func githubAnchor(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(heading)) {
		switch {
		case unicode.IsSpace(r):
			b.WriteRune('-')
		case unicode.IsLetter(r), unicode.IsNumber(r), r == '_', r == '-':
			b.WriteRune(r)
		}
	}
	return b.String()
}

var markdownHeading = regexp.MustCompile(`^#{1,6}\s+(.+)$`)

func headingAnchors(text string) map[string]bool {
	anchors := map[string]bool{}
	inCode := false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "```") {
			inCode = !inCode
		}
		if m := markdownHeading.FindStringSubmatch(line); m != nil && !inCode {
			anchors[githubAnchor(m[1])] = true
		}
	}
	return anchors
}

func TestAdminDocs_LinksResolveAndLanguagesMatch(t *testing.T) {
	root := filepath.Join(repoRoot, "docs", "admin")
	link := regexp.MustCompile(`\]\(([^)\s]+)\)`)
	checked := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(raw)
		for _, m := range link.FindAllStringSubmatch(text, -1) {
			href := m[1]
			if strings.HasPrefix(href, "https:") || strings.HasPrefix(href, "mailto:") {
				continue
			}
			checked++
			target, anchor, _ := strings.Cut(href, "#")
			targetText := text
			if target != "" {
				resolved := filepath.Join(filepath.Dir(path), filepath.FromSlash(target))
				info, err := os.Stat(resolved)
				if err != nil {
					t.Errorf("%s: link %q does not resolve", path, href)
					continue
				}
				if anchor == "" || info.IsDir() {
					continue
				}
				raw, err := os.ReadFile(resolved)
				if err != nil {
					return err
				}
				targetText = string(raw)
			}
			if anchor != "" && !headingAnchors(targetText)[anchor] {
				t.Errorf("%s: no heading for %q", path, href)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("no links checked")
	}
	de, en := adminPages(t, "de"), adminPages(t, "en")
	for name := range de {
		if _, ok := en[name]; !ok {
			t.Errorf("docs/admin/en/%s is missing", name)
		}
	}
	for name := range en {
		if _, ok := de[name]; !ok {
			t.Errorf("docs/admin/de/%s is missing", name)
		}
	}
}
