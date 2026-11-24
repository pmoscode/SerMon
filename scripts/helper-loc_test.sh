#!/bin/sh
# Tests for helper-loc.sh. Run: sh scripts/helper-loc_test.sh
set -eu

dir=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cat >"$work/a.rs" <<'RS'
//! A module.

// A comment.
fn one() {}
    fn two() {}
#[cfg(test)]
mod tests {
    fn not_counted() {}
}
RS
printf 'fn three() {}\n' >"$work/b.rs"
out=$(sh "$dir/helper-loc.sh" "$work" 3)
[ "$out" = "✓ sermon-helper: 3 lines of code (limit 3)" ] || {
	echo "FAIL: counted wrongly: $out"
	exit 1
}
if sh "$dir/helper-loc.sh" "$work" 2 >/dev/null 2>&1; then
	echo "FAIL: 3 lines passed a limit of 2"
	exit 1
fi
sh "$dir/helper-loc.sh" >/dev/null
echo "✓ helper-loc: counts code without comments and tests, fails above the limit"
