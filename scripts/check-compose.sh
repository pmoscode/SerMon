#!/bin/sh
# Checks a docker compose file against SerMon's rules (KONZEPT §18.6, ADR 0006):
#   - every image is pinned by digest (@sha256:…)
#   - TimescaleDB telemetry is off, Loki usage reporting is disabled
#   - with --loopback-only: every published port binds to 127.0.0.1
# Usage: check-compose.sh [--loopback-only] <compose-file>
set -eu

loopback_only=false
if [ "${1:-}" = "--loopback-only" ]; then
  loopback_only=true
  shift
fi
file=${1:?usage: check-compose.sh [--loopback-only] <compose-file>}

config=$(docker compose -f "$file" config --format json)

printf '%s' "$config" | python3 -I "$(dirname "$0")/check_compose.py" "$loopback_only" "$file"
