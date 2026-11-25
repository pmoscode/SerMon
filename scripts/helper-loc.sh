#!/bin/sh
# Counts the root helper's own code (ADR 0035, ADR 0036: at most 1 800 lines) and fails above the limit:
# lines of agent/crates/sermon-helper/src/*.rs up to their test module, without blank and
# comment lines. Generated code (sermon-helper-proto) and the tests don't count.
#
#   sh scripts/helper-loc.sh [DIR] [LIMIT]    defaults: the helper's src/, 1800
set -eu

dir=${1:-$(cd "$(dirname "$0")/.." && pwd)/agent/crates/sermon-helper/src}
limit=${2:-1800}
total=0
for file in "$dir"/*.rs; do
	# The test module ends the count; a #[cfg(test)] on an item inside the code counts like code.
	lines=$(awk 'held && /^mod tests/ { exit } held { n++; held = 0 } /^#\[cfg\(test\)\]$/ { held = 1; next } { sub(/^[ \t]+/, "") } $0 != "" && $0 !~ /^\/\// { n++ } END { print n + 0 }' "$file")
	total=$((total + lines))
done
if [ "$total" -gt "$limit" ]; then
	echo "✗ sermon-helper: $total lines of code, more than $limit (ADR 0035)" >&2
	exit 1
fi
echo "✓ sermon-helper: $total lines of code (limit $limit)"
