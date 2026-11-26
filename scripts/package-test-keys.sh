#!/bin/sh
# Keys of one test run for the agent packages (M2-21, scripts/package-agent.sh): an OpenPGP key
# (RSA 3072, no passphrase) for .deb and .rpm, an RSA key for .apk. Never for releases.
#
#   scripts/package-test-keys.sh DIR   → DIR/gpg.asc, DIR/gpg.pub.asc, DIR/apk.rsa, DIR/apk.rsa.pub
set -eu

[ $# -eq 1 ] || {
	echo "usage: $0 DIR" >&2
	exit 1
}
dir=$1
mkdir -p "$dir"
chmod 0700 "$dir"
home=$(mktemp -d)
trap 'rm -rf "$home"' EXIT
export GNUPGHOME="$home"
gpg --batch --quiet --pinentry-mode loopback --passphrase '' \
	--quick-gen-key 'SerMon package test <packages@sermon.invalid>' rsa3072 sign never 2>/dev/null
gpg --batch --quiet --armor --pinentry-mode loopback --passphrase '' --export-secret-keys >"$dir/gpg.asc"
gpg --batch --quiet --armor --export >"$dir/gpg.pub.asc"
openssl genrsa -out "$dir/apk.rsa" 2048 2>/dev/null
openssl rsa -in "$dir/apk.rsa" -pubout -out "$dir/apk.rsa.pub" 2>/dev/null
chmod 0600 "$dir/gpg.asc" "$dir/apk.rsa"
