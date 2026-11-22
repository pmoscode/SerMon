#!/bin/sh
# The scripts in single quotes run inside the containers, expansions happen there.
# shellcheck disable=SC2016
# Tests of the agent packages (M2-21) without a server: build .deb, .rpm and .apk for the Docker
# architecture with keys of this run, then per distribution in a container: the signature is
# checked (and a package without the key is refused), the package installs the agent, the user
# sermon and the rights, an upgrade keeps the identity, and the removal leaves nothing but the
# configuration (and the user).
# Running as a service against a server: the E2E demo (web/tests/demo).
#
#   scripts/test-packages.sh BINARY   (static agent binary of the Docker architecture)
set -eu

[ $# -eq 1 ] || {
	echo "usage: $0 BINARY" >&2
	exit 1
}
root=$(cd "$(dirname "$0")/.." && pwd)
binary=$1
case $(docker info --format '{{.Architecture}}') in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) echo "test-packages: unsupported Docker architecture" >&2 && exit 1 ;;
esac

work="$root/dist/package-test"
rm -rf "$work"
mkdir -p "$work"
trap 'rm -rf "$work"' EXIT
"$root/scripts/package-test-keys.sh" "$work/keys"
# Two versions: an upgrade must keep the identity (deb and rpm run the removal scripts then, too).
for version in 0.0.1 0.0.2; do
	SERMON_PKG_GPG_KEY="$work/keys/gpg.asc" SERMON_PKG_APK_KEY="$work/keys/apk.rsa" \
		"$root/scripts/package-agent.sh" "$arch" "$version" "$binary" "$work/out/$version"
done
# Only the public keys go into the containers.
mkdir -p "$work/pub"
cp "$work/keys/gpg.pub.asc" "$work/keys/sermon-agent.rsa.pub" "$work/pub/"

# Shared checks after installation and after removal, run inside each container.
checks='
installed() {
	id sermon | grep -q "(sermon)" || { echo "no user sermon"; exit 1; }
	[ "$(stat -c "%U:%G %a" /etc/sermon/agent.toml)" = "root:sermon 640" ] || { echo "rights of agent.toml"; exit 1; }
	[ "$(stat -c "%U:%G %a" /var/lib/sermon-agent)" = "sermon:sermon 750" ] || { echo "rights of the state"; exit 1; }
	/usr/bin/sermon-agent --version | grep -q sermon-agent || { echo "binary does not run"; exit 1; }
	[ -s /etc/machine-id ] || [ -s /var/lib/dbus/machine-id ] || { echo "no machine-id"; exit 1; }
	! grep -rq /usr/local/bin "$1" || { echo "$1 names /usr/local/bin"; exit 1; }
}
enroll_fake() {
	mkdir -p /var/lib/sermon-agent/identity && echo key >/var/lib/sermon-agent/identity/agent.key
}
kept() {
	[ -f /var/lib/sermon-agent/identity/agent.key ] || { echo "the upgrade removed the identity"; exit 1; }
	[ -x /usr/bin/sermon-agent ] || { echo "the upgrade removed the binary"; exit 1; }
}
removed() {
	for path in /usr/bin/sermon-agent /var/lib/sermon-agent "$1"; do
		[ ! -e "$path" ] || { echo "left behind: $path"; exit 1; }
	done
}
'
run() {
	name=$1 image=$2 script=$3
	if docker run --rm -v "$work/out:/p:ro" -v "$work/pub:/k:ro" "$image" sh -euc "$checks$script"; then
		echo "✓ $name"
	else
		echo "✗ $name" >&2
		exit 1
	fi
}

run debian-13 debian:13@sha256:9c6e381421d0f48d1b8133e573f33c185ee16cbf505eda9c4a8a4791b25ea8a6 '
apt-get update -qq >/dev/null
apt-get install -y -qq --no-install-recommends gpg gpg-agent binutils >/dev/null 2>&1
cd /tmp && ar x /p/0.0.1/*.deb && cat debian-binary control.tar.* data.tar.* >signed
export GNUPGHOME=/tmp/gnupg && mkdir -m 0700 "$GNUPGHOME"
! gpg --verify _gpgorigin signed 2>/dev/null || { echo "verified without the key"; exit 1; }
gpg --quiet --import /k/gpg.pub.asc 2>/dev/null
gpg --verify _gpgorigin signed 2>/dev/null || { echo "bad signature"; exit 1; }
echo x >>signed
! gpg --verify _gpgorigin signed 2>/dev/null || { echo "a changed package verifies"; exit 1; }
apt-get install -y -qq /p/0.0.1/*.deb >/dev/null 2>&1
installed /usr/lib/systemd/system/sermon-agent.service
enroll_fake
apt-get install -y -qq /p/0.0.2/*.deb >/dev/null 2>&1
kept
apt-get remove -y -qq sermon-agent >/dev/null 2>&1
removed /usr/lib/systemd/system/sermon-agent.service
[ -f /etc/sermon/agent.toml ] || { echo "the configuration went (deb keeps it until purge)"; exit 1; }'

run rockylinux-9 rockylinux:9@sha256:03303863808e9cd79f0c58c833db3e5f9cde9f2abe16804a267b05dc54af86e4 '
! rpm -K /p/0.0.1/*.rpm >/dev/null 2>&1 || { echo "verified without the key"; exit 1; }
rpm --import /k/gpg.pub.asc
rpm -K /p/0.0.1/*.rpm | grep -q "digests signatures OK" || { echo "bad signature"; exit 1; }
dnf install -y -q /p/0.0.1/*.rpm >/dev/null
installed /usr/lib/systemd/system/sermon-agent.service
enroll_fake
dnf install -y -q /p/0.0.2/*.rpm >/dev/null
kept
dnf remove -y -q sermon-agent >/dev/null
removed /usr/lib/systemd/system/sermon-agent.service'

run alpine-3.22 alpine:3.22@sha256:49340b346798875b5592e5d420241f2f9cfba5f47e62ebb1446a1b34734e898f '
! apk add -q /p/0.0.1/*.apk 2>/dev/null || { echo "installed without the key"; exit 1; }
cp /k/sermon-agent.rsa.pub /etc/apk/keys/
apk add -q /p/0.0.1/*.apk >/dev/null
installed /etc/init.d/sermon-agent
enroll_fake
apk add -q /p/0.0.2/*.apk >/dev/null
kept
apk del -q sermon-agent
removed /etc/init.d/sermon-agent'
