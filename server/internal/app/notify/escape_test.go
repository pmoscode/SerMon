package notify_test

import (
	"strings"
	"testing"

	"github.com/pmoscode/SerMon/server/internal/app/notify"
)

// What an attacker puts into a user name or log line, aimed at the formats of the channels.
var hostile = []string{
	"<script>alert(1)</script>",
	"<!channel> <!here> <@U123> <http://evil.example|click>",
	"@everyone @here @channel @all",
	"[click](https://evil.example) ![x](https://evil.example/p.png)",
	"*bold* _it_ ~strike~ `code` ```block``` > quote # head",
	"a & b \" ' \\ | {} + - . !",
	"line\nbreak\r\nand\ttab",
}

func TestEscapeHTML(t *testing.T) {
	for _, s := range hostile {
		got := notify.EscapeHTML(s)
		if strings.ContainsAny(got, "<>\"'") || strings.Contains(strings.ReplaceAll(got, "&amp;", ""), "& ") {
			t.Errorf("EscapeHTML(%q) = %q", s, got)
		}
	}
	if got := notify.EscapeHTML("a<b & \"c\""); got != "a&lt;b &amp; &#34;c&#34;" {
		t.Errorf("EscapeHTML = %q", got)
	}
}

// Markdown (CommonMark, Discord, Mattermost, Teams): every ASCII punctuation is escaped, so
// nothing becomes a link, image, emphasis, code, quote or heading; line breaks become spaces.
func TestEscapeMarkdown(t *testing.T) {
	for _, s := range hostile {
		got := notify.EscapeMarkdown(s)
		if strings.ContainsAny(got, "\r\n") {
			t.Errorf("EscapeMarkdown(%q) = %q spans lines", s, got)
		}
		for i := 0; i < len(got); i++ {
			c := got[i]
			if strings.IndexByte(notify.MarkdownPunctuation, c) < 0 {
				continue
			}
			if c == '\\' && i+1 < len(got) && strings.IndexByte(notify.MarkdownPunctuation, got[i+1]) >= 0 {
				i++ // an escape and its character
				continue
			}
			t.Errorf("EscapeMarkdown(%q) = %q: %q at %d unescaped", s, got, c, i)
			break
		}
	}
	if got := notify.EscapeMarkdown("[a](b)"); got != `\[a\]\(b\)` {
		t.Errorf("EscapeMarkdown = %q", got)
	}
}

// Slack and Mattermost read <…> as links and mentions; only &, < and > are escaped there.
func TestEscapeSlack(t *testing.T) {
	for _, s := range hostile {
		got := notify.EscapeSlack(s)
		if strings.ContainsAny(got, "<>\r\n") {
			t.Errorf("EscapeSlack(%q) = %q", s, got)
		}
	}
	if got := notify.EscapeSlack("<!here> & x"); got != "&lt;!here&gt; &amp; x" {
		t.Errorf("EscapeSlack = %q", got)
	}
}

// Plain @mentions ping in Mattermost and Discord: a zero-width space after the @ defuses them.
func TestDefuseMentions(t *testing.T) {
	got := notify.DefuseMentions("@everyone @here alice@example.org")
	if strings.Contains(got, "@everyone") || strings.Contains(got, "@here") || !strings.Contains(got, "@\u200beveryone") {
		t.Errorf("DefuseMentions = %q", got)
	}
}
