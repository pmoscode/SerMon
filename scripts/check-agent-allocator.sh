#!/bin/sh
# Checks that release binaries of the agent carry jemalloc with the settings of
# agent/.cargo/config.toml (M4-32, ADR 0038): an archiver that leaves the objects out would link
# musl's allocator unnoticed.
#
#   check-agent-allocator.sh BINARY...
set -eu

[ $# -ge 1 ] || { echo "usage: check-agent-allocator.sh BINARY..." >&2; exit 2; }
conf=$(sed -n 's/^JEMALLOC_SYS_WITH_MALLOC_CONF = "\(.*\)"$/\1/p' "$(dirname "$0")/../agent/.cargo/config.toml")
[ -n "$conf" ] || { echo "check-agent-allocator: no JEMALLOC_SYS_WITH_MALLOC_CONF in agent/.cargo/config.toml" >&2; exit 2; }
for binary in "$@"; do
	if ! LC_ALL=C grep -aqF "$conf" "$binary"; then
		echo "check-agent-allocator: $binary has no jemalloc with $conf" >&2
		exit 1
	fi
done
echo "✓ $# agent binaries with jemalloc ($conf)"
