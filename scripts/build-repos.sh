#!/bin/sh
# Builds the signed package repositories of the agent (M4-20, ADR 0036) from the packages of
# scripts/package-agent.sh – every version and architecture found in PACKAGES:
#
#   OUT/apt/    suite stable, component main (InRelease and Release.gpg, OpenPGP)
#   OUT/rpm/    one repository for x86_64 and aarch64 (repomd.xml.asc, OpenPGP; the packages are
#               signed themselves)
#   OUT/apk/    x86_64/ and aarch64/ with a signed APKINDEX (RSA, key name sermon-<id> from
#               scripts/repo-keys.sh)
#   OUT/keys/   sermon.asc (OpenPGP), sermon-<id>.rsa.pub (apk) and their SHA256SUMS for
#               install.sh --repo (scripts/repo-keys.sh)
#
#   scripts/build-repos.sh PACKAGES KEYS OUT
#
# KEYS holds gpg.asc, gpg.pub.asc, apk.rsa and apk.rsa.pub (scripts/package-test-keys.sh for tests;
# in a release the secrets of the pipeline). For a key rotation, KEYS/next/ may hold the public keys
# of the next pair: they are published alongside (ADR 0036). The release signs OUT/keys/SHA256SUMS
# afterwards (scripts/release-sign.sh), install.sh checks it. The server serves OUT under /repo/
# (SERMON_REPO_DIR). The tools run as containers (packaging/repo/*.Dockerfile); with
# SERMON_REPO_TOOLS=loaded the images sermon-repo-{apt,rpm,apk} are taken as they are (the release
# loads them from an artifact of a job without keys, V1-03) and nothing is built or pulled.
set -eu

die() {
	echo "build-repos: $*" >&2
	exit 1
}

[ $# -eq 3 ] || die "usage: $0 PACKAGES KEYS OUT"
root=$(cd "$(dirname "$0")/.." && pwd)
packages=$(cd "$1" && pwd)
keys=$(cd "$2" && pwd)
mkdir -p "$3"
out=$(cd "$3" && pwd)
for f in gpg.asc gpg.pub.asc apk.rsa apk.rsa.pub; do
	[ -r "$keys/$f" ] || die "$keys needs $f"
done
set -- "$packages"/*.deb
[ -e "$1" ] || die "no .deb in $packages"
rm -rf "$out/apt" "$out/rpm" "$out/apk" "$out/keys" "$out/.work"
# The private keys next to OUT, never inside it (review M4-30: OUT is served and archived as it
# is, and a killed run would leave them there); Docker Desktop shares the parent as well.
work=$(mktemp -d "$(dirname "$out")/.repo-keys.XXXXXX")
mkdir -p "$work/keys" "$out/apt/pool/main" "$out/rpm" "$out/apk/x86_64" "$out/apk/aarch64" "$out/keys"
chmod 0700 "$work" "$work/keys"
trap 'rm -rf "$work"' EXIT
trap 'exit 130' INT TERM HUP
cp "$keys/gpg.asc" "$work/keys/gpg.asc"
# abuild-sign names the signature after the key file: the name of the public key, as in the
# packages.
apk_key=$("$root/scripts/repo-keys.sh" apk-name "$keys/apk.rsa.pub")
cp "$keys/apk.rsa" "$work/keys/$apk_key.rsa"

for tool in apt rpm apk; do
	if [ "${SERMON_REPO_TOOLS:-}" = loaded ]; then
		docker image inspect "sermon-repo-$tool" >/dev/null 2>&1 || die "SERMON_REPO_TOOLS=loaded, but there is no image sermon-repo-$tool"
	else
		docker build -q -t "sermon-repo-$tool" -f "$root/packaging/repo/$tool.Dockerfile" "$root/packaging/repo" >/dev/null
	fi
done

# apt. No Valid-Until (M4-31): an expired Release fails apt-get update, and with it every update run
# of the helper, distribution updates included, whenever a release is late; an old Release only
# holds updates back, which whoever serves the repository can do anyway, and apt never downgrades.
# Origin and label: install.sh pins sermon-agent and sermon-helper to them.
cp "$packages"/*.deb "$out/apt/pool/main/"
docker run --rm --pull never --network none -u "$(id -u):$(id -g)" -v "$out/apt:/apt" -v "$work/keys:/keys:ro" -w /apt -e GNUPGHOME=/tmp/gnupg \
	sermon-repo-apt sh -euc '
		mkdir -m 0700 /tmp/gnupg && gpg --batch -q --import /keys/gpg.asc
		for arch in amd64 arm64; do
			mkdir -p dists/stable/main/binary-$arch
			apt-ftparchive --arch $arch packages pool/main >dists/stable/main/binary-$arch/Packages
			gzip -kf dists/stable/main/binary-$arch/Packages
		done
		apt-ftparchive -o APT::FTPArchive::Release::Origin=SerMon -o APT::FTPArchive::Release::Label=SerMon \
			-o APT::FTPArchive::Release::Suite=stable -o APT::FTPArchive::Release::Codename=stable \
			-o APT::FTPArchive::Release::Architectures="amd64 arm64" -o APT::FTPArchive::Release::Components=main \
			release dists/stable >/tmp/Release
		mv /tmp/Release dists/stable/Release
		gpg --batch -q --yes --clearsign -o dists/stable/InRelease dists/stable/Release
		gpg --batch -q --yes -abs -o dists/stable/Release.gpg dists/stable/Release'

# rpm
for rpm in "$packages"/*.rpm; do [ ! -e "$rpm" ] || cp "$rpm" "$out/rpm/"; done
docker run --rm --pull never --network none -u "$(id -u):$(id -g)" -v "$out/rpm:/rpm" -v "$work/keys:/keys:ro" -w /rpm -e GNUPGHOME=/tmp/gnupg \
	sermon-repo-rpm sh -euc '
		createrepo_c -q .
		mkdir -m 0700 /tmp/gnupg && gpg --batch -q --import /keys/gpg.asc
		gpg --batch -q --yes --armor --detach-sign -o repodata/repomd.xml.asc repodata/repomd.xml'

# apk looks a package up as NAME-VERSION.apk below its architecture; nfpm writes
# NAME_VERSION_ARCH.apk.
for apk in "$packages"/*.apk; do
	[ -e "$apk" ] || continue
	file=$(basename "$apk" .apk)
	# By suffix: x86_64 holds an underscore itself.
	case $file in
	*_x86_64) arch=x86_64 ;;
	*_aarch64) arch=aarch64 ;;
	*) die "unknown apk architecture in $apk" ;;
	esac
	name=${file%_"$arch"}
	cp "$apk" "$out/apk/$arch/$(echo "$name" | sed 's/_/-/').apk"
done
docker run --rm --pull never --network none -u "$(id -u):$(id -g)" -v "$out/apk:/apk" -v "$work/keys:/keys:ro" -e APK_KEY="$apk_key" sermon-repo-apk sh -euc '
	for arch in x86_64 aarch64; do
		cd /apk/$arch
		set -- ./*.apk
		if [ -e "$1" ]; then
			apk index -q --allow-untrusted --no-warnings -o /tmp/APKINDEX.tar.gz "$@"
		else
			apk index -q --allow-untrusted --no-warnings -o /tmp/APKINDEX.tar.gz
		fi
		abuild-sign -q -k "/keys/$APK_KEY.rsa" /tmp/APKINDEX.tar.gz
		mv /tmp/APKINDEX.tar.gz APKINDEX.tar.gz
	done'

# The public keys; during a rotation the next ones, too.
"$root/scripts/repo-keys.sh" publish "$keys" "$out/keys"
rm -rf "$work"
# Readable for the server: the four areas, nothing else of OUT.
chmod -R a+rX "$out/apt" "$out/rpm" "$out/apk" "$out/keys"
echo "✓ $out: apt, rpm and apk repositories of $(find "$packages" -maxdepth 1 -name '*.deb' -o -maxdepth 1 -name '*.rpm' -o -maxdepth 1 -name '*.apk' | wc -l | tr -d ' ') packages"
