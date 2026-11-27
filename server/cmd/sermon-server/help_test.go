package main

import (
	"bufio"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pmoscode/SerMon/server/internal/app/alerting/ruleset"
	"github.com/pmoscode/SerMon/server/internal/config"
	"github.com/pmoscode/SerMon/server/internal/domain/metric"
	"github.com/pmoscode/SerMon/server/internal/domain/module"
)

// V1-10: everything the server names for the UI has a help text in German and English – the UI
// builds these topics from the names it gets (metrics/<name>, channels/<type>, modules/<id>), so
// the literal check of the help pipeline (web/scripts/help.js) cannot see them.

var helpLanguages = []string{"de", "en"}

// helpTitle returns the title of a help file: its first line "# …" followed by a summary.
func helpTitle(t *testing.T, topic, lang string) string {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "..", "docs", "help", lang, topic+".md"))
	if err != nil {
		t.Errorf("%s: no help text in %s", topic, lang)
		return ""
	}
	defer f.Close()
	lines := bufio.NewScanner(f)
	var title, summary string
	for lines.Scan() {
		line := strings.TrimSpace(lines.Text())
		switch {
		case title == "":
			title, _ = strings.CutPrefix(line, "# ")
			if title == line {
				t.Errorf("%s (%s): the first line is not a title", topic, lang)
				return ""
			}
		case line != "":
			summary = line
		}
		if summary != "" {
			break
		}
	}
	if summary == "" || strings.HasPrefix(summary, "#") {
		t.Errorf("%s (%s): no summary after the title", topic, lang)
	}
	return title
}

func requireHelp(t *testing.T, topics []string) {
	t.Helper()
	if len(topics) == 0 {
		t.Fatal("no topics")
	}
	for _, topic := range topics {
		for _, lang := range helpLanguages {
			helpTitle(t, topic, lang)
		}
	}
}

func TestHelpTexts_EveryMetricOfTheCatalog(t *testing.T) {
	var topics []string
	modules := 0
	for _, d := range metric.All() {
		topics = append(topics, d.HelpKey())
		if d.Module != "" {
			modules++
		}
	}
	if modules == 0 {
		t.Fatal("the catalog has no module metrics")
	}
	requireHelp(t, topics)
}

func TestHelpTexts_EveryChannelType(t *testing.T) {
	// With SMTP, e-mail is a channel type as well.
	cfg := config.Config{SMTP: &config.SMTP{Host: "mail.example.org", Port: 587, TLS: config.SMTPStartTLS, From: "sermon@example.org"}}
	deps, err := newNotifyDeps(cfg, nil, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	var topics []string
	for _, n := range deps.Registry.Types() {
		topics = append(topics, "channels/"+string(n.Type()))
	}
	if len(topics) < 10 {
		t.Fatalf("%d channel types", len(topics))
	}
	requireHelp(t, topics)
}

func TestHelpTexts_EveryModule(t *testing.T) {
	catalog, err := module.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	var topics []string
	for _, m := range catalog.All() {
		topics = append(topics, "modules/"+m.ID)
	}
	requireHelp(t, topics)
}

// The help of a default rule is part of its YAML (spec.help); the UI shows it with the rule and
// the alert. The schema requires both languages; this test also catches an untranslated copy.
func TestHelpTexts_EveryDefaultRule(t *testing.T) {
	defs, err := ruleset.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) < 40 {
		t.Fatalf("%d default rules", len(defs))
	}
	for _, d := range defs {
		id := d.Doc.GetMetadata().GetId()
		for what, text := range map[string]interface {
			GetDe() string
			GetEn() string
		}{"name": d.Doc.GetMetadata().GetName(), "help": d.Doc.GetSpec().GetHelp()} {
			de, en := strings.TrimSpace(text.GetDe()), strings.TrimSpace(text.GetEn())
			switch {
			case de == "" || en == "":
				t.Errorf("%s: %s missing in a language (de %q, en %q)", id, what, de, en)
			case what == "help" && de == en:
				t.Errorf("%s: the German help is the English one", id)
			case what == "help" && (len(de) < 80 || len(en) < 80):
				t.Errorf("%s: help too short to explain the alert", id)
			}
		}
	}
}
