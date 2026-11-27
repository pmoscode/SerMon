#!/bin/sh
# Tests for release-artifacts.sh. Run: sh scripts/release-artifacts_test.sh (needs openssl)
set -eu

dir=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
fail() {
	echo "FAIL: $*"
	exit 1
}
# refused DESCRIPTION ARGS… must fail; with WHY set, for that reason.
refused() {
	description=$1
	shift
	if sh "$dir/release-artifacts.sh" "$@" >"$work/log" 2>&1; then fail "$description passed"; fi
	if [ -n "${WHY:-}" ] && ! grep -q "$WHY" "$work/log"; then fail "$description: refused for another reason: $(cat "$work/log")"; fi
	WHY=
}

fresh() {
	rm -rf "$work/a"
	mkdir -p "$work/a/sub"
	printf 'agent' >"$work/a/sermon-agent-linux-amd64"
	printf 'sig' >"$work/a/sermon-agent-linux-amd64.minisig"
	printf 'pkg' >"$work/a/sub/sermon-agent_1.0.0_amd64.deb"
	hash=$(sh "$dir/release-artifacts.sh" seal "$work/a")
}

fresh
echo "$hash" | grep -Eq '^[0-9a-f]{64}$' || fail "seal printed $hash, not a SHA-256"
[ -r "$work/a/SEAL.sha256sums" ] || fail "seal wrote no SEAL.sha256sums"
[ "$(wc -l <"$work/a/SEAL.sha256sums" | tr -d ' ')" = 3 ] || fail "the seal lists $(cat "$work/a/SEAL.sha256sums")"
grep -q '  sub/sermon-agent_1.0.0_amd64.deb$' "$work/a/SEAL.sha256sums" || fail "the seal misses a file below a directory"
[ "$(openssl dgst -sha256 -r "$work/a/SEAL.sha256sums" | cut -d' ' -f1)" = "$hash" ] || fail "the printed hash is not the seal's"
sh "$dir/release-artifacts.sh" check "$work/a" "$hash" >/dev/null || fail "an untouched directory was refused"

# Sealing again gives the same hash: the order does not depend on the file system.
[ "$(sh "$dir/release-artifacts.sh" seal "$work/a")" = "$hash" ] || fail "sealing twice gave two hashes"

refused "a wrong hash" check "$work/a" "$(printf '%064d' 0)"
refused "a hash that is no SHA-256" check "$work/a" "${hash%?}"
refused "no hash" check "$work/a"

printf 'evil' >"$work/a/sermon-agent-linux-amd64"
refused "a changed file" check "$work/a" "$hash"

fresh
printf 'extra' >"$work/a/install.sh"
refused "an extra file" check "$work/a" "$hash"

fresh
rm "$work/a/sermon-agent-linux-amd64.minisig"
WHY="missing: sermon-agent-linux-amd64.minisig" refused "a missing file" check "$work/a" "$hash"

# Changing file and seal together needs the hash from the job output.
fresh
printf 'evil' >"$work/a/sermon-agent-linux-amd64"
forged=$(sh "$dir/release-artifacts.sh" seal "$work/a")
refused "a re-sealed directory against the original hash" check "$work/a" "$hash"
sh "$dir/release-artifacts.sh" check "$work/a" "$forged" >/dev/null || fail "the forged seal is not even consistent"

# A seal that names a file outside the directory.
fresh
printf '%s  ../outside\n' "$(openssl dgst -sha256 -r "$work/a/sermon-agent-linux-amd64" | cut -d' ' -f1)" >>"$work/a/SEAL.sha256sums"
cp "$work/a/sermon-agent-linux-amd64" "$work/outside"
refused "a path out of the directory" check "$work/a" "$(openssl dgst -sha256 -r "$work/a/SEAL.sha256sums" | cut -d' ' -f1)"

# Links and odd names are refused when sealing.
fresh
ln -s /etc/passwd "$work/a/link"
refused "a symlink" seal "$work/a"
fresh
printf 'x' >"$work/a/with space"
refused "a name with a space" seal "$work/a"
refused "a missing directory" seal "$work/missing"

echo "✓ release-artifacts: sealed directories pass, changed, extra, missing and re-sealed files are refused"
