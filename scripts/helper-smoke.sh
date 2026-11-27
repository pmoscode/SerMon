#!/bin/sh
# Smoke test of the root helper's runs against real package managers (M4-03): in fresh containers
# of Debian, Ubuntu, Rocky, Fedora and Alpine with the package sources of the E2E environment
# (deploy/e2e/repos), `sermon-helper run` installs the security update only (apt, dnf) or all
# updates (apk) and stores its result.
#
#   sh scripts/helper-smoke.sh HELPER    HELPER: the static binary for Docker's architecture
set -eu

if [ $# -ne 1 ] || [ ! -x "$1" ]; then
	echo "usage: $0 HELPER" >&2
	exit 1
fi
helper=$(cd "$(dirname "$1")" && pwd)/$(basename "$1")
root=$(cd "$(dirname "$0")/.." && pwd)
run=sermon-helper-smoke-$$
work=$(mktemp -d)
failures=0
cleanup() {
	docker rm -f "$run-server" >/dev/null 2>&1 || true
	docker network rm "$run" >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 130' INT TERM HUP

image() { grep -o "\"$1@sha256:[0-9a-f]*\"" "$root/deploy/e2e/compose.yaml" | head -1 | tr -d '"'; }

"$root/scripts/package-test-keys.sh" "$work/keys" >/dev/null
sh "$root/deploy/e2e/repos/build.sh" "$work/keys" "$work/repos" >/dev/null
docker network create "$run" >/dev/null
docker run -d --name "$run-server" --network "$run" --network-alias packages.test -v "$work/repos:/srv:ro" \
	"$(grep -o 'caddy:[0-9.]*@sha256:[0-9a-f]*' "$root/deploy/compose/compose.yaml" | head -1)" \
	caddy file-server --root /srv --listen :8080 >/dev/null

id=0193f5a0-0000-7000-8000-000000000001
# smoke HOST SCOPE CHECK: switches to the sources, runs the helper and prints what CHECK says.
smoke() {
	docker run --rm --network "$run" -v "$root/deploy/e2e/repos/use.sh:/use.sh:ro" -v "$work/repos/keys:/keys:ro" \
		-v "$helper:/usr/bin/sermon-helper:ro" "$(image "$1")" sh -c "
			sh /use.sh http://packages.test:8080 /keys >/dev/null 2>&1 || { echo USE-FAILED; exit 0; }
			# As installed: the helper refuses to start without /etc/sermon (M4-30); no helper.toml
			# there means its defaults.
			mkdir -p -m 0755 /etc/sermon
			mkdir -p -m 0700 /var/lib/sermon-helper/runs/$id
			printf 'updates $2' >/var/lib/sermon-helper/runs/$id/request
			/usr/bin/sermon-helper run $id || echo RUN-FAILED
			[ -s /var/lib/sermon-helper/runs/$id/result ] && echo RESULT
			$3" 2>&1
}

fail() {
	echo "FAIL: $*"
	failures=$((failures + 1))
}

for host in debian:13 ubuntu:24.04; do
	out=$(smoke "$host" security "dpkg-query -W -f '\${Package} \${Version}\n' 'sermon-e2e-*'")
	echo "$out" | grep -q '^RESULT$' || fail "$host: no result: $out"
	echo "$out" | grep -q '^sermon-e2e-demo 2.0.0-1$' || fail "$host: the security update is missing: $out"
	echo "$out" | grep -q '^sermon-e2e-extra 1.0.0-1$' || fail "$host: the plain update was installed too: $out"
done
for host in rockylinux:9 fedora:44; do
	out=$(smoke "$host" security "rpm -q sermon-e2e-demo sermon-e2e-extra")
	echo "$out" | grep -q '^RESULT$' || fail "$host: no result: $out"
	echo "$out" | grep -q '^sermon-e2e-demo-2.0.0-1.noarch$' || fail "$host: the security update is missing: $out"
	echo "$out" | grep -q '^sermon-e2e-extra-1.0.0-1.noarch$' || fail "$host: the plain update was installed too: $out"
done
out=$(smoke alpine:3.22 all "apk info -v 2>/dev/null | grep sermon-e2e")
echo "$out" | grep -q '^RESULT$' || fail "alpine: no result: $out"
echo "$out" | grep -q '^sermon-e2e-demo-2.0.0-r1$' || fail "alpine: demo not upgraded: $out"
echo "$out" | grep -q '^sermon-e2e-extra-1.1.0-r1$' || fail "alpine: extra not upgraded: $out"

if [ "$failures" -gt 0 ]; then
	echo "$failures check(s) of the helper's runs failed"
	exit 1
fi
echo "✓ sermon-helper: security updates on Debian, Ubuntu, Rocky and Fedora, all updates on Alpine"
