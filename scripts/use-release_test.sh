#!/bin/sh
# Tests for deploy/compose/use-release.sh with a fake cosign (and a fake docker for the cosign
# container). Run: sh scripts/use-release_test.sh
set -eu

dir=$(cd "$(dirname "$0")" && pwd)
script=$dir/../deploy/compose/use-release.sh
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
fail() {
	echo "FAIL: $*"
	exit 1
}
d1=sha256:$(printf '1%.0s' $(seq 64))
d2=sha256:$(printf '2%.0s' $(seq 64))

# The fake cosign signs only for the identity of the tag FAKE_TAG, the image FAKE_IMAGE and
# prints the digests in FAKE_DIGESTS as cosign verify does.
mkdir "$work/bin" "$work/docker-bin"
cat >"$work/bin/cosign" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >>"$FAKE_LOG"
[ "$1" = verify ] || exit 1
shift
id='' iss='' target=''
while [ $# -gt 0 ]; do
	case $1 in
	--certificate-identity) id=$2 && shift 2 ;;
	--certificate-oidc-issuer) iss=$2 && shift 2 ;;
	-*) exit 1 ;;
	*) target=$1 && shift ;;
	esac
done
[ "$id" = "https://github.com/pmoscode/SerMon/.github/workflows/release.yml@refs/tags/$FAKE_TAG" ] || exit 1
[ "$iss" = https://token.actions.githubusercontent.com ] || exit 1
[ "$target" = "$FAKE_IMAGE" ] || exit 1
printf '['
sep=''
for d in $FAKE_DIGESTS; do
	printf '%s{"critical":{"identity":{"docker-reference":"ghcr.io/pmoscode/sermon-server"},"image":{"docker-manifest-digest":"%s"},"type":"cosign container image signature"},"optional":null}' "$sep" "$d"
	sep=,
done
printf ']\n'
EOF
# The fake docker runs the fake cosign for "docker run --rm IMAGE ARGS…" with the pinned image.
cat >"$work/docker-bin/docker" <<EOF
#!/bin/sh
[ "\$1 \$2" = "run --rm" ] || exit 1
case \$3 in ghcr.io/sigstore/cosign/cosign:v*@sha256:*) ;; *) exit 1 ;; esac
shift 3
exec "$work/bin/cosign" "\$@"
EOF
chmod +x "$work/bin/cosign" "$work/docker-bin/docker"
export FAKE_LOG="$work/log" FAKE_TAG=v1.2.3-rc.4 FAKE_IMAGE=ghcr.io/pmoscode/sermon-server:1.2.3-rc.4 FAKE_DIGESTS="$d1"

fresh() {
	printf 'SERMON_DOMAIN=sermon.example.org\n# The server image …\nSERMON_IMAGE_DIGEST=\nSERMON_SMTP_HOST=smtp.example.org\n' >"$work/.env"
	chmod 0640 "$work/.env"
	cp "$work/.env" "$work/.env.before"
}
use() { PATH="$work/bin:$PATH" sh "$script" "$@" "$work/.env" >"$work/out" 2>&1; }
# refused DESCRIPTION WHY VERSION: refused for the reason WHY, .env unchanged.
refused() {
	if use "$3"; then fail "$1: passed"; fi
	grep -q "$2" "$work/out" || fail "$1: refused for another reason: $(cat "$work/out")"
	cmp -s "$work/.env" "$work/.env.before" || fail "$1: .env changed"
}

fresh
use 1.2.3-rc.4 || fail "a signed release was refused: $(cat "$work/out")"
[ "$(grep -c '^SERMON_IMAGE_DIGEST=' "$work/.env")" = 1 ] || fail ".env: $(cat "$work/.env")"
grep -qx "SERMON_IMAGE_DIGEST=$d1" "$work/.env" || fail "the digest is not in .env: $(cat "$work/.env")"
for line in SERMON_DOMAIN=sermon.example.org SERMON_SMTP_HOST=smtp.example.org; do
	grep -qx "$line" "$work/.env" || fail "other settings changed: $(cat "$work/.env")"
done
grep -q '^# use-release.sh: release 1.2.3-rc.4' "$work/.env" || fail "no note of the release"
mode=$(stat -c %a "$work/.env" 2>/dev/null || stat -f %Lp "$work/.env")
[ "$mode" = 640 ] || fail ".env is now $mode"
[ -z "$(find "$work" -maxdepth 1 -name '.env.*' ! -name .env.before)" ] || fail "left a temporary file"

# An upgrade replaces the digest and keeps the previous one as a note.
export FAKE_TAG=v1.2.3 FAKE_IMAGE=ghcr.io/pmoscode/sermon-server:1.2.3 FAKE_DIGESTS="$d2"
cp "$work/.env" "$work/.env.before"
use 1.2.3 || fail "the upgrade was refused: $(cat "$work/out")"
grep -qx "SERMON_IMAGE_DIGEST=$d2" "$work/.env" || fail "the new digest is not in .env"
[ "$(grep -c '^SERMON_IMAGE_DIGEST=' "$work/.env")" = 1 ] || fail "two digests: $(cat "$work/.env")"
[ "$(grep -c '^# use-release.sh: ' "$work/.env")" = 1 ] || fail "two notes: $(cat "$work/.env")"
grep -q "before: $d1" "$work/.env" || fail "the previous digest is not noted: $(cat "$work/.env")"

# Not signed for this tag (another tag's signature, or none), more than one image, no digest.
fresh
refused "an image signed on another tag" "not signed by the release workflow of v1.2.4" 1.2.4
export FAKE_TAG=v1.2.3 FAKE_IMAGE=ghcr.io/pmoscode/sermon-server:1.2.3 FAKE_DIGESTS="$d1 $d2"
refused "signatures of two images" "more than one image" 1.2.3
export FAKE_DIGESTS=""
refused "no digest" "named no digest" 1.2.3
export FAKE_DIGESTS="sha256:short"
refused "a digest that is none" "no image digest" 1.2.3
export FAKE_DIGESTS="$d1"
refused "no release version" "not a release version" latest
refused "a version that is no release version" "not a release version" 1.2.3-beta.1
rm "$work/.env"
if PATH="$work/bin:$PATH" sh "$script" 1.2.3 "$work/.env" >/dev/null 2>&1; then fail "a missing .env passed"; fi

# Without cosign: the pinned cosign container (PATH without the fake cosign, with a fake docker).
fresh
: >"$work/log"
PATH="$work/docker-bin:/usr/bin:/bin" sh "$script" 1.2.3 "$work/.env" >"$work/out" 2>&1 || fail "the cosign container was not used: $(cat "$work/out")"
grep -qx "SERMON_IMAGE_DIGEST=$d1" "$work/.env" || fail "the digest from the container is not in .env"
echo "✓ use-release: only a digest signed by the release workflow of the tag goes into .env"
