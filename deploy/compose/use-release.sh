#!/bin/sh
# Pins the server image of a release in .env after checking its signature (V1-04, ADR 0043):
# cosign verifies that ghcr.io/pmoscode/sermon-server:VERSION is signed by the release workflow
# of SerMon on the tag vVERSION (Sigstore, keyless) and names the digest it checked; only that
# digest goes into .env as SERMON_IMAGE_DIGEST. Install and upgrade alike:
#
#   ./use-release.sh VERSION [ENV_FILE]       (default ./.env next to this script)
#   docker compose pull && docker compose up -d
#
# cosign comes from the PATH or, without it, runs as the container COSIGN_IMAGE (pinned by
# digest). The previous digest stays in ENV_FILE as a comment (rollback: put it back).
set -eu

COSIGN_IMAGE=ghcr.io/sigstore/cosign/cosign:v3.1.4@sha256:0f06154836d2de34f887a5c273506d52ca43edaa1d9e53e6510fa78a48f69c2d
IMAGE=ghcr.io/pmoscode/sermon-server
WORKFLOW=https://github.com/pmoscode/SerMon/.github/workflows/release.yml
ISSUER=https://token.actions.githubusercontent.com

die() {
	echo "use-release: $*" >&2
	exit 1
}

[ $# -ge 1 ] && [ $# -le 2 ] || die "usage: $0 VERSION [ENV_FILE]"
version=$1
env_file=${2:-$(cd "$(dirname "$0")" && pwd)/.env}
echo "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$' || die "not a release version: $version (X.Y.Z or X.Y.Z-rc.N)"
[ -f "$env_file" ] || die "no $env_file – cp env.example .env first"

run_cosign() {
	if command -v cosign >/dev/null 2>&1; then
		cosign "$@"
	elif command -v docker >/dev/null 2>&1; then
		docker run --rm "$COSIGN_IMAGE" "$@"
	else
		die "cosign or docker is needed"
	fi
}

echo "▸ checking $IMAGE:$version (signed by $WORKFLOW@refs/tags/v$version)"
out=$(run_cosign verify --certificate-identity "$WORKFLOW@refs/tags/v$version" --certificate-oidc-issuer "$ISSUER" \
	"$IMAGE:$version" 2>/dev/null) || die "$IMAGE:$version is not signed by the release workflow of v$version – not using it"
digests=$(printf '%s\n' "$out" | grep -o '"docker-manifest-digest" *: *"[^"]*"' | sed 's/.*"\([^"]*\)"$/\1/' | sort -u)
[ -n "$digests" ] || die "cosign named no digest"
[ "$(printf '%s\n' "$digests" | wc -l | tr -d ' ')" = 1 ] || die "the signatures name more than one image: $digests"
digest=$digests
echo "$digest" | grep -Eqx 'sha256:[0-9a-f]{64}' || die "cosign named no image digest: $digest"

previous=$(sed -n 's/^SERMON_IMAGE_DIGEST=//p' "$env_file" | tail -1)
tmp=$(mktemp "$env_file.XXXXXX")
trap 'rm -f "$tmp"' EXIT
grep -v -e '^SERMON_IMAGE_DIGEST=' -e '^# use-release.sh: ' "$env_file" >"$tmp" || true
{
	echo "# use-release.sh: release $version, checked $(date -u +%Y-%m-%dT%H:%MZ)${previous:+, before: $previous}"
	echo "SERMON_IMAGE_DIGEST=$digest"
} >>"$tmp"
chmod "$(stat -c %a "$env_file" 2>/dev/null || stat -f %Lp "$env_file")" "$tmp"
mv "$tmp" "$env_file"
trap - EXIT
echo "✓ $IMAGE@$digest (release $version) in $env_file"
echo "  next: a backup (docs/admin/backup.md), then docker compose pull && docker compose up -d"
