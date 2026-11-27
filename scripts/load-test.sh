#!/bin/sh
# Load test (V1-08): virtual nodes against the production compose (deploy/compose) with the E2E
# overlay and deploy/load/compose.yaml, measured by scripts/load-report.sh. Builds what the demo
# builds (scripts/e2e-demo.sh with PREPARE_ONLY=1) and the agent with the simulator, starts the
# server, PostgreSQL, Loki, Caddy and Mailpit as project sermon-load (its own ports and network, so
# a demo can run next to it), sets up an admin, a group "load" with the module simulate and an
# enrollment token with enough uses, starts the nodes with scenarios and synthetic logs, samples
# for DURATION and writes the report.
#
#   scripts/load-test.sh [DURATION] [NODES]   default 3h and 50; DURATION as 90s, 30m, 3h, 7d
#   KEEP=1 …                 leave the environment running (https://localhost:19443)
#   SKIP_BUILD=1 …           reuse the builds and secrets of the last run (deploy/e2e/.build)
#   INTERVAL=30              seconds between samples
#   LOG_RATE=2               synthetic journal lines per second and node
#   SCENARIOS="cpu-high ssh-bruteforce flapping"   scenarios of the simulator (M2-30)
#   LIMITS="'q.fleet.p95<=200' …"                  more limits for the report (load-report.sh)
#   OUT=DIR                  where samples and report go (default deploy/load/.runs/<time>)
#   BACKFILL_DAYS=14         before the sampling, fill that many days of log counts (V1-24) in front
#                            of the first measured minute, by SQL: Loki takes no lines that old, so
#                            the 14-day histogram reads 14 days of counts (0: none). Each node gets
#                            its per-minute lines and severities of the first BACKFILL_WARMUP seconds
#                            (default 660, of them the last 10 minutes) in all three sources
#   COMPRESS_AGGREGATES=1    compress the chunks of metrics_1m by hand after the sampling and sample
#                            the database once more: its policy reaches a chunk only 6 h after the
#                            chunk ended, a short run would extrapolate it uncompressed (V1-21)
#   SERMON_LOAD_{PORT,MAIL_PORT,METRICS_PORT,LOKI_METRICS_PORT}   ports on 127.0.0.1 (19443, 19025,
#                            19464, 19310)
# Stopping the sampling early (Ctrl-C) still writes the report.
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
e2e="$root/deploy/e2e"
build="$e2e/.build"
load="$build/load"
duration=${1:-3h}
nodes=${2:-50}
interval=${INTERVAL:-30}
log_rate=${LOG_RATE:-2}
scenarios=${SCENARIOS:-cpu-high ssh-bruteforce flapping}
out=${OUT:-$root/deploy/load/.runs/$(date -u +%Y%m%dT%H%MZ)}
case $nodes in '' | *[!0-9]*) echo "load-test: NODES must be a number" >&2 && exit 2 ;; esac
if [ "$nodes" -lt 1 ] || [ "$nodes" -gt 1000 ]; then
	echo "load-test: 1 to 1000 nodes (the uses of one enrollment token)" >&2
	exit 2
fi

case $(docker info --format '{{.Architecture}}') in
x86_64 | amd64) target=x86_64-unknown-linux-musl ;;
aarch64 | arm64) target=aarch64-unknown-linux-musl ;;
*) echo "load-test: unsupported Docker architecture" >&2 && exit 1 ;;
esac

# The demo's builds and secrets, with the demo's own settings (its port goes into Dex's
# configuration).
SKIP_BUILD=${SKIP_BUILD:-} PREPARE_ONLY=1 "$root/scripts/e2e-demo.sh"
mkdir -p "$load" "$out"
chmod 0700 "$load"
# Always (cargo only rebuilds what changed): the simulator is what the run measures.
echo "▸ building the agent with the simulator ($target)"
(cd "$root/agent" && cargo zigbuild --release --locked -q -p sermon-agent --features simulate --target "$target")
cp "$root/agent/target/$target/release/sermon-agent" "$load/sermon-agent"
cat >"$load/agent.toml" <<-'EOF'
	log_level = "info"
	[server]
	url = "https://sermon.test"
	agent_url = "https://server:8443"
	[paths]
	state_dir = "/var/lib/sermon-sim"
EOF
chmod 0644 "$load/agent.toml"

port=${SERMON_LOAD_PORT:-19443}
export SERMON_E2E_PORT="$port"
export SERMON_E2E_MAIL_PORT="${SERMON_LOAD_MAIL_PORT:-19025}"
export SERMON_LOAD_METRICS_PORT="${SERMON_LOAD_METRICS_PORT:-19464}"
export SERMON_LOAD_LOKI_METRICS_PORT="${SERMON_LOAD_LOKI_METRICS_PORT:-19310}"
export SERMON_EDGE_SUBNET=172.30.79.0/24 SERMON_EDGE_IP_RANGE=172.30.79.128/25 SERMON_PROXY_IP=172.30.79.10
export COMPOSE_PROJECT_NAME=sermon-load
export COMPOSE_FILE="$root/deploy/compose/compose.yaml:$root/deploy/compose/source.yaml:$e2e/compose.yaml:$root/deploy/load/compose.yaml"
export COMPOSE_ENV_FILES="$e2e/e2e.env"
SERMON_RELEASE_KEY=$(cat "$build/release.pub")
export SERMON_RELEASE_KEY
compose() { docker compose "$@"; }

# shellcheck disable=SC2317,SC2329 # run by the traps (SC2317 before shellcheck 0.10)
cleanup() {
	status=$?
	compose logs --no-color --tail 2000 server >"$out/server.log" 2>&1 || true
	compose logs --no-color --tail 500 simulator >"$out/simulator.log" 2>&1 || true
	docker logs --tail 500 sermon-load-simulator >>"$out/simulator.log" 2>&1 || true
	if [ "${KEEP:-}" = 1 ]; then
		echo "▸ environment left running: https://localhost:$port (stop: COMPOSE_PROJECT_NAME=sermon-load COMPOSE_FILE=$COMPOSE_FILE docker compose --profile load down -v)"
	else
		docker rm -f sermon-load-simulator >/dev/null 2>&1 || true
		compose --profile load down -v --remove-orphans >/dev/null 2>&1 || true
	fi
	exit "$status"
}
trap cleanup EXIT INT TERM

echo "▸ building the server image from source"
docker rm -f sermon-load-simulator >/dev/null 2>&1 || true
compose --profile load down -v --remove-orphans >/dev/null 2>&1 || true
compose build --quiet server
echo "▸ first installation, then the server, PostgreSQL, Loki, Caddy and Mailpit"
compose run --rm --quiet-pull migrate init --master-keys-file /var/lib/sermon/master_keys >/dev/null
compose up -d --quiet-pull server caddy mailpit loki-metrics
base="https://localhost:$port"
for _ in $(seq 1 90); do
	curl -fsSk "$base/readyz" >/dev/null 2>&1 && break
	sleep 1
done
curl -fsSk "$base/readyz" >/dev/null

# Caddy's CA of this run: curl trusts its root, the nodes pin its intermediate.
ca="$load/caddy-root.crt"
compose exec -T caddy cat /data/caddy/pki/authorities/local/root.crt >"$ca"
fingerprint=sha256:$(compose exec -T caddy cat /data/caddy/pki/authorities/local/intermediate.crt |
	openssl x509 -noout -fingerprint -sha256 | cut -d= -f2)

echo "▸ setting up an admin, the group \"load\" with the module simulate and an enrollment token"
email=load@example.org
openssl rand -hex 16 | tr -d '\n' >"$load/password"
chmod 0600 "$load/password"
jar="$load/cookies"
: >"$jar"
chmod 0600 "$jar"
api() {
	curl -fsS --cacert "$ca" -b "$jar" -c "$jar" -H 'Content-Type: application/json' -H "Origin: $base" \
		--data-binary "$2" "$base/sermon.web.v1.$1"
}
field() { grep -o "\"$1\":\"[^\"]*\"" | head -n 1 | cut -d'"' -f4; }
setup=$(compose logs --no-color server | grep -o '"setup_token":"[^"]*"' | tail -n 1 | cut -d'"' -f4)
password=$(cat "$load/password")
api SetupService/CompleteSetup "{\"token\":\"$setup\",\"email\":\"$email\",\"displayName\":\"Load Test\",\"password\":\"$password\",\"organizationName\":\"Load\"}" >/dev/null
api AuthService/Login "{\"email\":\"$email\",\"password\":\"$password\"}" >/dev/null
group=$(api AccessService/CreateNodeGroup '{"name":"load"}' | field id)
api ModuleService/SetGroupModule "{\"groupId\":\"$group\",\"setting\":{\"moduleId\":\"simulate\",\"enabled\":true,\"configJson\":\"{\\\"behavior\\\":\\\"ok\\\",\\\"value\\\":1}\"}}" >/dev/null
token=$(api EnrollmentTokenService/CreateEnrollmentToken "{\"groupId\":\"$group\",\"ttlSeconds\":3600,\"maxUses\":$nodes}" | field secret)

args=
for scenario in $scenarios; do args="$args --scenario $scenario"; done
echo "▸ starting $nodes virtual nodes (scenarios: $scenarios; $log_rate log lines per second and node)"
# shellcheck disable=SC2086 # the scenario arguments
SERMON_ENROLLMENT_TOKEN=$token compose run -d --quiet-pull --name sermon-load-simulator -e SERMON_ENROLLMENT_TOKEN simulator \
	simulate --config /sim/agent.toml --nodes "$nodes" --log-rate "$log_rate" --ca-fingerprint "$fingerprint" $args >/dev/null
online=0
for _ in $(seq 1 120); do
	online=$(api FleetService/GetFleetSummary '{}' | grep -o '"online":[0-9]*' | head -n 1 | cut -d: -f2)
	[ "${online:-0}" -lt "$nodes" ] || break
	sleep 5
done
[ "${online:-0}" -ge "$nodes" ] || {
	echo "load-test: only ${online:-0} of $nodes nodes online" >&2
	docker logs --tail 50 sermon-load-simulator >&2 || true
	exit 1
}

{
	printf 'setup\t%s virtual nodes, scenarios %s, %s log lines per second and node, a sample every %s s\n' "$nodes" "$scenarios" "$log_rate" "$interval"
	if command -v sysctl >/dev/null && sysctl -n machdep.cpu.brand_string >/dev/null 2>&1; then
		printf 'machine\t%s, %s cores, %s GiB RAM\n' "$(sysctl -n machdep.cpu.brand_string)" "$(sysctl -n hw.ncpu)" "$(($(sysctl -n hw.memsize) / 1073741824))"
	else
		printf 'machine\t%s, %s cores, %s\n' "$(uname -m)" "$(nproc 2>/dev/null || echo '?')" "$(awk '/MemTotal/ { printf "%d GiB RAM", $2 / 1048576 }' /proc/meminfo 2>/dev/null)"
	fi
} >>"$out/info.tsv"
backfill_days=${BACKFILL_DAYS:-14}
case $backfill_days in '' | *[!0-9]*) echo "load-test: BACKFILL_DAYS must be a number" >&2 && exit 2 ;; esac
if [ "$backfill_days" -gt 0 ]; then
	warmup=${BACKFILL_WARMUP:-660}
	echo "▸ waiting ${warmup} s for the log counts of the nodes, then $backfill_days days of them in front (V1-24)"
	sleep "$warmup"
	rows=$(compose exec -T postgres psql -qtAX -v ON_ERROR_STOP=1 -v days="$backfill_days" -U postgres -d sermon <<-'EOF'
		CREATE TEMP TABLE live AS
		  SELECT org_id, node_id, severity, sum(lines)::float8 / 10 AS per_minute FROM log_counts_1m
		  WHERE bucket > date_trunc('minute', now()) - interval '10 minutes' AND bucket <= date_trunc('minute', now())
		  GROUP BY 1, 2, 3;
		SELECT min(bucket) AS first FROM log_counts_1m \gset
		INSERT INTO log_counts_1m (bucket, org_id, node_id, source, severity, lines)
		SELECT m, l.org_id, l.node_id, s, l.severity, greatest(1, round(l.per_minute))::bigint
		FROM live l, generate_series(:'first'::timestamptz - (:'days' || ' days')::interval, :'first'::timestamptz - interval '1 minute', interval '1 minute') m,
		     generate_series(1, 3) s;
		INSERT INTO log_counts_1h AS c (bucket, org_id, node_id, source, severity, lines)
		SELECT time_bucket('1 hour', bucket - interval '1 microsecond', timestamptz '1970-01-01 00:00:00+00') + interval '1 hour',
		       org_id, node_id, source, severity, sum(lines)
		FROM log_counts_1m WHERE bucket < :'first'::timestamptz GROUP BY 1, 2, 3, 4, 5
		ON CONFLICT (node_id, source, severity, bucket) DO UPDATE SET lines = c.lines + EXCLUDED.lines;
		SELECT count(compress_chunk(c, if_not_compressed => true)) FROM show_chunks('log_counts_1m', older_than => interval '1 day') c \g /dev/null
		SELECT count(compress_chunk(c, if_not_compressed => true)) FROM show_chunks('log_counts_1h', older_than => interval '1 day') c \g /dev/null
		SELECT (SELECT count(*) FROM live) || ' node severities, ' || (SELECT count(*) FROM log_counts_1m) || ' rows per minute, ' ||
		       (SELECT count(*) FROM log_counts_1h) || ' per hour';
	EOF
	)
	printf 'backfill\t%s days of log counts in front by SQL, each node with its lines per minute and severity of the 10 minutes after the start in all 3 sources (%s); older chunks compressed as their policy would\n' "$backfill_days" "$rows" >>"$out/info.tsv"
fi
echo "▸ sampling for $duration into $out (Ctrl-C stops early and still reports)"
# Ctrl-C ends the sampling (it exits cleanly), then the report follows.
trap : INT
LOAD_URL=$base LOAD_CURL_ARGS="--cacert $ca" LOAD_EMAIL=$email LOAD_PASSWORD_FILE="$load/password" \
	LOAD_METRICS_URL="http://127.0.0.1:$SERMON_LOAD_METRICS_PORT/metrics" LOAD_CONTAINERS="simulator=sermon-load-simulator" \
	LOAD_LOKI_METRICS_URL="http://127.0.0.1:$SERMON_LOAD_LOKI_METRICS_PORT/metrics" \
	sh "$root/scripts/load-report.sh" sample "$out" "$duration" "$interval" || true
trap cleanup INT TERM
if [ "${COMPRESS_AGGREGATES:-1}" = 1 ]; then
	echo "▸ compressing the chunks of metrics_1m by hand, then one more sample of the database"
	compose exec -T postgres psql -qtAX -U postgres -d sermon \
		-c "SELECT count(compress_chunk(c, recompress => true)) FROM show_chunks('metrics_1m') c" >/dev/null
	printf 'compressed\tmetrics_1m compressed by hand after the sampling (COMPRESS_AGGREGATES=1)\n' >>"$out/info.tsv"
	# shellcheck disable=SC2016 # the tab is made by the shell that runs LOAD_PSQL
	LOAD_DOCKER=0 LOAD_PSQL='docker compose exec -T postgres psql -qtAX -F "$(printf "\t")" -U postgres -d sermon' \
		sh "$root/scripts/load-report.sh" sample "$out" 1s || true
fi
set -f
set -- --limit "fleet.online.p50>=$nodes"
for limit in ${LIMITS:-}; do set -- "$@" --limit "$limit"; done
set +f
status=0
sh "$root/scripts/load-report.sh" report "$out" "$@" >"$out/report.md" || status=$?
cat "$out/report.md"
echo "▸ report: $out/report.md"
exit "$status"
