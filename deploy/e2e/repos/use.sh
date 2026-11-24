#!/bin/sh
# Switches a host of the E2E environment to the package sources of build.sh (M4-08) and installs
# version 1.0.0 of the dummy packages, so that updates are waiting. The host's own sources are put
# aside: updates come only from here, reproducibly and without the internet.
#
#   sh use.sh URL KEYS    e.g. http://packages.test:8080 /keys (gpg.pub.asc, sermon-agent.rsa.pub)
set -eu

[ $# -eq 2 ] || {
	echo "usage: $0 URL KEYS" >&2
	exit 1
}
url=$1 keys=$2

if command -v apt-get >/dev/null 2>&1; then
	mkdir -p /etc/apt/sources.list.d.off /etc/apt/keyrings
	find /etc/apt/sources.list.d -mindepth 1 -maxdepth 1 -exec mv {} /etc/apt/sources.list.d.off/ \;
	[ ! -s /etc/apt/sources.list ] || mv /etc/apt/sources.list /etc/apt/sources.list.d.off/sources.list
	cp "$keys/gpg.pub.asc" /etc/apt/keyrings/sermon-e2e.asc
	cat >/etc/apt/sources.list.d/sermon-e2e.sources <<-EOF
		Types: deb
		URIs: $url/apt
		Suites: e2e e2e-updates e2e-security
		Components: main
		Signed-By: /etc/apt/keyrings/sermon-e2e.asc
	EOF
	apt-get update -qq
	DEBIAN_FRONTEND=noninteractive apt-get install -y -qq sermon-e2e-demo=1.0.0-1 sermon-e2e-extra=1.0.0-1 >/dev/null
elif command -v dnf >/dev/null 2>&1; then
	mkdir -p /etc/yum.repos.d.off /etc/pki/rpm-gpg
	find /etc/yum.repos.d -mindepth 1 -maxdepth 1 -name '*.repo' -exec mv {} /etc/yum.repos.d.off/ \;
	cp "$keys/gpg.pub.asc" /etc/pki/rpm-gpg/sermon-e2e.asc
	cat >/etc/yum.repos.d/sermon-e2e.repo <<-EOF
		[sermon-e2e]
		name=SerMon E2E
		baseurl=$url/rpm
		gpgcheck=1
		repo_gpgcheck=1
		gpgkey=file:///etc/pki/rpm-gpg/sermon-e2e.asc
		metadata_expire=0
	EOF
	dnf -y -q makecache >/dev/null
	dnf -y -q install sermon-e2e-demo-1.0.0 sermon-e2e-extra-1.0.0 >/dev/null
elif command -v apk >/dev/null 2>&1; then
	[ ! -f /etc/apk/repositories ] || mv /etc/apk/repositories /etc/apk/repositories.off
	cp "$keys/sermon-agent.rsa.pub" /etc/apk/keys/sermon-agent.rsa.pub
	echo "$url/apk" >/etc/apk/repositories
	apk update -q
	apk add -q sermon-e2e-demo=1.0.0-r1 sermon-e2e-extra=1.0.0-r1
	# apk keeps the version in /etc/apk/world and upgrades nothing pinned: unpin, as on a real host.
	sed -i 's/^\(sermon-e2e-[a-z]*\)=.*/\1/' /etc/apk/world
else
	echo "use.sh: no apt, dnf or apk" >&2
	exit 1
fi
