#!/usr/bin/env bash
# Installs the SerMon toolchain inside the devcontainer (postCreateCommand).
# Versions match CI and are updated by Renovate. Tools are built from their package registries
# with pinned versions (cargo --locked / Go checksum database); zig is verified by SHA-256.
set -euo pipefail

# renovate: datasource=github-releases depName=casey/just
JUST_VERSION=1.59.0
# renovate: datasource=github-releases depName=EmbarkStudios/cargo-deny
CARGO_DENY_VERSION=0.20.2
# renovate: datasource=github-releases depName=rust-cross/cargo-zigbuild
CARGO_ZIGBUILD_VERSION=0.24.1
# renovate: datasource=crate depName=zizmor
ZIZMOR_VERSION=1.32.0
# renovate: datasource=go depName=github.com/bufbuild/buf
BUF_VERSION=v1.75.0
# renovate: datasource=go depName=github.com/golangci/golangci-lint/v2
GOLANGCI_LINT_VERSION=v2.15.0
# renovate: datasource=go depName=golang.org/x/vuln
GOVULNCHECK_VERSION=v1.10.0
# renovate: datasource=go depName=github.com/rhysd/actionlint
ACTIONLINT_VERSION=v1.7.12
# renovate: datasource=go depName=github.com/evilmartians/lefthook/v2
LEFTHOOK_VERSION=v2.2.2
# renovate: datasource=go depName=github.com/betterleaks/betterleaks
BETTERLEAKS_VERSION=v1.10.1
# renovate: datasource=npm depName=pnpm
PNPM_VERSION=12.21.0
# Updated manually (see .github/workflows/ci.yml): checksums from https://ziglang.org/download/index.json
ZIG_VERSION=0.17.1
ZIG_SHA256_X86_64=3581863e3cbcfd0fce0efc682ce7e9e93da96668a977791d356a26d2a7e9b17a
ZIG_SHA256_AARCH64=a1546d5646eb184437918d6635ca6b762a0e889eac09cef720ee7a1ed3fe7795

cd "$(dirname "$0")/.."

echo "▶ system packages"
sudo apt-get update -qq
sudo apt-get install -y -qq shellcheck xz-utils >/dev/null

echo "▶ zig $ZIG_VERSION"
arch="$(uname -m)"
case "$arch" in
  x86_64) zig_sha="$ZIG_SHA256_X86_64" ;;
  aarch64) zig_sha="$ZIG_SHA256_AARCH64" ;;
  *) echo "unsupported architecture: $arch" >&2; exit 1 ;;
esac
archive="$(mktemp -d)/zig.tar.xz"
curl -fsSLo "$archive" "https://ziglang.org/download/${ZIG_VERSION}/zig-${arch}-linux-${ZIG_VERSION}.tar.xz"
echo "${zig_sha}  ${archive}" | sha256sum --check --strict
sudo mkdir -p /opt/zig && sudo tar -xJf "$archive" -C /opt/zig --strip-components=1
sudo ln -sf /opt/zig/zig /usr/local/bin/zig
rm -f "$archive"

echo "▶ Rust tools (cargo install --locked)"
(cd agent && rustup toolchain install) # installs the toolchain pinned in agent/rust-toolchain.toml
cargo install --locked --quiet "just@${JUST_VERSION}" "cargo-deny@${CARGO_DENY_VERSION}" \
  "cargo-zigbuild@${CARGO_ZIGBUILD_VERSION}" "zizmor@${ZIZMOR_VERSION}"
just install-proto-plugins

echo "▶ Go tools (go install, pinned versions)"
for tool in \
  "github.com/bufbuild/buf/cmd/buf@${BUF_VERSION}" \
  "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_LINT_VERSION}" \
  "golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}" \
  "github.com/rhysd/actionlint/cmd/actionlint@${ACTIONLINT_VERSION}" \
  "github.com/evilmartians/lefthook/v2@${LEFTHOOK_VERSION}" \
  "github.com/betterleaks/betterleaks@${BETTERLEAKS_VERSION}"; do
  go install "$tool"
done

echo "▶ pnpm $PNPM_VERSION, web dependencies, Playwright (Chromium)"
# npm comes from the node feature (user-owned prefix) – no sudo.
npm install -g --silent "pnpm@${PNPM_VERSION}"
# Keep the pnpm store out of the bind-mounted workspace (it would end up in the repository).
pnpm config set store-dir "$HOME/.local/share/pnpm/store"
(cd web && pnpm install --frozen-lockfile && pnpm exec playwright install --with-deps chromium)

echo "▶ git hooks"
just setup

echo "✓ SerMon devcontainer ready – run: just dev"
