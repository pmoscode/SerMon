#!/bin/sh
# Tests for check-commit-msg.sh. Run: sh scripts/check-commit-msg_test.sh
set -eu

script_dir=$(cd "$(dirname "$0")" && pwd)
checker="$script_dir/check-commit-msg.sh"
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
failures=0
total=0

# expect <pass|fail> <message>
expect() {
  expected=$1
  printf '%s\n' "$2" >"$tmp"
  if sh "$checker" "$tmp" 2>/dev/null; then actual=pass; else actual=fail; fi
  total=$((total + 1))
  if [ "$actual" != "$expected" ]; then
    echo "FAIL: expected $expected, got $actual for: '$2'"
    failures=$((failures + 1))
  fi
}

long_subject=$(printf 'x%.0s' $(seq 1 101))

# valid
expect pass "feat: add agent CA"
expect pass "fix(agent): handle counter wrap"
expect pass "feat(server)!: drop v0 API"
expect pass "docs: update concept"
expect pass "ci(github): pin actions by SHA"
expect pass "chore(deps): bump pgx to v5.8.0"
expect pass "Merge branch 'main' into m0-02-rust"
expect pass "Revert \"feat: add agent CA\""
expect pass "fixup! feat: add agent CA"

# invalid
expect fail "Add stuff"
expect fail "feat:missing space"
expect fail "feat: "
expect fail "feature: wrong type"
expect fail "feat(Agent): uppercase scope"
expect fail "feat: $long_subject"
expect fail ""

if [ "$failures" -ne 0 ]; then
  echo "$failures of $total commit-msg tests failed"
  exit 1
fi
echo "all $total commit-msg tests passed"
