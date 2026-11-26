#!/bin/sh
# Tests for agent-budget.sh (M4-29) against fake processes: a busy "sermon-agent run" breaks a
# CPU budget, an idle "sermon-helper serve" an RSS budget of 0, and both pass generous ones.
# Needs /proc; elsewhere (macOS) it runs itself in a container. Run: sh scripts/agent-budget_test.sh
set -eu

dir=$(cd "$(dirname "$0")" && pwd)
if [ ! -r /proc/self/stat ]; then
	exec docker run --rm -v "$dir:/scripts:ro" \
		alpine:3.22@sha256:49340b346798875b5592e5d420241f2f9cfba5f47e62ebb1446a1b34734e898f \
		sh /scripts/agent-budget_test.sh
fi

work=$(mktemp -d)
pids=
cleanup() {
	# shellcheck disable=SC2086 # the PIDs
	[ -z "$pids" ] || kill $pids 2>/dev/null || true
	rm -rf "$work"
}
trap cleanup EXIT
fail() {
	echo "FAIL: $*"
	exit 1
}

printf '#!/bin/sh\nwhile :; do :; done\n' >"$work/sermon-agent"
printf '#!/bin/sh\nwhile sleep 1; do :; done\n' >"$work/sermon-helper"
chmod 0755 "$work/sermon-agent" "$work/sermon-helper"

# Without the program: nothing to measure.
if out=$(sh "$dir/agent-budget.sh" 1 1 30 2>&1); then fail "measured without an agent: $out"; fi
case $out in *"expected one running agent"*) ;; *) fail "no agent: $out" ;; esac

"$work/sermon-agent" run --config /dev/null &
pids="$pids $!"
"$work/sermon-helper" serve &
pids="$pids $!"
sleep 1

# A busy agent breaks 1 % of a core, passes 1000 %.
if out=$(sh "$dir/agent-budget.sh" 2 1 30 2>&1); then fail "a busy agent passed 1 %: $out"; fi
case $out in *"CPU over budget"*) ;; *) fail "busy agent: $out" ;; esac
out=$(sh "$dir/agent-budget.sh" 2 1000 30) || fail "a generous budget failed: $out"
case $out in "agent-budget: cpu "*) ;; *) fail "agent line: $out" ;; esac

# The helper is measured on its own: its memory breaks 0 MiB, passes 30.
if out=$(sh "$dir/agent-budget.sh" 2 100 0 helper 2>&1); then fail "the helper passed 0 MiB: $out"; fi
case $out in *"helper-budget: memory over budget"*) ;; *) fail "helper memory: $out" ;; esac
out=$(sh "$dir/agent-budget.sh" 2 100 30 helper) || fail "the idle helper failed: $out"
case $out in "helper-budget: cpu "*) ;; *) fail "helper line: $out" ;; esac

if sh "$dir/agent-budget.sh" 2 1 30 other >/dev/null 2>&1; then fail "an unknown program was measured"; fi

# Socket-activated (M4-29): a helper that starts and ends within the window counts with what it
# used; none at all is a helper at rest.
kill "$(pgrep -f 'sermon-helper serve')"
out=$(sh "$dir/agent-budget.sh" 2 100 30 helper) || fail "a helper at rest failed: $out"
case $out in *"cpu 0.00 %"*", 0 processes"*) ;; *) fail "helper at rest: $out" ;; esac
# shellcheck disable=SC2016 # the fake's own code
printf '#!/bin/sh\ni=0; while [ $i -lt 2000000 ]; do i=$((i + 1)); done\n' >"$work/sermon-helper"
(sleep 1 && "$work/sermon-helper" serve) &
if out=$(sh "$dir/agent-budget.sh" 4 1 30 helper 2>&1); then fail "a busy helper that came and went passed 1 %: $out"; fi
case $out in *"CPU over budget"*) ;; *) fail "helper that came and went: $out" ;; esac

# Private memory of the agent's children counts (journalctl): a child holding 20 MiB (sort
# waiting for the end of its input) breaks a budget of 10 and the line names it (M4-29).
kill "$(pgrep -f 'sermon-agent run')"
# shellcheck disable=SC2016 # the fake's own code
printf '#!/bin/sh\n(head -c 20000000 /dev/zero | tr "\\000" x | fold -w 1000; sleep 60) | sort >/dev/null &\nwhile sleep 1; do :; done\n' >"$work/sermon-agent"
"$work/sermon-agent" run --config /dev/null &
pids="$pids $!"
sleep 3
if out=$(sh "$dir/agent-budget.sh" 2 100 10 2>&1); then fail "20 MiB of a child passed 10 MiB: $out"; fi
case $out in *"memory over budget"*) ;; *) fail "child memory: $out" ;; esac
case $out in *"(agent 0."*", children 1"[0-9].*", rss peak "*) ;; *) fail "split: $out" ;; esac
out=$(sh "$dir/agent-budget.sh" 2 100 100) || fail "100 MiB failed: $out"
echo "✓ agent-budget: agent and helper measured, budgets enforced"
