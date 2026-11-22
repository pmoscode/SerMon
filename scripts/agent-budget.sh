#!/bin/sh
# Resource budget of the agent (M2-25, KONZEPT §4.1): measures the running `sermon-agent run` and
# all its descendants – `journalctl` of the journal source counts, the agent's own metrics don't see
# it – and fails if they use more than the budget.
#
#   agent-budget.sh SECONDS MAX_CPU_PERCENT MAX_RSS_MIB
#
# CPU is the time of the process tree over the window in percent of one core (reaped children
# included); RSS is the peak of the sum of VmRSS, sampled every second. Runs as root inside the host,
# reads /proc only (systemd and OpenRC alike) and needs nothing but a POSIX shell and awk. The E2E
# demo feeds it over stdin (`sh -s -- 60 1 30`).
set -eu

[ $# -eq 3 ] || { echo "usage: agent-budget.sh SECONDS MAX_CPU_PERCENT MAX_RSS_MIB" >&2; exit 2; }
seconds=$1 max_cpu=$2 max_rss=$3

agent_pid() {
	found=
	for dir in /proc/[0-9]*; do
		args=$(tr '\0' ' ' <"$dir/cmdline" 2>/dev/null) || continue
		case $args in
		*/sermon-agent\ run\ * | */sermon-agent\ run) found="$found ${dir#/proc/}" ;;
		esac
	done
	# shellcheck disable=SC2086 # split into the PIDs
	set -- $found
	[ $# -eq 1 ] || { echo "agent-budget: expected one running agent, found $#" >&2; exit 2; }
	echo "$1"
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

# rss PID: the summed VmRSS of the tree in KiB.
rss() {
	for pid in $(tree "$1"); do
		awk '/^VmRSS:/ { print $2 }' "/proc/$pid/status" 2>/dev/null || true
	done | awk '{ s += $1 } END { print s + 0 }'
}

uptime_cs() { awk '{ split($1, t, "."); print t[1] * 100 + t[2] }' /proc/uptime; }

root=$(agent_pid)
hz=$(getconf CLK_TCK 2>/dev/null || echo 100)
start_ticks=$(ticks "$root")
start=$(uptime_cs)
peak=0
processes=0
i=0
while [ "$i" -lt "$seconds" ]; do
	sleep 1
	[ -d "/proc/$root" ] || { echo "agent-budget: the agent ($root) stopped during the measurement" >&2; exit 2; }
	now=$(rss "$root")
	[ "$now" -gt "$peak" ] && peak=$now
	n=$(tree "$root" | wc -l)
	[ "$n" -gt "$processes" ] && processes=$n
	i=$((i + 1))
done
end_ticks=$(ticks "$root")
end=$(uptime_cs)

# One line for the log, then the verdict.
awk -v t="$((end_ticks - start_ticks))" -v hz="$hz" -v cs="$((end - start))" -v peak="$peak" \
	-v procs="$processes" -v max_cpu="$max_cpu" -v max_rss="$max_rss" 'BEGIN {
	cpu = t / hz / (cs / 100) * 100
	mib = peak / 1024
	printf "agent-budget: cpu %.2f %% of a core over %.0f s, rss peak %.1f MiB, %d processes (budget: < %s %%, < %s MiB)\n", cpu, cs / 100, mib, procs, max_cpu, max_rss
	failed = 0
	if (cpu >= max_cpu) { printf "agent-budget: CPU over budget: %.2f %% >= %s %%\n", cpu, max_cpu; failed = 1 }
	if (mib >= max_rss) { printf "agent-budget: RSS over budget: %.1f MiB >= %s MiB\n", mib, max_rss; failed = 1 }
	exit failed
}'
