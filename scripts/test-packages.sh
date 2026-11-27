#!/bin/sh
# The scripts in single quotes run inside the containers, expansions happen there.
# shellcheck disable=SC2016
# Tests of the packages of agent (M2-21) and root helper (M4-04) without a server: build .deb, .rpm
# and .apk for the Docker architecture with keys of this run, then per distribution in a
# container: the signature is checked (and a package without the key is refused), the packages
# install agent and helper, the user sermon and the rights, an upgrade keeps the identity and a
# changed helper.toml, and the removal leaves nothing but the configuration (and the user).
# Then a rotation of the repository keys through three releases (M4-31): install.sh --repo sets a
# host up from the first, the second announces the next keys, the third is signed with them only –
# the package manager updates the host through all three, and the old keys go.
# Under OpenRC the agent runs as a service once and its output must reach syslog (V1-20).
# Running as a service against a server: the E2E demo (web/tests/demo).
#
#   scripts/test-packages.sh AGENT HELPER   (static binaries of the Docker architecture)
set -eu

[ $# -eq 2 ] || {
	echo "usage: $0 AGENT HELPER" >&2
	exit 1
}
root=$(cd "$(dirname "$0")/.." && pwd)
binary=$1 helper=$2
case $(docker info --format '{{.Architecture}}') in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) echo "test-packages: unsupported Docker architecture" >&2 && exit 1 ;;
esac

work="$root/dist/package-test"
rm -rf "$work"
mkdir -p "$work"
trap 'rm -rf "$work"' EXIT
release_key=$("$root/scripts/release-sign.sh" generate "$work/release.pem")
# The keys of the releases (current) and those they rotate to (next); rotation = current with the
# public keys of next in next/, as in a release that announces them.
"$root/scripts/package-test-keys.sh" "$work/keys"
"$root/scripts/package-test-keys.sh" "$work/next"
cp -R "$work/keys" "$work/rotation"
mkdir "$work/rotation/next"
cp "$work/next/gpg.pub.asc" "$work/next/apk.rsa.pub" "$work/rotation/next/"
# package VERSION SIGNING-KEYS PUBLIC-KEYS
package() {
	SERMON_PKG_GPG_KEY="$2/gpg.asc" SERMON_PKG_APK_KEY="$2/apk.rsa" SERMON_PKG_PUBLIC_KEYS="$3" \
		"$root/scripts/package-agent.sh" "$arch" "$1" "$binary" "$helper" "$work/out/$1"
}
# Two versions with the same key: an upgrade must keep the identity (deb and rpm run the removal
# scripts then, too). 0.0.2 announces the next keys, 0.0.3 is signed with them only.
package 0.0.1 "$work/keys" "$work/keys"
package 0.0.2 "$work/keys" "$work/rotation"
package 0.0.3 "$work/next" "$work/next"
for release in 1 2 3; do
	case $release in 1) keys=$work/keys ;; 2) keys=$work/rotation ;; 3) keys=$work/next ;; esac
	"$root/scripts/build-repos.sh" "$work/out/0.0.$release" "$keys" "$work/repos/$release" >/dev/null
	"$root/scripts/release-sign.sh" sign "$work/release.pem" --version "0.0.$release" "$work/repos/$release/keys/SHA256SUMS"
done
# install.sh as the server of release 1 renders it: its release key and version.
sed -e "s#@SERMON_RELEASE_KEY@#$release_key#" -e "s#@SERMON_AGENT_VERSION@#0.0.1#" "$root/packaging/install.sh" >"$work/install.sh"
# Only the public keys go into the containers, named as for the package managers.
"$root/scripts/repo-keys.sh" publish "$work/keys" "$work/pub"
old_apk_key=$("$root/scripts/repo-keys.sh" apk-name "$work/keys/apk.rsa.pub")

# Shared checks after installation and after removal, run inside each container.
checks='
installed() {
	id sermon | grep -q "(sermon)" || { echo "no user sermon"; exit 1; }
	[ "$(stat -c "%U:%G %a" /etc/sermon/agent.toml)" = "root:sermon 640" ] || { echo "rights of agent.toml"; exit 1; }
	[ "$(stat -c "%U:%G %a" /var/lib/sermon-agent)" = "sermon:sermon 750" ] || { echo "rights of the state"; exit 1; }
	/usr/bin/sermon-agent --version | grep -q sermon-agent || { echo "binary does not run"; exit 1; }
	[ -s /etc/machine-id ] || [ -s /var/lib/dbus/machine-id ] || { echo "no machine-id"; exit 1; }
	! grep -rq /usr/local/bin "$1" || { echo "$1 names /usr/local/bin"; exit 1; }
	/usr/bin/sermon-helper --version | grep -q sermon-helper || { echo "the helper does not run"; exit 1; }
	[ "$(stat -c "%U:%G %a" /etc/sermon/helper.toml)" = "root:root 644" ] || { echo "rights of helper.toml"; exit 1; }
	[ "$(stat -c "%U:%G %a" /var/lib/sermon-helper)" = "root:root 700" ] || { echo "rights of the helper state"; exit 1; }
	grep -q "^allow_remote_reboot = false" /etc/sermon/helper.toml || { echo "remote reboots allowed by default"; exit 1; }
	! grep -rq /usr/local/bin "$2" || { echo "$2 names /usr/local/bin"; exit 1; }
	# The admin allows reboots: an upgrade keeps that.
	sed -i "s/^allow_remote_reboot = false/allow_remote_reboot = true/" /etc/sermon/helper.toml
}
enroll_fake() {
	mkdir -p /var/lib/sermon-agent/identity && echo key >/var/lib/sermon-agent/identity/agent.key
}
kept() {
	[ -f /var/lib/sermon-agent/identity/agent.key ] || { echo "the upgrade removed the identity"; exit 1; }
	[ -x /usr/bin/sermon-agent ] || { echo "the upgrade removed the binary"; exit 1; }
	[ -x /usr/bin/sermon-helper ] || { echo "the upgrade removed the helper"; exit 1; }
	grep -q "^allow_remote_reboot = true" /etc/sermon/helper.toml || { echo "the upgrade replaced helper.toml"; exit 1; }
}
removed() {
	for path in /usr/bin/sermon-agent /usr/bin/sermon-helper /var/lib/sermon-agent /var/lib/sermon-helper "$@"; do
		[ ! -e "$path" ] || { echo "left behind: $path"; exit 1; }
	done
}
'
# The rotation, inside each container: install.sh --repo against release N (file:// in place of
# https://, its download reads the files), the package manager against release N.
rotation='
from_repo() {
	ln -s /repos/1 /repo
	SERMON_INSTALL_TEST=1 . /install.sh
	download() { cp "${1#file://}" "$2"; }
	SERVER=file:// NO_HELPER=""
	install_from_repo "$1" "$(mktemp -d)"
}
release() { ln -sfn "/repos/$1" /repo; }
same() { [ "$(sha256sum <"$1")" = "$(sha256sum <"$2")" ] || { echo "$1 differs from $2"; exit 1; }; }
'
run() {
	name=$1 image=$2 script=$3
	if docker run --rm -v "$work/out:/p:ro" -v "$work/pub:/k:ro" -v "$work/repos:/repos:ro" -v "$work/install.sh:/install.sh:ro" \
		-e OLD_APK_KEY="$old_apk_key" "$image" sh -euc "$checks$rotation$script"; then
		echo "✓ $name"
	else
		echo "✗ $name" >&2
		exit 1
	fi
}

run debian-13 debian:13@sha256:9c6e381421d0f48d1b8133e573f33c185ee16cbf505eda9c4a8a4791b25ea8a6 '
apt-get update -qq >/dev/null
apt-get install -y -qq --no-install-recommends gpg gpg-agent binutils >/dev/null 2>&1
export GNUPGHOME=/tmp/gnupg && mkdir -m 0700 "$GNUPGHOME"
for deb in /p/0.0.1/*.deb; do
	rm -rf /tmp/deb && mkdir /tmp/deb && cd /tmp/deb && ar x "$deb" && cat debian-binary control.tar.* data.tar.* >signed
	! gpg --verify _gpgorigin signed 2>/dev/null || { echo "verified without the key: $deb"; exit 1; }
done
gpg --quiet --import /k/sermon.asc 2>/dev/null
for deb in /p/0.0.1/*.deb; do
	rm -rf /tmp/deb && mkdir /tmp/deb && cd /tmp/deb && ar x "$deb" && cat debian-binary control.tar.* data.tar.* >signed
	gpg --verify _gpgorigin signed 2>/dev/null || { echo "bad signature: $deb"; exit 1; }
	echo x >>signed
	! gpg --verify _gpgorigin signed 2>/dev/null || { echo "a changed package verifies: $deb"; exit 1; }
done
cd /
apt-get install -y -qq /p/0.0.1/*.deb >/dev/null 2>&1
installed /usr/lib/systemd/system/sermon-agent.service /usr/lib/systemd/system/sermon-helper.service
[ -f /usr/lib/systemd/system/sermon-helper.socket ] || { echo "no socket unit"; exit 1; }
same /usr/share/keyrings/sermon-archive-keyring.asc /k/sermon.asc
enroll_fake
apt-get install -y -qq /p/0.0.2/*.deb >/dev/null 2>&1
kept
apt-get remove -y -qq sermon-agent >/dev/null 2>&1
removed /usr/lib/systemd/system/sermon-agent.service /usr/lib/systemd/system/sermon-helper.service /usr/share/keyrings/sermon-archive-keyring.asc
[ -f /etc/sermon/agent.toml ] || { echo "the configuration went (deb keeps it until purge)"; exit 1; }
[ -f /etc/sermon/helper.toml ] || { echo "the helper configuration went (deb keeps it until purge)"; exit 1; }'

run rockylinux-9 rockylinux:9@sha256:03303863808e9cd79f0c58c833db3e5f9cde9f2abe16804a267b05dc54af86e4 '
! rpm -K /p/0.0.1/*.rpm >/dev/null 2>&1 || { echo "verified without the key"; exit 1; }
rpm --import /k/sermon.asc
rpm -K /p/0.0.1/*.rpm | grep -q "digests signatures OK" || { echo "bad signature"; exit 1; }
dnf install -y -q /p/0.0.1/*.rpm >/dev/null
installed /usr/lib/systemd/system/sermon-agent.service /usr/lib/systemd/system/sermon-helper.service
same /etc/pki/rpm-gpg/RPM-GPG-KEY-sermon /k/sermon.asc
enroll_fake
dnf install -y -q /p/0.0.2/*.rpm >/dev/null
kept
dnf remove -y -q sermon-agent >/dev/null
removed /usr/lib/systemd/system/sermon-agent.service /usr/lib/systemd/system/sermon-helper.socket /etc/pki/rpm-gpg/RPM-GPG-KEY-sermon'

run alpine-3.22 alpine:3.22@sha256:49340b346798875b5592e5d420241f2f9cfba5f47e62ebb1446a1b34734e898f '
# OpenRC as on a host: the helper package enters its service into the default runlevel.
apk add -q openrc >/dev/null
! apk add -q /p/0.0.1/*.apk 2>/dev/null || { echo "installed without the key"; exit 1; }
cp /k/sermon-*.rsa.pub /etc/apk/keys/
apk add -q /p/0.0.1/*.apk >/dev/null
installed /etc/init.d/sermon-agent /etc/init.d/sermon-helper
rc-update show default 2>/dev/null | grep -q sermon-helper || { echo "the helper is not in the default runlevel"; exit 1; }
enroll_fake
# V1-20: what the agent prints lands in syslog (busybox syslogd, /var/log/messages), as under
# systemd in the journal. Its configuration points nowhere: it logs why it cannot connect.
syslogd
mkdir -p /run/openrc && touch /run/openrc/softlevel
rc-service sermon-agent start >/dev/null 2>&1 || true
# The tag of the line, after facility and priority (busybox syslogd), not text of the message.
tagged=" daemon[.][a-z]* sermon-agent: "
for _ in $(seq 1 30); do grep -q "$tagged" /var/log/messages 2>/dev/null && break; sleep 1; done
rc-service sermon-agent stop >/dev/null 2>&1 || true
grep -q "$tagged" /var/log/messages 2>/dev/null || { echo "no message of the agent in syslog"; cat /var/log/messages 2>/dev/null; exit 1; }
apk add -q /p/0.0.2/*.apk >/dev/null
kept
apk del -q sermon-agent sermon-helper
removed /etc/init.d/sermon-agent /etc/init.d/sermon-helper /etc/apk/keys/"$OLD_APK_KEY".rsa.pub'

# Rotation (M4-31): the host trusts nothing but what install.sh fetched from release 1. Release 2
# is still signed with the old keys and its agent package brings the next ones, release 3 is
# signed with those only; its package takes the old keys away.
run debian-13-rotation debian:13@sha256:9c6e381421d0f48d1b8133e573f33c185ee16cbf505eda9c4a8a4791b25ea8a6 '
apt-get update -qq >/dev/null
apt-get install -y -qq --no-install-recommends openssl >/dev/null 2>&1
rm /etc/apt/sources.list.d/debian.sources
upgrade() { apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get -y -qq --with-new-pkgs upgrade >/dev/null 2>&1; }
version() { dpkg-query -W -f="\${Version}\n" sermon-agent sermon-helper | sort -u; }
from_repo debian >/dev/null
[ "$(version)" = 0.0.1-1 ] || { echo "release 1: $(version)"; exit 1; }
# Another source with a newer sermon-agent does not replace the agent (pin).
mkdir /tmp/decoy && cp /p/0.0.3/*.deb /tmp/decoy/ && cd /tmp/decoy
for deb in *.deb; do
	dpkg-deb -f "$deb" && echo "Filename: ./$deb" && echo "Size: $(stat -c %s "$deb")" && echo "SHA256: $(sha256sum <"$deb" | cut -d" " -f1)" && echo
done >Packages
cd / && echo "deb [trusted=yes] file:/tmp/decoy ./" >/etc/apt/sources.list.d/decoy.list
# The flat source has no compressed index: apt warns about each.
apt-get update -qq 2>/dev/null && DEBIAN_FRONTEND=noninteractive apt-get -y -qq --with-new-pkgs upgrade >/dev/null 2>&1
[ "$(version)" = 0.0.1-1 ] || { echo "another source replaced the agent: $(version)"; exit 1; }
rm /etc/apt/sources.list.d/decoy.list
release 2 && upgrade
[ "$(version)" = 0.0.2-1 ] || { echo "release 2: $(version)"; exit 1; }
same /usr/share/keyrings/sermon-archive-keyring.asc /repos/2/keys/sermon.asc
release 3 && upgrade || { echo "release 3 does not verify"; exit 1; }
[ "$(version)" = 0.0.3-1 ] || { echo "release 3: $(version)"; exit 1; }
same /usr/share/keyrings/sermon-archive-keyring.asc /repos/3/keys/sermon.asc
release 1
! apt-get update -qq --error-on=any >/tmp/old 2>&1 && grep -qi "sign\|key" /tmp/old || { echo "the old key still verifies"; exit 1; }'

run rockylinux-9-rotation rockylinux:9@sha256:03303863808e9cd79f0c58c833db3e5f9cde9f2abe16804a267b05dc54af86e4 '
dnf install -y -q createrepo_c >/dev/null
mkdir /etc/yum.repos.d.off && mv /etc/yum.repos.d/*.repo /etc/yum.repos.d.off/
# As the helper does: dnf takes the next key from the gpgkey file without asking.
upgrade() { dnf -y -q makecache --refresh >/dev/null 2>&1 && dnf -y -q upgrade >/dev/null 2>&1; }
version() { rpm -q --qf "%{VERSION}-%{RELEASE}\n" sermon-agent sermon-helper | sort -u; }
from_repo rocky >/dev/null
[ "$(version)" = 0.0.1-1 ] || { echo "release 1: $(version)"; exit 1; }
# Another source with a newer sermon-agent does not replace the agent (priority).
mkdir /tmp/decoy && cp /p/0.0.3/*.rpm /tmp/decoy/ && createrepo_c -q /tmp/decoy
printf "[decoy]\nname=Decoy\nbaseurl=file:///tmp/decoy\ngpgcheck=0\n" >/etc/yum.repos.d/decoy.repo
upgrade
[ "$(version)" = 0.0.1-1 ] || { echo "another source replaced the agent: $(version)"; exit 1; }
rm /etc/yum.repos.d/decoy.repo
release 2 && upgrade
[ "$(version)" = 0.0.2-1 ] || { echo "release 2: $(version)"; exit 1; }
same /etc/pki/rpm-gpg/RPM-GPG-KEY-sermon /repos/2/keys/sermon.asc
release 3 && upgrade || { echo "release 3 does not verify"; exit 1; }
[ "$(version)" = 0.0.3-1 ] || { echo "release 3: $(version)"; exit 1; }
same /etc/pki/rpm-gpg/RPM-GPG-KEY-sermon /repos/3/keys/sermon.asc'

run alpine-3.22-rotation alpine:3.22@sha256:49340b346798875b5592e5d420241f2f9cfba5f47e62ebb1446a1b34734e898f '
apk add -q openrc openssl >/dev/null
: >/etc/apk/repositories
upgrade() { apk update -q --force-refresh && apk upgrade -q >/dev/null; }
version() { apk info -e -v sermon-agent sermon-helper | sed "s/^sermon-[a-z]*-//" | sort -u; }
from_repo alpine >/dev/null
[ "$(version)" = 0.0.1-r1 ] || { echo "release 1: $(version)"; exit 1; }
release 2 && upgrade
[ "$(version)" = 0.0.2-r1 ] || { echo "release 2: $(version)"; exit 1; }
for key in /repos/2/keys/sermon-*.rsa.pub; do same "/etc/apk/keys/${key##*/}" "$key"; done
release 3 && upgrade || { echo "release 3 does not verify"; exit 1; }
[ "$(version)" = 0.0.3-r1 ] || { echo "release 3: $(version)"; exit 1; }
[ ! -e /etc/apk/keys/"$OLD_APK_KEY".rsa.pub ] || { echo "the old key stayed"; exit 1; }
for key in /repos/3/keys/sermon-*.rsa.pub; do same "/etc/apk/keys/${key##*/}" "$key"; done
release 1
# apk only warns, with exit code 0.
apk update --force-refresh >/tmp/old 2>&1
grep -q "UNTRUSTED signature" /tmp/old || { echo "the old key still verifies"; exit 1; }'
