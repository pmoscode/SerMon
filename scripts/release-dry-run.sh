#!/bin/sh
# A release without GitHub (V1-03): what release.yml does with the files, with keys of this run –
# agent and helper with the test release key, signing, packages and repositories
# (scripts/release-package.sh), the offline checks of scripts/verify-release.sh and, with
# IMAGE=1, the server image of the release for the Docker platform with its files inside.
#
#   scripts/release-dry-run.sh [VERSION]        (default 0.0.1-rc.1; Docker needed)
#   SKIP_AGENT=1 …                              take the binaries of the last run
#   IMAGE=1 …                                   also build the image sermon-server:dry-run
#
# Everything goes to dist/release-dry-run/; the test keys never leave it.
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
version=${1:-0.0.1-rc.1}
out=$root/dist/release-dry-run
die() {
	echo "release-dry-run: $*" >&2
	exit 1
}

if [ "${SKIP_AGENT:-}" != 1 ]; then
	rm -rf "$out"
	mkdir -p "$out/keys" "$out/public/keys/next"
	chmod 0700 "$out/keys"
	"$root/scripts/release-sign.sh" generate "$out/keys/release.pem" >"$out/public/release.pub"
	"$root/scripts/release-sign.sh" generate "$out/keys/release-next.pem" >"$out/public/release-next.pub"
	"$root/scripts/package-test-keys.sh" "$out/pkg" >/dev/null
	"$root/scripts/package-test-keys.sh" "$out/pkg-next" >/dev/null
	cp "$out/pkg/gpg.asc" "$out/pkg/apk.rsa" "$out/keys/"
	cp "$out/pkg/gpg.pub.asc" "$out/pkg/apk.rsa.pub" "$out/public/keys/"
	cp "$out/pkg-next/gpg.pub.asc" "$out/pkg-next/apk.rsa.pub" "$out/public/keys/next/"
	echo "▸ building agent and helper $version with the test release key"
	(cd "$root/agent" && SERMON_RELEASE_KEY=$(cat "$out/public/release.pub") SERMON_RELEASE_KEY_NEXT=$(cat "$out/public/release-next.pub") \
		SERMON_VERSION=$version cargo zigbuild --release --locked -q -p sermon-agent -p sermon-helper \
		--target x86_64-unknown-linux-musl --target aarch64-unknown-linux-musl)
	mkdir -p "$out/agent"
	for a in amd64:x86_64 arm64:aarch64; do
		for b in agent helper; do
			cp "$root/agent/target/${a#*:}-unknown-linux-musl/release/sermon-$b" "$out/agent/sermon-$b-linux-${a%%:*}"
		done
	done
	printf '{"bomFormat":"CycloneDX","specVersion":"1.6","components":[{"type":"library","name":"dry-run"}]}\n' >"$out/agent/sermon-agent-$version.cdx.json"
fi
[ -d "$out/agent" ] || die "no binaries of an earlier run (SKIP_AGENT=1)"

echo "▸ signing, packages and repositories (scripts/release-package.sh)"
rm -rf "$out/release"
SERMON_PUBLIC_KEYS=$out/public "$root/scripts/release-package.sh" "$version" "$out/agent" "$out/keys" "$out/release"
"$root/scripts/verify-release.sh" --offline --dir "$out/release" --keys "$out/public" "$version"

if [ "${IMAGE:-}" = 1 ]; then
	echo "▸ the server image of the release (sermon-server:dry-run)"
	files=$out/image-files
	rm -rf "$files"
	mkdir -p "$files/downloads" "$files/repo"
	for b in agent helper; do
		for a in amd64 arm64; do
			cp "$out/release/sermon-$b-linux-$a" "$out/release/sermon-$b-linux-$a.minisig" "$files/downloads/"
		done
	done
	tar -xzf "$out/release/sermon-repo-$version.tar.gz" -C "$files/repo"
	chmod -R u=rwX,go=rX "$files"
	docker buildx build -q --load -t sermon-server:dry-run -f "$root/deploy/compose/server.Dockerfile" \
		--build-context "release-files=$files" --build-arg "SERMON_VERSION=$version" \
		--build-arg "SERMON_RELEASE_KEY=$(cat "$out/public/release.pub")" "$root" >/dev/null
	container=$(docker create sermon-server:dry-run)
	docker export "$container" | tar -tf - >"$out/image.list"
	docker rm "$container" >/dev/null
	for f in srv/downloads/sermon-agent-linux-amd64.minisig srv/downloads/sermon-helper-linux-arm64 \
		srv/repo/apt/dists/stable/InRelease srv/repo/keys/SHA256SUMS.minisig usr/local/bin/sermon-server; do
		grep -qx "$f" "$out/image.list" || die "the image has no /$f"
	done
	docker run --rm sermon-server:dry-run --version | grep -qF "$version" || die "the server in the image is not $version"
	echo "✓ sermon-server:dry-run: server $version with the signed downloads and repositories of the release"
fi
echo "✓ dry run of release $version in $out"
