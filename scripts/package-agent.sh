#!/bin/sh
# Builds the signed agent packages (.deb, .rpm, .apk) of one architecture with nfpm (M2-21).
#
#   scripts/package-agent.sh ARCH VERSION BINARY OUTDIR
#
# ARCH is amd64 or arm64, VERSION a package version (e.g. 1.2.3), BINARY the static agent binary.
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

[ $# -eq 4 ] || die "usage: $0 ARCH VERSION BINARY OUTDIR"
arch=$1 version=$2 binary=$3 out=$4
case $arch in amd64 | arm64) ;; *) die "unknown architecture $arch" ;; esac
case $version in [0-9]*) ;; *) die "the version must start with a digit: $version" ;; esac
[ -x "$binary" ] || die "no binary at $binary"
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
trap 'rm -rf "$work"' EXIT

cp "$binary" "$work/root/sermon-agent"
# Packages install to /usr/bin; install.sh to /usr/local/bin.
sed 's#/usr/local/bin/sermon-agent#/usr/bin/sermon-agent#' "$root/packaging/systemd/sermon-agent.service" >"$work/root/sermon-agent.service"
sed 's#/usr/local/bin/sermon-agent#/usr/bin/sermon-agent#' "$root/packaging/openrc/sermon-agent" >"$work/root/sermon-agent.openrc"
if grep -q /usr/local/bin "$work/root/sermon-agent.service" "$work/root/sermon-agent.openrc"; then
	die "a service file still names /usr/local/bin"
fi
cp "$root/packaging/nfpm/nfpm.yaml" "$root/packaging/nfpm/agent.toml" "$root"/packaging/nfpm/*.sh "$work/root/"
cp "$SERMON_PKG_GPG_KEY" "$work/keys/gpg.asc"
cp "$SERMON_PKG_APK_KEY" "$work/keys/apk.rsa"

for packager in deb rpm apk; do
	docker run --rm -q -u "$(id -u):$(id -g)" -v "$work:/work" -w /work \
		-e PKG_ARCH="$arch" -e PKG_VERSION="$version" \
		-e NFPM_PASSPHRASE="${SERMON_PKG_KEY_PASSPHRASE:-}" \
		"$NFPM_IMAGE" package --config /work/root/nfpm.yaml --packager "$packager" --target /work/ >/dev/null
done
mv "$work"/*.deb "$work"/*.rpm "$work"/*.apk "$out/"
echo "✓ $out: sermon-agent $version ($arch) as .deb, .rpm and .apk, signed"
