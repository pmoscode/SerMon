#!/bin/sh
# Tests for check-compose.sh. Run: sh scripts/check-compose_test.sh (requires docker compose).
set -eu

dir=$(cd "$(dirname "$0")" && pwd)
data="$dir/testdata/compose"
failures=0
total=0

# expect <pass|fail> <description> <args...>
expect() {
  expected=$1 description=$2
  shift 2
  if sh "$dir/check-compose.sh" "$@" >/dev/null 2>&1; then actual=pass; else actual=fail; fi
  total=$((total + 1))
  if [ "$actual" != "$expected" ]; then
    echo "FAIL: $description – expected $expected, got $actual"
    failures=$((failures + 1))
  fi
}

expect pass "pinned images, loopback ports, telemetry off" --loopback-only "$data/good.yaml"
expect fail "image without digest" "$data/unpinned.yaml"
expect fail "port bound to all interfaces with --loopback-only" --loopback-only "$data/public-port.yaml"
expect pass "public port allowed without --loopback-only" "$data/public-port.yaml"
expect fail "TimescaleDB telemetry not disabled" "$data/timescale-telemetry.yaml"
expect fail "Loki usage reporting not disabled" "$data/loki-telemetry.yaml"
expect pass "ports with variables and loopback defaults" --loopback-only "$data/variable-port.yaml"
expect fail "missing file" "$data/does-not-exist.yaml"

if [ "$failures" -ne 0 ]; then
  echo "$failures of $total compose checks failed"
  exit 1
fi
echo "all $total compose checks passed"
