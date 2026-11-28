#!/bin/sh
# Resource budget of the agent (M2-25, KONZEPT §4.1): measures the running `sermon-agent run` and
# all its descendants – `journalctl` of the journal source counts, the agent's own metrics don't see
# it – and fails if they use more than the budget.
#
#   agent-budget.sh SECONDS MAX_CPU_PERCENT MAX_RSS_MIB [agent|helper]
#
# With `helper` it measures the root helper (`sermon-helper serve`, M4-29) on its own, as it is no
# descendant of the agent; apt, dnf and apk of a listing count as its children. Under systemd the
# helper is socket-activated and ends a minute after its last request: every process that runs in
# the window counts with what it used in it (sampled every second; a process ending loses at most
# its last second), none at all is a helper at rest.
#
# CPU is the time of the process tree over the window in percent of one core (reaped children
# included). Memory is sampled every second: the budget holds for the peak of the tree's private
# memory (RssAnon + RssShmem); the line also names the peak of VmRSS (M4-29). The rest of VmRSS
# are mapped files – the binaries and the journal files journalctl maps, about 12 MiB in the demo:
# page cache shared with other processes that the kernel can drop. Runs as root inside the host,
# reads /proc only (systemd and OpenRC alike) and needs nothing but a POSIX shell and awk. The E2E
# demo feeds it over stdin (`sh -s -- 60 1 30`).
set -eu
# Numbers with a decimal point whatever the host speaks: awk writes 8,0 under de_DE and printf
# can't read it back (seen in the test operation, V1-18).
export LC_ALL=C

usage() {
	echo "usage: agent-budget.sh SECONDS MAX_CPU_PERCENT MAX_RSS_MIB [agent|helper]" >&2
	exit 2
}
[ $# -eq 3 ] || [ $# -eq 4 ] || usage
seconds=$1 max_cpu=$2 max_rss=$3 program=${4:-agent}
case $program in agent | helper) ;; *) usage ;; esac

# program_pid: the one running `sermon-agent run` or `sermon-helper serve`.
program_pid() {
	found=
	for dir in /proc/[0-9]*; do
		# A process may end meanwhile: the shell's own message about the redirect stays quiet too.
		args=$({ tr '\0' ' ' <"$dir/cmdline"; } 2>/dev/null) || continue
		case $program:$args in
		agent:*/sermon-agent\ run\ * | agent:*/sermon-agent\ run) found="$found ${dir#/proc/}" ;;
		helper:*/sermon-helper\ serve\ * | helper:*/sermon-helper\ serve) found="$found ${dir#/proc/}" ;;
		esac
	done
	# shellcheck disable=SC2086 # split into the PIDs
	set -- $found
	[ "$program" = helper ] || [ $# -eq 1 ] || { echo "$program-budget: expected one running $program, found $#" >&2; exit 2; }
	echo "$@"
}

# tree PID: PID and its descendants, from the parent field of /proc/*/stat. The command name in
# parentheses may hold spaces, so the fields are counted after the last ')'.
tree() {
	for stat in /proc/[0-9]*/stat; do
		line=$(cat "$stat" 2>/dev/null) || continue
		pid=${line%% *}
		rest=${line##*) }
		echo "$pid ${rest#* }"
	done | awk -v root="$1" '
		{ parent[$1] = $2 }
		END {
			for (p in parent) {
				q = p
				while (q != "" && q != root && q != 0 && q != 1) q = parent[q]
				if (q == root) print p
			}
		}'
}

# ticks PID: CPU ticks of the tree; the agent's own field includes its reaped children
# (cutime, cstime), the living descendants count with their own time.
ticks() {
	top=$1
	for pid in $(tree "$top"); do
		line=$(cat "/proc/$pid/stat" 2>/dev/null) || continue
		# shellcheck disable=SC2086 # split into the fields
		set -- ${line##*) }
		# After the name: state ppid … utime=12 stime=13 cutime=14 cstime=15 (fields 14–17 of stat).
		if [ "$pid" = "$top" ]; then
			echo $((${12} + ${13} + ${14} + ${15}))
		else
			echo $((${12} + ${13}))
		fi
	done | awk '{ s += $1 } END { print s + 0 }'
}

# mem PID: "private rss root" of the tree in KiB – private memory (RssAnon + RssShmem) and
# VmRSS summed over the tree, and the private memory of PID alone.
mem() {
	for pid in $(tree "$1"); do
		awk -v top="$([ "$pid" = "$1" ] && echo 1 || echo 0)" '
			/^VmRSS:/ { rss = $2 } /^RssAnon:/ || /^RssShmem:/ { private += $2 }
			END { print private + 0, rss + 0, top * private }' "/proc/$pid/status" 2>/dev/null || true
	done | awk '{ p += $1; r += $2; t += $3 } END { print p + 0, r + 0, t + 0 }'
}

# sample PRIVATE RSS ROOT: keeps the peaks (of the private memory with what made it up).
sample() {
	if [ "$1" -gt "$peak" ]; then peak=$1 peak_root=$3; fi
	if [ "$2" -gt "$peak_rss" ]; then peak_rss=$2; fi
}

uptime_cs() { awk '{ split($1, t, "."); print t[1] * 100 + t[2] }' /proc/uptime; }

hz=$(getconf CLK_TCK 2>/dev/null || echo 100)
peak=0 peak_root=0 peak_rss=0
processes=0
if [ "$program" = agent ]; then
	root=$(program_pid)
	start_ticks=$(ticks "$root")
	start=$(uptime_cs)
	i=0
	while [ "$i" -lt "$seconds" ]; do
		sleep 1
		[ -d "/proc/$root" ] || { echo "agent-budget: the agent ($root) stopped during the measurement" >&2; exit 2; }
		# shellcheck disable=SC2046 # the three numbers
		sample $(mem "$root")
		n=$(tree "$root" | wc -l)
		[ "$n" -gt "$processes" ] && processes=$n
		i=$((i + 1))
	done
	end_ticks=$(ticks "$root")
	end=$(uptime_cs)
else
	# Samples "phase pid ticks": per helper process the highest value counts, less what it had
	# already used at the start.
	samples=$(mktemp)
	trap 'rm -f "$samples"' EXIT
	for root in $(program_pid); do echo "start $root $(ticks "$root")" >>"$samples"; done
	start=$(uptime_cs)
	i=0
	while [ "$i" -lt "$seconds" ]; do
		sleep 1
		private=0 rss=0 top=0
		n=0
		for root in $(program_pid); do
			echo "run $root $(ticks "$root")" >>"$samples"
			# shellcheck disable=SC2046 # the three numbers
			set -- $(mem "$root")
			private=$((private + $1)) rss=$((rss + $2)) top=$((top + $3))
			n=$((n + $(tree "$root" | wc -l)))
		done
		sample "$private" "$rss" "$top"
		[ "$n" -gt "$processes" ] && processes=$n
		i=$((i + 1))
	done
	end=$(uptime_cs)
	start_ticks=0
	end_ticks=$(awk '
		$1 == "start" { first[$2] = $3 }
		{ if ($3 > top[$2]) top[$2] = $3; seen[$2] = 1 }
		END { for (p in seen) s += top[p] - first[p]; print s + 0 }' "$samples")
fi

# One line for the log, then the verdict.
awk -v t="$((end_ticks - start_ticks))" -v hz="$hz" -v cs="$((end - start))" -v peak="$peak" \
	-v root="$peak_root" -v rss="$peak_rss" -v procs="$processes" -v max_cpu="$max_cpu" -v max_rss="$max_rss" \
	-v name="$program-budget" -v program="$program" 'BEGIN {
	cpu = t / hz / (cs / 100) * 100
	mib = peak / 1024
	printf "%s: cpu %.2f %% of a core over %.0f s, private memory peak %.1f MiB (%s %.1f, children %.1f), rss peak %.1f MiB, %d processes (budget: < %s %%, < %s MiB)\n", name, cpu, cs / 100, mib, program, root / 1024, (peak - root) / 1024, rss / 1024, procs, max_cpu, max_rss
	failed = 0
	if (cpu >= max_cpu) { printf "%s: CPU over budget: %.2f %% >= %s %%\n", name, cpu, max_cpu; failed = 1 }
	if (mib >= max_rss) { printf "%s: memory over budget: %.1f MiB >= %s MiB\n", name, mib, max_rss; failed = 1 }
	exit failed
}'
