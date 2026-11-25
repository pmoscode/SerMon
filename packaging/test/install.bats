#!/usr/bin/env bats
# Tests of packaging/install.sh (M1-27); run with `just test-packaging` (Docker: bats, openssl, minisign).

setup() {
	TMP=$(mktemp -d)
	export SERMON_INSTALL_TEST=1 SERMON_INSTALL_ROOT="$TMP/root"
	mkdir -p "$SERMON_INSTALL_ROOT/etc"
	# The helper's configuration as the server renders it into the script (M4-04).
	sed -e "/@SERMON_HELPER_CONFIG@/{r $BATS_TEST_DIRNAME/../helper.toml" -e "d}" "$BATS_TEST_DIRNAME/../install.sh" >"$TMP/install.sh"
	# shellcheck source=../install.sh
	. "$TMP/install.sh"
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

# Hosts without journald (M2-20, ADR 0025): busybox syslogd writes with group wheel; the flag lets it
# use the group sermon through start_pre, because its init script sets the group itself.
@test "grants the agent read access to busybox syslogd's files only when asked" {
	mkdir -p "$SERMON_INSTALL_ROOT/etc/init.d" "$SERMON_INSTALL_ROOT/etc/conf.d"
	printf '#!/sbin/openrc-run\ncommand="/sbin/syslogd"\nstart_stop_daemon_args="-g wheel -k 027"\n' >"$SERMON_INSTALL_ROOT/etc/init.d/syslog"
	printf 'SYSLOGD_OPTS="-t"\n' >"$SERMON_INSTALL_ROOT/etc/conf.d/syslog"
	SERMON_ENROLLMENT_TOKEN=sme_x parse_args --server https://sermon.example.com
	[ -z "$GRANT_LOG_ACCESS" ]
	SERMON_ENROLLMENT_TOKEN=sme_x parse_args --server https://sermon.example.com --grant-log-access
	[ "$GRANT_LOG_ACCESS" = 1 ]

	grant_log_access
	grant_log_access
	[ "$(grep -c 'start_stop_daemon_args="-g sermon -k 027"' "$SERMON_INSTALL_ROOT/etc/conf.d/syslog")" -eq 1 ]
	grep -q '^SYSLOGD_OPTS="-t"$' "$SERMON_INSTALL_ROOT/etc/conf.d/syslog"
	# The function runs in the init script's shell: it must parse.
	sh -n "$SERMON_INSTALL_ROOT/etc/conf.d/syslog"

	# An admin's own start_pre stays; so does another syslog daemon.
	printf 'start_pre() {\n\ttrue\n}\n' >"$SERMON_INSTALL_ROOT/etc/conf.d/syslog"
	run grant_log_access
	[ "$status" -ne 0 ]
	[[ "$output" == *"defines start_pre"* ]]
	printf '#!/sbin/openrc-run\ncommand="/usr/sbin/rsyslogd"\n' >"$SERMON_INSTALL_ROOT/etc/init.d/syslog"
	run grant_log_access
	[ "$status" -ne 0 ]
	[[ "$output" == *"busybox syslogd"* ]]
}

@test "tells how to give the agent its security events when it cannot read a log file" {
	mkdir -p "$SERMON_INSTALL_ROOT/var/log"
	can_read() { return 1; }
	run log_source_hint
	[[ "$output" == *"--grant-log-access"* ]]
	[[ "$output" == *"rsyslog"* ]]
	# With journald, or a readable file, nothing to say.
	mkdir -p "$SERMON_INSTALL_ROOT/run/systemd/journal"
	run log_source_hint
	[ -z "$output" ]
	rmdir "$SERMON_INSTALL_ROOT/run/systemd/journal"
	: >"$SERMON_INSTALL_ROOT/var/log/messages"
	can_read() { [ "$1" = "$SERMON_INSTALL_ROOT/var/log/messages" ]; }
	run log_source_hint
	[ -z "$output" ]
}

# Alpine has no machine-id without systemd or dbus; the agent needs one (M2-20).
@test "creates a machine-id only where there is none" {
	ensure_machine_id
	id=$(cat "$SERMON_INSTALL_ROOT/etc/machine-id")
	[[ "$id" =~ ^[0-9a-f]{32}$ ]]
	[ "$(stat -c %a "$SERMON_INSTALL_ROOT/etc/machine-id")" = 444 ]
	ensure_machine_id
	[ "$(cat "$SERMON_INSTALL_ROOT/etc/machine-id")" = "$id" ]

	rm "$SERMON_INSTALL_ROOT/etc/machine-id"
	mkdir -p "$SERMON_INSTALL_ROOT/var/lib/dbus"
	printf 'fedcba9876543210fedcba9876543210\n' >"$SERMON_INSTALL_ROOT/var/lib/dbus/machine-id"
	ensure_machine_id
	[ ! -e "$SERMON_INSTALL_ROOT/etc/machine-id" ]
}

# M4-04: the root helper's configuration is the host's veto (ADR 0035). Without flags an existing
# helper.toml stays as the admin wrote it; a flag changes its own value only.
@test "writes the helper's configuration and keeps the admin's values" {
	CONFIG_DIR=/etc/sermon
	conf="$SERMON_INSTALL_ROOT/etc/sermon/helper.toml"
	parse_args --server https://sermon.example.com --token-file <(echo sme_x) 2>/dev/null
	write_helper_config
	grep -q '^allow_remote_updates = true$' "$conf"
	grep -q '^allow_remote_reboot = false$' "$conf"
	grep -q '^allow_self_update = true$' "$conf"
	[ "$(stat -c %a "$conf")" = 644 ]
	[ "$(grep -c '^allow_' "$conf")" = 3 ]
	# The admin allows reboots; a second run without flags keeps that.
	sed -i 's/^allow_remote_reboot = false$/allow_remote_reboot = true/' "$conf"
	echo '# own note' >>"$conf"
	write_helper_config
	grep -q '^allow_remote_reboot = true$' "$conf"
	grep -q '^# own note$' "$conf"
	# A flag changes only its value.
	parse_args --server https://sermon.example.com --token-file <(echo sme_x) --no-remote-updates --no-self-update 2>/dev/null
	write_helper_config
	grep -q '^allow_remote_updates = false$' "$conf"
	grep -q '^allow_self_update = false$' "$conf"
	grep -q '^allow_remote_reboot = true$' "$conf"
	grep -q '^# own note$' "$conf"
	[ "$(grep -c '^allow_remote_updates' "$conf")" = 1 ]
}

@test "takes the helper's flags and --no-helper" {
	parse_args --server https://sermon.example.com --token-file <(echo sme_x) 2>/dev/null
	[ -z "$NO_HELPER" ] && [ -z "$HELPER_FLAGS" ]
	parse_args --server https://sermon.example.com --token-file <(echo sme_x) --allow-remote-reboot 2>/dev/null
	[ "$REMOTE_REBOOT" = true ] && [ -n "$HELPER_FLAGS" ]
	parse_args --server https://sermon.example.com --token-file <(echo sme_x) --no-helper 2>/dev/null
	[ -n "$NO_HELPER" ]
	run parse_args --server https://sermon.example.com --token-file <(echo sme_x) --no-helper --allow-remote-reboot
	[ "$status" -ne 0 ]
	[[ "$output" == *"--no-helper"* ]]
}

# The package's script enables the helper on the first installation only: an admin who switched
# it off keeps it off across upgrades (deb: configure OLD, rpm: 2).
@test "the helper package enables the helper only when first installed" {
	mkdir -p "$TMP/fake" "$TMP/run/systemd/system"
	printf '#!/bin/sh\necho "systemctl $*" >>%s/calls\n' "$TMP" >"$TMP/fake/systemctl"
	chmod 0755 "$TMP/fake/systemctl"
	script="$BATS_TEST_DIRNAME/../nfpm/helper-postinstall.sh"
	helper_postinstall() { sed -e "s#/run/systemd/system#$TMP/run/systemd/system#" -e "s#/var/lib/sermon-helper#$TMP/state#g" "$script" | PATH="$TMP/fake:$PATH" sh -s -- "$@"; }
	for args in "configure" "configure ''" "1"; do
		rm -f "$TMP/calls"
		eval "helper_postinstall $args"
		grep -q 'enable --now sermon-helper.socket' "$TMP/calls" || { echo "not enabled on first install ($args)"; return 1; }
	done
	[ "$(stat -c %a "$TMP/state")" = 700 ]
	for args in "configure 0.0.1" "2"; do
		rm -f "$TMP/calls"
		eval "helper_postinstall $args"
		! grep -q 'enable' "$TMP/calls" || { echo "enabled again on an upgrade ($args)"; return 1; }
		grep -q 'try-restart sermon-helper.service' "$TMP/calls"
	done
}

# Review M4-04: a rerun enables the helper only if it was not installed before, and --no-helper
# switches an installed one off.
@test "a rerun respects a helper the admin switched off, --no-helper switches it off" {
	mkdir -p "$TMP/fake" "$SERMON_INSTALL_ROOT/etc/systemd/system" "$SERMON_INSTALL_ROOT/usr/local/bin" "$TMP/work"
	for tool in systemctl rc-update rc-service; do
		printf '#!/bin/sh\necho "%s $*" >>%s/calls\n' "$tool" "$TMP" >"$TMP/fake/$tool"
		chmod 0755 "$TMP/fake/$tool"
	done
	printf '#!/bin/sh\necho "install $*" >>%s/calls\n' "$TMP" >"$TMP/fake/install"
	chmod 0755 "$TMP/fake/install"
	download() { cp "$BIN" "$2"; }
	verify() { :; }
	SERVER=https://sermon.example.com CONFIG_DIR=/etc/sermon BIN_DIR=/usr/local/bin
	REMOTE_UPDATES='' REMOTE_REBOOT='' SELF_UPDATE='' HELPER_FLAGS=''
	PATH="$TMP/fake:$PATH" install_helper "$TMP/work" amd64 systemd 2>/dev/null || true
	grep -q 'systemctl enable --now sermon-helper.socket' "$TMP/calls"
	# Installed before: a rerun restarts but doesn't enable.
	touch "$SERMON_INSTALL_ROOT/usr/local/bin/sermon-helper"
	rm "$TMP/calls"
	PATH="$TMP/fake:$PATH" install_helper "$TMP/work" amd64 systemd 2>/dev/null || true
	! grep -q 'enable' "$TMP/calls"
	grep -q 'try-restart sermon-helper.service' "$TMP/calls"
	rm "$TMP/calls"
	PATH="$TMP/fake:$PATH" install_helper "$TMP/work" amd64 openrc 2>/dev/null || true
	! grep -q 'rc-update add' "$TMP/calls"
	grep -q 'rc-service --ifstarted sermon-helper restart' "$TMP/calls"
	# --no-helper: switched off.
	rm "$TMP/calls"
	PATH="$TMP/fake:$PATH" run remove_helper systemd
	[[ "$output" == *"switched off"* ]]
	grep -q 'systemctl disable --now sermon-helper.socket sermon-helper.service' "$TMP/calls"
}
