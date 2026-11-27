#!/bin/sh
# Tests for check-compose.sh. Run: sh scripts/check-compose_test.sh (requires docker compose).
set -eu

dir=$(cd "$(dirname "$0")" && pwd)
data="$dir/testdata/compose"
failures=0
total=0

# expect <pass|fail> <description> <args...>
expect() {
  expected=$1 description=$2
  shift 2
  if sh "$dir/check-compose.sh" "$@" >/dev/null 2>&1; then actual=pass; else actual=fail; fi
  total=$((total + 1))
  if [ "$actual" != "$expected" ]; then
    echo "FAIL: $description – expected $expected, got $actual"
    failures=$((failures + 1))
  fi
}

expect pass "pinned images, loopback ports, telemetry off" --loopback-only "$data/good.yaml"
expect fail "image without digest" "$data/unpinned.yaml"
expect fail "port bound to all interfaces with --loopback-only" --loopback-only "$data/public-port.yaml"
expect pass "public port allowed without --loopback-only" "$data/public-port.yaml"
expect fail "TimescaleDB telemetry not disabled" "$data/timescale-telemetry.yaml"
expect fail "Loki usage reporting not disabled" "$data/loki-telemetry.yaml"
# Loki without a login of its own (ADR 0031): multi-tenancy on, no published port in production.
expect fail "Loki without auth_enabled (one tenant for all organizations)" "$data/loki-no-auth.yaml"
expect fail "Loki port published with --hardened" --hardened "$data/hardened-loki-port.yaml"
# M3-27: Loki's memory is bounded in production.
expect pass "hardened Loki with a memory limit" --hardened "$data/hardened-loki-good.yaml"
expect fail "Loki without a memory limit with --hardened" --hardened "$data/hardened-loki-no-memory.yaml"
expect pass "ports with variables and loopback defaults" --loopback-only "$data/variable-port.yaml"
expect fail "missing file" "$data/does-not-exist.yaml"
expect pass "built images with pinned bases (literal, build arg, earlier stage)" "$data/build-good.yaml"
expect fail "built image with an unpinned base" "$data/build-unpinned.yaml"
expect fail "built image whose base comes from an unpinned build arg default" "$data/build-arg-unpinned.yaml"
# Production (M2-26): every container read-only, without capabilities and new privileges.
expect pass "hardened services" --hardened "$data/hardened-good.yaml"
expect fail "writable root file system with --hardened" --hardened "$data/hardened-writable.yaml"
expect pass "writable root file system without --hardened" "$data/hardened-writable.yaml"
expect fail "capabilities not dropped with --hardened" --hardened "$data/hardened-caps.yaml"
expect fail "new privileges allowed with --hardened" --hardened "$data/hardened-new-privileges.yaml"
expect fail "privileged container with --hardened" --hardened "$data/hardened-privileged.yaml"
expect fail "Docker socket mounted with --hardened" --hardened "$data/hardened-socket.yaml"
expect fail "host network with --hardened" --hardened "$data/hardened-host-network.yaml"
# Database credentials (M2-36): the owner only in a one-shot container, the superuser only in the database.
expect pass "owner URL in a one-shot container, superuser in the database" --hardened "$data/hardened-owner-good.yaml"
expect fail "owner URL in a long-running container" --hardened "$data/hardened-owner-in-server.yaml"
expect fail "superuser password outside the database" --hardened "$data/hardened-superuser-in-app.yaml"
expect fail "backup role in a long-running container" --hardened "$data/hardened-backup-in-server.yaml"
expect fail "secret backup key in a long-running container" --hardened "$data/hardened-identity-in-server.yaml"
expect fail "writable root file system behind a profile" --hardened "$data/hardened-profile-writable.yaml"
# More ways out of a container (M2-39).
expect fail "SYS_ADMIN added back with --hardened" --hardened "$data/hardened-cap-sys-admin.yaml"
expect fail "host PID namespace with --hardened" --hardened "$data/hardened-pid-host.yaml"
expect fail "seccomp unconfined with --hardened" --hardened "$data/hardened-seccomp-unconfined.yaml"
expect fail "host device with --hardened" --hardened "$data/hardened-devices.yaml"
expect fail "host /run mounted with --hardened" --hardened "$data/hardened-run-mount.yaml"
# The server image (V1-04): the published one by digest, or built from source.
expect pass "the published server image by digest" --hardened "$data/hardened-server-release.yaml"
expect fail "the server image by tag" --hardened "$data/hardened-server-tag.yaml"
# Docker ignores a tag next to the digest; it would only mislead about the version.
expect fail "the server image with a tag next to the digest" --hardened "$data/hardened-server-tag-digest.yaml"
expect fail "a server image from another registry" --hardened "$data/hardened-server-other-registry.yaml"
expect pass "the server built from source" --hardened "$data/hardened-server-source.yaml"
# Several files are merged like docker compose -f a -f b; the result is checked.
expect pass "base and overlay merged" "$data/hardened-good.yaml" "$data/good.yaml"
expect fail "an overlay that unpins an image" "$data/hardened-good.yaml" "$data/overlay-unpinned.yaml"

if [ "$failures" -ne 0 ]; then
  echo "$failures of $total compose checks failed"
  exit 1
fi
echo "all $total compose checks passed"
