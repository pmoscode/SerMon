#!/bin/sh
# Builds the package sources of the E2E environment (M4-08): an apt, an rpm and an apk repository
# with two dummy packages, signed with the package keys of the run (scripts/package-test-keys.sh).
# Updates arise from them reproducibly and without the internet:
#
#   sermon-e2e-demo   1.0.0 → 2.0.0  security update (apt suite e2e-security, updateinfo type security);
#                                     on Debian and Ubuntu 2.0.0 asks for a reboot, like a kernel
#   sermon-e2e-extra  1.0.0 → 1.1.0  plain update (apt suite e2e-updates, updateinfo type bugfix)
#
# Alpine has no security metadata (KONZEPT O6): its index holds both updates alike.
#
#   sh deploy/e2e/repos/build.sh KEYS OUT    KEYS from package-test-keys.sh, OUT gets apt/ rpm/ apk/ keys/
#
# nfpm and the repository tools run as containers (pinned in the Dockerfiles here); nothing is
# installed on the host. Never for releases: the keys are those of one test run.
set -eu

NFPM_IMAGE=goreleaser/nfpm:v2.49.0@sha256:9ea46a59c4d055ef7071b22f3520b53e8dad98e18043b2f881b53bd7ffec634e

die() {
	echo "e2e-repos: $*" >&2
	exit 1
}

[ $# -eq 2 ] || die "usage: $0 KEYS OUT"
for f in gpg.asc gpg.pub.asc apk.rsa sermon-agent.rsa.pub; do
	[ -r "$1/$f" ] || die "$1 needs $f (scripts/package-test-keys.sh)"
done
here=$(cd "$(dirname "$0")" && pwd)
keys=$(cd "$1" && pwd)
mkdir -p "$2"
out=$(cd "$2" && pwd)
rm -rf "$out/apt" "$out/rpm" "$out/apk" "$out/keys" "$out/.work"
work="$out/.work" # inside OUT: Docker Desktop shares it with the containers
mkdir -p "$work/keys" "$out/apt" "$out/rpm" "$out/keys"
chmod 0700 "$work/keys"
trap 'rm -rf "$work"' EXIT
trap 'exit 130' INT TERM HUP
cp "$keys/gpg.asc" "$work/keys/gpg.asc"
# abuild-sign names the signature after the key file: the same name as the packages' (sermon-agent).
cp "$keys/apk.rsa" "$work/keys/sermon-agent.rsa"

for tool in apt rpm apk; do
	docker build -q -t "sermon-e2e-repo-$tool" -f "$here/$tool.Dockerfile" "$here" >/dev/null
done

# package NAME VERSION [reboot]: .deb, .rpm and .apk of a dummy package into work/NAME-VERSION/.
package() {
	dir="$work/$1-$2"
	mkdir -p "$dir"
	echo "$1 $2" >"$dir/version"
	{
		echo "name: $1"
		echo "arch: all"
		echo "platform: linux"
		echo "version: \"$2\""
		echo 'release: "1"'
		echo "maintainer: SerMon project"
		echo "license: AGPL-3.0-only"
		echo "description: Dummy package of the SerMon E2E environment (M4-08), one text file."
		echo "contents:"
		echo "  - { src: /work/$1-$2/version, dst: /usr/share/$1/version }"
		if [ "${3:-}" = reboot ]; then
			printf '#!/bin/sh\n# Like a kernel: Debian and Ubuntu learn about the reboot from this file.\n' >"$dir/postinst"
			printf 'touch /var/run/reboot-required\necho %s >>/var/run/reboot-required.pkgs\n' "$1" >>"$dir/postinst"
			echo "overrides:"
			echo "  deb: { scripts: { postinstall: /work/$1-$2/postinst } }"
		fi
		echo "deb: { signature: { key_file: /work/keys/gpg.asc, type: origin } }"
		echo "rpm: { signature: { key_file: /work/keys/gpg.asc } }"
		echo "apk: { signature: { key_file: /work/keys/sermon-agent.rsa, key_name: sermon-agent } }"
	} >"$dir/nfpm.yaml"
	for packager in deb rpm apk; do
		docker run --rm -q -u "$(id -u):$(id -g)" -v "$work:/work" "$NFPM_IMAGE" \
			package --config "/work/$1-$2/nfpm.yaml" --packager "$packager" --target "/work/$1-$2/" >/dev/null
	done
}

# Versions as nfpm writes them (semantic versions).
package sermon-e2e-demo 1.0.0
package sermon-e2e-extra 1.0.0
package sermon-e2e-demo 2.0.0 reboot
package sermon-e2e-extra 1.1.0

# apt: one suite per kind, like Debian's bookworm, bookworm-updates and bookworm-security.
for suite in e2e e2e-updates e2e-security; do mkdir -p "$out/apt/pool/$suite"; done
cp "$work"/sermon-e2e-demo-1.0.0/*.deb "$work"/sermon-e2e-extra-1.0.0/*.deb "$out/apt/pool/e2e/"
cp "$work"/sermon-e2e-extra-1.1.0/*.deb "$out/apt/pool/e2e-updates/"
cp "$work"/sermon-e2e-demo-2.0.0/*.deb "$out/apt/pool/e2e-security/"
docker run --rm -u "$(id -u):$(id -g)" -v "$out/apt:/apt" -v "$work/keys:/keys:ro" -w /apt -e GNUPGHOME=/tmp/gnupg \
	sermon-e2e-repo-apt sh -euc '
		mkdir -m 0700 /tmp/gnupg && gpg --batch -q --import /keys/gpg.asc
		for suite in e2e e2e-updates e2e-security; do
			for arch in amd64 arm64; do
				mkdir -p dists/$suite/main/binary-$arch
				apt-ftparchive --arch $arch packages pool/$suite >dists/$suite/main/binary-$arch/Packages
				gzip -kf dists/$suite/main/binary-$arch/Packages
			done
			apt-ftparchive -o APT::FTPArchive::Release::Origin=SerMon-E2E -o APT::FTPArchive::Release::Label=SerMon-E2E \
				-o APT::FTPArchive::Release::Suite=$suite -o APT::FTPArchive::Release::Codename=$suite \
				-o APT::FTPArchive::Release::Architectures="amd64 arm64" -o APT::FTPArchive::Release::Components=main \
				release dists/$suite >/tmp/Release
			mv /tmp/Release dists/$suite/Release
			gpg --batch -q --yes --clearsign -o dists/$suite/InRelease dists/$suite/Release
			gpg --batch -q --yes -abs -o dists/$suite/Release.gpg dists/$suite/Release
		done'

# rpm: one repository; updateinfo.xml says which update is a security update.
cp "$work"/sermon-e2e-*/*.rpm "$out/rpm/"
issued=$(date -u '+%Y-%m-%d %H:%M:%S')
cat >"$work/updateinfo.xml" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<updates>
  <update from="e2e@sermon.invalid" status="final" type="security" version="1">
    <id>SERMON-E2E-2026-0001</id>
    <title>sermon-e2e-demo security update</title>
    <severity>Important</severity>
    <issued date="$issued"/>
    <description>Security update of the E2E environment (M4-08).</description>
    <reboot_suggested>True</reboot_suggested>
    <pkglist><collection short="e2e"><name>E2E</name>
      <package name="sermon-e2e-demo" version="2.0.0" release="1" epoch="0" arch="noarch"><filename>sermon-e2e-demo-2.0.0-1.noarch.rpm</filename></package>
    </collection></pkglist>
  </update>
  <update from="e2e@sermon.invalid" status="final" type="bugfix" version="1">
    <id>SERMON-E2E-2026-0002</id>
    <title>sermon-e2e-extra bug fix</title>
    <issued date="$issued"/>
    <description>Plain update of the E2E environment (M4-08).</description>
    <pkglist><collection short="e2e"><name>E2E</name>
      <package name="sermon-e2e-extra" version="1.1.0" release="1" epoch="0" arch="noarch"><filename>sermon-e2e-extra-1.1.0-1.noarch.rpm</filename></package>
    </collection></pkglist>
  </update>
</updates>
EOF
docker run --rm -u "$(id -u):$(id -g)" -v "$out/rpm:/rpm" -v "$work:/work:ro" -w /rpm -e GNUPGHOME=/tmp/gnupg \
	sermon-e2e-repo-rpm sh -euc '
		createrepo_c -q .
		modifyrepo_c --mdtype=updateinfo /work/updateinfo.xml repodata
		mkdir -m 0700 /tmp/gnupg && gpg --batch -q --import /work/keys/gpg.asc
		gpg --batch -q --yes --armor --detach-sign -o repodata/repomd.xml.asc repodata/repomd.xml'

# apk: the packages are noarch, and apk fetches them from noarch/; each architecture has the same
# index. apk looks a package up as NAME-VERSION.apk, nfpm writes NAME_VERSION_ARCH.apk.
mkdir -p "$out/apk/noarch" "$out/apk/x86_64" "$out/apk/aarch64"
for apk in "$work"/sermon-e2e-*/*.apk; do
	file=$(basename "$apk" _noarch.apk)
	cp "$apk" "$out/apk/noarch/$(echo "$file" | sed 's/_/-/').apk"
done
docker run --rm -u "$(id -u):$(id -g)" -v "$out/apk:/apk" -v "$work/keys:/keys:ro" -w /apk/noarch sermon-e2e-repo-apk sh -euc '
	apk index -q --allow-untrusted --no-warnings -o /tmp/APKINDEX.tar.gz ./*.apk
	abuild-sign -q -k /keys/sermon-agent.rsa /tmp/APKINDEX.tar.gz
	cp /tmp/APKINDEX.tar.gz /apk/x86_64/
	cp /tmp/APKINDEX.tar.gz /apk/aarch64/'

# The public keys for use.sh.
cp "$keys/gpg.pub.asc" "$keys/sermon-agent.rsa.pub" "$out/keys/"
chmod -R a+rX "$out"
echo "✓ $out: apt, rpm and apk sources with sermon-e2e-demo 2.0.0 (security) and sermon-e2e-extra 1.1.0"
