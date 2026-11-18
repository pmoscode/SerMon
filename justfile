# SerMon task runner – `just --list` shows all recipes.
# Recipes for parts that don't exist yet are skipped (lint/test/fmt) or fail with the
# task ID from docs/IMPLEMENTIERUNGSPLAN.md that will implement them.

set shell := ["bash", "-euo", "pipefail", "-c"]

# List all recipes
default:
    @just --list

# Check prerequisites and install git hooks
setup:
    #!/usr/bin/env bash
    set -euo pipefail
    missing=0
    for tool in just lefthook gitleaks shellcheck git; do
        if command -v "$tool" >/dev/null; then echo "✓ $tool"; else echo "✗ $tool fehlt"; missing=1; fi
    done
    # Needed from M0-02..M0-05 on – reported, but not fatal yet.
    for tool in cargo cargo-zigbuild cargo-deny zig go pnpm buf docker; do
        if command -v "$tool" >/dev/null; then echo "✓ $tool"; else echo "… $tool fehlt (ab M0-02..M0-07 nötig)"; fi
    done
    [ "$missing" -eq 0 ] || { echo "Bitte fehlende Tools installieren (z. B. brew install just lefthook gitleaks shellcheck)." >&2; exit 1; }
    lefthook install

# Start the development environment (infra, server with hot reload, web dev server)
dev: (_todo "dev" "M0-07")

# Generate code from proto/ for Rust, Go and TypeScript
gen: (_todo "gen" "M0-05")

# Format all code
fmt: fmt-agent fmt-server fmt-web

# Lint all parts
lint: lint-shell lint-agent lint-server lint-web

# Run all unit tests
test: test-scripts test-agent test-server test-web

# Run integration tests (Testcontainers)
test-integration: (_todo "test-integration" "M0-08")

# Run end-to-end tests (Playwright, agent containers)
e2e: (_todo "e2e" "M0-04")

# Run security scans (secrets now; gosec, govulncheck, cargo-audit/deny, pnpm audit follow)
security: security-secrets security-agent security-server security-web

# --- shell & scripts ---------------------------------------------------------

# Lint shell scripts
lint-shell:
    shellcheck scripts/*.sh

# Test helper scripts
test-scripts:
    sh scripts/check-commit-msg_test.sh

# Scan the git history for leaked secrets
security-secrets:
    gitleaks git --redact --no-banner

# --- agent (Rust) ------------------------------------------------------------

agent_targets := "x86_64-unknown-linux-musl aarch64-unknown-linux-musl"

# Format the agent code
[working-directory: 'agent']
fmt-agent:
    cargo fmt --all

# Check formatting and run clippy on the agent
[working-directory: 'agent']
lint-agent:
    cargo fmt --all -- --check
    cargo clippy --workspace --all-targets --locked -- -D warnings

# Run the agent tests
[working-directory: 'agent']
test-agent:
    cargo test --workspace --locked

# Check agent dependencies: advisories, licenses, bans, sources
[working-directory: 'agent']
security-agent:
    cargo deny check

# Build static release binaries for amd64 and arm64 (cargo-zigbuild, see ADR 0002)
[working-directory: 'agent']
build-agent:
    cargo zigbuild --release --locked {{ prepend("--target ", agent_targets) }}
    @for t in {{ agent_targets }}; do \
        for b in sermon-agent sermon-helper; do \
            f="target/$t/release/$b"; \
            file -b "$f" | grep -q "statically linked" || { echo "✗ $f is not statically linked" >&2; exit 1; }; \
            echo "✓ $f ($(du -h "$f" | cut -f1), static)"; \
        done; \
    done

# Run the release binaries in amd64 (Debian) and arm64 (Alpine) containers
[working-directory: 'agent']
smoke-agent: build-agent
    docker run --rm -q --platform linux/amd64 -v "$PWD/target/x86_64-unknown-linux-musl/release:/b:ro" debian:13-slim /b/sermon-agent --version
    docker run --rm -q --platform linux/arm64 -v "$PWD/target/aarch64-unknown-linux-musl/release:/b:ro" alpine:3.22 /b/sermon-agent --version

# --- server (Go) -------------------------------------------------------------

fmt-server: (_pending "server" "M0-03")
lint-server: (_pending "server" "M0-03")
test-server: (_pending "server" "M0-03")
security-server: (_pending "server" "M0-06")

# --- web (SvelteKit) ---------------------------------------------------------

fmt-web: (_pending "web" "M0-04")
lint-web: (_pending "web" "M0-04")
test-web: (_pending "web" "M0-04")
security-web: (_pending "web" "M0-06")

# --- helpers -----------------------------------------------------------------

# Part not set up yet: skip without failing the aggregate recipe.
[private]
_pending part task:
    @echo "⏭  {{part}}: noch nicht eingerichtet – siehe {{task}} in docs/IMPLEMENTIERUNGSPLAN.md"

# Feature not implemented yet: fail loudly.
[private]
_todo recipe task:
    @echo "✗ 'just {{recipe}}' ist noch nicht umgesetzt – siehe {{task}} in docs/IMPLEMENTIERUNGSPLAN.md" >&2
    @exit 1
