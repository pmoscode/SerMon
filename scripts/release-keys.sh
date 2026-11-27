#!/bin/sh
# The real key set of SerMon's releases (V1-03, ADR 0043 §5). The product owner runs it once,
# locally; the release workflow checks the keys it gets against the public parts in the
# repository.
#
#   scripts/release-keys.sh generate [--public DIR] [--bundle FILE] [--recipient AGE_KEY]…
#                                    [--set-secrets OWNER/REPO]
#       Creates in a private temporary directory, removed on every way out:
#         release key (Ed25519, minisign format, scripts/release-sign.sh)   – install.sh, helper
#         OpenPGP signing key (RSA 4096, sign only)                         – .deb, .rpm, apt, dnf
#         apk key (RSA 4096)                                                – .apk, APKINDEX
#       each twice: "current" signs the releases, "next" is the successor for a rotation (ADR
#       0036). Writes
#         DIR/release.pub, DIR/release-next.pub, DIR/keys/{gpg.pub.asc,apk.rsa.pub} and
#         DIR/keys/next/{gpg.pub.asc,apk.rsa.pub}       (default DIR: packaging/ – commit them)
#         FILE  an age-encrypted tar of all keys, private and public (default
#               ./sermon-release-keys-<date>.tar.age): the offline copy, to the AGE_KEYs given
#               (e.g. age-keygen or a YubiKey plugin) or else to a passphrase that age asks for
#       and prints the names of the secrets of the GitHub environment "release" and the commands
#       that set them from FILE. --set-secrets sets them right away (gh, from the temporary
#       files, never shown). Only "current" goes to GitHub; "next" stays in the offline copy, so
#       a leak of the environment doesn't take the successor with it.
#       Never overwrites a key or a bundle.
#
#   scripts/release-keys.sh check SECRETS PUBLIC
#       SECRETS/{release.pem,gpg.asc,apk.rsa} (the secrets of the environment) must be the private
#       keys of PUBLIC/release.pub, PUBLIC/keys/gpg.pub.asc and PUBLIC/keys/apk.rsa.pub, and the
#       OpenPGP key must sign without a passphrase (the signing job has none to give).
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)

die() {
	echo "release-keys: $*" >&2
	exit 1
}

# The names of the secrets in the environment "release" and their files of the bundle.
SECRETS="SERMON_RELEASE_SIGNING_KEY:release.pem SERMON_PKG_GPG_KEY:gpg.asc SERMON_PKG_APK_KEY:apk.rsa"

sha256() { openssl dgst -sha256 -r | cut -d' ' -f1; }

# gpg_fingerprints HOME FILE prints the fingerprints of the primary keys in FILE.
gpg_fingerprints() {
	GNUPGHOME=$1 gpg --batch --quiet --with-colons --import-options show-only --import "$2" 2>/dev/null |
		awk -F: '$1 == "pub" || $1 == "sec" { want = 1; next } $1 == "fpr" && want { print $10; want = 0 }'
}

# run_age ARGS… runs age: the CLI if installed, else the age of the server's go.mod (ADR 0042).
run_age() {
	if command -v age >/dev/null 2>&1; then
		age "$@"
	elif command -v go >/dev/null 2>&1; then
		version=$(sed -n 's#^[[:space:]]*filippo.io/age \(v[^ ]*\).*#\1#p' "$root/server/go.mod" | head -1)
		[ -n "$version" ] || die "no version of filippo.io/age in server/go.mod"
		go run "filippo.io/age/cmd/age@$version" "$@"
	else
		die "age is needed (install age, or Go for go run filippo.io/age/cmd/age)"
	fi
}

check() {
	secrets=$1 public=$2
	for f in release.pem gpg.asc apk.rsa; do [ -s "$secrets/$f" ] || die "$secrets/$f is missing or empty"; done
	for f in release.pub keys/gpg.pub.asc keys/apk.rsa.pub; do [ -s "$public/$f" ] || die "$public/$f is missing"; done
	home=$(mktemp -d)
	trap 'GNUPGHOME="$home" gpgconf --kill gpg-agent 2>/dev/null || true; rm -rf "$home"' EXIT

	pub=$("$root/scripts/release-sign.sh" pubkey "$secrets/release.pem") || die "release.pem is no Ed25519 key"
	[ "$pub" = "$(head -1 "$public/release.pub")" ] ||
		die "release.pem is not the key of $public/release.pub"

	mkdir -m 0700 "$home/sec" "$home/pub"
	sec_fprs=$(gpg_fingerprints "$home/sec" "$secrets/gpg.asc")
	pub_fprs=$(gpg_fingerprints "$home/pub" "$public/keys/gpg.pub.asc")
	[ "$(printf '%s\n' "$sec_fprs" | grep -c .)" = 1 ] || die "gpg.asc must hold exactly one key"
	if [ -z "$pub_fprs" ] || [ "$sec_fprs" != "$pub_fprs" ]; then
		die "gpg.asc is not the key of $public/keys/gpg.pub.asc"
	fi
	GNUPGHOME="$home/sec" gpg --batch --quiet --import "$secrets/gpg.asc" 2>/dev/null || die "cannot import gpg.asc"
	echo check | GNUPGHOME="$home/sec" gpg --batch --quiet --pinentry-mode loopback --passphrase '' \
		--local-user "$sec_fprs" --detach-sign >/dev/null 2>&1 ||
		die "gpg.asc cannot sign without a passphrase"

	apk_sec=$(openssl pkey -in "$secrets/apk.rsa" -pubout -outform DER 2>/dev/null | sha256) || die "apk.rsa is no private key"
	apk_pub=$(openssl pkey -pubin -in "$public/keys/apk.rsa.pub" -outform DER 2>/dev/null | sha256)
	[ "$apk_sec" = "$apk_pub" ] || die "apk.rsa is not the key of $public/keys/apk.rsa.pub"
	echo "✓ the signing keys belong to the public keys in $public"
}

generate() {
	public=$root/packaging
	bundle=./sermon-release-keys-$(date -u +%Y%m%d).tar.age
	recipients=
	repo=
	while [ $# -gt 0 ]; do
		case $1 in
		--public) [ $# -ge 2 ] || die "--public needs a directory" && public=$2 && shift 2 ;;
		--bundle) [ $# -ge 2 ] || die "--bundle needs a file" && bundle=$2 && shift 2 ;;
		--recipient)
			[ $# -ge 2 ] || die "--recipient needs an age public key"
			case $2 in age1* | ssh-*) ;; *) die "not an age recipient: $2" ;; esac
			recipients="$recipients $2"
			shift 2
			;;
		--set-secrets)
			[ $# -ge 2 ] || die "--set-secrets needs OWNER/REPO"
			case $2 in */*) ;; *) die "--set-secrets needs OWNER/REPO" ;; esac
			repo=$2
			shift 2
			;;
		*) die "unknown option $1" ;;
		esac
	done
	for tool in openssl gpg gpgconf tar; do command -v "$tool" >/dev/null 2>&1 || die "$tool is needed"; done
	[ -z "$repo" ] || command -v gh >/dev/null 2>&1 || die "--set-secrets needs gh (GitHub CLI), logged in"
	for f in release.pub release-next.pub keys/gpg.pub.asc keys/apk.rsa.pub keys/next/gpg.pub.asc keys/next/apk.rsa.pub; do
		[ ! -e "$public/$f" ] || die "$public/$f exists – never replacing a key (a rotation uses next/, ADR 0036)"
	done
	[ ! -e "$bundle" ] || die "$bundle exists – not overwriting it"

	work=$(mktemp -d)
	# The private keys exist only here; every way out removes them, gpg-agent included.
	trap 'GNUPGHOME="$work/gnupg" gpgconf --kill gpg-agent 2>/dev/null || true; rm -rf "$work"' EXIT
	trap 'exit 130' INT TERM HUP
	chmod 0700 "$work"
	umask 077
	mkdir -p "$work/gnupg" "$work/keys/current" "$work/keys/next"
	export GNUPGHOME="$work/gnupg"

	for set in current next; do
		k=$work/keys/$set
		"$root/scripts/release-sign.sh" generate "$k/release.pem" >"$k/release.pub"
		uid="SerMon packages (https://github.com/pmoscode/SerMon)"
		[ "$set" = current ] || uid="SerMon packages, next (https://github.com/pmoscode/SerMon)"
		gpg --batch --quiet --pinentry-mode loopback --passphrase '' --quick-gen-key "$uid" rsa4096 sign never 2>/dev/null
		fpr=$(gpg --batch --with-colons --list-keys "=$uid" 2>/dev/null | awk -F: '$1 == "fpr" { print $10; exit }')
		[ -n "$fpr" ] || die "gpg created no key"
		gpg --batch --quiet --armor --pinentry-mode loopback --passphrase '' --export-secret-keys "$fpr" >"$k/gpg.asc"
		gpg --batch --quiet --armor --export "$fpr" >"$k/gpg.pub.asc"
		openssl genrsa -out "$k/apk.rsa" 4096 2>/dev/null
		openssl rsa -in "$k/apk.rsa" -pubout -out "$k/apk.rsa.pub" 2>/dev/null
		# The same check as the signing job's, on the layout of the repository (in a subshell:
		# check has a trap of its own).
		view=$work/view-$set
		mkdir -p "$view/keys"
		cp "$k/release.pub" "$view/"
		cp "$k/gpg.pub.asc" "$k/apk.rsa.pub" "$view/keys/"
		(check "$k" "$view") >/dev/null
		if [ "$set" = current ]; then fpr_current=$fpr; else fpr_next=$fpr; fi
	done
	keyid=$(head -1 "$work/keys/current/release.pub" | openssl base64 -d -A | dd bs=1 skip=2 count=8 2>/dev/null | od -An -tx1 | tr -d ' \n')
	cat >"$work/keys/README.txt" <<-EOF
		SerMon release keys, created $(date -u +%Y-%m-%dT%H:%MZ) by scripts/release-keys.sh (ADR 0043).

		current/  signs the releases; its private keys are the secrets of the GitHub environment
		          "release": SERMON_RELEASE_SIGNING_KEY = release.pem, SERMON_PKG_GPG_KEY = gpg.asc,
		          SERMON_PKG_APK_KEY = apk.rsa.
		next/     the successor for a rotation (ADR 0036); its public keys are published from the
		          start (packaging/release-next.pub, packaging/keys/next/), its private keys are
		          only here.

		release key ID: $keyid
		OpenPGP current: $fpr_current
		OpenPGP next:    $fpr_next
		apk current: $("$root/scripts/repo-keys.sh" apk-name "$work/keys/current/apk.rsa.pub")
		apk next:    $("$root/scripts/repo-keys.sh" apk-name "$work/keys/next/apk.rsa.pub")
	EOF

	# The offline copy: tar straight into age, nothing unencrypted outside the work directory.
	if [ -n "$recipients" ]; then
		set --
		for r in $recipients; do set -- "$@" -r "$r"; done
		tar -C "$work" -cf - keys | run_age -e "$@" -o "$bundle.tmp"
	else
		echo "▸ age asks for the passphrase of the offline copy (twice)" >&2
		tar -C "$work" -cf - keys | run_age -e -p -o "$bundle.tmp"
	fi
	mv "$bundle.tmp" "$bundle"

	umask 022
	mkdir -p "$public/keys/next"
	cp "$work/keys/current/release.pub" "$public/release.pub"
	cp "$work/keys/next/release.pub" "$public/release-next.pub"
	cp "$work/keys/current/gpg.pub.asc" "$work/keys/current/apk.rsa.pub" "$public/keys/"
	cp "$work/keys/next/gpg.pub.asc" "$work/keys/next/apk.rsa.pub" "$public/keys/next/"

	if [ -n "$repo" ]; then
		for entry in $SECRETS; do
			gh secret set "${entry%%:*}" --env release --repo "$repo" <"$work/keys/current/${entry#*:}" >/dev/null ||
				die "gh could not set ${entry%%:*}"
			echo "✓ secret ${entry%%:*} set in the environment \"release\" of $repo"
		done
	fi
	rm -rf "$work"

	cat <<-EOF
		✓ release keys created

		Commit the public keys (they go into server, helper, packages and repositories):
		  $public/release.pub, $public/release-next.pub
		  $public/keys/gpg.pub.asc, $public/keys/apk.rsa.pub
		  $public/keys/next/gpg.pub.asc, $public/keys/next/apk.rsa.pub

		Offline copy of all keys (keep it apart from this machine, e.g. two USB sticks): $bundle

		Secrets of the GitHub environment "release" (only "current"; "next" never goes to GitHub):
	EOF
	for entry in $SECRETS; do
		name=${entry%%:*} file=${entry#*:}
		echo "  $name  = keys/current/$file"
		[ -n "$repo" ] || echo "      age -d [-i IDENTITY] $bundle | tar -xOf - keys/current/$file | gh secret set $name --env release --repo pmoscode/SerMon"
	done
	echo "(The values are never shown; the commands pipe them from the offline copy into gh.)"
}

case ${1:-} in
generate) shift && generate "$@" ;;
check) [ $# -eq 3 ] || die "usage: $0 check SECRETS PUBLIC" && check "$2" "$3" ;;
*) die "usage: $0 generate [--public DIR] [--bundle FILE] [--recipient AGE_KEY]… [--set-secrets OWNER/REPO] | check SECRETS PUBLIC" ;;
esac
