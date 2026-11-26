#!/bin/sh
# SerMon agent installer (KONZEPT §6, ADR 0012).
#
#    export SERMON_ENROLLMENT_TOKEN=sme_XXXXXXXX
#   curl -fsSL https://sermon.example.com/install.sh |
#     sudo --preserve-env=SERMON_ENROLLMENT_TOKEN sh -s -- --server https://sermon.example.com
#
# The token comes from SERMON_ENROLLMENT_TOKEN or --token-file; arguments are visible to every
# local user in the process list (the leading space keeps the export out of the bash history).
# The script downloads the binaries of agent and root helper from this server and checks their
# signatures against the release key embedded below before anything is installed; a wrong or
# missing signature aborts. The root helper (ADR 0035) takes updates and reboots for the agent as
# root; /etc/sermon/helper.toml says what this host allows, --no-helper leaves it out entirely.
# With --repo the packages come from the server's signed package repository instead (apt, dnf or
# apk, ADR 0036): the package manager keeps them up to date.
# Read it before you run it: https://<server>/install.sh
set -eu

# Release public key (minisign format, Ed25519). Replaced when the server is built; a server
# without release key serves the placeholder and the script refuses to install.
RELEASE_KEY='@SERMON_RELEASE_KEY@'
# The version of the server; only an agent signed for exactly this version is installed (no
# rollback to an older signed release). Replaced when the server is built.
AGENT_VERSION='@SERMON_AGENT_VERSION@'

BIN_DIR=/usr/local/bin
# Where the packages install the binaries, and the server of their example configuration.
PACKAGE_BIN_DIR=/usr/bin
PACKAGE_EXAMPLE=https://sermon.example.com
# The repository keys as the agent package installs them (scripts/package-agent.sh).
APT_KEYRING=/usr/share/keyrings/sermon-archive-keyring.asc
RPM_KEY=/etc/pki/rpm-gpg/RPM-GPG-KEY-sermon
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
usage: install.sh --server URL [--token-file FILE] [--ca-fingerprint sha256:…] [--agent-url URL] [--force]
                  [--repo] [--grant-log-access]
                  [--no-helper | --no-remote-updates --allow-remote-reboot --no-self-update --no-smart]

  --server URL           public HTTPS address of sermon-server (as in the browser)
  --token-file FILE      file with the enrollment token from the UI (or SERMON_ENROLLMENT_TOKEN)
  --ca-fingerprint FP    SHA-256 fingerprint of a self-signed server certificate
  --agent-url URL        mTLS agent API if not <server host>:8443
  --force                replace an existing enrollment of this host
  --repo                 install the packages from the server's signed repository (apt, dnf, apk)
                         instead of the binaries; the package manager updates them from then on
  --grant-log-access     without journald (Alpine): let busybox syslogd write its files with the
                         group sermon instead of wheel, so the agent reads the security events
  --no-helper            no root helper: the server can't install updates or reboot this host
  --no-remote-updates    the helper installs no updates on request (helper.toml)
  --allow-remote-reboot  the helper reboots on request, e.g. after updates (off by default)
  --no-self-update       the helper doesn't replace the agent with a new signed version
  --no-smart             the helper reads no SMART data of the disks (module smart)
                         Without these flags an existing /etc/sermon/helper.toml stays as it is.
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

# verify FILE SIGFILE checks a minisign signature (Ed25519, "Ed" algorithm) against RELEASE_KEY,
# including the trusted comment: it must name this file and AGENT_VERSION. minisign is used when
# installed; otherwise openssl (3.0 or later) checks the same signatures.
verify() {
	# Prefixed names: POSIX sh has no local variables, main() uses work and binary.
	verify_file=$1
	verify_sig=$2
	case $RELEASE_KEY in
	@*@ | "") die "this server was built without a release key – install the agent from the package repository" ;;
	esac
	case $AGENT_VERSION in
	@*@ | "") die "this server names no agent version – install the agent from the package repository" ;;
	esac
	verify_comment=$(sed -n '3s/^trusted comment: //p' "$verify_sig")
	if command -v minisign >/dev/null 2>&1; then
		# minisign checks the file signature and the global signature over the trusted comment.
		minisign -Vq -P "$RELEASE_KEY" -m "$verify_file" -x "$verify_sig" >/dev/null 2>&1 ||
			die "signature check failed for $(basename "$verify_file") – not installing"
		check_comment "$(basename "$verify_file")" "$verify_comment"
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
	# The global signature covers the file signature and the trusted comment (minisign format).
	sed -n 4p "$verify_sig" | b64decode >"$verify_dir/global" 2>/dev/null || die "malformed signature file"
	[ "$(wc -c <"$verify_dir/global" | tr -d ' ')" = 64 ] || die "malformed signature file"
	{
		cat "$verify_dir/sig.raw"
		printf '%s' "$verify_comment"
	} >"$verify_dir/global.in"
	openssl pkeyutl -verify -pubin -keyform DER -inkey "$verify_dir/key.der" -rawin \
		-in "$verify_dir/global.in" -sigfile "$verify_dir/global" >/dev/null 2>&1 ||
		die "signature check failed for $(basename "$verify_file") (trusted comment) – not installing"
	check_comment "$(basename "$verify_file")" "$verify_comment"
}

# check_comment NAME COMMENT requires the signed trusted comment to name the file NAME and
# AGENT_VERSION: a valid signature of another release or another file is not enough.
check_comment() {
	comment_file=$(printf '%s\n' "$2" | tr '\t' '\n' | sed -n 's/^file://p')
	comment_version=$(printf '%s\n' "$2" | tr '\t' '\n' | sed -n 's/^version://p')
	[ "$comment_file" = "$1" ] ||
		die "the signature belongs to ${comment_file:-an unnamed file}, not $1 – not installing"
	[ "$comment_version" = "$AGENT_VERSION" ] ||
		die "$1 is signed for version ${comment_version:-unknown}, this server runs $AGENT_VERSION – not installing"
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
	if id sermon >/dev/null 2>&1; then
		check_system_user
	else
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
# as_service_user CMD… runs a command as the sermon user with the environment kept (the enrollment
# token travels in SERMON_ENROLLMENT_TOKEN, not in the arguments). enroll must not run as root in
# the state directory that belongs to sermon (symlink attacks; the agent refuses that, too).
as_service_user() {
	if command -v runuser >/dev/null 2>&1; then
		SERMON_ENROLLMENT_TOKEN=$TOKEN runuser -u sermon -- "$@"
	elif command -v setpriv >/dev/null 2>&1; then
		SERMON_ENROLLMENT_TOKEN=$TOKEN setpriv --reuid=sermon --regid=sermon --init-groups -- "$@"
	else
		die "neither runuser nor setpriv found (util-linux) – needed to enroll as the sermon user"
	fi
}

# check_system_user refuses an existing sermon user that looks like a person's account: it would
# get the agent's key and the read access to the logs.
check_system_user() {
	entry=$(getent passwd sermon) || die "cannot read the account of the existing user sermon"
	uid=$(printf '%s\n' "$entry" | cut -d: -f3)
	shell=$(printf '%s\n' "$entry" | cut -d: -f7)
	case "$shell" in
	*/nologin | */false) ;;
	*) uid='' ;;
	esac
	if [ -z "$uid" ] || [ "$uid" -ge 1000 ] || [ "$uid" -eq 0 ]; then
		die "the existing user sermon is not a system account without login (uid ${uid:-?}, shell $shell) – remove or rename it first"
	fi
}

agent_url_for() {
	host=$(printf '%s\n' "$1" | sed -e 's|^[a-z]*://||' -e 's|[/?#].*$||' -e 's|:[0-9]*$||')
	echo "https://$host:$AGENT_PORT"
}

write_config() {
	mkdir -p "$ROOT$CONFIG_DIR"
	if [ -e "$ROOT$CONFIG_DIR/agent.toml" ]; then
		# The token goes to the server of the configuration: never to another one than asked for.
		current=$(sed -n 's/^url = "\(.*\)"$/\1/p' "$ROOT$CONFIG_DIR/agent.toml" | head -n 1)
		if [ "$current" = "$SERVER" ]; then
			say "keeping the existing $CONFIG_DIR/agent.toml"
			return 0
		fi
		# The example of the package (--repo) points nowhere yet.
		[ -n "$FORCE" ] || { [ -n "${REPO:-}" ] && [ "$current" = "$PACKAGE_EXAMPLE" ]; } || die "$CONFIG_DIR/agent.toml points to ${current:-an unknown server}, not $SERVER – use --force to replace it"
		say "replacing $CONFIG_DIR/agent.toml (was ${current:-unknown})"
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

# --- root helper (ADR 0035) --------------------------------------------------------------------

# set_option KEY VALUE sets KEY in helper.toml, appended if missing.
set_option() {
	conf="$ROOT$CONFIG_DIR/helper.toml"
	if grep -q "^$1 *=" "$conf"; then
		sed -i "s/^$1 *=.*/$1 = $2/" "$conf"
	else
		echo "$1 = $2" >>"$conf"
	fi
}

# write_helper_config writes /etc/sermon/helper.toml once; later runs change only the values of
# the flags given – the host's own choices stay.
write_helper_config() {
	conf="$ROOT$CONFIG_DIR/helper.toml"
	mkdir -p "$ROOT$CONFIG_DIR"
	if [ ! -e "$conf" ]; then
		cat >"$conf" <<'TOML'
@SERMON_HELPER_CONFIG@
TOML
	elif [ -z "$HELPER_FLAGS" ]; then
		say "keeping the existing $CONFIG_DIR/helper.toml"
	fi
	[ -z "$REMOTE_UPDATES" ] || set_option allow_remote_updates "$REMOTE_UPDATES"
	[ -z "$REMOTE_REBOOT" ] || set_option allow_remote_reboot "$REMOTE_REBOOT"
	[ -z "$SELF_UPDATE" ] || set_option allow_self_update "$SELF_UPDATE"
	[ -z "$SMART" ] || set_option allow_smart "$SMART"
	[ -n "$ROOT" ] || chown root:root "$conf"
	chmod 0644 "$conf"
}

# remove_helper INIT switches an installed helper off for --no-helper: the server can't install
# updates or reboot this host any more. Its files stay, so it can be switched on again.
remove_helper() {
	[ -e "$ROOT$BIN_DIR/sermon-helper" ] || return 0
	case $1 in
	systemd) systemctl disable --now sermon-helper.socket sermon-helper.service >/dev/null 2>&1 || true ;;
	openrc)
		rc-service sermon-helper stop >/dev/null 2>&1 || true
		rc-update del sermon-helper default >/dev/null 2>&1 || true
		;;
	esac
	say "--no-helper: the root helper is switched off (no remote updates or reboots)"
}

# restart_helper INIT makes a running helper read helper.toml again.
restart_helper() {
	case $1 in
	systemd) systemctl try-restart sermon-helper.service ;;
	openrc) rc-service --ifstarted sermon-helper restart ;;
	esac
}

# keep_agent_signature SIGFILE keeps the checked signature of the installed agent for the root
# helper: its record of the installed version, which a self-update must exceed (ADR 0036).
keep_agent_signature() {
	mkdir -p "$ROOT/var/lib/sermon-helper"
	chmod 0700 "$ROOT/var/lib/sermon-helper"
	install -m 0600 "$1" "$ROOT/var/lib/sermon-helper/agent.minisig"
}

# install_helper WORK ARCH INIT installs the signed root helper and starts its socket (systemd) or
# service (OpenRC). A server without helper binary leaves the agent without remote updates.
install_helper() {
	helper="sermon-helper-linux-$2"
	if ! download "$SERVER/downloads/$helper" "$1/$helper" 2>/dev/null; then
		say "warning: $SERVER offers no root helper – this host gets no remote updates or reboots"
		return 0
	fi
	download "$SERVER/downloads/$helper.minisig" "$1/$helper.minisig" || die "cannot download the helper's signature"
	verify "$1/$helper" "$1/$helper.minisig"
	# Enabled on the first installation only: an admin who switched the helper off keeps it off.
	first=1
	[ ! -e "$ROOT$BIN_DIR/sermon-helper" ] || first=''
	install -m 0755 "$1/$helper" "$BIN_DIR/sermon-helper"
	write_helper_config
	mkdir -p /var/lib/sermon-helper
	chmod 0700 /var/lib/sermon-helper
	case $3 in
	systemd)
		cat >"$ROOT/etc/systemd/system/sermon-helper.service" <<'UNIT'
@SERMON_HELPER_UNIT@
UNIT
		cat >"$ROOT/etc/systemd/system/sermon-helper.socket" <<'UNIT'
@SERMON_HELPER_SOCKET@
UNIT
		systemctl daemon-reload
		[ -z "$first" ] || systemctl enable --now sermon-helper.socket
		systemctl try-restart sermon-helper.service
		;;
	openrc)
		cat >"$ROOT/etc/init.d/sermon-helper" <<'SERVICE'
@SERMON_HELPER_SERVICE@
SERVICE
		chmod 0755 "$ROOT/etc/init.d/sermon-helper"
		if [ -n "$first" ]; then
			rc-update add sermon-helper default
			rc-service sermon-helper restart
		else
			rc-service --ifstarted sermon-helper restart
		fi
		;;
	esac
	say "root helper installed, $CONFIG_DIR/helper.toml: $(grep '^allow_' "$ROOT$CONFIG_DIR/helper.toml" | tr '\n' ' ')"
}

# sha256_of FILE prints the SHA-256 of FILE in hex.
sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	else
		openssl dgst -sha256 <"$1" | sed 's/^.*= //'
	fi
}

# fetch_key_list WORK downloads the list of the repository's public keys (keys/SHA256SUMS, from
# scripts/repo-keys.sh). With a release key it must carry the release's signature, like the
# binaries: whoever can change the files the server serves can't swap the keys.
fetch_key_list() {
	download "$repo/keys/SHA256SUMS" "$1/SHA256SUMS" || die "cannot download the repository keys from $SERVER"
	case $RELEASE_KEY in
	@*@ | "") say "warning: this server has no release key – only TLS vouches for the repository keys" ;;
	*)
		download "$repo/keys/SHA256SUMS.minisig" "$1/SHA256SUMS.minisig" ||
			die "cannot download the signature of the repository keys from $SERVER"
		verify "$1/SHA256SUMS" "$1/SHA256SUMS.minisig"
		;;
	esac
}

# install_key WORK NAME DEST downloads the key NAME of the list to DEST if its SHA-256 matches.
install_key() {
	key_sum=$(awk -v name="$2" '$2 == name { print $1 }' "$1/SHA256SUMS")
	[ -n "$key_sum" ] || die "the repository of $SERVER lists no key $2"
	download "$repo/keys/$2" "$1/$2" || die "cannot download the repository key $2 from $SERVER"
	[ "$(sha256_of "$1/$2")" = "$key_sum" ] || die "the repository key $2 does not match its checksum – not installing"
	cp "$1/$2" "$3"
	chmod 0644 "$3"
}

# install_from_repo DISTRO WORK sets up the server's package repository with its keys and installs
# the agent (and the root helper unless --no-helper); apt, dnf and apk check every index and
# package against the keys (ADR 0036). The keys go where the agent package keeps them: from then on
# every update of it brings the keys of the next rotation (M4-31).
install_from_repo() {
	repo="$SERVER/repo"
	packages=sermon-agent
	[ -n "$NO_HELPER" ] || packages="sermon-agent sermon-helper"
	case $1 in
	debian | ubuntu)
		fetch_key_list "$2"
		mkdir -p "$ROOT/usr/share/keyrings" "$ROOT/etc/apt/sources.list.d" "$ROOT/etc/apt/preferences.d"
		install_key "$2" sermon.asc "$ROOT$APT_KEYRING"
		cat >"$ROOT/etc/apt/sources.list.d/sermon.sources" <<EOF
Types: deb
URIs: $repo/apt
Suites: stable
Components: main
Signed-By: $APT_KEYRING
EOF
		# A newer package of the same name from another source would replace the agent with
		# the next upgrade (M4-31). Origin and label as build-repos.sh sets them.
		cat >"$ROOT/etc/apt/preferences.d/sermon" <<EOF
Package: sermon-agent sermon-helper
Pin: release o=SerMon,l=SerMon
Pin-Priority: 999
EOF
		apt-get update -qq || die "apt cannot read the repository of $SERVER"
		# Without recommends: --no-helper means no helper.
		# shellcheck disable=SC2086 # one word per package
		DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends $packages >/dev/null ||
			die "apt cannot install $packages"
		;;
	rhel | rocky | almalinux | centos | fedora)
		fetch_key_list "$2"
		mkdir -p "$ROOT/etc/pki/rpm-gpg" "$ROOT/etc/yum.repos.d"
		install_key "$2" sermon.asc "$ROOT$RPM_KEY"
		# Only the SerMon packages from here, and these before any other source (M4-31).
		cat >"$ROOT/etc/yum.repos.d/sermon.repo" <<EOF
[sermon]
name=SerMon
baseurl=$repo/rpm
gpgcheck=1
repo_gpgcheck=1
gpgkey=file://$RPM_KEY
includepkgs=sermon-agent sermon-helper
priority=10
EOF
		# shellcheck disable=SC2086 # one word per package
		dnf install -y -q --setopt=install_weak_deps=False $packages >/dev/null || die "dnf cannot install $packages"
		;;
	alpine)
		fetch_key_list "$2"
		# apk knows a key by its file name (scripts/repo-keys.sh): only names of that form.
		apk_keys=$(sed -n 's/^[0-9a-f]\{64\}  \(sermon-[0-9a-f]\{16\}\.rsa\.pub\)$/\1/p' "$2/SHA256SUMS")
		[ -n "$apk_keys" ] || die "the repository of $SERVER lists no apk key"
		mkdir -p "$ROOT/etc/apk/keys"
		for key in $apk_keys; do
			install_key "$2" "$key" "$ROOT/etc/apk/keys/$key"
		done
		grep -qxF "$repo/apk" "$ROOT/etc/apk/repositories" 2>/dev/null || echo "$repo/apk" >>"$ROOT/etc/apk/repositories"
		apk update -q || die "apk cannot read the repository of $SERVER"
		# shellcheck disable=SC2086 # one word per package
		apk add -q $packages || die "apk cannot install $packages"
		;;
	*) die "--repo knows apt, dnf and apk distributions, not $1 – install without --repo" ;;
	esac
	say "installed $packages from $repo"
}

# ensure_machine_id creates /etc/machine-id (128 random bits, like systemd-machine-id-setup) where
# neither systemd nor dbus left one, as on Alpine: the agent names the host by it.
ensure_machine_id() {
	if [ -s "$ROOT/etc/machine-id" ] || [ -s "$ROOT/var/lib/dbus/machine-id" ]; then
		return 0
	fi
	id=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
	printf '%s\n' "$id" >"$ROOT/etc/machine-id"
	chmod 0444 "$ROOT/etc/machine-id"
	say "created /etc/machine-id"
}

# --- log files (hosts without journald, ADR 0025) ----------------------------------------------

# The files the agent reads its security events from, in its order (agent/…/logfile.rs).
LOG_FILES='var/log/sermon/security.log var/log/auth.log var/log/secure var/log/messages'

# can_read FILE reports whether the sermon user can read FILE.
can_read() {
	if command -v runuser >/dev/null 2>&1; then
		runuser -u sermon -- test -r "$1"
	else
		setpriv --reuid=sermon --regid=sermon --init-groups -- test -r "$1"
	fi
}

# log_source_hint explains how the agent gets its security events if journald is missing and it
# cannot read any of the log files.
log_source_hint() {
	[ ! -d "$ROOT/run/systemd/journal" ] || return 0
	for file in $LOG_FILES; do
		if [ -f "$ROOT/$file" ] && can_read "$ROOT/$file"; then
			return 0
		fi
	done
	say "note: no journald, and the agent cannot read a log file – no security events (SSH, sudo) yet."
	say "  verified events: rsyslog with the SerMon template (help: Unverified source), or"
	say "  rerun with --grant-log-access: busybox syslogd then writes with the group sermon instead of wheel"
}

# grant_log_access lets busybox syslogd under OpenRC write its files with the group sermon instead of
# wheel. Its init script sets the group itself, so the change goes into start_pre of
# /etc/conf.d/syslog, which runs just before the daemon starts. The caller restarts syslog.
grant_log_access() {
	script="$ROOT/etc/init.d/syslog"
	conf="$ROOT/etc/conf.d/syslog"
	grep -q '^command="/sbin/syslogd"' "$script" 2>/dev/null ||
		die "--grant-log-access needs busybox syslogd under OpenRC ($script)"
	grep -q 'SerMon: --grant-log-access' "$conf" 2>/dev/null && return 0
	if grep -q '^start_pre()' "$conf" 2>/dev/null; then
		die "$conf defines start_pre already – set the group sermon for syslogd there yourself"
	fi
	cat >>"$conf" <<'CONF'
# SerMon: --grant-log-access of install.sh. The agent (group sermon) reads the security events from
# the log files; root keeps reading them, members of wheel need doas from now on.
start_pre() {
	start_stop_daemon_args="-g sermon -k 027"
}
CONF
}

# parse_args ARG… sets SERVER, TOKEN, FINGERPRINT, AGENT_URL, FORCE, GRANT_LOG_ACCESS and the
# helper's NO_HELPER, REMOTE_UPDATES, REMOTE_REBOOT, SELF_UPDATE, SMART and HELPER_FLAGS and checks
# them.
parse_args() {
	SERVER=''
	TOKEN=${SERMON_ENROLLMENT_TOKEN:-}
	FINGERPRINT=''
	AGENT_URL=''
	FORCE=''
	REPO=''
	GRANT_LOG_ACCESS=''
	NO_HELPER=''
	# Empty: not given, helper.toml keeps its value (or the default of a new file).
	REMOTE_UPDATES='' REMOTE_REBOOT='' SELF_UPDATE='' SMART='' HELPER_FLAGS=''
	while [ $# -gt 0 ]; do
		case $1 in
		--server) SERVER=${2:-} && shift ;;
		--token)
			TOKEN=${2:-} && shift
			say "warning: --token is visible in the process list – prefer SERMON_ENROLLMENT_TOKEN or --token-file"
			;;
		--token-file)
			[ -r "${2:-}" ] || die "cannot read the token file ${2:-}"
			TOKEN=$(head -n 1 "$2" | tr -d '\r') && shift
			;;
		--ca-fingerprint) FINGERPRINT=${2:-} && shift ;;
		--agent-url) AGENT_URL=${2:-} && shift ;;
		--force) FORCE=--force ;;
		--repo) REPO=1 ;;
		--grant-log-access) GRANT_LOG_ACCESS=1 ;;
		--no-helper) NO_HELPER=1 ;;
		--no-remote-updates) REMOTE_UPDATES=false HELPER_FLAGS=1 ;;
		--allow-remote-reboot) REMOTE_REBOOT=true HELPER_FLAGS=1 ;;
		--no-self-update) SELF_UPDATE=false HELPER_FLAGS=1 ;;
		--no-smart) SMART=false HELPER_FLAGS=1 ;;
		-h | --help) usage && exit 0 ;;
		*) usage && die "unknown argument $1" ;;
		esac
		shift
	done
	[ -n "$SERVER" ] || { usage; die "--server is required"; }
	case $SERVER in https://*) ;; *) die "--server must be an https:// URL" ;; esac
	SERVER=${SERVER%/}
	[ -n "$TOKEN" ] || die "an enrollment token is required (SERMON_ENROLLMENT_TOKEN or --token-file)"
	[ -n "$AGENT_URL" ] || AGENT_URL=$(agent_url_for "$SERVER")
	if [ -n "$NO_HELPER" ] && [ -n "$HELPER_FLAGS" ]; then
		die "--no-helper takes no flags of the helper"
	fi
	if [ -n "$REPO" ] && [ -n "$FINGERPRINT" ]; then
		die "--repo needs a certificate the package manager trusts – install the server's CA instead of --ca-fingerprint"
	fi
}

main() {
	parse_args "$@"
	[ "$(id -u)" = 0 ] || die "please run as root (sudo)"

	arch=$(detect_arch)
	distro=$(detect_distro)
	init=$(detect_init)
	say "installing for $distro ($arch, $init)"

	work=$(mktemp -d)
	trap 'rm -rf "$work"' EXIT
	agent=$BIN_DIR/sermon-agent
	if [ -n "$REPO" ]; then
		install_from_repo "$distro" "$work"
		agent=$PACKAGE_BIN_DIR/sermon-agent
	else
		binary="sermon-agent-linux-$arch"
		download "$SERVER/downloads/$binary" "$work/$binary" || die "cannot download the agent from $SERVER"
		download "$SERVER/downloads/$binary.minisig" "$work/$binary.minisig" || die "cannot download the signature"
		verify "$work/$binary" "$work/$binary.minisig"
		say "signature ok"
		create_user
		install -m 0755 "$work/$binary" "$BIN_DIR/sermon-agent"
		keep_agent_signature "$work/$binary.minisig"
	fi
	ensure_machine_id
	write_config
	chown root:sermon "$CONFIG_DIR" "$CONFIG_DIR/agent.toml"
	chmod 0750 "$CONFIG_DIR"
	mkdir -p "$STATE_DIR"
	chown sermon:sermon "$STATE_DIR"
	chmod 0750 "$STATE_DIR"

	set -- enroll --config "$CONFIG_DIR/agent.toml"
	[ -z "$FINGERPRINT" ] || set -- "$@" --ca-fingerprint "$FINGERPRINT"
	[ -z "$FORCE" ] || set -- "$@" "$FORCE"
	as_service_user "$agent" "$@" || die "enrollment failed (see above)"

	case $init in
	systemd)
		# The packages bring their units.
		[ -n "$REPO" ] || install_systemd_unit
		systemctl daemon-reload
		systemctl enable --now sermon-agent
		;;
	openrc)
		if [ -n "$GRANT_LOG_ACCESS" ]; then
			grant_log_access
			rc-service syslog restart
			# The old daemon may have created a file with wheel while stopping.
			for file in "$ROOT"/var/log/messages*; do
				[ ! -f "$file" ] || chgrp sermon "$file"
			done
			say "syslogd writes with the group sermon now"
		fi
		[ -n "$REPO" ] || install_openrc_service
		rc-update add sermon-agent default
		rc-service sermon-agent restart
		;;
	esac
	if [ -n "$REPO" ]; then
		# The package brought the helper with its helper.toml; the flags change only their values.
		if [ -z "$NO_HELPER" ] && [ -n "$HELPER_FLAGS" ]; then
			write_helper_config
			restart_helper "$init"
		fi
	elif [ -n "$NO_HELPER" ]; then
		remove_helper "$init"
	else
		install_helper "$work" "$arch" "$init"
	fi
	log_source_hint
	say "done – the node shows up in the UI within seconds"
}

[ "${SERMON_INSTALL_TEST:-}" = 1 ] || main "$@"
