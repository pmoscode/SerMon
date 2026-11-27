#!/bin/sh
# Hands the files of one job of the release workflow to the next (V1-03, ADR 0043). The job that
# made them seals the directory and passes the seal's SHA-256 as a job output; the job that takes
# them checks the directory against that hash before it uses anything in it. An artifact that was
# swapped, changed or extended between the jobs (anyone who can write the run's artifacts) is
# refused, also when the seal was rewritten with it.
#
#   scripts/release-artifacts.sh seal DIR          DIR/SEAL.sha256sums over every file below DIR;
#                                                  prints its SHA-256
#   scripts/release-artifacts.sh check DIR HASH    the seal has HASH, every file matches it, none
#                                                  is missing or extra
#
# Names are plain (letters, digits, . _ + - and /); links are refused. The artifact zip loses the
# executable bit: the taking job sets it again.
set -eu

SEAL=SEAL.sha256sums

die() {
	echo "release-artifacts: $*" >&2
	exit 1
}

sha256() { openssl dgst -sha256 -r "$1" | cut -d' ' -f1; }

# files DIR lists the files below DIR (relative, sorted), the seal excepted.
files() {
	(cd "$1" && find . ! -type d ! -path "./$SEAL" | sed 's#^\./##' | LC_ALL=C sort)
}

plain() {
	case $1 in
	"" | /* | *..* | *[!A-Za-z0-9._+/-]*) return 1 ;;
	esac
}

seal() {
	[ -d "$1" ] || die "no directory $1"
	links=$(cd "$1" && find . -type l)
	[ -z "$links" ] || die "links are not sealed: $links"
	list=$(mktemp)
	trap 'rm -f "$list"' EXIT
	files "$1" | while IFS= read -r f; do
		plain "$f" || die "not a plain name: $f"
		[ -f "$1/$f" ] || die "not a regular file: $f"
		printf '%s  %s\n' "$(sha256 "$1/$f")" "$f"
	done >"$list"
	cp "$list" "$1/$SEAL"
	sha256 "$1/$SEAL"
}

check() {
	dir=$1 want=$2
	echo "$want" | grep -Eq '^[0-9a-f]{64}$' || die "not a SHA-256: $want"
	[ -f "$dir/$SEAL" ] || die "$dir has no $SEAL"
	[ "$(sha256 "$dir/$SEAL")" = "$want" ] || die "the seal of $dir is not the one of the job that made it"
	links=$(cd "$dir" && find . -type l)
	[ -z "$links" ] || die "links in $dir: $links"
	while read -r sum f; do
		plain "$f" || die "the seal names $f"
		[ -f "$dir/$f" ] || die "missing: $f"
		[ "$(sha256 "$dir/$f")" = "$sum" ] || die "changed: $f"
	done <"$dir/$SEAL"
	sealed=$(cut -d' ' -f3- "$dir/$SEAL" | LC_ALL=C sort)
	[ "$(files "$dir")" = "$sealed" ] || die "files in $dir that the seal does not name: $(files "$dir" | grep -vxF "$sealed" | tr '\n' ' ')"
	echo "✓ $dir: $(wc -l <"$dir/$SEAL" | tr -d ' ') files as sealed"
}

case ${1:-} in
seal) [ $# -eq 2 ] || die "usage: $0 seal DIR" && seal "$2" ;;
check) [ $# -eq 3 ] || die "usage: $0 check DIR HASH" && check "$2" "$3" ;;
*) die "usage: $0 seal DIR | check DIR HASH" ;;
esac
