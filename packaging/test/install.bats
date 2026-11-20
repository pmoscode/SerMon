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
	printf 'agent binary\n' >"$TMP/sermon-agent-linux-amd64"
	"$BATS_TEST_DIRNAME/../../scripts/release-sign.sh" sign "$KEY" "$TMP/sermon-agent-linux-amd64"
	BIN="$TMP/sermon-agent-linux-amd64"
}

teardown() { rm -rf "$TMP"; }

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
	"$BATS_TEST_DIRNAME/../../scripts/release-sign.sh" sign "$TMP/other.pem" "$BIN"
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
