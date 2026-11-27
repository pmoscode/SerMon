#!/bin/sh
# Tests for release-keys.sh. Run: sh scripts/release-keys_test.sh (needs openssl, gpg and age)
set -eu

dir=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'GNUPGHOME="$work/gnupg" gpgconf --kill gpg-agent 2>/dev/null || true; rm -rf "$work"' EXIT
fail() {
	echo "FAIL: $*"
	exit 1
}
# age as release-keys.sh finds it: the CLI, else the version of the server's go.mod.
if ! command -v age >/dev/null 2>&1; then
	command -v go >/dev/null 2>&1 || {
		echo "⏭  release-keys: neither age nor Go installed"
		exit 0
	}
	age_version=$(sed -n 's#^[[:space:]]*filippo.io/age \(v[^ ]*\).*#\1#p' "$dir/../server/go.mod" | head -1)
	age() { go run "filippo.io/age/cmd/age@$age_version" "$@"; }
	age_keygen() { go run "filippo.io/age/cmd/age-keygen@$age_version" "$@"; }
else
	age_keygen() { age-keygen "$@"; }
fi
mkdir -m 0700 "$work/gnupg" "$work/tmp"
age_keygen -o "$work/identity" 2>/dev/null
recipient=$(age_keygen -y "$work/identity")

# A fake gh records what it is asked to set and the value it reads.
mkdir "$work/bin"
cat >"$work/bin/gh" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >>"$GH_LOG"
cat >"$GH_LOG.$3"
EOF
chmod +x "$work/bin/gh"

# Everything the script writes to temporary files goes below $work/tmp, which must be empty after.
TMPDIR=$work/tmp PATH="$work/bin:$PATH" GH_LOG=$work/gh.log sh "$dir/release-keys.sh" generate \
	--public "$work/packaging" --bundle "$work/bundle.tar.age" --recipient "$recipient" \
	--set-secrets pmoscode/SerMon >"$work/out" 2>&1 || fail "generate failed: $(cat "$work/out")"
[ -z "$(ls -A "$work/tmp")" ] || fail "left in the temporary directory: $(ls -A "$work/tmp")"

p=$work/packaging
for f in release.pub release-next.pub keys/gpg.pub.asc keys/apk.rsa.pub keys/next/gpg.pub.asc keys/next/apk.rsa.pub; do
	[ -s "$p/$f" ] || fail "no $f"
done
[ "$(find "$p" -type f | wc -l | tr -d ' ')" = 6 ] || fail "more than the public keys: $(find "$p" -type f)"
if grep -rl 'PRIVATE' "$p" >/dev/null; then fail "private key material in the public directory"; fi
if grep -q 'PRIVATE' "$work/out"; then fail "the output shows private key material"; fi
[ "$(openssl base64 -d -A <"$p/release.pub" | wc -c | tr -d ' ')" = 42 ] || fail "release.pub is no minisign public key"
cmp -s "$p/release.pub" "$p/release-next.pub" && fail "current and next release key are the same"
cmp -s "$p/keys/gpg.pub.asc" "$p/keys/next/gpg.pub.asc" && fail "current and next OpenPGP key are the same"
[ "$(sh "$dir/repo-keys.sh" apk-name "$p/keys/apk.rsa.pub")" != "$(sh "$dir/repo-keys.sh" apk-name "$p/keys/next/apk.rsa.pub")" ] ||
	fail "current and next apk key are the same"
bits=$(GNUPGHOME="$work/gnupg" gpg --batch --with-colons --import-options show-only --import "$p/keys/gpg.pub.asc" 2>/dev/null | awk -F: '$1 == "pub" { print $3 ":" $12 }')
[ "$bits" = "4096:scSC" ] || [ "$bits" = "4096:sSC" ] || [ "$bits" = "4096:scS" ] || [ "$bits" = "4096:sS" ] ||
	fail "the OpenPGP key is $bits, not RSA 4096 for signing"
openssl pkey -pubin -in "$p/keys/apk.rsa.pub" -text -noout 2>/dev/null | grep -q '4096 bit' || fail "the apk key is not RSA 4096"

# The bundle opens with the identity and holds both sets, whose private keys fit the public ones.
age -d -i "$work/identity" "$work/bundle.tar.age" | tar -xf - -C "$work" || fail "the bundle does not open"
for set in current next; do
	for f in release.pem release.pub gpg.asc gpg.pub.asc apk.rsa apk.rsa.pub; do
		[ -s "$work/keys/$set/$f" ] || fail "the bundle has no $set/$f"
	done
done
grep -q 'OpenPGP current:' "$work/keys/README.txt" || fail "the bundle has no README"
sh "$dir/release-keys.sh" check "$work/keys/current" "$p" >/dev/null || fail "the current keys don't fit the public ones"
mkdir -p "$work/next-view/keys"
cp "$p/release-next.pub" "$work/next-view/release.pub"
cp "$p/keys/next/gpg.pub.asc" "$p/keys/next/apk.rsa.pub" "$work/next-view/keys/"
sh "$dir/release-keys.sh" check "$work/keys/next" "$work/next-view" >/dev/null || fail "the next keys don't fit the public ones"

# Only the current private keys went to GitHub, one secret each, in the environment "release".
[ "$(wc -l <"$work/gh.log" | tr -d ' ')" = 3 ] || fail "gh was called: $(cat "$work/gh.log")"
for entry in SERMON_RELEASE_SIGNING_KEY:release.pem SERMON_PKG_GPG_KEY:gpg.asc SERMON_PKG_APK_KEY:apk.rsa; do
	grep -qx "secret set ${entry%%:*} --env release --repo pmoscode/SerMon" "$work/gh.log" || fail "${entry%%:*} not set: $(cat "$work/gh.log")"
	cmp -s "$work/gh.log.${entry%%:*}" "$work/keys/current/${entry#*:}" || fail "${entry%%:*} is not current/${entry#*:}"
done

# check refuses keys that are not the committed ones, and an OpenPGP key with a passphrase.
refused() {
	if sh "$dir/release-keys.sh" check "$1" "$p" >"$work/log" 2>&1; then fail "check passed: $2"; fi
	if [ -n "${3:-}" ] && ! grep -q "$3" "$work/log"; then fail "$2: refused for another reason: $(cat "$work/log")"; fi
}
mkdir "$work/mixed"
cp "$work/keys/current/release.pem" "$work/keys/current/gpg.asc" "$work/keys/current/apk.rsa" "$work/mixed/"
cp "$work/keys/next/release.pem" "$work/mixed/release.pem"
refused "$work/mixed" "the next release key as the current one"
cp "$work/keys/current/release.pem" "$work/mixed/release.pem"
cp "$work/keys/next/gpg.asc" "$work/mixed/gpg.asc"
refused "$work/mixed" "the next OpenPGP key as the current one"
cp "$work/keys/current/gpg.asc" "$work/mixed/gpg.asc"
cp "$work/keys/next/apk.rsa" "$work/mixed/apk.rsa"
refused "$work/mixed" "the next apk key as the current one"
cp "$work/keys/current/apk.rsa" "$work/mixed/apk.rsa"
cat "$work/keys/next/gpg.asc" >>"$work/mixed/gpg.asc"
refused "$work/mixed" "two OpenPGP keys in gpg.asc" "exactly one key"
cp "$work/keys/current/gpg.asc" "$work/mixed/gpg.asc"
: >"$work/mixed/apk.rsa"
refused "$work/mixed" "an empty secret"
cp "$work/keys/current/apk.rsa" "$work/mixed/apk.rsa"
sh "$dir/release-keys.sh" check "$work/mixed" "$p" >/dev/null || fail "the restored set was refused"
# An OpenPGP key with a passphrase, committed as such: only the passphrase is wrong.
GNUPGHOME="$work/gnupg" gpg --batch --quiet --pinentry-mode loopback --passphrase secret \
	--quick-gen-key 'SerMon test <test@sermon.invalid>' rsa2048 sign never 2>/dev/null
fpr=$(GNUPGHOME="$work/gnupg" gpg --batch --with-colons --list-secret-keys 2>/dev/null | awk -F: '$1 == "fpr" { print $10; exit }')
GNUPGHOME="$work/gnupg" gpg --batch --quiet --armor --pinentry-mode loopback --passphrase secret --export-secret-keys "$fpr" >"$work/mixed/gpg.asc"
mkdir -p "$work/protected/keys"
cp "$p/release.pub" "$work/protected/"
cp "$p/keys/apk.rsa.pub" "$work/protected/keys/"
GNUPGHOME="$work/gnupg" gpg --batch --quiet --armor --export "$fpr" >"$work/protected/keys/gpg.pub.asc"
if sh "$dir/release-keys.sh" check "$work/mixed" "$work/protected" >/dev/null 2>&1; then
	fail "check passed: an OpenPGP key with a passphrase"
fi

# Never a second key set over the first, never a bundle over a bundle.
if TMPDIR=$work/tmp sh "$dir/release-keys.sh" generate --public "$p" --bundle "$work/other.tar.age" --recipient "$recipient" >/dev/null 2>&1; then
	fail "a second generate replaced the keys"
fi
[ ! -e "$work/other.tar.age" ] || fail "a refused generate wrote a bundle"
cp "$work/bundle.tar.age" "$work/bundle.copy"
if TMPDIR=$work/tmp sh "$dir/release-keys.sh" generate --public "$work/fresh" --bundle "$work/bundle.tar.age" --recipient "$recipient" >/dev/null 2>&1; then
	fail "generate overwrote a bundle"
fi
cmp -s "$work/bundle.tar.age" "$work/bundle.copy" || fail "the bundle changed"
[ ! -e "$work/fresh/release.pub" ] || fail "a refused generate wrote public keys"
if sh "$dir/release-keys.sh" generate --public "$work/fresh" --recipient "not-a-key" >/dev/null 2>&1; then
	fail "a recipient that is no age key passed"
fi
[ -z "$(ls -A "$work/tmp")" ] || fail "refused runs left $(ls -A "$work/tmp")"

echo "✓ release-keys: current and next keys, public parts only in packaging, encrypted bundle, secrets only of current, checks"
