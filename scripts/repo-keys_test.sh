#!/bin/sh
# Tests for repo-keys.sh. Run: sh scripts/repo-keys_test.sh (needs gpg and openssl)
set -eu

dir=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
fail() {
	echo "FAIL: $*"
	exit 1
}
sha256() { openssl dgst -sha256 <"$1" | sed 's/^.*= //'; }
# keys_in FILE counts the OpenPGP certificates in FILE.
keys_in() { GNUPGHOME="$work/gnupg" gpg --batch --show-keys --with-colons "$1" 2>/dev/null | grep -c '^pub:'; }
mkdir -m 0700 "$work/gnupg"

sh "$dir/package-test-keys.sh" "$work/keys" >/dev/null
sh "$dir/package-test-keys.sh" "$work/next" >/dev/null
[ -r "$work/keys/apk.rsa.pub" ] || fail "package-test-keys.sh wrote no apk.rsa.pub"

name=$(sh "$dir/repo-keys.sh" apk-name "$work/keys/apk.rsa.pub")
echo "$name" | grep -Eq '^sermon-[0-9a-f]{16}$' || fail "apk key name $name is not sermon-<16 hex digits>"
[ "$(sh "$dir/repo-keys.sh" apk-name "$work/keys/apk.rsa.pub")" = "$name" ] || fail "the apk key name is not stable"
# The name is the key's: the private key (with -pubout) gets the same one.
openssl pkey -in "$work/keys/apk.rsa" -pubout -out "$work/derived.pub" 2>/dev/null
[ "$(sh "$dir/repo-keys.sh" apk-name "$work/derived.pub")" = "$name" ] || fail "the same key in another PEM file got another name"
next=$(sh "$dir/repo-keys.sh" apk-name "$work/next/apk.rsa.pub")
[ "$next" != "$name" ] || fail "two keys got the same name $name"

sh "$dir/repo-keys.sh" publish "$work/keys" "$work/out" >/dev/null
files() { find "$work/out" -type f | sed 's#.*/##' | LC_ALL=C sort | tr '\n' ' '; }
[ "$(files)" = "SHA256SUMS $name.rsa.pub sermon.asc " ] || fail "published $(files)"
cmp -s "$work/out/$name.rsa.pub" "$work/keys/apk.rsa.pub" || fail "the apk key changed on the way"
[ "$(keys_in "$work/out/sermon.asc")" = 1 ] || fail "sermon.asc does not hold the one OpenPGP key"
printf '%s  %s\n%s  %s\n' "$(sha256 "$work/out/$name.rsa.pub")" "$name.rsa.pub" "$(sha256 "$work/out/sermon.asc")" sermon.asc >"$work/expected"
cmp -s "$work/expected" "$work/out/SHA256SUMS" || fail "SHA256SUMS: $(cat "$work/out/SHA256SUMS")"

# A rotation: the next keys go along, apk under their own name, OpenPGP in the same armored block.
mkdir "$work/keys/next"
cp "$work/next/gpg.pub.asc" "$work/next/apk.rsa.pub" "$work/keys/next/"
sh "$dir/repo-keys.sh" publish "$work/keys" "$work/out" >/dev/null
if [ ! -r "$work/out/$name.rsa.pub" ] || [ ! -r "$work/out/$next.rsa.pub" ]; then fail "with next/: $(files)"; fi
cmp -s "$work/out/$next.rsa.pub" "$work/next/apk.rsa.pub" || fail "the next apk key changed on the way"
[ "$(keys_in "$work/out/sermon.asc")" = 2 ] || fail "sermon.asc does not hold both OpenPGP keys"
[ "$(grep -c 'BEGIN PGP PUBLIC KEY BLOCK' "$work/out/sermon.asc")" = 1 ] || fail "sermon.asc has more than one armored block"
[ "$(wc -l <"$work/out/SHA256SUMS" | tr -d ' ')" = 3 ] || fail "SHA256SUMS: $(cat "$work/out/SHA256SUMS")"
(cd "$work/out" && while read -r sum file; do [ "$(sha256 "$file")" = "$sum" ] || exit 1; done <SHA256SUMS) || fail "SHA256SUMS does not match the files"

# The next apk key must be another key; without the current one nothing is published.
cp "$work/keys/apk.rsa.pub" "$work/keys/next/apk.rsa.pub"
if sh "$dir/repo-keys.sh" publish "$work/keys" "$work/out2" >/dev/null 2>&1; then
	fail "next/ with the current apk key passed"
fi
rm "$work/keys/next/apk.rsa.pub" "$work/keys/apk.rsa.pub"
if sh "$dir/repo-keys.sh" publish "$work/keys" "$work/out3" >/dev/null 2>&1; then
	fail "KEYS without apk.rsa.pub passed"
fi
echo "✓ repo-keys: apk keys named by fingerprint, current and next published with their SHA-256"
