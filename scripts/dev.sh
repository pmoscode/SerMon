#!/usr/bin/env bash
# Development environment (`just dev`): infrastructure containers, sermon-server with hot
# reload (air) and the Vite dev server. Ctrl+C stops server and Vite; containers keep running
# (`just dev-down` stops them).
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
compose=(docker compose -f "$root/deploy/dev/compose.yaml")

if ! docker info >/dev/null 2>&1; then
  echo "✗ Docker is not running – start Docker Desktop (or the docker daemon) first." >&2
  exit 1
fi

"$root/scripts/dev-secrets.sh"
echo "▶ starting infrastructure (postgres/timescaledb, loki, mailpit, ntfy) …"
"${compose[@]}" up -d --wait

if [ ! -d "$root/web/node_modules" ]; then
  (cd "$root/web" && pnpm install --frozen-lockfile)
fi

pids=()
cleanup() {
  trap - EXIT INT TERM
  for pid in "${pids[@]}"; do kill "$pid" 2>/dev/null || true; done
  wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

(
  cd "$root/server"
  export SERMON_HTTP_ADDR=127.0.0.1:8080 SERMON_LOG_FORMAT=text SERMON_LOG_LEVEL=debug
  exec go tool air
) &
pids+=($!)

(cd "$root/web" && exec pnpm exec vite dev) &
pids+=($!)

cat <<INFO

  SerMon development environment
  ─────────────────────────────────────────────
  Web UI (Vite, hot reload)   http://127.0.0.1:5173
  sermon-server (air)         http://127.0.0.1:8080   (/healthz, /readyz)
  PostgreSQL/TimescaleDB      127.0.0.1:55432  user sermon (secret file: deploy/dev/.secrets/)
  Loki                        http://127.0.0.1:3100   (header X-Scope-OrgID required)
  Mailpit (SMTP 1025)         http://127.0.0.1:8025
  ntfy                        http://127.0.0.1:8090
  Ctrl+C stops server and Vite – containers keep running (just dev-down).

INFO

wait -n "${pids[@]}"
