#!/bin/sh
# Counts the root helper's own code (ADR 0035: under 1 000 lines) and fails above the limit:
# lines of agent/crates/sermon-helper/src/*.rs up to their tests (#[cfg(test)]), without blank and
# comment lines. Generated code (sermon-helper-proto) and the tests don't count.
#
#   sh scripts/helper-loc.sh [DIR] [LIMIT]    defaults: the helper's src/, 1000
set -eu

dir=${1:-$(cd "$(dirname "$0")/.." && pwd)/agent/crates/sermon-helper/src}
limit=${2:-1000}
total=0
for file in "$dir"/*.rs; do
	lines=$(awk '/^#\[cfg\(test\)\]/ { exit } { sub(/^[ \t]+/, "") } $0 != "" && $0 !~ /^\/\// { n++ } END { print n + 0 }' "$file")
	total=$((total + lines))
done
if [ "$total" -gt "$limit" ]; then
	echo "✗ sermon-helper: $total lines of code, more than $limit (ADR 0035)" >&2
	exit 1
fi
echo "✓ sermon-helper: $total lines of code (limit $limit)"
