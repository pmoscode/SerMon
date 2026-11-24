#!/bin/sh
# Tests the package sources of the E2E environment (M4-08): build.sh builds them with the keys of
# this run, a file server serves them, and fresh containers of the target distros switch to them
# with use.sh. Each must see exactly the expected updates: sermon-e2e-demo 2.0.0 as a security update
# (not on Alpine, which has no such metadata), sermon-e2e-extra 1.1.0 as a plain one. On Debian and
# Ubuntu the upgrade asks for a reboot. Sources signed with another key are refused.
#
#   sh deploy/e2e/repos/test.sh    (just test-e2e-repos; Docker required)
set -eu

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
run=sermon-e2e-repos-test-$$
work=$(mktemp -d)
failures=0

cleanup() {
	docker rm -f "$run-server" >/dev/null 2>&1 || true
	docker network rm "$run" >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 130' INT TERM HUP

fail() {
	echo "FAIL: $*"
	failures=$((failures + 1))
}

# image NAME: the pinned base image of a host in the E2E compose, so the test sees what the hosts see.
image() {
	grep -o "\"$1@sha256:[0-9a-f]*\"" "$root/deploy/e2e/compose.yaml" | head -1 | tr -d '"'
}

"$root/scripts/package-test-keys.sh" "$work/keys" >/dev/null
"$root/scripts/package-test-keys.sh" "$work/other" >/dev/null
mkdir "$work/other-public"
cp "$work/other/gpg.pub.asc" "$work/other/sermon-agent.rsa.pub" "$work/other-public/"
sh "$here/build.sh" "$work/keys" "$work/repos" >/dev/null

docker network create "$run" >/dev/null
docker run -d --name "$run-server" --network "$run" --network-alias packages.test \
	-v "$work/repos:/srv:ro" "$(grep -o 'caddy:[0-9.]*@sha256:[0-9a-f]*' "$root/deploy/compose/compose.yaml" | head -1)" \
	caddy file-server --root /srv --listen :8080 >/dev/null

# client NAME KEYS SCRIPT: runs SCRIPT after use.sh in a fresh container of NAME; prints its output.
client() {
	docker run --rm --network "$run" -v "$here/use.sh:/use.sh:ro" -v "$2:/keys:ro" "$(image "$1")" \
		sh -c "sh /use.sh http://packages.test:8080 /keys >/dev/null 2>&1 || { echo USE-FAILED; exit 0; }; $3" 2>&1
}

# The checks run inside the containers: nothing expands here.
# shellcheck disable=SC2016
apt_checks='
	echo "upgradable:"; apt list --upgradable 2>/dev/null | grep -v "^Listing"
	DEBIAN_FRONTEND=noninteractive apt-get -y -qq upgrade >/dev/null 2>&1
	test -e /var/run/reboot-required && echo "reboot-required: $(cat /var/run/reboot-required.pkgs)"'
dnf_checks='
	dnf -q check-update >/tmp/updates 2>&1; echo "check-update: $?"; grep sermon-e2e /tmp/updates
	echo "security:"; dnf -q updateinfo list --security 2>/dev/null | grep sermon-e2e'
apk_checks='echo "upgradable:"; apk version -l "<" 2>/dev/null | grep sermon-e2e; grep -c "=" /etc/apk/world | sed "s/^/pinned: /"'

for host in debian:13 ubuntu:24.04; do
	out=$(client "$host" "$work/repos/keys" "$apt_checks")
	echo "$out" | grep -q '^sermon-e2e-demo/e2e-security 2.0.0-1 all \[upgradable from: 1.0.0-1\]' || fail "$host: demo 2.0.0 not a security update: $out"
	echo "$out" | grep -q '^sermon-e2e-extra/e2e-updates 1.1.0-1 all \[upgradable from: 1.0.0-1\]' || fail "$host: extra 1.1.0 missing: $out"
	[ "$(echo "$out" | grep -c '\[upgradable from')" = 2 ] || fail "$host: not exactly two updates: $out"
	echo "$out" | grep -q '^reboot-required: sermon-e2e-demo' || fail "$host: no reboot required after the upgrade: $out"
done
for host in rockylinux:9 fedora:44; do
	out=$(client "$host" "$work/repos/keys" "$dnf_checks")
	echo "$out" | grep -q '^check-update: 100' || fail "$host: check-update without updates: $out"
	echo "$out" | grep -q '^sermon-e2e-demo\.noarch  *2\.0\.0-1 ' || fail "$host: demo 2.0.0 missing: $out"
	echo "$out" | grep -q '^sermon-e2e-extra\.noarch  *1\.1\.0-1 ' || fail "$host: extra 1.1.0 missing: $out"
	sec=$(echo "$out" | sed -n '/^security:/,$p')
	echo "$sec" | grep -q 'sermon-e2e-demo-2\.0\.0-1' || fail "$host: demo 2.0.0 not a security update: $out"
	echo "$sec" | grep -q 'sermon-e2e-extra' && fail "$host: extra 1.1.0 counted as security: $out"
done
out=$(client alpine:3.22 "$work/repos/keys" "$apk_checks")
echo "$out" | grep -q '^sermon-e2e-demo-1\.0\.0-r1  *<  *2\.0\.0-r1' || fail "alpine: demo 2.0.0 missing: $out"
echo "$out" | grep -q '^sermon-e2e-extra-1\.0\.0-r1  *<  *1\.1\.0-r1' || fail "alpine: extra 1.1.0 missing: $out"
# Nothing pinned in /etc/apk/world: apk upgrade takes the updates (M4-03).
echo "$out" | grep -q '^pinned: 0$' || fail "alpine: versions pinned in /etc/apk/world: $out"

# Another key: every distro refuses the sources.
for host in debian:13 rockylinux:9 alpine:3.22; do
	out=$(client "$host" "$work/other-public" 'echo USED')
	[ "$out" = USE-FAILED ] || fail "$host: sources with another key were used: $out"
done

if [ "$failures" -gt 0 ]; then
	echo "$failures check(s) of the E2E package sources failed"
	exit 1
fi
echo "✓ E2E package sources: five distros see the expected updates, another key is refused"
