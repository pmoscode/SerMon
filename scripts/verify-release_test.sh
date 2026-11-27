#!/bin/sh
# Tests for verify-release.sh against a release made here with test keys (scripts/release-sign.sh,
# scripts/package-test-keys.sh): offline with every way to break it, and the online checks with
# a fake cosign and slsa-verifier. Run: sh scripts/verify-release_test.sh (needs openssl, gpg, python3)
set -eu

dir=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'GNUPGHOME="$work/gnupg-sign" gpgconf --kill gpg-agent 2>/dev/null || true; rm -rf "$work"' EXIT
fail() {
	echo "FAIL: $*"
	exit 1
}
V=1.2.3-rc.4
image=ghcr.io/pmoscode/sermon-server
digest=sha256:$(printf 'a%.0s' $(seq 64))

# The committed public keys of the test: release key, its successor, OpenPGP and apk.
mkdir -p "$work/keys/keys"
sh "$dir/release-sign.sh" generate "$work/release.pem" >"$work/keys/release.pub"
sh "$dir/release-sign.sh" generate "$work/next.pem" >"$work/release-next.pub"
sh "$dir/release-sign.sh" generate "$work/other.pem" >/dev/null
sh "$dir/package-test-keys.sh" "$work/pkg" >/dev/null
sh "$dir/package-test-keys.sh" "$work/pkg-other" >/dev/null
cp "$work/pkg/gpg.pub.asc" "$work/pkg/apk.rsa.pub" "$work/keys/keys/"

# resign DIR lists the files of DIR in SHA256SUMS again and signs it with the release key: a
# release whose signature holds, with what the case changed.
resign() {
	(cd "$1" && for f in *; do
		case $f in SHA256SUMS*) ;; *) printf '%s  %s\n' "$(openssl dgst -sha256 -r "$f" | cut -d' ' -f1)" "$f" ;; esac
	done >"$work/SHA256SUMS")
	mv "$work/SHA256SUMS" "$1/SHA256SUMS"
	sh "$dir/release-sign.sh" sign "${2:-$work/release.pem}" --version "$V" "$1/SHA256SUMS"
}

# make_release OUT [RELEASE_KEY [OPENPGP_KEYS]] builds the files of release V into OUT as the
# workflow does.
make_release() {
	out=$1 key=${2:-$work/release.pem} gpg_keys=${3:-$work/pkg}
	rm -rf "$out" "$work/repo-src"
	mkdir -p "$out"
	for b in agent helper; do
		for a in amd64 arm64; do
			printf '%s %s %s\n' "$b" "$a" "$V" >"$out/sermon-$b-linux-$a"
			for k in deb rpm apk; do printf 'package %s %s %s\n' "$b" "$a" "$k" >"$out/sermon-$b-$V-$a.$k"; done
		done
	done
	sh "$dir/release-sign.sh" sign "$key" --version "$V" "$out"/sermon-agent-linux-* "$out"/sermon-helper-linux-*
	printf '{"bomFormat":"CycloneDX","specVersion":"1.6","components":[{"type":"library","name":"tokio"}]}\n' >"$out/sermon-agent-$V.cdx.json"

	# The repositories: the release's packages under the names of nfpm, signed indexes and keys.
	r=$work/repo-src
	mkdir -p "$r/apt/pool/main" "$r/apt/dists/stable" "$r/rpm/repodata" "$r/apk/x86_64" "$r/apk/aarch64"
	for b in agent helper; do
		cp "$out/sermon-$b-$V-amd64.deb" "$r/apt/pool/main/sermon-${b}_1.2.3~rc.4-1_amd64.deb"
		cp "$out/sermon-$b-$V-arm64.deb" "$r/apt/pool/main/sermon-${b}_1.2.3~rc.4-1_arm64.deb"
		cp "$out/sermon-$b-$V-amd64.rpm" "$r/rpm/sermon-$b-1.2.3~rc.4-1.x86_64.rpm"
		cp "$out/sermon-$b-$V-arm64.rpm" "$r/rpm/sermon-$b-1.2.3~rc.4-1.aarch64.rpm"
		cp "$out/sermon-$b-$V-amd64.apk" "$r/apk/x86_64/sermon-$b-1.2.3_rc4-r1.apk"
		cp "$out/sermon-$b-$V-arm64.apk" "$r/apk/aarch64/sermon-$b-1.2.3_rc4-r1.apk"
	done
	printf 'Origin: SerMon\nSuite: stable\n' >"$r/apt/dists/stable/Release"
	printf '<repomd/>\n' >"$r/rpm/repodata/repomd.xml"
	home=$work/gnupg-sign
	rm -rf "$home" && mkdir -m 0700 "$home"
	GNUPGHOME=$home gpg --batch --quiet --import "$gpg_keys/gpg.asc" 2>/dev/null
	GNUPGHOME=$home gpg --batch --quiet --yes --clearsign -o "$r/apt/dists/stable/InRelease" "$r/apt/dists/stable/Release"
	GNUPGHOME=$home gpg --batch --quiet --yes --armor --detach-sign -o "$r/rpm/repodata/repomd.xml.asc" "$r/rpm/repodata/repomd.xml"
	GNUPGHOME=$home gpgconf --kill gpg-agent 2>/dev/null || true
	sh "$dir/repo-keys.sh" publish "$work/pkg" "$r/keys"
	sh "$dir/release-sign.sh" sign "$key" --version "$V" "$r/keys/SHA256SUMS"
	tar -C "$r" -czf "$out/sermon-repo-$V.tar.gz" apt rpm apk keys
	resign "$out" "$key"
}

# The files the workflow adds after signing: image digest and SBOM, provenance, a Sigstore bundle
# for each file (the fake cosign takes the word "good" in it as a valid one).
online_files() {
	out=$1
	printf '%s@%s\n' "$image" "$digest" >"$out/sermon-server-$V.digest"
	printf '{"bomFormat":"CycloneDX","specVersion":"1.6","components":[{"type":"library","name":"connectrpc.com/connect"}]}\n' >"$out/sermon-server-$V.cdx.json"
	printf '{}\n' >"$out/sermon-$V.intoto.jsonl"
	for f in "$out"/*; do
		case $f in *.intoto.jsonl) continue ;; esac
		printf 'good\n' >"$f.sigstore.json"
	done
}

verify() { sh "$dir/verify-release.sh" --keys "$work/keys" "$@"; }
pass() {
	description=$1
	shift
	verify "$@" >"$work/log" 2>&1 || fail "$description: refused – $(cat "$work/log")"
}
refused() {
	description=$1
	shift
	if verify "$@" >"$work/log" 2>&1; then fail "$description: passed"; fi
}
# refused_for WHY DESCRIPTION ARGS… must fail for that reason (its message), not a later check.
refused_for() {
	why=$1
	shift
	refused "$@"
	grep -q "$why" "$work/log" || fail "$1: refused for another reason: $(tail -1 "$work/log")"
}

make_release "$work/good"
pass "an untouched release" --offline --dir "$work/good" "$V"
refused "another version" --offline --dir "$work/good" 1.2.3
refused "a version that is no release version" --offline --dir "$work/good" "1.2.3-beta.1"

# copy NAME makes $work/r a copy of the good release.
copy() { rm -rf "$work/r" && cp -R "$work/good" "$work/r"; }

copy && printf 'evil\n' >>"$work/r/sermon-agent-linux-amd64"
refused "a changed binary" --offline --dir "$work/r" "$V"
copy && rm "$work/r/sermon-helper-$V-arm64.rpm"
refused "a missing package" --offline --dir "$work/r" "$V"
copy && printf 'evil\n' >>"$work/r/sermon-helper-$V-arm64.apk"
refused "a changed package" --offline --dir "$work/r" "$V"

# Changed and re-hashed: the signature of SHA256SUMS no longer holds.
copy && printf 'evil\n' >>"$work/r/sermon-agent-linux-amd64"
(cd "$work/r" && for f in sermon-*; do printf '%s  %s\n' "$(openssl dgst -sha256 -r "$f" | cut -d' ' -f1)" "$f"; done >SHA256SUMS)
refused "a re-hashed SHA256SUMS" --offline --dir "$work/r" "$V"

# Signed with a key that is not the release key, or for another version: the whole release.
make_release "$work/r" "$work/other.pem"
refused "a release signed with another key" --offline --dir "$work/r" "$V"
V=1.2.2
make_release "$work/r-old"
V=1.2.3-rc.4
refused "the files of another version" --offline --dir "$work/r-old" "$V"

# A binary with the signature of another binary, or of the same file for another version.
copy && cp "$work/r/sermon-helper-linux-amd64" "$work/r/sermon-agent-linux-amd64" &&
	cp "$work/r/sermon-helper-linux-amd64.minisig" "$work/r/sermon-agent-linux-amd64.minisig"
resign "$work/r"
refused "a binary with the signature of another file" --offline --dir "$work/r" "$V"
copy && sh "$dir/release-sign.sh" sign "$work/release.pem" --version 9.9.9 "$work/r/sermon-agent-linux-arm64"
resign "$work/r"
refused "a binary signed for another version" --offline --dir "$work/r" "$V"

# A trusted comment changed by hand: the file signature holds, the global one not.
copy && cp "$work/r/sermon-helper-linux-arm64.minisig" "$work/sig"
awk 'NR == 3 { sub(/timestamp:[0-9]+/, "timestamp:1") } { print }' "$work/sig" >"$work/r/sermon-helper-linux-arm64.minisig"
resign "$work/r"
refused_for "sermon-helper-linux-arm64: no valid signature" "a trusted comment changed by hand" --offline --dir "$work/r" "$V"

# SHA256SUMS that leaves a file out (a valid signature over too little).
copy && grep -v "sermon-agent-$V-arm64.deb" "$work/r/SHA256SUMS" >"$work/sums" && mv "$work/sums" "$work/r/SHA256SUMS"
sh "$dir/release-sign.sh" sign "$work/release.pem" --version "$V" "$work/r/SHA256SUMS"
refused "SHA256SUMS without a package" --offline --dir "$work/r" "$V"

# A rotation: the release is signed with the next key, which the checkout knows.
make_release "$work/r" "$work/next.pem"
refused "the next key before it is committed" --offline --dir "$work/r" "$V"
cp "$work/release-next.pub" "$work/keys/release-next.pub"
pass "a release signed with the next key after it is committed" --offline --dir "$work/r" "$V"
rm "$work/keys/release-next.pub"

# Repositories signed with another OpenPGP key, publishing other keys, or holding other packages.
make_release "$work/r" "$work/release.pem" "$work/pkg-other"
refused "repositories signed with another OpenPGP key" --offline --dir "$work/r" "$V"
make_release "$work/r"
mkdir "$work/x" && tar -xzf "$work/r/sermon-repo-$V.tar.gz" -C "$work/x"
printf 'extra package\n' >"$work/x/apk/x86_64/sermon-agent-9.9.9-r1.apk"
tar -C "$work/x" -czf "$work/r/sermon-repo-$V.tar.gz" apt rpm apk keys
resign "$work/r"
refused "repositories with a package that is not the release's" --offline --dir "$work/r" "$V"
rm -rf "$work/x" && mkdir "$work/x" && tar -xzf "$work/good/sermon-repo-$V.tar.gz" -C "$work/x"
cp "$work/pkg-other/gpg.pub.asc" "$work/x/keys/sermon.asc"
tar -C "$work/x" -czf "$work/r/sermon-repo-$V.tar.gz" apt rpm apk keys
resign "$work/r"
refused "repositories with other keys than their signed list" --offline --dir "$work/r" "$V"
python3 -I -c '
import io, sys, tarfile
with tarfile.open(sys.argv[1], "w:gz") as t:
    data = b"x\n"
    info = tarfile.TarInfo("apt/../../evil")
    info.size = len(data)
    t.addfile(info, io.BytesIO(data))
' "$work/r/sermon-repo-$V.tar.gz"
resign "$work/r"
refused "a repository tarball with a path out of it" --offline --dir "$work/r" "$V"

# repo_case: $work/r is the good release whose repositories (in $work/x) the case changed.
repo_case() {
	tar -C "$work/x" -czf "$work/r/sermon-repo-$V.tar.gz" apt rpm apk keys
	resign "$work/r"
}
unpacked() { copy && rm -rf "$work/x" && mkdir "$work/x" && tar -xzf "$work/good/sermon-repo-$V.tar.gz" -C "$work/x"; }
other_sign() {
	h=$work/gnupg-other
	rm -rf "$h" && mkdir -m 0700 "$h"
	GNUPGHOME=$h gpg --batch --quiet --import "$work/pkg-other/gpg.asc" 2>/dev/null
	GNUPGHOME=$h gpg --batch --quiet --yes "$@"
	GNUPGHOME=$h gpgconf --kill gpg-agent 2>/dev/null || true
}
unpacked && other_sign --clearsign -o "$work/x/apt/dists/stable/InRelease" "$work/x/apt/dists/stable/Release" && repo_case
refused_for "apt: InRelease" "apt signed with another OpenPGP key" --offline --dir "$work/r" "$V"
unpacked && other_sign --armor --detach-sign -o "$work/x/rpm/repodata/repomd.xml.asc" "$work/x/rpm/repodata/repomd.xml" && repo_case
refused_for "rpm: repomd.xml" "rpm signed with another OpenPGP key" --offline --dir "$work/r" "$V"
unpacked && sh "$dir/repo-keys.sh" publish "$work/pkg-other" "$work/x/keys" &&
	sh "$dir/release-sign.sh" sign "$work/release.pem" --version "$V" "$work/x/keys/SHA256SUMS" && repo_case
refused_for "other OpenPGP keys than the committed" "repositories publishing another key in a signed list" --offline --dir "$work/r" "$V"
unpacked && printf 'other package\n' >"$work/x/apk/x86_64/sermon-agent-1.2.3_rc4-r1.apk" && repo_case
refused_for "not one of the release" "a package in the repositories replaced" --offline --dir "$work/r" "$V"
unpacked && (cd "$work/x" && tar -cf "$work/x.tar" apt rpm apk keys)
python3 -I -c '
import io, sys, tarfile
with tarfile.open(sys.argv[1], "a") as t:
    data = b"x\n"
    info = tarfile.TarInfo("apt/../../evil")
    info.size = len(data)
    t.addfile(info, io.BytesIO(data))
' "$work/x.tar"
gzip -c "$work/x.tar" >"$work/r/sermon-repo-$V.tar.gz"
resign "$work/r"
refused_for "names apt/../../evil" "good repositories with a path out of them" --offline --dir "$work/r" "$V"

# SBOM: missing or not CycloneDX.
copy && printf '{"bomFormat":"SPDX","components":[{"name":"tokio"}]}\n' >"$work/r/sermon-agent-$V.cdx.json"
resign "$work/r"
refused_for "no CycloneDX" "an SBOM that is no CycloneDX" --offline --dir "$work/r" "$V"

# --- online, with fakes --------------------------------------------------------------------
mkdir "$work/bin"
cat >"$work/bin/cosign" <<'EOF'
#!/bin/sh
cmd=$1
shift
id='' iss='' bundle=''
while [ $# -gt 1 ]; do
	case $1 in
	--certificate-identity) id=$2 && shift 2 ;;
	--certificate-oidc-issuer) iss=$2 && shift 2 ;;
	--bundle) bundle=$2 && shift 2 ;;
	--type) shift 2 ;;
	*) shift ;;
	esac
done
[ "$id" = "https://github.com/pmoscode/SerMon/.github/workflows/release.yml@refs/tags/v$FAKE_VERSION" ] || exit 1
[ "$iss" = https://token.actions.githubusercontent.com ] || exit 1
case $cmd in
verify-blob) grep -qx good "$bundle" ;;
verify) printf '[{"critical":{"image":{"docker-manifest-digest":"%s"}}}]\n' "${FAKE_TAG_DIGEST:-$FAKE_DIGEST}" ;;
verify-attestation) [ -z "${FAKE_NO_ATTESTATION:-}" ] ;;
*) exit 1 ;;
esac
EOF
cat >"$work/bin/slsa-verifier" <<'EOF'
#!/bin/sh
exit "${FAKE_SLSA_EXIT:-0}"
EOF
chmod +x "$work/bin/cosign" "$work/bin/slsa-verifier"
export FAKE_VERSION="$V" FAKE_DIGEST="$digest"
online() { PATH="$work/bin:$PATH" verify --dir "$work/r" "$V"; }
online_pass() { online >"$work/log" 2>&1 || fail "$1: refused – $(cat "$work/log")"; }
online_refused() {
	if online >"$work/log" 2>&1; then fail "$1: passed"; fi
	[ -z "${2:-}" ] || grep -q "$2" "$work/log" || fail "$1: refused for another reason: $(tail -1 "$work/log")"
}

copy && online_files "$work/r"
online_pass "a release with its Sigstore bundles, image and provenance"
grep -q "SLSA provenance" "$work/log" || fail "the provenance was not checked: $(cat "$work/log")"
copy && online_files "$work/r" && printf 'bad\n' >"$work/r/sermon-repo-$V.tar.gz.sigstore.json"
online_refused "a file whose Sigstore bundle does not verify"
copy && online_files "$work/r" && rm "$work/r/SHA256SUMS.minisig.sigstore.json"
online_refused "a file without a Sigstore bundle"
copy && online_files "$work/r" && printf '%s@sha256:%s\n' "ghcr.io/evil/sermon-server" "${digest#sha256:}" >"$work/r/sermon-server-$V.digest" &&
	printf 'good\n' >"$work/r/sermon-server-$V.digest.sigstore.json"
online_refused "a digest of another image" "digest names ghcr.io/evil"
copy && online_files "$work/r"
# In subshells: an assignment before a function call stays in some shells.
other_digest=sha256:$(printf 'b%.0s' $(seq 64))
(export FAKE_TAG_DIGEST="$other_digest" && online_refused "a tag that points at another image")
(export FAKE_NO_ATTESTATION=1 && online_refused "an image without an attested SBOM")
(export FAKE_SLSA_EXIT=1 && online_refused "provenance that does not verify")
(export FAKE_VERSION=1.2.2 && online_refused "signatures of the workflow on another tag")
copy && online_files "$work/r" && rm "$work/r/sermon-$V.intoto.jsonl"
online_refused "a release without provenance"
copy
online_refused "the online checks without the files made after signing"
# Without cosign the online checks don't pass (PATH without the fake and without a real one).
if ! command -v cosign >/dev/null 2>&1; then
	copy && online_files "$work/r"
	refused "the online checks without cosign" --dir "$work/r" "$V"
fi

echo "✓ verify-release: a good release passes, offline and online; changed, re-signed, foreign and incomplete ones are refused"
