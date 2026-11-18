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
    for tool in cargo cargo-zigbuild cargo-deny zig go golangci-lint govulncheck pnpm buf docker; do
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

server_version := `git describe --tags --always --dirty 2>/dev/null || echo dev`

# Format the server code (gofumpt, goimports)
[working-directory: 'server']
fmt-server:
    golangci-lint fmt ./...

# Check formatting and lint the server
[working-directory: 'server']
lint-server:
    golangci-lint fmt --diff ./...
    golangci-lint run ./...

# Run the server tests with race detector and coverage
[working-directory: 'server']
test-server:
    go test -race -count=1 -coverprofile=coverage.out ./...
    @go tool cover -func=coverage.out | tail -1

# Check the server for known vulnerabilities in dependencies and stdlib
[working-directory: 'server']
security-server:
    govulncheck ./...

# Build a static server binary into server/bin/
[working-directory: 'server']
build-server:
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version={{ server_version }}" -o bin/sermon-server ./cmd/sermon-server
    @echo "✓ server/bin/sermon-server ({{ server_version }})"

# Start the server binary, probe /healthz and /readyz, stop it with SIGTERM
[working-directory: 'server']
smoke-server: build-server
    #!/usr/bin/env bash
    set -euo pipefail
    addr=127.0.0.1:18080
    SERMON_HTTP_ADDR=$addr SERMON_LOG_FORMAT=text bin/sermon-server & pid=$!
    trap 'kill $pid 2>/dev/null || true' EXIT
    for _ in $(seq 1 50); do curl -fsS "http://$addr/healthz" >/dev/null 2>&1 && break; sleep 0.1; done
    echo "healthz: $(curl -fsS "http://$addr/healthz")"
    echo "readyz:  $(curl -fsS "http://$addr/readyz")"
    kill -TERM $pid
    wait $pid && echo "✓ stopped cleanly on SIGTERM"

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
