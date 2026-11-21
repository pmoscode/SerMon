package notify

import (
	"html"
	"strings"
)

// The values of a message come from the monitored hosts (host names, user names) and are written
// by attackers. Channels escape them for their target format with these helpers.

// EscapeHTML escapes for HTML (e-mail, Telegram HTML).
func EscapeHTML(s string) string { return html.EscapeString(s) }

// MarkdownPunctuation is every ASCII punctuation character; CommonMark allows a backslash before
// each.
const MarkdownPunctuation = "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"

// EscapeMarkdown escapes for Markdown (Discord, Mattermost, Teams, CommonMark): a backslash
// before every ASCII punctuation character, so nothing becomes a link, image, emphasis, code,
// quote or heading; line breaks and tabs become spaces.
func EscapeMarkdown(s string) string {
	var b strings.Builder
	for _, r := range oneLine(s) {
		if r < 128 && strings.ContainsRune(MarkdownPunctuation, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

var slack = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// EscapeSlack escapes for Slack mrkdwn: &, < and > (links and mentions are written <…>); line
// breaks become spaces.
func EscapeSlack(s string) string { return slack.Replace(oneLine(s)) }

// DefuseMentions puts a zero-width space after every @, so @everyone, @here or @channel do not
// notify anybody in Mattermost or Discord.
func DefuseMentions(s string) string { return strings.ReplaceAll(s, "@", "@\u200b") }
