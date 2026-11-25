#!/bin/sh
# Builds the signed packages (.deb, .rpm, .apk) of one architecture with nfpm: sermon-agent
# (M2-21) and sermon-helper, the root helper (M4-04, ADR 0035).
#
#   scripts/package-agent.sh ARCH VERSION AGENT HELPER OUTDIR
#
# ARCH is amd64 or arm64, VERSION a package version (e.g. 1.2.3), AGENT and HELPER the static
# binaries.
# The keys come from the environment, as files:
#   SERMON_PKG_GPG_KEY   armored OpenPGP secret key, signs .deb (origin) and .rpm
#   SERMON_PKG_APK_KEY   RSA private key (PEM), signs .apk as sermon-agent.rsa.pub
#   SERMON_PKG_KEY_PASSPHRASE   their passphrase, if any
# nfpm runs as a container image pinned by digest; nothing is installed on the host.
set -eu

NFPM_IMAGE=goreleaser/nfpm:v2.49.0@sha256:9ea46a59c4d055ef7071b22f3520b53e8dad98e18043b2f881b53bd7ffec634e

die() {
	echo "package-agent: $*" >&2
	exit 1
}

[ $# -eq 5 ] || die "usage: $0 ARCH VERSION AGENT HELPER OUTDIR"
arch=$1 version=$2 binary=$3 helper=$4 out=$5
case $arch in amd64 | arm64) ;; *) die "unknown architecture $arch" ;; esac
case $version in [0-9]*) ;; *) die "the version must start with a digit: $version" ;; esac
[ -x "$binary" ] || die "no binary at $binary"
[ -x "$helper" ] || die "no helper binary at $helper"
[ -r "${SERMON_PKG_GPG_KEY:-}" ] || die "SERMON_PKG_GPG_KEY must name the OpenPGP key file (packages are always signed)"
[ -r "${SERMON_PKG_APK_KEY:-}" ] || die "SERMON_PKG_APK_KEY must name the RSA key file for .apk"

root=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$out"
out=$(cd "$out" && pwd)
# Inside the output directory: Docker Desktop shares it with the container, unlike most temp dirs.
work="$out/.package-$arch"
rm -rf "$work"
mkdir -p "$work/root" "$work/keys"
chmod 0700 "$work/keys"
# The key copies go on every way out, interrupts included (dash runs EXIT traps only on exit).
trap 'rm -rf "$work"' EXIT
trap 'exit 130' INT TERM HUP

cp "$binary" "$work/root/sermon-agent"
cp "$helper" "$work/root/sermon-helper"
# Packages install to /usr/bin; install.sh to /usr/local/bin.
for name in sermon-agent sermon-helper; do
	sed "s#/usr/local/bin/$name#/usr/bin/$name#" "$root/packaging/systemd/$name.service" >"$work/root/$name.service"
	sed "s#/usr/local/bin/$name#/usr/bin/$name#" "$root/packaging/openrc/$name" >"$work/root/$name.openrc"
done
cp "$root/packaging/systemd/sermon-helper.socket" "$root/packaging/helper.toml" "$work/root/"
if grep -q /usr/local/bin "$work/root"/*.service "$work/root"/*.openrc; then
	die "a service file still names /usr/local/bin"
fi
cp "$root/packaging/nfpm/nfpm.yaml" "$root/packaging/nfpm/helper.yaml" "$root/packaging/nfpm/agent.toml" "$root"/packaging/nfpm/*.sh "$work/root/"
cp "$SERMON_PKG_GPG_KEY" "$work/keys/gpg.asc"
cp "$SERMON_PKG_APK_KEY" "$work/keys/apk.rsa"

# The passphrase goes by name only: its value would show in docker's command line (ps).
NFPM_PASSPHRASE="${SERMON_PKG_KEY_PASSPHRASE:-}"
export NFPM_PASSPHRASE
for config in nfpm helper; do
	for packager in deb rpm apk; do
		docker run --rm -q -u "$(id -u):$(id -g)" -v "$work:/work" -w /work \
			-e PKG_ARCH="$arch" -e PKG_VERSION="$version" \
			-e NFPM_PASSPHRASE \
			"$NFPM_IMAGE" package --config "/work/root/$config.yaml" --packager "$packager" --target /work/ >/dev/null
	done
done
mv "$work"/*.deb "$work"/*.rpm "$work"/*.apk "$out/"
echo "✓ $out: sermon-agent and sermon-helper $version ($arch) as .deb, .rpm and .apk, signed"
