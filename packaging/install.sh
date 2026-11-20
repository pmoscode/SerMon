#!/bin/sh
# SerMon agent installer (KONZEPT §6, ADR 0012).
#
#   curl -fsSL https://sermon.example.com/install.sh | sudo sh -s -- \
#     --server https://sermon.example.com --token sme_XXXXXXXX
#
# The token can also come from SERMON_ENROLLMENT_TOKEN (arguments are visible in the process list).
# The script downloads the agent binary of this server and checks its signature against the
# release key embedded below before anything is installed; a wrong or missing signature aborts.
# Read it before you run it: https://<server>/install.sh
set -eu

# Release public key (minisign format, Ed25519). Replaced when the server is built; a server
# without release key serves the placeholder and the script refuses to install.
RELEASE_KEY='@SERMON_RELEASE_KEY@'

BIN_DIR=/usr/local/bin
CONFIG_DIR=/etc/sermon
STATE_DIR=/var/lib/sermon-agent
AGENT_PORT=8443
# Test hook: a root to install into (bats); empty on real hosts.
ROOT=${SERMON_INSTALL_ROOT:-}

say() { printf 'sermon: %s\n' "$*" >&2; }
die() {
	say "error: $*"
	exit 1
}

usage() {
	cat >&2 <<'USAGE'
usage: install.sh --server URL [--token TOKEN] [--ca-fingerprint sha256:…] [--agent-url URL] [--force]

  --server URL           public HTTPS address of sermon-server (as in the browser)
  --token TOKEN          enrollment token from the UI (or SERMON_ENROLLMENT_TOKEN)
  --ca-fingerprint FP    SHA-256 fingerprint of a self-signed server certificate
  --agent-url URL        mTLS agent API if not <server host>:8443
  --force                replace an existing enrollment of this host
USAGE
}

# --- detection --------------------------------------------------------------------------------

# detect_arch prints amd64 or arm64.
detect_arch() {
	case $(uname -m) in
	x86_64 | amd64) echo amd64 ;;
	aarch64 | arm64) echo arm64 ;;
	*) die "unsupported architecture $(uname -m) (amd64 and arm64 are supported)" ;;
	esac
}

# detect_distro prints the ID from os-release, e.g. debian, ubuntu, alpine.
detect_distro() {
	release="$ROOT/etc/os-release"
	[ -r "$release" ] || release="$ROOT/usr/lib/os-release"
	[ -r "$release" ] || die "cannot detect the distribution (no os-release)"
	id=$(sed -n 's/^ID=//p' "$release" | tr -d '"' | head -n 1)
	[ -n "$id" ] || die "cannot detect the distribution (no ID in $release)"
	echo "$id"
}

# detect_init prints systemd or openrc.
detect_init() {
	if [ -d "$ROOT/run/systemd/system" ]; then
		echo systemd
	elif command -v openrc-run >/dev/null 2>&1 || [ -x "$ROOT/sbin/openrc-run" ]; then
		echo openrc
	else
		die "no supported init system found (systemd or OpenRC)"
	fi
}

# --- signature ---------------------------------------------------------------------------------

# b64decode decodes base64 from stdin.
b64decode() {
	if base64 -d </dev/null >/dev/null 2>&1; then base64 -d; else openssl base64 -d -A; fi
}

# hex prints the bytes of stdin as lowercase hex.
hex() { od -An -v -tx1 | tr -d ' \n'; }

# verify FILE SIGFILE checks a minisign signature (Ed25519, "Ed" algorithm) against RELEASE_KEY.
# minisign is used when installed; otherwise openssl (3.0 or later) checks the same signature.
verify() {
	# Prefixed names: POSIX sh has no local variables, main() uses work and binary.
	verify_file=$1
	verify_sig=$2
	case $RELEASE_KEY in
	@*@ | "") die "this server was built without a release key – install the agent from the package repository" ;;
	esac
	if command -v minisign >/dev/null 2>&1; then
		minisign -Vq -P "$RELEASE_KEY" -m "$verify_file" -x "$verify_sig" >/dev/null 2>&1 ||
			die "signature check failed for $(basename "$verify_file") – not installing"
		return 0
	fi
	command -v openssl >/dev/null 2>&1 ||
		die "neither minisign nor openssl found to check the signature – install one of them"
	# Scratch files next to the download; the caller removes the directory.
	verify_dir="$(dirname "$verify_file")/verify"
	mkdir -p "$verify_dir"
	printf '%s' "$RELEASE_KEY" | b64decode >"$verify_dir/key" || die "invalid release key"
	sed -n 2p "$verify_sig" | b64decode >"$verify_dir/sig" 2>/dev/null || die "malformed signature file"
	[ "$(wc -c <"$verify_dir/key" | tr -d ' ')" = 42 ] || die "invalid release key"
	[ "$(wc -c <"$verify_dir/sig" | tr -d ' ')" = 74 ] || die "malformed signature file"
	# Algorithm "Ed" (not pre-hashed) and the key ID must match the release key.
	[ "$(dd if="$verify_dir/sig" bs=1 count=2 2>/dev/null | hex)" = 4564 ] ||
		die "unsupported signature algorithm (expected minisign Ed)"
	[ "$(dd if="$verify_dir/sig" bs=1 skip=2 count=8 2>/dev/null | hex)" = "$(dd if="$verify_dir/key" bs=1 skip=2 count=8 2>/dev/null | hex)" ] ||
		die "signature by an unknown key – not installing"
	dd if="$verify_dir/sig" of="$verify_dir/sig.raw" bs=1 skip=10 count=64 2>/dev/null
	# DER SubjectPublicKeyInfo of an Ed25519 key: fixed 12-byte prefix + 32-byte key.
	printf '\060\052\060\005\006\003\053\145\160\003\041\000' >"$verify_dir/key.der"
	dd if="$verify_dir/key" bs=1 skip=10 count=32 2>/dev/null >>"$verify_dir/key.der"
	openssl pkeyutl -verify -pubin -keyform DER -inkey "$verify_dir/key.der" -rawin \
		-in "$verify_file" -sigfile "$verify_dir/sig.raw" >/dev/null 2>&1 ||
		die "signature check failed for $(basename "$verify_file") – not installing"
}

# --- installation ------------------------------------------------------------------------------

# download URL FILE. With a pinned fingerprint (self-signed server) the TLS certificate is not
# checked here: the signature check protects the binary, enroll checks the fingerprint.
download() {
	url=$1
	out=$2
	if command -v curl >/dev/null 2>&1; then
		if [ -n "$FINGERPRINT" ]; then
			curl -fsSL --proto '=https' --insecure -o "$out" "$url"
		else
			curl -fsSL --proto '=https' -o "$out" "$url"
		fi
	elif command -v wget >/dev/null 2>&1; then
		if [ -n "$FINGERPRINT" ]; then wget -q --no-check-certificate -O "$out" "$url"; else wget -q -O "$out" "$url"; fi
	else
		die "neither curl nor wget found"
	fi
}

create_user() {
	if ! id sermon >/dev/null 2>&1; then
		if command -v useradd >/dev/null 2>&1; then
			useradd --system --home-dir "$STATE_DIR" --no-create-home --shell /usr/sbin/nologin sermon
		else
			addgroup -S sermon 2>/dev/null || true
			adduser -S -D -H -h "$STATE_DIR" -s /sbin/nologin -G sermon sermon
		fi
	fi
	# Read access to logs (KONZEPT §4.1); only groups that exist on this system.
	for group in systemd-journal adm; do
		if getent group "$group" >/dev/null 2>&1; then
			if command -v usermod >/dev/null 2>&1; then usermod -a -G "$group" sermon; else addgroup sermon "$group"; fi
		fi
	done
}

# agent_url_for SERVER prints https://<host of SERVER>:8443.
agent_url_for() {
	host=$(printf '%s\n' "$1" | sed -e 's|^[a-z]*://||' -e 's|[/?#].*$||' -e 's|:[0-9]*$||')
	echo "https://$host:$AGENT_PORT"
}

write_config() {
	mkdir -p "$ROOT$CONFIG_DIR"
	if [ -e "$ROOT$CONFIG_DIR/agent.toml" ]; then
		say "keeping the existing $CONFIG_DIR/agent.toml"
		return 0
	fi
	cat >"$ROOT$CONFIG_DIR/agent.toml" <<EOF
# SerMon agent – written by install.sh. Everything else comes from the server.
[server]
url = "$SERVER"
agent_url = "$AGENT_URL"
EOF
	chmod 0640 "$ROOT$CONFIG_DIR/agent.toml"
}

install_systemd_unit() {
	cat >"$ROOT/etc/systemd/system/sermon-agent.service" <<'UNIT'
@SERMON_SYSTEMD_UNIT@
UNIT
}

install_openrc_service() {
	cat >"$ROOT/etc/init.d/sermon-agent" <<'SERVICE'
@SERMON_OPENRC_SERVICE@
SERVICE
	chmod 0755 "$ROOT/etc/init.d/sermon-agent"
}

main() {
	SERVER=''
	TOKEN=${SERMON_ENROLLMENT_TOKEN:-}
	FINGERPRINT=''
	AGENT_URL=''
	FORCE=''
	while [ $# -gt 0 ]; do
		case $1 in
		--server) SERVER=${2:-} && shift ;;
		--token) TOKEN=${2:-} && shift ;;
		--ca-fingerprint) FINGERPRINT=${2:-} && shift ;;
		--agent-url) AGENT_URL=${2:-} && shift ;;
		--force) FORCE=--force ;;
		-h | --help) usage && exit 0 ;;
		*) usage && die "unknown argument $1" ;;
		esac
		shift
	done
	[ -n "$SERVER" ] || { usage; die "--server is required"; }
	case $SERVER in https://*) ;; *) die "--server must be an https:// URL" ;; esac
	SERVER=${SERVER%/}
	[ -n "$TOKEN" ] || die "an enrollment token is required (--token or SERMON_ENROLLMENT_TOKEN)"
	[ -n "$AGENT_URL" ] || AGENT_URL=$(agent_url_for "$SERVER")
	[ "$(id -u)" = 0 ] || die "please run as root (sudo)"

	arch=$(detect_arch)
	distro=$(detect_distro)
	init=$(detect_init)
	say "installing for $distro ($arch, $init)"

	work=$(mktemp -d)
	trap 'rm -rf "$work"' EXIT
	binary="sermon-agent-linux-$arch"
	download "$SERVER/downloads/$binary" "$work/$binary" || die "cannot download the agent from $SERVER"
	download "$SERVER/downloads/$binary.minisig" "$work/$binary.minisig" || die "cannot download the signature"
	verify "$work/$binary" "$work/$binary.minisig"
	say "signature ok"

	create_user
	install -m 0755 "$work/$binary" "$BIN_DIR/sermon-agent"
	write_config
	chown root:sermon "$CONFIG_DIR" "$CONFIG_DIR/agent.toml"
	chmod 0750 "$CONFIG_DIR"
	mkdir -p "$STATE_DIR"
	chown sermon:sermon "$STATE_DIR"
	chmod 0750 "$STATE_DIR"

	set -- enroll --config "$CONFIG_DIR/agent.toml"
	[ -z "$FINGERPRINT" ] || set -- "$@" --ca-fingerprint "$FINGERPRINT"
	[ -z "$FORCE" ] || set -- "$@" "$FORCE"
	SERMON_ENROLLMENT_TOKEN=$TOKEN "$BIN_DIR/sermon-agent" "$@" || die "enrollment failed (see above)"

	case $init in
	systemd)
		install_systemd_unit
		systemctl daemon-reload
		systemctl enable --now sermon-agent
		;;
	openrc)
		install_openrc_service
		rc-update add sermon-agent default
		rc-service sermon-agent restart
		;;
	esac
	say "done – the node shows up in the UI within seconds"
}

[ "${SERMON_INSTALL_TEST:-}" = 1 ] || main "$@"
