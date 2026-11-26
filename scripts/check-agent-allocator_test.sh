#!/bin/sh
# Tests for check-agent-allocator.sh. Run: sh scripts/check-agent-allocator_test.sh
set -eu

dir=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
printf 'ELF...narenas:1,tcache:false,dirty_decay_ms:1000,muzzy_decay_ms:0\0...' >"$work/with"
printf 'ELF...malloc_conf\0...' >"$work/without"
out=$(sh "$dir/check-agent-allocator.sh" "$work/with") || {
	echo "FAIL: a binary with jemalloc failed: $out"
	exit 1
}
if sh "$dir/check-agent-allocator.sh" "$work/with" "$work/without" >/dev/null 2>&1; then
	echo "FAIL: a binary without jemalloc passed"
	exit 1
fi
if sh "$dir/check-agent-allocator.sh" >/dev/null 2>&1; then
	echo "FAIL: no binary passed"
	exit 1
fi
echo "✓ check-agent-allocator: binaries with and without jemalloc told apart"
