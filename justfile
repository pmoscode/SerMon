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
    for tool in cargo go pnpm buf docker; do
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

fmt-agent: (_pending "agent" "M0-02")
lint-agent: (_pending "agent" "M0-02")
test-agent: (_pending "agent" "M0-02")
security-agent: (_pending "agent" "M0-06")

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
