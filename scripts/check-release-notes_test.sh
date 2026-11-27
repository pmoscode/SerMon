#!/bin/sh
# Tests for check-release-notes.sh. Run: sh scripts/check-release-notes_test.sh
set -eu

dir=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
failures=0
total=0

# expect pass|fail DESCRIPTION [ARGS…] checks the input written last to $work/in (a pipe into
# expect would run it in a subshell and lose the counters).
expect() {
	expected=$1 description=$2
	shift 2
	if sh "$dir/check-release-notes.sh" "$@" <"$work/in" >/dev/null 2>&1; then actual=pass; else actual=fail; fi
	total=$((total + 1))
	if [ "$actual" != "$expected" ]; then
		echo "FAIL: $description – expected $expected, got $actual"
		failures=$((failures + 1))
	fi
}

# body NOTES prints a release PR body as release-please writes it, with NOTES between the lines.
body() {
	printf ':robot: I have created a release *beep* *boop*\n---\n\n\n## [1.0.0-rc.1](https://github.com/pmoscode/SerMon/compare/v1.0.0-rc.0...v1.0.0-rc.1) (2026-11-30)\n\n### Features\n\n* something\n\n%s\n\n---\nVor dem Merge: Abschnitt ### Upgrade oberhalb der Linie ergänzen.\n' "$1"
}

upgrade='### Upgrade

- Keine Breaking Changes. Rollback: das vorige Image per Digest.'

body "$upgrade" >"$work/in"
expect pass "an Upgrade section in the notes" --pr-body
body "## Upgrade

Neue Secret-Datei secrets/postgres_backup_password (./secrets.sh)." >"$work/in"
expect pass "an Upgrade heading of level 2" --pr-body
body "" >"$work/in"
expect fail "no Upgrade section" --pr-body
body "### Upgrade" >"$work/in"
expect fail "an empty Upgrade section" --pr-body
body "### Upgrade

<!-- Breaking Changes, neue Secrets, Daten, die das Upgrade löscht, Rollback -->" >"$work/in"
expect fail "an Upgrade section with only a comment" --pr-body
body "### Upgrade
<!--
mehrzeiliger Kommentar
-->

TODO" >"$work/in"
expect fail "an Upgrade section with a multi-line comment and TODO" --pr-body
body "### Upgrade

…" >"$work/in"
expect fail "an Upgrade section with an ellipsis" --pr-body
body "### Upgrade

### Bug Fixes

* fix" >"$work/in"
expect fail "an Upgrade section that ends at the next heading" --pr-body
body "### Upgrades and more

text" >"$work/in"
expect fail "a heading that only starts with Upgrade" --pr-body

# Only the notes count: the footer and header are not released.
printf 'header\n---\n\n## [1.0.0](x) (2026-11-30)\n\n* feat\n\n---\n### Upgrade\n\nim Footer\n' >"$work/in"
expect fail "an Upgrade section in the footer" --pr-body
printf 'no delimiters at all\n### Upgrade\n\ntext\n' >"$work/in"
expect fail "a body without the delimiters of release-please" --pr-body

# Release notes themselves (the draft release): no delimiters.
printf '## [1.0.0](x) (2026-11-30)\n\n### Upgrade\n\nNichts zu tun.\n' >"$work/in"
expect pass "release notes with an Upgrade section"
printf '## [1.0.0](x) (2026-11-30)\n\n### Features\n\n* x\n' >"$work/in"
expect fail "release notes without an Upgrade section"
printf '' >"$work/in"
expect fail "empty input"

if [ "$failures" -ne 0 ]; then
	echo "$failures of $total release note checks failed"
	exit 1
fi
echo "✓ check-release-notes: all $total cases"
