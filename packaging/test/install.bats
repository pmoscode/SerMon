#!/usr/bin/env bats
# Tests of packaging/install.sh (M1-27); run with `just test-packaging` (Docker: bats, openssl, minisign).

setup() {
	TMP=$(mktemp -d)
	export SERMON_INSTALL_TEST=1 SERMON_INSTALL_ROOT="$TMP/root"
	mkdir -p "$SERMON_INSTALL_ROOT/etc"
	# shellcheck source=../install.sh
	. "$BATS_TEST_DIRNAME/../install.sh"
	KEY="$TMP/key.pem"
	RELEASE_KEY=$("$BATS_TEST_DIRNAME/../../scripts/release-sign.sh" generate "$KEY")
	# The version the server expects (written into the script when the server is built).
	AGENT_VERSION=1.2.3
	printf 'agent binary\n' >"$TMP/sermon-agent-linux-amd64"
	sign "$KEY" "$TMP/sermon-agent-linux-amd64"
	BIN="$TMP/sermon-agent-linux-amd64"
}

teardown() { rm -rf "$TMP"; }

# sign KEY FILE… [VERSION] signs like a release of $AGENT_VERSION (or $SIGN_VERSION).
sign() {
	"$BATS_TEST_DIRNAME/../../scripts/release-sign.sh" sign "$1" --version "${SIGN_VERSION:-$AGENT_VERSION}" "$2"
}

# both runs a command with minisign and with the openssl fallback; both must fail with $1.
both_reject() {
	expected=$1
	shift
	run without_minisign "$@"
	[ "$status" -ne 0 ] || return 1
	[[ "$output" == *"$expected"* ]] || { echo "openssl: $output"; return 1; }
	command -v minisign >/dev/null
	run "$@"
	[ "$status" -ne 0 ] || return 1
	[[ "$output" == *"$expected"* ]] || { echo "minisign: $output"; return 1; }
}

# without_minisign runs a command with a PATH that has openssl but no minisign.
without_minisign() {
	mkdir -p "$TMP/bin"
	for tool in openssl od tr dd wc sed base64 dirname mkdir basename head cat; do
		ln -sf "$(command -v "$tool")" "$TMP/bin/$tool"
	done
	PATH="$TMP/bin" "$@"
}

@test "detects the architecture" {
	uname() { echo x86_64; }
	[ "$(detect_arch)" = amd64 ]
	uname() { echo aarch64; }
	[ "$(detect_arch)" = arm64 ]
	uname() { echo riscv64; }
	run detect_arch
	[ "$status" -ne 0 ]
	[[ "$output" == *"unsupported architecture riscv64"* ]]
}

@test "detects the distribution from os-release" {
	printf 'NAME="Debian GNU/Linux"\nID=debian\nVERSION_ID="13"\n' >"$SERMON_INSTALL_ROOT/etc/os-release"
	[ "$(detect_distro)" = debian ]
	printf 'ID="ubuntu"\nID_LIKE=debian\n' >"$SERMON_INSTALL_ROOT/etc/os-release"
	[ "$(detect_distro)" = ubuntu ]
	rm "$SERMON_INSTALL_ROOT/etc/os-release"
	run detect_distro
	[ "$status" -ne 0 ]
}

@test "detects systemd and OpenRC" {
	mkdir -p "$SERMON_INSTALL_ROOT/run/systemd/system"
	[ "$(detect_init)" = systemd ]
	rm -rf "$SERMON_INSTALL_ROOT/run"
	mkdir -p "$SERMON_INSTALL_ROOT/sbin"
	touch "$SERMON_INSTALL_ROOT/sbin/openrc-run" && chmod +x "$SERMON_INSTALL_ROOT/sbin/openrc-run"
	[ "$(detect_init)" = openrc ]
	rm "$SERMON_INSTALL_ROOT/sbin/openrc-run"
	run detect_init
	[ "$status" -ne 0 ]
}

@test "derives the agent API from the server URL" {
	[ "$(agent_url_for https://sermon.example.com)" = https://sermon.example.com:8443 ]
	[ "$(agent_url_for https://sermon.example.com:443/ui/)" = https://sermon.example.com:8443 ]
}

@test "accepts a valid signature with openssl" {
	without_minisign verify "$BIN" "$BIN.minisig"
}

@test "verify leaves the variables of the caller alone (POSIX sh has no locals)" {
	work=/caller/work binary=caller-binary
	without_minisign verify "$BIN" "$BIN.minisig"
	[ "$work" = /caller/work ]
	[ "$binary" = caller-binary ]
}

@test "accepts a valid signature with minisign" {
	command -v minisign
	verify "$BIN" "$BIN.minisig"
}

@test "rejects a modified binary" {
	printf 'malware\n' >>"$BIN"
	run without_minisign verify "$BIN" "$BIN.minisig"
	[ "$status" -ne 0 ]
	[[ "$output" == *"signature check failed"* ]]
	run verify "$BIN" "$BIN.minisig"
	[ "$status" -ne 0 ]
}

@test "rejects a signature of another key" {
	"$BATS_TEST_DIRNAME/../../scripts/release-sign.sh" generate "$TMP/other.pem" >/dev/null
	sign "$TMP/other.pem" "$BIN"
	run without_minisign verify "$BIN" "$BIN.minisig"
	[ "$status" -ne 0 ]
	[[ "$output" == *"unknown key"* ]]
}

@test "rejects broken signature files and servers without release key" {
	printf 'garbage\n' >"$BIN.minisig"
	run without_minisign verify "$BIN" "$BIN.minisig"
	[ "$status" -ne 0 ]
	RELEASE_KEY='@SERMON_RELEASE_KEY@'
	run verify "$BIN" "$BIN.minisig"
	[ "$status" -ne 0 ]
	[[ "$output" == *"without a release key"* ]]
}

@test "release-sign writes file name and version into the trusted comment" {
	[ "$(sed -n 3p "$BIN.minisig")" != "" ]
	comment=$(sed -n 's/^trusted comment: //p' "$BIN.minisig")
	[[ "$comment" == *"	file:sermon-agent-linux-amd64	"* ]]
	[[ "$comment" == *"	version:1.2.3" ]]
	run "$BATS_TEST_DIRNAME/../../scripts/release-sign.sh" sign "$KEY" "$BIN"
	[ "$status" -ne 0 ]
	[[ "$output" == *"--version"* ]]
	run "$BATS_TEST_DIRNAME/../../scripts/release-sign.sh" sign "$KEY" --version '1.2 3' "$BIN"
	[ "$status" -ne 0 ]
}

# Rollback and substitution: a valid signature of the release key is not enough, it must be the
# signature of exactly this file of exactly the version the server runs.
@test "rejects the signed binary of another version" {
	SIGN_VERSION=1.2.2 sign "$KEY" "$BIN"
	both_reject "version 1.2.2" verify "$BIN" "$BIN.minisig"
}

@test "rejects a signed file under another name" {
	printf 'other tool\n' >"$TMP/sermon-agent-linux-arm64"
	sign "$KEY" "$TMP/sermon-agent-linux-arm64"
	cp "$TMP/sermon-agent-linux-arm64" "$BIN"
	cp "$TMP/sermon-agent-linux-arm64.minisig" "$BIN.minisig"
	both_reject "sermon-agent-linux-arm64" verify "$BIN" "$BIN.minisig"
}

@test "rejects a forged trusted comment" {
	SIGN_VERSION=1.2.2 sign "$KEY" "$BIN"
	sed -i 's/version:1.2.2/version:1.2.3/' "$BIN.minisig"
	both_reject "signature check failed" verify "$BIN" "$BIN.minisig"
}

@test "refuses to install from a server that names no agent version" {
	AGENT_VERSION='@SERMON_AGENT_VERSION@'
	both_reject "no agent version" verify "$BIN" "$BIN.minisig"
}

@test "checks the arguments before anything happens" {
	run main
	[[ "$output" == *"--server is required"* ]]
	run main --server http://sermon.example.com --token sme_x
	[[ "$output" == *"must be an https:// URL"* ]]
	SERMON_ENROLLMENT_TOKEN='' run main --server https://sermon.example.com
	[[ "$output" == *"enrollment token is required"* ]]
	run main --server https://sermon.example.com --token sme_x --bogus
	[[ "$output" == *"unknown argument --bogus"* ]]
}

# Arguments are visible to every local user (ps): the token comes from the environment or a file.
@test "reads the token from a file and warns about --token" {
	printf 'sme_from_file\n' >"$TMP/token"
	SERMON_ENROLLMENT_TOKEN='' parse_args --server https://sermon.example.com --token-file "$TMP/token"
	[ "$TOKEN" = sme_from_file ]
	[ "$AGENT_URL" = https://sermon.example.com:8443 ]

	SERMON_ENROLLMENT_TOKEN='' run parse_args --server https://sermon.example.com --token-file "$TMP/missing"
	[ "$status" -ne 0 ]
	[[ "$output" == *"cannot read the token file"* ]]
	printf '\n' >"$TMP/empty"
	SERMON_ENROLLMENT_TOKEN='' run parse_args --server https://sermon.example.com --token-file "$TMP/empty"
	[[ "$output" == *"enrollment token is required"* ]]

	SERMON_ENROLLMENT_TOKEN='' run parse_args --server https://sermon.example.com --token sme_x
	[ "$status" -eq 0 ]
	[[ "$output" == *"visible in the process list"* ]]
	SERMON_ENROLLMENT_TOKEN=sme_env run parse_args --server https://sermon.example.com
	[ "$status" -eq 0 ]
	[[ "$output" != *"process list"* ]]
}

@test "writes the agent configuration once" {
	SERVER=https://sermon.example.com AGENT_URL=https://sermon.example.com:8443 CONFIG_DIR=/etc/sermon
	write_config
	grep -q '^url = "https://sermon.example.com"$' "$SERMON_INSTALL_ROOT/etc/sermon/agent.toml"
	grep -q '^agent_url = "https://sermon.example.com:8443"$' "$SERMON_INSTALL_ROOT/etc/sermon/agent.toml"
	# The same server again: kept as it is.
	write_config
	grep -q 'sermon.example.com' "$SERMON_INSTALL_ROOT/etc/sermon/agent.toml"
}

@test "never sends the token to the server of an old configuration" {
	SERVER=https://sermon.example.com AGENT_URL=https://sermon.example.com:8443 CONFIG_DIR=/etc/sermon FORCE=''
	write_config
	SERVER=https://other.example.com AGENT_URL=https://other.example.com:8443
	run write_config
	[ "$status" -ne 0 ]
	[[ "$output" == *"points to https://sermon.example.com"*"--force"* ]]
	grep -q '^url = "https://sermon.example.com"$' "$SERMON_INSTALL_ROOT/etc/sermon/agent.toml"
	# With --force the configuration follows the new server.
	FORCE=--force
	write_config
	grep -q '^url = "https://other.example.com"$' "$SERMON_INSTALL_ROOT/etc/sermon/agent.toml"
	grep -q '^agent_url = "https://other.example.com:8443"$' "$SERMON_INSTALL_ROOT/etc/sermon/agent.toml"
}

@test "uses an existing sermon user only if it is a system account" {
	STATE_DIR=/var/lib/sermon-agent
	id() { return 0; }
	usermod() { :; }
	getent() {
		case "$1 $2" in
		"passwd sermon") printf '%s\n' "$ACCOUNT" ;;
		*) return 2 ;;
		esac
	}
	ACCOUNT='sermon:x:998:998::/var/lib/sermon-agent:/usr/sbin/nologin'
	run create_user
	[ "$status" -eq 0 ]
	ACCOUNT='sermon:x:101:101:sermon:/var/lib/sermon-agent:/sbin/nologin'
	run create_user
	[ "$status" -eq 0 ]
	ACCOUNT='sermon:x:1001:1001:Sermon Person:/home/sermon:/bin/bash'
	run create_user
	[ "$status" -ne 0 ]
	[[ "$output" == *"not a system account"* ]]
	ACCOUNT='sermon:x:997:997::/var/lib/sermon-agent:/bin/sh'
	run create_user
	[ "$status" -ne 0 ]
}

@test "enrolls as the service user, never as root" {
	runuser() { printf '%s\n' "runuser $*" "token=${SERMON_ENROLLMENT_TOKEN:-}"; }
	TOKEN=sme_secret BIN_DIR=/opt/bin
	run as_service_user /opt/bin/sermon-agent enroll --config /etc/sermon/agent.toml
	[ "$status" -eq 0 ]
	[[ "$output" == *"runuser -u sermon -- /opt/bin/sermon-agent enroll --config /etc/sermon/agent.toml"* ]]
	[[ "$output" == *"token=sme_secret"* ]]
	# The token travels in the environment, not in the arguments (visible in ps).
	[[ "${lines[0]}" != *sme_secret* ]]
}

@test "release-sign refuses keys other than Ed25519" {
	openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$TMP/ec.pem" 2>/dev/null
	run "$BATS_TEST_DIRNAME/../../scripts/release-sign.sh" pubkey "$TMP/ec.pem"
	[ "$status" -ne 0 ]
	[[ "$output" == *"not an Ed25519 key"* ]]
	run "$BATS_TEST_DIRNAME/../../scripts/release-sign.sh" sign "$TMP/ec.pem" "$BIN"
	[ "$status" -ne 0 ]
}
