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
    for tool in just lefthook betterleaks shellcheck actionlint zizmor git; do
        if command -v "$tool" >/dev/null; then echo "✓ $tool"; else echo "✗ $tool fehlt"; missing=1; fi
    done
    # Needed from M0-02..M0-05 on – reported, but not fatal yet.
    for tool in cargo cargo-zigbuild cargo-deny zig go golangci-lint govulncheck node pnpm buf protoc-gen-prost protoc-gen-tonic docker; do
        if command -v "$tool" >/dev/null; then echo "✓ $tool"; else echo "… $tool fehlt (ab M0-02..M0-07 nötig)"; fi
    done
    [ "$missing" -eq 0 ] || { echo "Bitte fehlende Tools installieren (z. B. brew install just lefthook betterleaks shellcheck actionlint zizmor)." >&2; exit 1; }
    lefthook install

# Start the development environment (infra, server with hot reload, web dev server)
dev:
    scripts/dev.sh

# Run virtual nodes against `just dev` (M1-34); the first run needs SERMON_ENROLLMENT_TOKEN with enough uses.
# More options pass through, e.g. `just simulate 5 --seed 2 --scenario cpu-high` (scenarios: M2-30)
simulate nodes="10" *args:
    cd agent && cargo build --locked -q -p sermon-agent --features simulate
    mkdir -p deploy/dev/.simulate
    printf 'log_level = "info"\n[server]\nurl = "http://127.0.0.1:8080"\nagent_url = "https://localhost:8443"\n[paths]\nstate_dir = "%s"\n' \
        "{{ justfile_directory() }}/deploy/dev/.simulate" > deploy/dev/.simulate/agent.toml
    agent/target/debug/sermon-agent simulate --config deploy/dev/.simulate/agent.toml --nodes {{ nodes }} {{ args }}

# Start only the development containers (postgres/timescaledb, loki, mailpit, ntfy)
dev-infra:
    scripts/dev-secrets.sh
    docker compose -f deploy/dev/compose.yaml up -d --wait

# Stop the development containers (data is kept)
dev-down:
    docker compose -f deploy/dev/compose.yaml down

# Delete all development data (containers, volumes, generated secrets)
dev-reset:
    docker compose -f deploy/dev/compose.yaml down --volumes
    rm -rf deploy/dev/.secrets
    @echo "✓ development data deleted – next \`just dev\` starts from scratch"

# Show logs of the development containers
dev-logs:
    docker compose -f deploy/dev/compose.yaml logs -f

# Generate all code: protocol (buf), typed SQL (sqlc) and the copies the server embeds
gen: gen-proto gen-sql gen-packaging gen-rules gen-modules

# Generate code from proto/ for Rust, Go and TypeScript (local plugins only)
[working-directory: 'proto']
gen-proto:
    buf generate
    cd ../agent && cargo fmt --all
    @echo "✓ generated: server/internal/gen, web/src/lib/api/gen, agent/crates/sermon-{transport,testserver,helper-proto}/src/gen"

# Generate typed Go code from SQL queries and migrations (sqlc)
[working-directory: 'server']
gen-sql:
    go tool sqlc generate
    @echo "✓ generated: server/internal/adapters/postgres/sqlcdb"

# Copy install.sh and the init files into the server, which serves them (ADR 0012)
gen-packaging:
    cp packaging/install.sh server/internal/transport/download/assets/install.sh
    cp packaging/systemd/sermon-agent.service server/internal/transport/download/assets/sermon-agent.service
    cp packaging/openrc/sermon-agent server/internal/transport/download/assets/sermon-agent.openrc
    cp packaging/systemd/sermon-helper.service server/internal/transport/download/assets/sermon-helper.service
    cp packaging/systemd/sermon-helper.socket server/internal/transport/download/assets/sermon-helper.socket
    cp packaging/openrc/sermon-helper server/internal/transport/download/assets/sermon-helper.openrc
    cp packaging/helper.toml server/internal/transport/download/assets/helper.toml
    @echo "✓ copied: server/internal/transport/download/assets"

# Copy the default alert rules into the server, which embeds them (M2-04)
gen-rules:
    rm -rf server/internal/app/alerting/ruleset/defaults
    cp -R rules server/internal/app/alerting/ruleset/defaults
    @echo "✓ copied: server/internal/app/alerting/ruleset/defaults"

# Copy the modules' schemas and metric catalogs into the server, which embeds them (ADR 0039)
gen-modules:
    rm -rf server/internal/domain/module/schemas server/internal/domain/metric/modules
    cp -R agent/crates/sermon-modules/schemas server/internal/domain/module/schemas
    cp -R agent/crates/sermon-modules/metrics server/internal/domain/metric/modules
    @echo "✓ copied: server/internal/domain/module/schemas, server/internal/domain/metric/modules"

# Fail if the committed generated code is not up to date with proto/ (used in CI)
check-gen: gen
    git diff --exit-code -- server/internal/gen web/src/lib/api/gen agent/crates/sermon-transport/src/gen agent/crates/sermon-testserver/src/gen agent/crates/sermon-helper-proto/src/gen server/internal/adapters/postgres/sqlcdb server/internal/transport/download/assets server/internal/app/alerting/ruleset/defaults server/internal/domain/module/schemas server/internal/domain/metric/modules
    @test -z "$(git ls-files --others --exclude-standard -- server/internal/gen web/src/lib/api/gen agent/crates/sermon-transport/src/gen agent/crates/sermon-testserver/src/gen agent/crates/sermon-helper-proto/src/gen server/internal/adapters/postgres/sqlcdb server/internal/transport/download/assets server/internal/app/alerting/ruleset/defaults server/internal/domain/module/schemas server/internal/domain/metric/modules)" || { echo "✗ untracked generated files – run just gen and commit" >&2; exit 1; }

# Install the pinned Rust protoc plugins used by `just gen`
install-proto-plugins:
    cargo install --locked protoc-gen-prost@0.5.0 protoc-gen-tonic@0.5.0

# Format all code
fmt: fmt-agent fmt-server fmt-web

# Lint all parts
lint: lint-shell lint-ci lint-compose lint-proto lint-agent lint-server lint-web

# Run all unit tests
test: test-scripts test-agent test-server test-web

# The database modules against real servers in containers: PostgreSQL 13 to 17 with a standby (M5-13), MySQL and MariaDB (M5-14); FIXTURES=1 records their answers
test-db-modules:
    scripts/test-db-modules.sh

# Run integration tests against real containers (Testcontainers; Docker required)
[working-directory: 'server']
test-integration:
    go test -tags integration -race -count=1 ./...

# Run end-to-end tests against sermon-server with the embedded UI (Playwright)
[working-directory: 'web']
e2e:
    pnpm run test:e2e

# Fast demo end to end for every change (M1-33): real stack, install.sh in containers, Playwright (KEEP=1 keeps it running)
e2e-demo:
    scripts/e2e-demo.sh

# Whole demo with the long scenarios of M4-28 (anomalies, rollout, budget during a listing): nightly, before a release
e2e-demo-full:
    FULL=1 scripts/e2e-demo.sh

# Run the Rust agent binary against the real server (Testcontainers; Docker and Rust required)
e2e-agent:
    cd agent && cargo build --locked -p sermon-agent --features simulate
    cd server && SERMON_AGENT_BIN="{{ justfile_directory() }}/agent/target/debug/sermon-agent" go test -tags integration -count=1 -run 'TestAgentBinary_' ./cmd/sermon-server/

# Run security scans (secrets now; gosec, govulncheck, cargo-audit/deny, pnpm audit follow)
security: security-secrets security-agent security-server security-web

# --- shell & scripts ---------------------------------------------------------

# Lint shell scripts
lint-shell:
    shellcheck scripts/*.sh deploy/compose/*.sh deploy/compose/initdb/*.sh deploy/e2e/repos/*.sh .devcontainer/*.sh packaging/install.sh packaging/nfpm/*.sh agent/fixtures/capture-host.sh agent/fixtures/updates/capture.sh agent/fixtures/discovery/capture.sh agent/fixtures/modules/docker/capture.sh agent/fixtures/modules/tls/generate.sh agent/fixtures/modules/systemd_units/capture.sh agent/fixtures/modules/nginx/capture.sh agent/fixtures/modules/apache/capture.sh agent/fixtures/modules/redis/capture.sh agent/fixtures/logs/capture/capture.sh packaging/openrc/sermon-agent agent/fuzz/seed.sh agent/crates/sermon-logs/tests/fake-journalctl

# Lint GitHub Actions workflows: syntax (actionlint) and security (zizmor, strictest persona)
lint-ci:
    actionlint
    zizmor --offline --persona=pedantic .github/workflows

# Check compose files: images pinned by digest, no telemetry, dev and E2E ports on 127.0.0.1 only,
# production containers hardened (M2-26); the E2E overlay is checked merged with the production file
lint-compose:
    sh scripts/check-compose.sh --loopback-only deploy/dev/compose.yaml
    COMPOSE_ENV_FILES=deploy/compose/env.example sh scripts/check-compose.sh --hardened deploy/compose/compose.yaml
    SERMON_RELEASE_KEY= COMPOSE_ENV_FILES=deploy/e2e/e2e.env sh scripts/check-compose.sh --loopback-only deploy/compose/compose.yaml deploy/e2e/compose.yaml

# Test helper scripts
test-scripts:
    sh scripts/check-commit-msg_test.sh
    sh scripts/check-compose_test.sh
    sh scripts/helper-loc_test.sh
    sh scripts/agent-budget_test.sh
    sh scripts/check-agent-allocator_test.sh
    sh scripts/repo-keys_test.sh

# Build the package sources of the E2E environment and check them on five distros (M4-08; Docker required)
test-e2e-repos:
    sh deploy/e2e/repos/test.sh

# Precision and recall of the anomaly detection on synthetic series against the reference, and its
# computing budget (M4-22, ADR 0037); `go test ./...` fails as well when a value drops
bench-anomaly:
    cd server && go test -count=1 -v -run 'TestBenchmark_' ./internal/domain/anomaly/ | grep -v '^=== '
    cd server && go test -run '^$' -bench . -benchmem ./internal/domain/anomaly/

# Run the root helper's updates against real apt, dnf and apk in containers (M4-03; Docker required)
test-helper:
    #!/usr/bin/env bash
    set -euo pipefail
    case $(docker info --format '{{{{.Architecture}}') in
    x86_64 | amd64) target=x86_64-unknown-linux-musl ;;
    *) target=aarch64-unknown-linux-musl ;;
    esac
    (cd agent && cargo zigbuild --release --locked -q -p sermon-helper --target "$target")
    sh scripts/helper-smoke.sh "agent/target/$target/release/sermon-helper"

# Scan the git history for leaked secrets (betterleaks, ADR 0005; no live validation against provider APIs)
security-secrets:
    betterleaks git --redact --no-banner .

# --- proto (buf) -------------------------------------------------------------

# Lint and format-check the protocol definitions
[working-directory: 'proto']
lint-proto:
    buf lint
    buf format --diff --exit-code

# Detect breaking protocol changes against main (KONZEPT §5: agents N and N-1 must keep working)
[working-directory: 'proto']
breaking-proto against="main":
    #!/usr/bin/env bash
    set -euo pipefail
    if ! git cat-file -e "{{ against }}:proto/buf.yaml" 2>/dev/null; then
        echo "⏭  no proto/ on {{ against }} yet – nothing to compare"; exit 0
    fi
    buf breaking --against "../.git#ref={{ against }},subdir=proto"
    echo "✓ no breaking changes against {{ against }}"

# --- agent (Rust) ------------------------------------------------------------

agent_targets := "x86_64-unknown-linux-musl aarch64-unknown-linux-musl"

# Format the agent code
[working-directory: 'agent']
fmt-agent:
    cargo fmt --all

# Check formatting, the root helper's size (ADR 0035) and clippy on the agent, its fuzz targets included
[working-directory: 'agent']
lint-agent:
    cargo fmt --all -- --check
    sh ../scripts/helper-loc.sh
    cargo clippy --workspace --all-targets --locked -- -D warnings
    cargo clippy --workspace --all-targets --all-features --locked -- -D warnings

# Run the agent tests
[working-directory: 'agent']
test-agent:
    cargo test --workspace --locked
    cargo test -p sermon-agent -p sermon-modules --features sermon-agent/simulate --locked

# Check agent dependencies: advisories, licenses, bans, sources
[working-directory: 'agent']
security-agent:
    cargo deny check

# The fuzz builds don't take the agent's release profile: LTO drops libFuzzer's coverage symbols
# (__sancov_gen_) on Linux; debug info names the lines of a crash.
fuzz_profile := "CARGO_PROFILE_RELEASE_LTO=false CARGO_PROFILE_RELEASE_CODEGEN_UNITS=16 CARGO_PROFILE_RELEASE_STRIP=none CARGO_PROFILE_RELEASE_DEBUG=1"

# Fuzz the parsers of untrusted input: /proc texts, journal lines, security events, redaction, WAL recovery, multi-line log entries, process files, what the root helper reads and what the modules read (cargo-fuzz, pinned nightly)
[working-directory: 'agent/fuzz']
fuzz-agent seconds="60":
    ./seed.sh
    {{ fuzz_profile }} cargo fuzz run proc_parsers -- -max_total_time={{ seconds }} -max_len=65536
    {{ fuzz_profile }} cargo fuzz run journal_line -- -max_total_time={{ seconds }} -max_len=65536
    {{ fuzz_profile }} cargo fuzz run security_parser -- -max_total_time={{ seconds }} -max_len=65536
    {{ fuzz_profile }} cargo fuzz run redaction -- -max_total_time={{ seconds }} -max_len=65536
    {{ fuzz_profile }} cargo fuzz run wal_recovery -- -max_total_time={{ seconds }} -max_len=65536
    {{ fuzz_profile }} cargo fuzz run helper_request -- -max_total_time={{ seconds }} -max_len=65536
    {{ fuzz_profile }} cargo fuzz run update_parsers -- -max_total_time={{ seconds }} -max_len=65536
    {{ fuzz_profile }} cargo fuzz run multiline -- -max_total_time={{ seconds }} -max_len=65536
    {{ fuzz_profile }} cargo fuzz run process_parsers -- -max_total_time={{ seconds }} -max_len=65536
    {{ fuzz_profile }} cargo fuzz run helper_parsers -- -max_total_time={{ seconds }} -max_len=65536
    {{ fuzz_profile }} cargo fuzz run module_parsers -- -max_total_time={{ seconds }} -max_len=65536
    {{ fuzz_profile }} cargo fuzz run smart_parser -- -max_total_time={{ seconds }} -max_len=131072
    {{ fuzz_profile }} cargo fuzz run cgroup_parsers -- -max_total_time={{ seconds }} -max_len=65536
    {{ fuzz_profile }} cargo fuzz run postgres_wire -- -max_total_time={{ seconds }} -max_len=65536

# Fuzz the server's LogQL builder (no input may break the structure of a query, M3-08) and its copy of the redaction (M3-12) (Go fuzzing)
[working-directory: 'server']
fuzz-server seconds="60":
    go test ./internal/domain/logql/ -run '^$' -fuzz FuzzLines -fuzztime {{ seconds }}s
    go test ./internal/domain/redact/ -run '^$' -fuzz FuzzRedact -fuzztime {{ seconds }}s

# Test the agent packages (.deb, .rpm, .apk) in containers: signature, install, upgrade, removal (M2-21)
test-packages:
    #!/usr/bin/env sh
    set -eu
    case $(docker info --format '{{{{.Architecture}}') in
    x86_64 | amd64) target=x86_64-unknown-linux-musl ;;
    *) target=aarch64-unknown-linux-musl ;;
    esac
    (cd agent && cargo zigbuild --release --locked -q -p sermon-agent -p sermon-helper --target "$target")
    scripts/check-agent-allocator.sh "agent/target/$target/release/sermon-agent"
    scripts/test-packages.sh "agent/target/$target/release/sermon-agent" "agent/target/$target/release/sermon-helper"

# Signed packages of agent and helper of VERSION (amd64, arm64) into dist/packages/; keys: see scripts/package-agent.sh (M2-21, M4-04)
package-agent version: build-agent
    scripts/package-agent.sh amd64 "{{ version }}" agent/target/x86_64-unknown-linux-musl/release/sermon-agent agent/target/x86_64-unknown-linux-musl/release/sermon-helper dist/packages
    scripts/package-agent.sh arm64 "{{ version }}" agent/target/aarch64-unknown-linux-musl/release/sermon-agent agent/target/aarch64-unknown-linux-musl/release/sermon-helper dist/packages

# Test install.sh and the systemd units in Debian: bats (signature, detection, helper) and `systemd-analyze security` (agent < 2.0, helper ≤ 6.0, ADR 0035)
test-packaging:
    docker run --rm -q -v "{{ justfile_directory() }}:/src:ro" -w /src debian:13 sh -c '\
        apt-get update -qq >/dev/null && apt-get install -y -qq --no-install-recommends bats openssl minisign systemd >/dev/null 2>&1 && \
        bats packaging/test && \
        { systemd-analyze security --offline=true --threshold=20 packaging/systemd/sermon-agent.service >/tmp/score; status=$?; tail -1 /tmp/score; [ $status -eq 0 ] || exit $status; } && \
        { systemd-analyze security --offline=true --threshold=60 packaging/systemd/sermon-helper.service >/tmp/score; status=$?; tail -1 /tmp/score; exit $status; }'

# Build and sign the release binaries of agent and helper of VERSION (= server version) with KEY into dist/agent/ (SERMON_AGENT_DOWNLOADS_DIR); the helper takes KEY's public key, both the version (M4-31)
sign-agent key version:
    #!/usr/bin/env sh
    set -eu
    pub=$(scripts/release-sign.sh pubkey "{{ key }}")
    SERMON_RELEASE_KEY="$pub" SERMON_VERSION="{{ version }}" just build-agent
    for t in {{ agent_targets }}; do \
        for b in sermon-agent sermon-helper; do \
            LC_ALL=C grep -aqF "{{ version }}" "agent/target/$t/release/$b" || { echo "✗ $t $b doesn't carry the version {{ version }}" >&2; exit 1; }; \
        done; \
        LC_ALL=C grep -aqF "$pub" "agent/target/$t/release/sermon-helper" || { echo "✗ the $t helper doesn't carry the release key" >&2; exit 1; }; \
    done
    mkdir -p dist/agent
    cp agent/target/x86_64-unknown-linux-musl/release/sermon-agent dist/agent/sermon-agent-linux-amd64
    cp agent/target/aarch64-unknown-linux-musl/release/sermon-agent dist/agent/sermon-agent-linux-arm64
    cp agent/target/x86_64-unknown-linux-musl/release/sermon-helper dist/agent/sermon-helper-linux-amd64
    cp agent/target/aarch64-unknown-linux-musl/release/sermon-helper dist/agent/sermon-helper-linux-arm64
    scripts/release-sign.sh sign "{{ key }}" --version "{{ version }}" dist/agent/sermon-agent-linux-amd64 dist/agent/sermon-agent-linux-arm64 dist/agent/sermon-helper-linux-amd64 dist/agent/sermon-helper-linux-arm64
    echo "✓ dist/agent: binaries and minisign signatures"

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
        sh ../scripts/check-agent-allocator.sh "target/$t/release/sermon-agent"; \
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
    go mod tidy -diff

# Run the server tests with race detector and coverage
[working-directory: 'server']
test-server:
    go test -race -count=1 -coverprofile=coverage.out ./...
    @go tool cover -func=coverage.out | tail -1

# Check the server for known vulnerabilities in dependencies and stdlib
[working-directory: 'server']
security-server:
    govulncheck ./...

# Release public key compiled into the server for install.sh (ADR 0012): SERMON_RELEASE_KEY or packaging/release.pub
release_key := env("SERMON_RELEASE_KEY", `cat packaging/release.pub 2>/dev/null || true`)

# Build a static server binary with the embedded web UI into server/bin/
[working-directory: 'server']
build-server: build-web
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version={{ server_version }} -X main.releaseKey={{ release_key }}" -o bin/sermon-server ./cmd/sermon-server
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

# Format the web code (prettier)
[working-directory: 'web']
fmt-web:
    pnpm run format

# Lint the web UI: prettier, eslint, svelte-check
[working-directory: 'web']
lint-web:
    pnpm run lint
    pnpm run check

# Run the web unit and component tests (Vitest, Chromium)
[working-directory: 'web']
test-web:
    pnpm test

# Check all web dependencies (incl. dev tooling – it runs on dev machines and in CI) for known vulnerabilities
[working-directory: 'web']
security-web:
    pnpm audit

# Build the web UI, check the bundle budget and copy it into the server for embedding
[working-directory: 'web']
build-web:
    pnpm install --frozen-lockfile --silent
    pnpm run build
    pnpm run check:size
    find ../server/web/dist -mindepth 1 ! -name .gitkeep -delete
    cp -R build/. ../server/web/dist/
    @echo "✓ web UI copied to server/web/dist"

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
