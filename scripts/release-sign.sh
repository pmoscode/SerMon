#!/bin/sh
# Release signing with plain OpenSSL (ADR 0012): Ed25519 keys, signatures in the minisign format
# ("Ed" algorithm, file not pre-hashed), so `minisign -V` and install.sh verify them.
#
#   scripts/release-sign.sh generate KEY.pem     new private key (keep it secret, e.g. a CI secret)
#   scripts/release-sign.sh pubkey KEY.pem       print the public key line for install.sh
#   scripts/release-sign.sh sign KEY.pem FILE…   write FILE.minisig next to each file
#
# The key ID is the first 8 bytes of SHA-256 over the public key, so the private key alone is enough.
set -eu

die() {
	printf 'release-sign: %s\n' "$*" >&2
	exit 1
}

command -v openssl >/dev/null 2>&1 || die "openssl (3.0 or later) is required"

b64() { openssl base64 -A; }

# Every Ed25519 SubjectPublicKeyInfo starts with these 12 bytes, followed by the 32-byte key.
ED25519_SPKI_PREFIX=302a300506032b6570032100

# check_key KEY.pem refuses anything but an Ed25519 key: of another key, the last 32 bytes of the
# SPKI would silently become a wrong public key.
check_key() {
	spki=$(openssl pkey -in "$1" -pubout -outform DER 2>/dev/null | od -An -v -tx1 | tr -d ' \n') ||
		die "cannot read the key $1"
	case $spki in
	"$ED25519_SPKI_PREFIX"*) [ ${#spki} -eq 88 ] || die "$1 is not an Ed25519 key" ;;
	*) die "$1 is not an Ed25519 key" ;;
	esac
}

# raw_public KEY.pem writes the 32-byte public key to stdout (last 32 bytes of the DER SPKI).
raw_public() {
	openssl pkey -in "$1" -pubout -outform DER 2>/dev/null | tail -c 32
}

key_id() {
	raw_public "$1" | openssl dgst -sha256 -binary | head -c 8
}

pubkey() {
	{
		printf 'Ed'
		key_id "$1"
		raw_public "$1"
	} | b64
	printf '\n'
}

sign_file() {
	key=$1
	file=$2
	[ -f "$file" ] || die "no such file: $file"
	work=$(mktemp -d)
	trap 'rm -rf "$work"' EXIT
	openssl pkeyutl -sign -rawin -inkey "$key" -in "$file" -out "$work/sig" ||
		die "cannot sign $file (OpenSSL 3.0 or later needed)"
	{
		printf 'Ed'
		key_id "$key"
		cat "$work/sig"
	} >"$work/signature"
	trusted="timestamp:$(date +%s)	file:$(basename "$file")	hashed:0"
	# The global signature covers the file signature and the trusted comment (minisign format).
	{
		cat "$work/sig"
		printf '%s' "$trusted"
	} >"$work/global.in"
	openssl pkeyutl -sign -rawin -inkey "$key" -in "$work/global.in" -out "$work/global"
	{
		printf 'untrusted comment: signature from the SerMon release key\n'
		b64 <"$work/signature"
		printf '\ntrusted comment: %s\n' "$trusted"
		b64 <"$work/global"
		printf '\n'
	} >"$file.minisig"
	rm -rf "$work"
	trap - EXIT
}

[ $# -ge 2 ] || die "usage: release-sign.sh generate|pubkey|sign KEY.pem [FILE…]"
command=$1
key=$2
shift 2
[ "$command" = generate ] || check_key "$key"
case $command in
generate)
	[ ! -e "$key" ] || die "$key exists – not overwriting a key"
	(umask 077 && openssl genpkey -algorithm ed25519 -out "$key")
	pubkey "$key"
	;;
pubkey) pubkey "$key" ;;
sign)
	[ $# -gt 0 ] || die "nothing to sign"
	for file in "$@"; do sign_file "$key" "$file"; done
	;;
*) die "unknown command $command" ;;
esac
