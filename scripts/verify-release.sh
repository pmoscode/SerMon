#!/bin/sh
# Verifies a published release of SerMon with public keys only (V1-03, ADR 0043): what an admin
# runs before installing it, and what the release workflow runs after publishing it.
#
#   scripts/verify-release.sh [--dir DIR] [--offline] [--require-slsa] [--keys DIR] VERSION
#
# Without --dir the files are downloaded from the GitHub release vVERSION. Checked:
#   - SHA256SUMS is signed with the release key (minisign, as install.sh checks: Ed25519, the
#     signed comment names the file and VERSION) and lists the binaries of agent and helper
#     (amd64, arm64), the 12 packages, the repositories and the SBOM of the agent, each of which
#     matches; each binary is signed itself for its name and VERSION
#   - the repositories: keys/SHA256SUMS signed for VERSION, the OpenPGP keys in them are exactly
#     the committed ones, apt (InRelease) and rpm (repomd.xml.asc) are signed with them, every
#     package in them is one of the release
#   - the SBOMs are CycloneDX
# and, unless --offline (needs cosign; slsa-verifier when installed, with --require-slsa always):
#   - every file has a Sigstore bundle (cosign keyless) from the release workflow on tag vVERSION
#   - the server image: signed by the same identity, the tag VERSION is the digest of the
#     release, its SBOM is attested
#   - the SLSA provenance of the files and of the image
#
# KEYS (default packaging/ of this checkout) holds release.pub (and release-next.pub during a
# rotation), keys/gpg.pub.asc (and keys/next/gpg.pub.asc). SERMON_GITHUB_REPO (pmoscode/SerMon)
# and SERMON_IMAGE (ghcr.io/pmoscode/sermon-server) for forks.
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
repo=${SERMON_GITHUB_REPO:-pmoscode/SerMon}
image=${SERMON_IMAGE:-ghcr.io/pmoscode/sermon-server}
issuer=https://token.actions.githubusercontent.com

die() {
	echo "✗ verify-release: $*" >&2
	exit 1
}
ok() { echo "✓ $*"; }

dir='' offline=false require_slsa=false keys=$root/packaging
while [ $# -gt 1 ]; do
	case $1 in
	--dir) dir=$2 && shift 2 ;;
	--keys) keys=$2 && shift 2 ;;
	--offline) offline=true && shift ;;
	--require-slsa) require_slsa=true && shift ;;
	*) die "unknown option $1" ;;
	esac
done
[ $# -eq 1 ] || die "usage: $0 [--dir DIR] [--offline] [--require-slsa] [--keys DIR] VERSION"
version=$1
echo "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$' || die "not a release version: $version"
tag=v$version
identity="https://github.com/$repo/.github/workflows/release.yml@refs/tags/$tag"
for tool in openssl gpg python3 tar; do command -v "$tool" >/dev/null 2>&1 || die "$tool is needed"; done
[ -s "$keys/release.pub" ] || die "no release key $keys/release.pub"
[ -s "$keys/keys/gpg.pub.asc" ] || die "no OpenPGP key $keys/keys/gpg.pub.asc"

work=$(mktemp -d)
trap 'GNUPGHOME="$work/gnupg" gpgconf --kill gpg-agent 2>/dev/null || true; rm -rf "$work"' EXIT
trap 'exit 130' INT TERM HUP

if [ -z "$dir" ]; then
	dir=$work/release
	mkdir "$dir"
	command -v curl >/dev/null 2>&1 || die "curl is needed to download the release"
	# With GITHUB_TOKEN or GH_TOKEN the API call is authenticated: shared runners share the
	# anonymous rate limit (403). The token goes to curl on stdin, not on its command line.
	token=${GITHUB_TOKEN:-${GH_TOKEN:-}}
	url="https://api.github.com/repos/$repo/releases/tags/$tag"
	if [ -n "$token" ]; then
		printf 'header = "Authorization: Bearer %s"\n' "$token" | curl -fsSL -K - "$url" >"$work/release.json"
	else
		curl -fsSL "$url" >"$work/release.json"
	fi || die "no published release $tag in $repo (rate limit? set GITHUB_TOKEN)"
	python3 -I -c '
import json, re, sys
for a in json.load(open(sys.argv[1]))["assets"]:
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._+-]*", a["name"]):
        sys.exit("odd asset name " + a["name"])
    print(a["name"], a["browser_download_url"])
' "$work/release.json" >"$work/assets" || die "cannot read the assets of $tag"
	while read -r name url; do
		curl -fsSL -o "$dir/$name" "$url" || die "cannot download $name"
	done <"$work/assets"
	ok "downloaded $(wc -l <"$work/assets" | tr -d ' ') files of $repo $tag"
fi
[ -d "$dir" ] || die "no directory $dir"

# --- minisign (as install.sh: Ed25519 over the file, global signature over the trusted comment) ---

b64decode() { openssl base64 -d -A; }
hex() { od -An -v -tx1 | tr -d ' \n'; }

# minisign_ok KEYLINE FILE SIG checks the file signature and the global one with one key.
minisign_ok() {
	m=$work/minisign
	rm -rf "$m" && mkdir "$m"
	printf '%s' "$1" | b64decode >"$m/key" 2>/dev/null || return 1
	sed -n 2p "$3" | b64decode >"$m/sig" 2>/dev/null || return 1
	sed -n 4p "$3" | b64decode >"$m/global" 2>/dev/null || return 1
	[ "$(wc -c <"$m/key" | tr -d ' ')" = 42 ] || return 1
	[ "$(wc -c <"$m/sig" | tr -d ' ')" = 74 ] || return 1
	[ "$(wc -c <"$m/global" | tr -d ' ')" = 64 ] || return 1
	[ "$(dd if="$m/sig" bs=1 count=2 2>/dev/null | hex)" = 4564 ] || return 1
	[ "$(dd if="$m/sig" bs=1 skip=2 count=8 2>/dev/null | hex)" = "$(dd if="$m/key" bs=1 skip=2 count=8 2>/dev/null | hex)" ] || return 1
	dd if="$m/sig" of="$m/sig.raw" bs=1 skip=10 count=64 2>/dev/null
	printf '\060\052\060\005\006\003\053\145\160\003\041\000' >"$m/key.der"
	dd if="$m/key" bs=1 skip=10 count=32 2>/dev/null >>"$m/key.der"
	openssl pkeyutl -verify -pubin -keyform DER -inkey "$m/key.der" -rawin -in "$2" -sigfile "$m/sig.raw" >/dev/null 2>&1 || return 1
	{
		cat "$m/sig.raw"
		sed -n '3s/^trusted comment: //p' "$3" | tr -d '\n'
	} >"$m/global.in"
	openssl pkeyutl -verify -pubin -keyform DER -inkey "$m/key.der" -rawin -in "$m/global.in" -sigfile "$m/global" >/dev/null 2>&1
}

# signed FILE: FILE.minisig is valid for one of the release keys and names FILE and VERSION.
signed() {
	name=$(basename "$1")
	[ -f "$1.minisig" ] || die "no signature $name.minisig"
	good=false
	for key in "$keys/release.pub" "$keys/release-next.pub"; do
		[ -s "$key" ] || continue
		if minisign_ok "$(head -1 "$key")" "$1" "$1.minisig"; then good=true && break; fi
	done
	$good || die "$name: no valid signature of the release key"
	comment=$(sed -n '3s/^trusted comment: //p' "$1.minisig" | tr '\t' '\n')
	[ "$(printf '%s\n' "$comment" | sed -n 's/^file://p')" = "$name" ] || die "$name: the signature belongs to another file"
	[ "$(printf '%s\n' "$comment" | sed -n 's/^version://p')" = "$version" ] || die "$name: the signature is for another version"
}

sha256() { openssl dgst -sha256 -r "$1" | cut -d' ' -f1; }

# --- the files of the release ---------------------------------------------------------------

signed "$dir/SHA256SUMS"
ok "SHA256SUMS signed with the release key for $version"
while read -r sum name; do
	case $name in */* | "" | .*) die "SHA256SUMS names $name" ;; esac
	[ -f "$dir/$name" ] || die "missing: $name"
	[ "$(sha256 "$dir/$name")" = "$sum" ] || die "changed: $name"
done <"$dir/SHA256SUMS"
# listed NAME… counts the names of SHA256SUMS among NAME….
listed() {
	n=0
	for want in "$@"; do
		if cut -d' ' -f3 "$dir/SHA256SUMS" | grep -qxF "$want"; then n=$((n + 1)); fi
	done
	echo "$n"
}
for bin in sermon-agent-linux-amd64 sermon-agent-linux-arm64 sermon-helper-linux-amd64 sermon-helper-linux-arm64; do
	[ "$(listed "$bin" "$bin.minisig")" = 2 ] || die "SHA256SUMS does not list $bin and its signature"
	signed "$dir/$bin"
done
for kind in deb rpm apk; do
	[ "$(listed "sermon-agent-$version-amd64.$kind" "sermon-agent-$version-arm64.$kind" \
		"sermon-helper-$version-amd64.$kind" "sermon-helper-$version-arm64.$kind")" = 4 ] ||
		die "SHA256SUMS does not list the four .$kind"
done
[ "$(listed "sermon-repo-$version.tar.gz")" = 1 ] || die "SHA256SUMS does not list sermon-repo-$version.tar.gz"
[ "$(listed "sermon-agent-$version.cdx.json")" = 1 ] || die "SHA256SUMS does not list the SBOM of the agent"
ok "binaries, packages, repositories and SBOM as signed; binaries signed for their names and $version"

# --- repositories ----------------------------------------------------------------------------

tar -tzf "$dir/sermon-repo-$version.tar.gz" >"$work/repo.list" 2>/dev/null || die "sermon-repo-$version.tar.gz is no tar.gz"
while IFS= read -r entry; do
	case $entry in
	/* | ../* | */../* | *..) die "the repository tarball names $entry" ;;
	apt | apt/* | rpm | rpm/* | apk | apk/* | keys | keys/* | ./ | ./apt* | ./rpm* | ./apk* | ./keys*) ;;
	*) die "the repository tarball holds $entry" ;;
	esac
done <"$work/repo.list"
mkdir "$work/repo"
tar -xzf "$dir/sermon-repo-$version.tar.gz" -C "$work/repo"
links=$(find "$work/repo" -type l)
[ -z "$links" ] || die "links in the repositories: $links"
signed "$work/repo/keys/SHA256SUMS"
(cd "$work/repo/keys" && while read -r sum name; do
	case $name in */* | "") exit 1 ;; esac
	[ "$(sha256 "$name")" = "$sum" ] || exit 1
done <SHA256SUMS) || die "the keys of the repositories don't match their signed SHA256SUMS"

mkdir -m 0700 "$work/gnupg" "$work/gnupg-repo"
export GNUPGHOME="$work/gnupg"
set -- "$keys/keys/gpg.pub.asc"
[ ! -s "$keys/keys/next/gpg.pub.asc" ] || set -- "$@" "$keys/keys/next/gpg.pub.asc"
gpg --batch --quiet --import "$@" 2>/dev/null || die "cannot read the committed OpenPGP keys"
fprs() { gpg --batch --with-colons "$@" 2>/dev/null | awk -F: '$1 == "pub" { want = 1; next } $1 == "fpr" && want { print $10; want = 0 }' | LC_ALL=C sort; }
committed=$(fprs --list-keys)
published=$(GNUPGHOME="$work/gnupg-repo" fprs --import-options show-only --import "$work/repo/keys/sermon.asc")
[ "$committed" = "$published" ] || die "the repositories publish other OpenPGP keys than the committed ones"
gpg_signed() {
	gpg --batch --status-fd 1 --verify "$@" 2>/dev/null | grep -q '^\[GNUPG:\] VALIDSIG ' || return 1
}
gpg_signed "$work/repo/apt/dists/stable/InRelease" || die "apt: InRelease is not signed with the committed key"
gpg_signed "$work/repo/rpm/repodata/repomd.xml.asc" "$work/repo/rpm/repodata/repomd.xml" ||
	die "rpm: repomd.xml is not signed with the committed key"
cut -d' ' -f1 "$dir/SHA256SUMS" >"$work/sums"
find "$work/repo/apt" "$work/repo/rpm" "$work/repo/apk" -type f \( -name '*.deb' -o -name '*.rpm' -o -name '*.apk' \) >"$work/packages"
while IFS= read -r pkg; do
	grep -qx "$(sha256 "$pkg")" "$work/sums" || die "the repositories hold a package that is not one of the release: ${pkg#"$work/repo/"}"
done <"$work/packages"
count=$(wc -l <"$work/packages" | tr -d ' ')
[ "$count" = 12 ] || die "the repositories hold $count packages, not the 12 of the release"
ok "repositories: keys signed for $version, apt and rpm signed with the committed keys, the release's 12 packages"

# --- SBOM ------------------------------------------------------------------------------------

cyclonedx() {
	python3 -I -c '
import json, sys
doc = json.load(open(sys.argv[1]))
if doc.get("bomFormat") != "CycloneDX" or not doc.get("components"):
    sys.exit(1)
' "$1" 2>/dev/null || die "$(basename "$1") is no CycloneDX SBOM with components"
}
cyclonedx "$dir/sermon-agent-$version.cdx.json"
ok "SBOM of agent and helper (CycloneDX)"

if $offline; then
	ok "release $version: offline checks passed (Sigstore and SLSA skipped)"
	exit 0
fi

# --- Sigstore (cosign keyless) and SLSA --------------------------------------------------------

command -v cosign >/dev/null 2>&1 || die "cosign is needed for the online checks (or --offline)"
[ -f "$dir/sermon-server-$version.digest" ] || die "missing: sermon-server-$version.digest"
[ -f "$dir/sermon-server-$version.cdx.json" ] || die "missing: sermon-server-$version.cdx.json"
[ -f "$dir/sermon-$version.intoto.jsonl" ] || die "missing: sermon-$version.intoto.jsonl"
cyclonedx "$dir/sermon-server-$version.cdx.json"
files=$(cd "$dir" && find . -maxdepth 1 -type f ! -name '*.sigstore.json' ! -name '*.intoto.jsonl' | sed 's#^\./##' | LC_ALL=C sort)
for name in $files; do
	[ -f "$dir/$name.sigstore.json" ] || die "$name has no Sigstore bundle"
	cosign verify-blob --bundle "$dir/$name.sigstore.json" --certificate-identity "$identity" \
		--certificate-oidc-issuer "$issuer" "$dir/$name" >/dev/null 2>&1 ||
		die "$name: no valid Sigstore signature of $identity"
done
ok "every file signed by the release workflow on $tag (Sigstore)"

ref=$(cat "$dir/sermon-server-$version.digest")
digest=${ref#"$image@"}
if ! echo "$digest" | grep -Eq '^sha256:[0-9a-f]{64}$' || [ "$ref" != "$image@$digest" ]; then
	die "sermon-server-$version.digest names $ref"
fi
cosign verify --certificate-identity "$identity" --certificate-oidc-issuer "$issuer" "$image@$digest" >/dev/null 2>&1 ||
	die "the image $image@$digest is not signed by $identity"
cosign verify --certificate-identity "$identity" --certificate-oidc-issuer "$issuer" "$image:$version" >"$work/tag.json" 2>/dev/null ||
	die "the image $image:$version is not signed by $identity"
python3 -I -c '
import json, sys
found = {s["critical"]["image"]["docker-manifest-digest"] for s in json.load(open(sys.argv[1]))}
sys.exit(0 if found == {sys.argv[2]} else 1)
' "$work/tag.json" "$digest" || die "the tag $version is not the image of the release ($digest)"
cosign verify-attestation --type cyclonedx --certificate-identity "$identity" --certificate-oidc-issuer "$issuer" \
	"$image@$digest" >/dev/null 2>&1 || die "the image has no SBOM attested by $identity"
ok "image $image@$digest: signed, tag $version, SBOM attested"

if command -v slsa-verifier >/dev/null 2>&1; then
	# Everything but the digest file, which is written after the image is pushed: the image has a
	# provenance of its own.
	set --
	for name in $files; do
		[ "$name" = "sermon-server-$version.digest" ] || set -- "$@" "$dir/$name"
	done
	slsa-verifier verify-artifact --provenance-path "$dir/sermon-$version.intoto.jsonl" --source-uri "github.com/$repo" \
		--source-tag "$tag" "$@" >/dev/null 2>&1 || die "the SLSA provenance does not cover the files"
	slsa-verifier verify-image "$image@$digest" --source-uri "github.com/$repo" --source-tag "$tag" >/dev/null 2>&1 ||
		die "the image has no valid SLSA provenance"
	ok "SLSA provenance of files and image (built from $repo at $tag)"
elif $require_slsa; then
	die "slsa-verifier is needed (--require-slsa)"
else
	echo "… slsa-verifier not installed: provenance not checked"
fi
ok "release $version verified"
