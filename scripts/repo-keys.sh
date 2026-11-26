#!/bin/sh
# The public keys of the agent's package repositories as hosts get them (M4-31, ADR 0036): in
# /repo/keys/ (scripts/build-repos.sh) for install.sh --repo, and in the agent package
# (scripts/package-agent.sh), whose every update brings the keys of the next rotation along.
#
#   scripts/repo-keys.sh publish KEYS OUT   → OUT/sermon.asc, OUT/sermon-<id>.rsa.pub…, OUT/SHA256SUMS
#   scripts/repo-keys.sh apk-name PUB       → sermon-<id>, the name apk knows the key PUB by
#
# KEYS holds gpg.pub.asc and apk.rsa.pub, KEYS/next/ the same of the next pair during a rotation.
# sermon.asc carries both OpenPGP keys in one armored block (apt and dnf). apk finds a key by the
# name its signature gives, so every apk key is named by its fingerprint: the first 16 hex digits
# of SHA-256 over its DER public key, the same in every release and before and after a rotation.
# SHA256SUMS lists all files; the release signs it (release-sign.sh), install.sh checks it.
set -eu

die() {
	echo "repo-keys: $*" >&2
	exit 1
}

sha256() { openssl dgst -sha256 | sed 's/^.*= //'; }

apk_name() {
	id=$(openssl pkey -pubin -in "$1" -outform DER 2>/dev/null | sha256 | cut -c1-16)
	[ ${#id} -eq 16 ] || die "$1 is no public key"
	echo "sermon-$id"
}

publish() {
	keys=$1 out=$2
	for f in gpg.pub.asc apk.rsa.pub; do
		[ -r "$keys/$f" ] || die "$keys needs $f"
	done
	mkdir -p "$out"
	rm -f "$out/sermon.asc" "$out"/sermon-*.rsa.pub "$out/SHA256SUMS"
	home=$(mktemp -d)
	trap 'rm -rf "$home"' EXIT
	set -- "$keys/gpg.pub.asc"
	[ ! -r "$keys/next/gpg.pub.asc" ] || set -- "$@" "$keys/next/gpg.pub.asc"
	GNUPGHOME=$home gpg --batch --quiet --import "$@" 2>/dev/null || die "cannot read the OpenPGP keys of $keys"
	GNUPGHOME=$home gpg --batch --quiet --armor --export >"$out/sermon.asc"
	current=$(apk_name "$keys/apk.rsa.pub")
	cp "$keys/apk.rsa.pub" "$out/$current.rsa.pub"
	if [ -r "$keys/next/apk.rsa.pub" ]; then
		next=$(apk_name "$keys/next/apk.rsa.pub")
		[ "$next" != "$current" ] || die "$keys/next/apk.rsa.pub is the current apk key"
		cp "$keys/next/apk.rsa.pub" "$out/$next.rsa.pub"
	fi
	for file in "$out"/sermon-*.rsa.pub "$out/sermon.asc"; do
		printf '%s  %s\n' "$(sha256 <"$file")" "$(basename "$file")"
	done >"$out/SHA256SUMS"
	chmod 0644 "$out/sermon.asc" "$out"/sermon-*.rsa.pub "$out/SHA256SUMS"
}

case ${1:-} in
publish) [ $# -eq 3 ] || die "usage: $0 publish KEYS OUT" && publish "$2" "$3" ;;
apk-name) [ $# -eq 2 ] || die "usage: $0 apk-name PUB" && apk_name "$2" ;;
*) die "usage: $0 publish KEYS OUT | apk-name PUB" ;;
esac
