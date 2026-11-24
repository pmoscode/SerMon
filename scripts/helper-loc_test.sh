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
# An item only for tests inside the code counts; the test module ends the count.
printf 'enum A {\n    #[cfg(test)]\n    Inline,\n}\n#[cfg(test)]\nmod tests {\n    fn x() {}\n}\n' >"$work/c.rs"
out=$(sh "$dir/helper-loc.sh" "$work" 7)
[ "$out" = "✓ sermon-helper: 7 lines of code (limit 7)" ] || {
	echo "FAIL: counted wrongly: $out"
	exit 1
}
if sh "$dir/helper-loc.sh" "$work" 6 >/dev/null 2>&1; then
	echo "FAIL: 7 lines passed a limit of 6"
	exit 1
fi
sh "$dir/helper-loc.sh" >/dev/null
echo "✓ helper-loc: counts code without comments and tests, fails above the limit"
