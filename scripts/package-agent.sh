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
#   SERMON_PKG_APK_KEY   RSA private key (PEM), signs .apk
#   SERMON_PKG_KEY_PASSPHRASE   their passphrase, if any
#   SERMON_PKG_PUBLIC_KEYS   directory with gpg.pub.asc and apk.rsa.pub of these keys, during a
#                        rotation next/ with those of the next pair (as KEYS of build-repos.sh).
#                        The agent package installs them for the package manager, so that its
#                        updates bring new repository keys (M4-31); the .apk are signed under the
#                        name of apk.rsa.pub (scripts/repo-keys.sh).
#   SERMON_NFPM_IMAGE    an nfpm image already loaded (the release, V1-03); never pulled
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
if [ ! -r "${SERMON_PKG_PUBLIC_KEYS:-}/gpg.pub.asc" ] || [ ! -r "$SERMON_PKG_PUBLIC_KEYS/apk.rsa.pub" ]; then
	die "SERMON_PKG_PUBLIC_KEYS must name the directory of the public keys (gpg.pub.asc, apk.rsa.pub)"
fi

root=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$out"
out=$(cd "$out" && pwd)
# Next to the output directory, not inside it (review M4-30: a killed run left the key copies
# among the packages); Docker Desktop shares it with the container, unlike most temp dirs.
work=$(mktemp -d "$(dirname "$out")/.package-$arch.XXXXXX")
mkdir -p "$work/root" "$work/keys"
chmod 0700 "$work" "$work/keys"
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
cp "$root/packaging/nfpm/agent.toml" "$root"/packaging/nfpm/*.sh "$work/root/"
"$root/scripts/repo-keys.sh" publish "$SERMON_PKG_PUBLIC_KEYS" "$work/root/repo-keys"
apk_key=$("$root/scripts/repo-keys.sh" apk-name "$SERMON_PKG_PUBLIC_KEYS/apk.rsa.pub")
for config in nfpm helper; do
	sed "s/@APK_KEY_NAME@/$apk_key/" "$root/packaging/nfpm/$config.yaml" >"$work/root/$config.yaml"
done
cp "$SERMON_PKG_GPG_KEY" "$work/keys/gpg.asc"
cp "$SERMON_PKG_APK_KEY" "$work/keys/apk.rsa"

# The passphrase goes by name only: its value would show in docker's command line (ps).
NFPM_PASSPHRASE="${SERMON_PKG_KEY_PASSPHRASE:-}"
export NFPM_PASSPHRASE
# A release candidate (V1-03): nfpm writes 1.0.0-rc.1 as 1.0.0~rc.1 for deb and rpm, but apk
# takes a suffix only with digits right after it (1.0.0_rc1, not 1.0.0_rc.1). All three sort the
# candidate before its release.
apk_version=$(printf '%s' "$version" | sed -E 's/-([a-z]+)\.([0-9]+)$/-\1\2/')
# The release loads nfpm from an artifact (SERMON_NFPM_IMAGE) instead of pulling it.
nfpm_image=$NFPM_IMAGE pull=missing
[ -z "${SERMON_NFPM_IMAGE:-}" ] || nfpm_image=$SERMON_NFPM_IMAGE pull=never
for config in nfpm helper; do
	for packager in deb rpm apk; do
		pkg_version=$version
		[ "$packager" != apk ] || pkg_version=$apk_version
		docker run --rm -q --pull "$pull" --network none -u "$(id -u):$(id -g)" -v "$work:/work" -w /work \
			-e PKG_ARCH="$arch" -e PKG_VERSION="$pkg_version" \
			-e NFPM_PASSPHRASE \
			"$nfpm_image" package --config "/work/root/$config.yaml" --packager "$packager" --target /work/ >/dev/null
	done
done
mv "$work"/*.deb "$work"/*.rpm "$work"/*.apk "$out/"
echo "✓ $out: sermon-agent and sermon-helper $version ($arch) as .deb, .rpm and .apk, signed"
