#!/bin/sh
# End-to-end demo test (M1-33): builds server, UI and agent, signs the agent with a key of this run,
# starts deploy/e2e/compose.yaml and runs web/tests/demo with Playwright against it:
# setup → login → enrollment token → install.sh in agent containers with systemd → nodes with
# metrics in < 2 min → a user of another organization sees nothing.
#
#   scripts/e2e-demo.sh          run and tear down
#   KEEP=1 scripts/e2e-demo.sh   leave the environment running (https://localhost:18443)
#   SKIP_BUILD=1 …               reuse the binaries of the last run (deploy/e2e/.build)
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
e2e="$root/deploy/e2e"
build="$e2e/.build"
port=${SERMON_E2E_PORT:-18443}
export SERMON_E2E_PORT="$port"
compose() { docker compose -f "$e2e/compose.yaml" "$@"; }

case $(docker info --format '{{.Architecture}}') in
x86_64 | amd64) arch=amd64 target=x86_64-unknown-linux-musl ;;
aarch64 | arm64) arch=arm64 target=aarch64-unknown-linux-musl ;;
*) echo "e2e-demo: unsupported Docker architecture" >&2 && exit 1 ;;
esac

if [ "${SKIP_BUILD:-}" != 1 ] || [ ! -x "$build/sermon-server" ]; then
	rm -rf "$build"
	mkdir -p "$build/secrets" "$build/downloads"

	# Release key of this run only: the server embeds its public half, install.sh checks against it.
	"$root/scripts/release-sign.sh" generate "$build/release.pem" >"$build/release.pub"

	echo "▸ building the agent ($target)"
	(cd "$root/agent" && cargo zigbuild --release --locked -q -p sermon-agent --target "$target")
	cp "$root/agent/target/$target/release/sermon-agent" "$build/downloads/sermon-agent-linux-$arch"
	"$root/scripts/release-sign.sh" sign "$build/release.pem" "$build/downloads/sermon-agent-linux-$arch"

	echo "▸ building the web UI and the server (linux/$arch)"
	(cd "$root" && just build-web >/dev/null)
	(cd "$root/server" && CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath \
		-ldflags "-s -w -X main.version=e2e -X main.releaseKey=$(cat "$build/release.pub")" \
		-o "$build/sermon-server" ./cmd/sermon-server)

	# Database passwords of this run (never committed).
	for name in postgres_password postgres_app_password; do
		openssl rand -hex 24 | tr -d '\n' >"$build/secrets/$name"
	done
	printf 'postgres://sermon_app:%s@postgres:5432/sermon?sslmode=disable' "$(cat "$build/secrets/postgres_app_password")" \
		>"$build/secrets/database_url"
	printf 'postgres://sermon:%s@postgres:5432/sermon?sslmode=disable' "$(cat "$build/secrets/postgres_password")" \
		>"$build/secrets/database_migrate_url"
	chmod 0644 "$build/secrets/"* # read by the container users; the directory is local and temporary
fi

cleanup() {
	status=$?
	if [ "$status" -ne 0 ]; then compose logs --no-color server init >"$build/server.log" 2>&1 || true; fi
	if [ "${KEEP:-}" = 1 ]; then
		echo "▸ environment left running: https://localhost:$port (stop: docker compose -f deploy/e2e/compose.yaml down -v)"
	else
		compose down -v --remove-orphans >/dev/null 2>&1 || true
	fi
	exit "$status"
}
trap cleanup EXIT INT TERM

echo "▸ starting deploy/e2e/compose.yaml"
compose down -v --remove-orphans >/dev/null 2>&1 || true
compose up -d --build --quiet-pull
for _ in $(seq 1 90); do
	curl -fsSk "https://localhost:$port/readyz" >/dev/null 2>&1 && break
	sleep 1
done
curl -fsSk "https://localhost:$port/readyz" >/dev/null

echo "▸ running the demo test"
cd "$root/web"
E2E_BASE_URL="https://localhost:$port" E2E_COMPOSE_FILE="$e2e/compose.yaml" \
	pnpm exec playwright test --config playwright.demo.config.ts
