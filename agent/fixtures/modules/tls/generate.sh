#!/bin/sh
# Writes the certificates of the TLS module's tests (M5-09) with OpenSSL (3.4 or later for
# -not_before/-not_after): a root and an intermediate CA, a leaf for localhost and 127.0.0.1 as a
# chain, an expired leaf, one of a foreign name and a self-signed one. The keys are thrown away;
# only certificates land in certs/ (.crt: *.pem is ignored by git as a safety net).
# Usage: agent/fixtures/modules/tls/generate.sh
set -eu
cd "$(dirname "$0")"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p certs

key() { openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$work/$1.key" 2>/dev/null; }

key root
openssl req -x509 -new -key "$work/root.key" -subj "/O=SerMon Fixtures/CN=SerMon Fixture Root" \
	-not_before 20250101000000Z -not_after 20450101000000Z -set_serial 1 \
	-addext basicConstraints=critical,CA:TRUE -addext keyUsage=critical,keyCertSign,cRLSign \
	-out certs/root.crt

# sign NAME SUBJECT ISSUER NOT_BEFORE NOT_AFTER SERIAL EXTENSIONS
sign() {
	key "$1"
	openssl req -new -key "$work/$1.key" -subj "$2" -out "$work/$1.csr"
	printf '%s\n' "$7" >"$work/$1.ext"
	openssl x509 -req -in "$work/$1.csr" -CA "certs/$3.crt" -CAkey "$work/$3.key" \
		-not_before "$4" -not_after "$5" -set_serial "$6" -extfile "$work/$1.ext" -out "$work/$1.pem" 2>/dev/null
}

sign intermediate "/O=SerMon Fixtures/CN=SerMon Fixture Intermediate" root \
	20250101000000Z 20400101000000Z 2 \
	"basicConstraints=critical,CA:TRUE,pathlen:0
keyUsage=critical,keyCertSign,cRLSign"
cp "$work/intermediate.pem" certs/intermediate.crt

leaf_ext() {
	printf 'basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=serverAuth\nsubjectAltName=%s\n' "$1"
}
sign leaf "/CN=localhost" intermediate 20260101000000Z 20270101000000Z 10 "$(leaf_ext DNS:localhost,IP:127.0.0.1)"
cat "$work/leaf.pem" certs/intermediate.crt >certs/fullchain.crt
cp "$work/leaf.pem" certs/leaf-only.crt
sign expired "/CN=localhost" intermediate 20240101000000Z 20250601000000Z 11 "$(leaf_ext DNS:localhost)"
cat "$work/expired.pem" certs/intermediate.crt >certs/expired.crt
sign foreign "/CN=other.example" intermediate 20260101000000Z 20270101000000Z 12 "$(leaf_ext DNS:other.example)"
cat "$work/foreign.pem" certs/intermediate.crt >certs/foreign.crt

key self
openssl req -x509 -new -key "$work/self.key" -subj "/CN=localhost" \
	-not_before 20260101000000Z -not_after 20280101000000Z -set_serial 20 \
	-addext subjectAltName=DNS:localhost -out certs/selfsigned.crt
echo "✓ agent/fixtures/modules/tls/certs/"
