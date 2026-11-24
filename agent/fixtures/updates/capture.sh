#!/bin/sh
# Captures what the root helper's list commands print on every target distribution (M4-06), for
# the parsers of sermon-updates: in fresh containers of the pinned base images, against the
# distributions' own sources (the images are older than the sources, so updates wait). The
# commands and environment are the helper's (ADR 0035, Nachtrag M4-03); the exit code goes to
# <file>.exit. Needs Docker and the internet.
#
#   sh agent/fixtures/updates/capture.sh    → agent/fixtures/updates/<distro>/…
set -eu

here=$(cd "$(dirname "$0")" && pwd)
compose=$(cd "$here/../../.." && pwd)/deploy/e2e/compose.yaml
image() { grep -o "\"$1@sha256:[0-9a-f]*\"" "$compose" | head -1 | tr -d '"'; }

# capture NAME IMAGE SCRIPT: SCRIPT prints sections "=== file exit" followed by the output.
capture() {
	out="$here/$1"
	rm -rf "$out"
	mkdir -p "$out"
	docker run --rm -e LC_ALL=C -e LANG=C -e DEBIAN_FRONTEND=noninteractive "$(image "$2")" sh -c "$3" | awk -v dir="$out" '
		/^=== / { if (file) close(file); file = dir "/" $2; print $3 > (file ".exit"); close(file ".exit"); printf "" > file; next }
		{ print >> file }'
	set -- "$1" "$out"/*.exit
	echo "✓ $1: $(($# - 1)) outputs"
}

# section FILE COMMAND…: runs the command and frames its output for capture; defined for the
# container's shell, so nothing expands here.
# shellcheck disable=SC2016
section='section() { f=$1; shift; out=$("$@" 2>/dev/null); code=$?; echo "=== $f $code"; [ -z "$out" ] || printf "%s\n" "$out"; }'

apt="$section
apt-get update -q >/dev/null 2>&1
section apt-list.txt apt list --upgradable"
dnf="$section
dnf -q makecache >/dev/null 2>&1
section dnf-check-update.txt dnf -q check-update
section dnf-updateinfo-security.txt dnf -q updateinfo list --security
section dnf-needs-restarting.txt dnf needs-restarting -r"
apk="$section
apk update -q >/dev/null 2>&1
section apk-version.txt apk version -l '<'"

capture debian-12 debian:12 "$apt"
capture debian-13 debian:13 "$apt"
capture ubuntu-24.04 ubuntu:24.04 "$apt"
capture rockylinux-9 rockylinux:9 "$dnf"
capture almalinux-10 almalinux:10 "$dnf"
capture fedora-44 fedora:44 "$dnf"
capture alpine-3.22 alpine:3.22 "$apk"
