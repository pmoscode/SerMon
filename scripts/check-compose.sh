#!/bin/sh
# Checks a docker compose file against SerMon's rules (KONZEPT §18.6, ADR 0006):
#   - every image is pinned by digest (@sha256:…)
#   - TimescaleDB telemetry is off, Loki usage reporting is disabled
#   - with --loopback-only: every published port binds to 127.0.0.1
#   - with --hardened (production, M2-26): every container is read-only, drops all capabilities,
#     forbids new privileges, is not privileged and sees neither the Docker socket nor the host
#     network
# Several files are merged like `docker compose -f a -f b`; the result is checked.
# Usage: check-compose.sh [--loopback-only] [--hardened] <compose-file>...
set -eu

loopback_only=false
hardened=false
while :; do
  case ${1:-} in
  --loopback-only) loopback_only=true && shift ;;
  --hardened) hardened=true && shift ;;
  *) break ;;
  esac
done
[ $# -gt 0 ] || { echo "usage: check-compose.sh [--loopback-only] [--hardened] <compose-file>..." >&2; exit 2; }

files=
for file in "$@"; do files="$files -f $file"; done
# shellcheck disable=SC2086 # one -f per file; the paths of the repository hold no spaces
config=$(docker compose $files config --format json)

printf '%s' "$config" | python3 -I "$(dirname "$0")/check_compose.py" "$loopback_only" "$hardened" "$1"
