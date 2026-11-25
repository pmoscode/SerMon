#!/bin/sh
# End-to-end demo test (M1-33): builds the agent, signs it with a key of this run, starts the
# production compose (deploy/compose, M2-26) with the overlay deploy/e2e/compose.yaml – the server
# image is built from source like on a real installation – and runs web/tests/demo with Playwright:
# setup → login → enrollment token → install.sh in agent containers with systemd and OpenRC → nodes
# with metrics in < 2 min → signed packages on three more hosts (M2-21) → a user of another
# organization sees nothing; meanwhile two hosts measure the agent against its resource budget
# (scripts/agent-budget.sh, M2-25). Then (M2-27) a host attacks the sshd of another: the alert
# reaches ntfy and a Slack mock with time, user, address and port, repeats until it is
# acknowledged in the browser and then stays quiet. The agent hosts take their package updates only
# from sources of this run (deploy/e2e/repos, M4-08).
#
#   scripts/e2e-demo.sh          run and tear down
#   KEEP=1 scripts/e2e-demo.sh   leave the environment running (https://localhost:18443)
#   SKIP_BUILD=1 …               reuse the agent, packages and secrets of the last run (deploy/e2e/.build)
#   SERMON_E2E_{PORT,MAIL_PORT,NTFY_PORT,SLACK_PORT}  ports on 127.0.0.1 (18443, 18025, 18090, 18026)
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
e2e="$root/deploy/e2e"
build="$e2e/.build"
port=${SERMON_E2E_PORT:-18443}
export SERMON_E2E_PORT="$port"
# The production file plus the overlay; e2e.env instead of a local deploy/compose/.env.
export COMPOSE_FILE="$root/deploy/compose/compose.yaml:$e2e/compose.yaml"
export COMPOSE_ENV_FILES="$e2e/e2e.env"
compose() { docker compose "$@"; }

case $(docker info --format '{{.Architecture}}') in
x86_64 | amd64) arch=amd64 target=x86_64-unknown-linux-musl ;;
aarch64 | arm64) arch=arm64 target=aarch64-unknown-linux-musl ;;
*) echo "e2e-demo: unsupported Docker architecture" >&2 && exit 1 ;;
esac

if [ "${SKIP_BUILD:-}" != 1 ] || [ ! -f "$build/release.pub" ] || [ ! -f "$build/dex/config.yaml" ] || [ ! -f "$build/repos/keys/gpg.pub.asc" ] || [ ! -f "$build/agent-repos/current/keys/sermon.asc" ]; then
	rm -rf "$build"
	mkdir -p "$build/secrets" "$build/downloads"
	# Release key and database passwords: only for the user who runs the demo. The containers see the
	# files through bind mounts, which don't need the parent directories to be accessible.
	chmod 0700 "$build" "$build/secrets"

	# Release key of this run only: the server embeds its public half, install.sh checks against it.
	"$root/scripts/release-sign.sh" generate "$build/release.pem" >"$build/release.pub"

	# The server's version (e2e.env): install.sh and the self-update compare against it.
	version=$(sed -n 's/^SERMON_VERSION=//p' "$e2e/e2e.env")
	echo "▸ building the agent ($target)"
	# The helper takes self-updates signed with this key only, the agent reports the server's
	# version like a release (ADR 0036).
	(cd "$root/agent" && SERMON_RELEASE_KEY=$(cat "$build/release.pub") SERMON_VERSION=$version cargo zigbuild --release --locked -q -p sermon-agent -p sermon-helper --target "$target")
	cp "$root/agent/target/$target/release/sermon-agent" "$build/downloads/sermon-agent-linux-$arch"
	cp "$root/agent/target/$target/release/sermon-helper" "$build/downloads/sermon-helper-linux-$arch"
	"$root/scripts/release-sign.sh" sign "$build/release.pem" --version "$version" \
		"$build/downloads/sermon-agent-linux-$arch" "$build/downloads/sermon-helper-linux-$arch"

	echo "▸ packaging the agent (.deb, .rpm, .apk)"
	"$root/scripts/package-test-keys.sh" "$build/package-keys"
	SERMON_PKG_GPG_KEY="$build/package-keys/gpg.asc" SERMON_PKG_APK_KEY="$build/package-keys/apk.rsa" \
		"$root/scripts/package-agent.sh" "$arch" 0.0.1 "$build/downloads/sermon-agent-linux-$arch" \
		"$build/downloads/sermon-helper-linux-$arch" "$build/packages" >/dev/null
	# The package hosts get the packages and the public keys only.
	cp "$build/package-keys/gpg.pub.asc" "$build/package-keys/sermon-agent.rsa.pub" "$build/packages/"
	chmod 0644 "$build/packages/"*
	# Package sources with updates for the agent hosts (M4-08), signed with the same keys.
	echo "▸ building the package sources with updates"
	sh "$e2e/repos/build.sh" "$build/package-keys" "$build/repos" >/dev/null
	# The agent's repositories (M4-20): this version; with the next one (0.0.2, the same binaries)
	# for an update by the package manager; and one whose indexes another key signed.
	echo "▸ building the agent's package repositories"
	SERMON_PKG_GPG_KEY="$build/package-keys/gpg.asc" SERMON_PKG_APK_KEY="$build/package-keys/apk.rsa" \
		"$root/scripts/package-agent.sh" "$arch" 0.0.2 "$build/downloads/sermon-agent-linux-$arch" \
		"$build/downloads/sermon-helper-linux-$arch" "$build/packages-next" >/dev/null
	cp "$build/packages/"*.deb "$build/packages/"*.rpm "$build/packages/"*.apk "$build/packages-next/"
	"$root/scripts/package-test-keys.sh" "$build/other-keys"
	mkdir -p "$build/forged-keys"
	cp "$build/other-keys/gpg.asc" "$build/other-keys/apk.rsa" "$build/forged-keys/"
	cp "$build/package-keys/gpg.pub.asc" "$build/package-keys/sermon-agent.rsa.pub" "$build/forged-keys/"
	"$root/scripts/build-repos.sh" "$build/packages" "$build/package-keys" "$build/agent-repos/current" >/dev/null
	"$root/scripts/build-repos.sh" "$build/packages-next" "$build/package-keys" "$build/agent-repos/next" >/dev/null
	"$root/scripts/build-repos.sh" "$build/packages" "$build/forged-keys" "$build/agent-repos/forged" >/dev/null

	# Database passwords and URLs as on a real installation; Mailpit accepts any SMTP login.
	openssl rand -hex 16 | tr -d '\n' >"$build/secrets/smtp_password"
	# Dex's client secret (M3-22), for the server and Dex's configuration.
	openssl rand -hex 24 | tr -d '\n' >"$build/secrets/oidc_client_secret"
	"$root/deploy/compose/secrets.sh" "$build/secrets" >/dev/null
	# Mailpit's certificate of this run; the server trusts exactly it (SERMON_SMTP_CA_FILE).
	mkdir -p "$build/mailpit"
	openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 2 -subj /CN=mailpit \
		-addext subjectAltName=DNS:mailpit -keyout "$build/mailpit/mailpit.key" -out "$build/mailpit/mailpit.crt" 2>/dev/null
	chmod 0644 "$build/mailpit/"* # read by Mailpit and the server; the directory is private
	# The Slack mock's certificate (M2-27): the server trusts it as its only root (SSL_CERT_FILE).
	mkdir -p "$build/slack-mock"
	openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 2 -subj /CN=hooks.slack.com \
		-addext subjectAltName=DNS:hooks.slack.com -keyout "$build/slack-mock/slack-mock.key" -out "$build/slack-mock/slack-mock.crt" 2>/dev/null
	chmod 0644 "$build/slack-mock/"*
	# Dex (M3-22): a certificate for dex.test and its configuration with the client of this run.
	mkdir -p "$build/dex"
	openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 2 -subj /CN=dex.test \
		-addext subjectAltName=DNS:dex.test -keyout "$build/dex/dex.key" -out "$build/dex/dex.crt" 2>/dev/null
	cat >"$build/dex/config.yaml" <<-EOF
		issuer: https://dex.test:18556/dex
		storage: { type: memory }
		web: { https: 0.0.0.0:18556, tlsCert: /etc/dex/dex.crt, tlsKey: /etc/dex/dex.key }
		oauth2: { skipApprovalScreen: true }
		staticClients:
		  - id: sermon
		    name: SerMon
		    secret: "$(cat "$build/secrets/oidc_client_secret")"
		    redirectURIs: ["https://localhost:$port/auth/oidc/callback"]
		connectors:
		  - { type: mockCallback, id: mock, name: Example }
	EOF
	chmod 0644 "$build/dex/"*
	# The server's only roots for outgoing HTTPS: the Slack mock and Dex.
	cat "$build/slack-mock/slack-mock.crt" "$build/dex/dex.crt" >"$build/outbound-ca.pem"
	chmod 0644 "$build/outbound-ca.pem"
fi

cleanup() {
	status=$?
	if [ "$status" -ne 0 ]; then compose logs --no-color server >"$build/server.log" 2>&1 || true; fi
	if [ "${KEEP:-}" = 1 ]; then
		echo "▸ environment left running: https://localhost:$port (stop: COMPOSE_FILE=$COMPOSE_FILE docker compose down -v)"
	else
		compose down -v --remove-orphans >/dev/null 2>&1 || true
	fi
	exit "$status"
}
trap cleanup EXIT INT TERM

# The release key of this run goes into the server image (build arg, ADR 0012).
SERMON_RELEASE_KEY=$(cat "$build/release.pub")
export SERMON_RELEASE_KEY

echo "▸ building the server image from source (deploy/compose/server.Dockerfile)"
compose down -v --remove-orphans >/dev/null 2>&1 || true
compose build --quiet
# The server offers the agent's current repository; the demo switches it (web/tests/demo/stack.ts).
rm -rf "$build/agent-repo"
cp -R "$build/agent-repos/current" "$build/agent-repo"

echo "▸ first installation: sermon-server init, then docker compose up"
compose run --rm --quiet-pull migrate init --master-keys-file /var/lib/sermon/master_keys >/dev/null
compose up -d --quiet-pull
for _ in $(seq 1 90); do
	curl -fsSk "https://localhost:$port/readyz" >/dev/null 2>&1 && break
	sleep 1
done
curl -fsSk "https://localhost:$port/readyz" >/dev/null

# The agent hosts take their updates from the package sources of this run only (M4-08), the
# package hosts, too (their agent repository comes from the server, M4-20).
echo "▸ switching the agent hosts to the package sources of this run"
pids=""
for service in $(compose config --services | grep -E '^(agent|pkg)-'); do
	compose exec -T "$service" sh /e2e/use.sh http://packages.test:8080 /e2e/keys >"$build/use-$service.log" 2>&1 &
	pids="$pids $!"
done
for pid in $pids; do
	wait "$pid" || {
		tail -n 20 "$build"/use-*.log >&2
		exit 1
	}
done

echo "▸ running the demo test"
cd "$root/web"
E2E_BASE_URL="https://localhost:$port" E2E_DIR="$e2e" \
	pnpm exec playwright test --config playwright.demo.config.ts
