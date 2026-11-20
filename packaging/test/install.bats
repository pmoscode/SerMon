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
	SERVER=https://other.example.com
	write_config
	grep -q 'sermon.example.com' "$SERMON_INSTALL_ROOT/etc/sermon/agent.toml"
}
