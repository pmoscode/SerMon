#!/bin/sh
# The work of the signing job of the release (V1-03, ADR 0043 §5): from the unsigned binaries of
# the build job to the signed files of the release, with the keys of the environment "release".
#
#   scripts/release-package.sh VERSION AGENT KEYS OUT
#
#   AGENT  sermon-{agent,helper}-linux-{amd64,arm64} (just release-agent) and
#          sermon-agent-VERSION.cdx.json (SBOM)
#   KEYS   release.pem, gpg.asc, apk.rsa – the private keys; checked first against the public
#          ones in PUBLIC (SERMON_PUBLIC_KEYS, default packaging/: release.pub, keys/, keys/next/)
#   OUT    (empty or new) the files of the release:
#            sermon-{agent,helper}-linux-{amd64,arm64}{,.minisig}   signed for name and VERSION
#            sermon-{agent,helper}-VERSION-{amd64,arm64}.{deb,rpm,apk}
#            sermon-repo-VERSION.tar.gz   apt/ rpm/ apk/ keys/ of scripts/build-repos.sh, the
#                                         list of keys signed – what the image serves under /repo
#            sermon-agent-VERSION.cdx.json
#            SHA256SUMS{,.minisig}        over all of them, signed for VERSION
#
# Packages get names of their own in the release: those of nfpm hold "~" (1.0.0~rc.1), which
# GitHub changes in the names of release files. In the repositories they keep nfpm's names.
# SERMON_NFPM_IMAGE and SERMON_REPO_TOOLS=loaded pass through (tools loaded from an artifact, no
# network); SOURCE_DATE_EPOCH sets the times in the tarball.
set -eu

die() {
	echo "release-package: $*" >&2
	exit 1
}

[ $# -eq 4 ] || die "usage: $0 VERSION AGENT KEYS OUT"
root=$(cd "$(dirname "$0")/.." && pwd)
version=$1 agent=$2 keys=$3 out=$4
public=${SERMON_PUBLIC_KEYS:-$root/packaging}
echo "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$' || die "not a release version: $version"
for arch in amd64 arm64; do
	for b in agent helper; do [ -f "$agent/sermon-$b-linux-$arch" ] || die "no $agent/sermon-$b-linux-$arch"; done
done
[ -f "$agent/sermon-agent-$version.cdx.json" ] || die "no SBOM $agent/sermon-agent-$version.cdx.json"
mkdir -p "$out"
out=$(cd "$out" && pwd)
[ -z "$(ls -A "$out")" ] || die "$out is not empty"

"$root/scripts/release-keys.sh" check "$keys" "$public"

# Below OUT's parent: Docker Desktop shares it with the containers (as package-agent.sh does).
work=$(mktemp -d "$(dirname "$out")/.release-package.XXXXXX")
trap 'rm -rf "$work"' EXIT
trap 'exit 130' INT TERM HUP
chmod 0700 "$work"

for arch in amd64 arm64; do
	for b in agent helper; do
		cp "$agent/sermon-$b-linux-$arch" "$out/"
		chmod 0755 "$out/sermon-$b-linux-$arch"
	done
done
"$root/scripts/release-sign.sh" sign "$keys/release.pem" --version "$version" "$out"/sermon-agent-linux-* "$out"/sermon-helper-linux-*
cp "$agent/sermon-agent-$version.cdx.json" "$out/"

for arch in amd64 arm64; do
	SERMON_PKG_GPG_KEY="$keys/gpg.asc" SERMON_PKG_APK_KEY="$keys/apk.rsa" SERMON_PKG_PUBLIC_KEYS="$public/keys" \
		"$root/scripts/package-agent.sh" "$arch" "$version" "$out/sermon-agent-linux-$arch" \
		"$out/sermon-helper-linux-$arch" "$work/packages" >/dev/null
done
# The names of the release: sermon-<name>-<version>-<amd64|arm64>.<kind>.
for pkg in "$work/packages"/*; do
	file=$(basename "$pkg")
	case $file in
	sermon-agent[_-]*) name=agent ;;
	sermon-helper[_-]*) name=helper ;;
	*) die "unknown package $file" ;;
	esac
	case $file in
	*_amd64.deb | *.x86_64.rpm | *_x86_64.apk) arch=amd64 ;;
	*_arm64.deb | *.aarch64.rpm | *_aarch64.apk) arch=arm64 ;;
	*) die "unknown architecture of $file" ;;
	esac
	target=$out/sermon-$name-$version-$arch.${file##*.}
	[ ! -e "$target" ] || die "two packages for $(basename "$target")"
	cp "$pkg" "$target"
done
[ "$(find "$out" -name "sermon-*-$version-*" | wc -l | tr -d ' ')" = 12 ] || die "not the 12 packages of agent and helper"

# The repositories: the private keys with the public ones (and next/ during a rotation).
mkdir -m 0700 "$work/repo-keys"
cp "$keys/gpg.asc" "$keys/apk.rsa" "$public/keys/gpg.pub.asc" "$public/keys/apk.rsa.pub" "$work/repo-keys/"
if [ -d "$public/keys/next" ]; then
	mkdir "$work/repo-keys/next"
	cp "$public/keys/next/gpg.pub.asc" "$public/keys/next/apk.rsa.pub" "$work/repo-keys/next/"
fi
"$root/scripts/build-repos.sh" "$work/packages" "$work/repo-keys" "$work/repo" >/dev/null
rm -rf "$work/repo-keys"
"$root/scripts/release-sign.sh" sign "$keys/release.pem" --version "$version" "$work/repo/keys/SHA256SUMS"
set --
if tar --version 2>/dev/null | grep -q 'GNU tar'; then
	set -- --owner=0 --group=0 --numeric-owner --sort=name --mtime="@${SOURCE_DATE_EPOCH:-0}" --format=gnu
fi
(cd "$work/repo" && tar "$@" -cf - apt rpm apk keys) | gzip -n >"$out/sermon-repo-$version.tar.gz"

(cd "$out" && for f in *; do printf '%s  %s\n' "$(openssl dgst -sha256 -r "$f" | cut -d' ' -f1)" "$f"; done) >"$work/SHA256SUMS"
mv "$work/SHA256SUMS" "$out/SHA256SUMS"
"$root/scripts/release-sign.sh" sign "$keys/release.pem" --version "$version" "$out/SHA256SUMS"
echo "✓ $out: release $version – binaries, 12 packages, repositories and SBOM, signed"
