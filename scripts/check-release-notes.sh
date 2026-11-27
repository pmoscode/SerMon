#!/bin/sh
# Every release says what its upgrade means (ADR 0043 §9): the notes need a section "Upgrade"
# (heading of level 2 to 4) with content – breaking changes, new secrets, data the upgrade
# deletes, how to roll back. HTML comments, "TODO" and "…" don't count.
#
#   scripts/check-release-notes.sh --pr-body < BODY    the body of a release PR of release-please:
#                                                      only the notes between its two "---" lines
#                                                      become the release, so only they count
#   scripts/check-release-notes.sh < NOTES             release notes as they are
set -eu

pr_body=false
[ "${1:-}" != --pr-body ] || pr_body=true

input=$(cat | tr -d '\r')
if $pr_body; then
	# As release-please splits it: from the first line "---" to the last one.
	first=$(printf '%s\n' "$input" | grep -nx -- '---' | head -1 | cut -d: -f1)
	last=$(printf '%s\n' "$input" | grep -nx -- '---' | tail -1 | cut -d: -f1)
	if [ -z "$first" ] || [ "$first" = "$last" ]; then
		echo "✗ the body has no notes between two lines \"---\" (not a release PR of release-please?)" >&2
		exit 1
	fi
	input=$(printf '%s\n' "$input" | sed -n "$((first + 1)),$((last - 1))p")
fi

content=$(printf '%s\n' "$input" | awk '
	# Comments, also over several lines.
	{
		line = $0
		while (1) {
			if (incomment) {
				end = index(line, "-->")
				if (!end) { line = ""; break }
				line = substr(line, end + 3)
				incomment = 0
			}
			start = index(line, "<!--")
			if (!start) break
			rest = substr(line, start + 4)
			end = index(rest, "-->")
			if (end) { line = substr(line, 1, start - 1) substr(rest, end + 3) }
			else { line = substr(line, 1, start - 1); incomment = 1 }
		}
	}
	match(line, /^#+[ \t]/) {
		level = RLENGTH - 1
		title = substr(line, RLENGTH + 1)
		sub(/[ \t]+$/, "", title)
		if (insection && level <= depth) insection = 0
		if (level >= 2 && level <= 4 && title == "Upgrade") { insection = 1; depth = level; found = 1; next }
	}
	insection {
		text = line
		gsub(/[ \t]/, "", text)
		if (text != "" && text != "TODO" && text != "…" && text != "..." && text != "-" && text != "*") print line
	}
	END { if (!found) exit 2 }
') || {
	echo "✗ the release notes have no section \"### Upgrade\" (ADR 0043: breaking changes, new secrets, data the upgrade deletes, rollback)" >&2
	exit 1
}
if [ -z "$content" ]; then
	echo "✗ the section \"Upgrade\" of the release notes is empty – say what the upgrade means, also when nothing changes" >&2
	exit 1
fi
echo "✓ release notes with a section \"Upgrade\""
