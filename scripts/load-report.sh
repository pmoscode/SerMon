#!/bin/sh
# Load test evaluation (V1-08; the test operation of V1-18 uses it the same way): samples a running
# SerMon periodically and writes a Markdown report with limits.
#
#   load-report.sh sample DIR DURATION [INTERVAL]   sample every INTERVAL seconds (default 30) for
#                                                   DURATION (90s, 30m, 3h, 7d) into DIR/samples.tsv
#   load-report.sh report DIR [--limit 'KEY<=V'|'KEY>=V']... [--horizon DAYS] [--event-days DAYS]
#                                                   the report on stdout; exit 1 if a limit fails
#   load-report.sh limits                           the default limits
#
# What a sample takes, each part only where it is reachable (otherwise the report says n/a):
#   - docker stats of the compose services server, postgres, loki and caddy (CPU in % of one core,
#     memory in MiB) and of LOAD_CONTAINERS ("name=container …", e.g. the simulator), the size of
#     the volumes of PostgreSQL (with the WAL) and Loki; `docker compose` with the COMPOSE_* of the
#     environment, LOAD_DOCKER=0 leaves it out;
#   - the server's /metrics (ADR 0011) from LOAD_METRICS_URL: ingest rate, lag, dropped entries;
#   - Loki's /metrics from LOAD_LOKI_METRICS_URL (the load test publishes it on 127.0.0.1): its Go
#     heap, GC and resident memory next to the memory of the container (V1-23);
#   - SQL through LOAD_PSQL (a command that reads SQL on stdin; default: psql as the superuser in
#     the postgres service): database size, metric tables, WAL, checkpoints, failing jobs;
#   - the main reads of the UI through the web API at LOAD_URL with a real session: the fleet,
#     its sparklines, a node, charts over 1 h, 24 h and 30 days, logs and their histogram (1 h, 1 h
#     with a text, 2 days and the longest range of the UI, 14 days; V1-24). The
#     session comes from LOAD_EMAIL and LOAD_PASSWORD_FILE (a user without a second factor; it
#     logs in again when the session ends) or LOAD_COOKIE_FILE (the value of the session cookie
#     from a browser). LOAD_CURL_ARGS adds options such as --cacert FILE.
# Only POSIX sh, awk, curl and (optionally) docker; nothing is installed on the server.
set -eu
# Numbers with a decimal point whatever the host speaks: awk writes 8,0 under de_DE and printf
# can't read it back (seen in the test operation, V1-18).
export LC_ALL=C

tab=$(printf '\t')
self=$0

usage() {
	sed -n '2,10p' "$self" >&2
	exit 2
}

# --- limits ------------------------------------------------------------------------------------

# The defaults: the guideline of KONZEPT §22 (4 vCPU, 8 GB RAM, 200 GB SSD) with room for the host,
# the criteria of the spike (ADR 0001) and nothing dropped. KEY is a measurement and a statistic.
default_limits() {
	cat <<-'EOF'
		cpu.total.p95<=200
		cpu.postgres.avg<=100
		mem.total.max<=6144
		m.sermon_server_ingest_lag_seconds.p95<=2
		m.sermon_server_ingest_lag_seconds.max<=10
		ingest.dropped.delta<=0
		m.sermon_server_ingest_write_errors_total.delta<=0
		q.errors.sum<=0
		q.fleet.p95<=300
		q.sparklines.p95<=500
		q.node.p95<=300
		q.chart_1h.p95<=200
		q.chart_24h.p95<=300
		q.chart_30d.p95<=300
		q.logs.p95<=1000
		q.log_histogram.p95<=1000
		q.log_histogram_2d.p95<=1000
		q.log_histogram_14d.p95<=1000
		db.jobs_failed.max<=0
		proj.7d_gib.last<=160
		proj.horizon_gib.last<=160
	EOF
}

# --- sampling ----------------------------------------------------------------------------------

seconds_of() {
	case $1 in
	*[!0-9smhd]* | '' | [smhd]*) return 1 ;;
	*s) echo "${1%s}" ;;
	*m) echo $((${1%m} * 60)) ;;
	*h) echo $((${1%h} * 3600)) ;;
	*d) echo $((${1%d} * 86400)) ;;
	*) return 1 ;;
	esac
}

iso() { date -u -d "@$1" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -r "$1" +%Y-%m-%dT%H:%M:%SZ; }

put() { printf '%s\t%s\t%s\n' "$t" "$1" "$2" >>"$out"; }

docker_ok() {
	[ "${LOAD_DOCKER:-1}" != 0 ] && command -v docker >/dev/null 2>&1 &&
		[ -n "$(docker compose ps -q server 2>/dev/null)" ]
}

# CPU and memory of the services and LOAD_CONTAINERS; the total counts the services only.
sample_docker() {
	refs=
	for service in server postgres loki caddy; do
		id=$(docker compose ps -q "$service" 2>/dev/null | head -n 1)
		[ -z "$id" ] || refs="$refs $service=$id"
	done
	refs="$refs ${LOAD_CONTAINERS:-}"
	targets=
	for ref in $refs; do targets="$targets ${ref#*=}"; done
	[ -n "$targets" ] || return 0
	# shellcheck disable=SC2086 # the containers
	docker stats --no-stream --format '{{.ID}}\t{{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}' $targets 2>/dev/null |
		awk -F '\t' -v refs="$refs" -v t="$t" '
			function mib(s,   n, u) {
				n = s + 0; u = s; sub(/^[0-9.]+/, "", u)
				if (u == "KiB" || u == "kB") return n / 1024
				if (u == "MiB" || u == "MB") return n
				if (u == "GiB" || u == "GB") return n * 1024
				if (u == "TiB") return n * 1048576
				return n / 1048576
			}
			BEGIN { n = split(refs, r, " ") }
			{
				name = ""
				for (i = 1; i <= n; i++) {
					label = r[i]; sub(/=.*/, "", label); ref = r[i]; sub(/^[^=]*=/, "", ref)
					if (ref == $2 || index(ref, $1) == 1) name = label
				}
				if (name == "") next
				cpu = $3; sub(/%/, "", cpu); split($4, m, " / ")
				printf "%s\tcpu.%s\t%s\n%s\tmem.%s\t%.1f\n", t, name, cpu + 0, t, name, mib(m[1])
				if (name ~ /^(server|postgres|loki|caddy)$/) { tc += cpu; tm += mib(m[1]) }
			}
			END { if (tc != "" || tm != "") printf "%s\tcpu.total\t%.2f\n%s\tmem.total\t%.1f\n", t, tc, t, tm }
		' >>"$out"
	# The volumes on disk: PostgreSQL with its WAL, Loki with chunks and index.
	pg=$(docker compose ps -q postgres 2>/dev/null | head -n 1)
	[ -n "$pg" ] || return 0
	image=$(docker inspect -f '{{.Image}}' "$pg")
	for part in postgres:/var/lib/postgresql/data loki:/loki; do
		id=$(docker compose ps -q "${part%%:*}" 2>/dev/null | head -n 1)
		[ -n "$id" ] || continue
		volume=$(docker inspect -f "{{range .Mounts}}{{if eq .Destination \"${part#*:}\"}}{{.Name}}{{end}}{{end}}" "$id")
		[ -n "$volume" ] || continue
		kib=$(docker run --rm --network none --entrypoint du -v "$volume:/v:ro" "$image" -sk /v 2>/dev/null | awk '{print $1}')
		[ -z "$kib" ] || put "disk.${part%%:*}" "$((kib * 1024))"
	done
}

# The server's counters and gauges, summed over their labels; ingest.dropped sums every counter of
# dropped or refused entries.
sample_metrics() {
	curl -fsS --max-time 10 "$LOAD_METRICS_URL" 2>/dev/null | awk -v t="$t" '
		/^#/ || NF < 2 { next }
		{ name = $1; sub(/\{.*/, "", name); v[name] += $NF }
		END {
			for (name in v) {
				printf "%s\tm.%s\t%s\n", t, name, v[name]
				if (name ~ /^sermon_server_ingest_(late|rejected|unknown)_samples_total$/ ||
					name ~ /^sermon_server_ingest_dropped_(series|security_events|log_entries|update_reports)_total$/ ||
					name ~ /^sermon_server_ingest_rejected_(security_events|update_reports)_total$/ ||
					name == "sermon_server_ingest_write_errors_total")
					d += v[name]
			}
			printf "%s\tingest.dropped\t%d\n", t, d
		}' >>"$out" || true
}

# Loki's memory as Go sees it: heap, GC target, resident memory, collections, chunks in memory.
sample_loki() {
	curl -fsS --max-time 10 "$LOAD_LOKI_METRICS_URL" 2>/dev/null | awk -v t="$t" '
		/^#/ || NF < 2 { next }
		{ name = $1; sub(/\{.*/, "", name) }
		name ~ /^(go_memstats_(heap_inuse|heap_alloc|next_gc|sys)_bytes|process_resident_memory_bytes|go_gc_duration_seconds_count|loki_ingester_memory_(chunks|streams))$/ {
			v[name] += $NF
		}
		END { for (name in v) printf "%s\tloki.%s\t%s\n", t, name, v[name] }' >>"$out" || true
}

sql() {
	if [ -n "${LOAD_PSQL:-}" ]; then
		sh -c "$LOAD_PSQL"
	else
		docker compose exec -T postgres psql -qtAX -F "$tab" -U postgres -d sermon
	fi
}

sample_sql() {
	sql 2>/dev/null <<-'EOF' | awk -F '\t' -v t="$t" 'NF == 2 && $2 != "" { printf "%s\t%s\t%s\n", t, $1, $2 }' >>"$out" || true
		WITH cagg AS (
		  SELECT view_name, format('%I.%I', materialization_hypertable_schema, materialization_hypertable_name) AS ht
		  FROM timescaledb_information.continuous_aggregates),
		size AS (
		  SELECT pg_database_size(current_database()) AS total,
		         hypertable_size('metrics_raw') AS raw,
		         (SELECT hypertable_size(ht::regclass) FROM cagg WHERE view_name = 'metrics_1m') AS m1,
		         (SELECT hypertable_size(ht::regclass) FROM cagg WHERE view_name = 'metrics_1h') AS h1,
		         hypertable_size('anomaly_points') AS anomaly,
		         -- The counts of the log histogram (V1-24); older versions have none.
		         (SELECT coalesce(sum(hypertable_size(h)), 0) FROM (VALUES (to_regclass('log_counts_1m')), (to_regclass('log_counts_1h'))) v (h)
		          WHERE h IS NOT NULL) AS logcounts),
		parts AS (
		  SELECT 'raw' AS part, 'metrics_raw'::regclass AS ht
		  UNION ALL SELECT '1m', ht::regclass FROM cagg WHERE view_name = 'metrics_1m'
		  UNION ALL SELECT '1h', ht::regclass FROM cagg WHERE view_name = 'metrics_1h'),
		comp AS (
		  SELECT p.part, count(*) FILTER (WHERE s.compression_status = 'Compressed') AS chunks,
		         coalesce(sum(s.after_compression_total_bytes), 0) AS after,
		         coalesce(sum(s.before_compression_total_bytes), 0) AS before
		  FROM parts p LEFT JOIN LATERAL chunk_compression_stats(p.ht) s ON true GROUP BY p.part)
		SELECT 'db.total', total FROM size
		UNION ALL SELECT 'db.raw', raw FROM size
		UNION ALL SELECT 'db.1m', m1 FROM size
		UNION ALL SELECT 'db.1h', h1 FROM size
		UNION ALL SELECT 'db.anomaly', anomaly FROM size
		UNION ALL SELECT 'db.logcounts', logcounts FROM size
		UNION ALL SELECT 'db.rest', total - raw - coalesce(m1, 0) - coalesce(h1, 0) - anomaly - logcounts FROM size
		UNION ALL SELECT 'db.' || part || '.compressed_chunks', chunks FROM comp
		UNION ALL SELECT 'db.' || part || '.compressed_bytes', after FROM comp
		UNION ALL SELECT 'db.' || part || '.uncompressed_bytes', before FROM comp
		UNION ALL SELECT 'db.series', (SELECT count(*) FROM series)
		UNION ALL SELECT 'db.wal_bytes', (SELECT wal_bytes FROM pg_stat_wal)
		UNION ALL SELECT 'db.checkpoints_requested', (SELECT num_requested FROM pg_stat_checkpointer)
		UNION ALL SELECT 'db.max_wal_size_bytes', pg_size_bytes(current_setting('max_wal_size'))
		UNION ALL SELECT 'db.pg_wal_bytes', (SELECT sum(size) FROM pg_ls_waldir())
		UNION ALL SELECT 'db.jobs_failed', (SELECT count(*) FROM timescaledb_information.job_stats WHERE last_run_status = 'Failed')
		UNION ALL SELECT 'db.rows.security_events', (SELECT count(*) FROM security_events)
		UNION ALL SELECT 'db.table.' || c.relname, pg_total_relation_size(c.oid) FROM pg_class c
		  WHERE c.relnamespace = 'public'::regnamespace AND c.relkind = 'r' AND pg_total_relation_size(c.oid) >= 1048576;
	EOF
}

# --- web API with a session --------------------------------------------------------------------

json_string() { printf '"%s"' "$(printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g')"; }

# rpc METHOD BODY: the answer in $work/answer; prints "HTTP-STATUS SECONDS".
rpc() {
	printf '%s' "$2" >"$work/body"
	# shellcheck disable=SC2086 # LOAD_CURL_ARGS are options
	curl -sS --max-time 30 -o "$work/answer" -w '%{http_code} %{time_total}' ${LOAD_CURL_ARGS:-} \
		-K "$work/curlrc" -b "$work/jar" -c "$work/jar" -X POST \
		-H 'Content-Type: application/json' -H "Origin: $origin" \
		--data-binary "@$work/body" "$LOAD_URL/sermon.web.v1.$1" 2>/dev/null || echo "000 0"
}

login() {
	[ -n "${LOAD_EMAIL:-}" ] && [ -r "${LOAD_PASSWORD_FILE:-}" ] || return 0
	email=$(json_string "$LOAD_EMAIL")
	password=$(json_string "$(cat "$LOAD_PASSWORD_FILE")")
	credentials="{\"email\":$email,\"password\":$password}"
	set -- "$(rpc AuthService/Login "$credentials")"
	credentials='' password=''
	rm -f "$work/body"
	case $1 in 200\ *) ;; *) echo "load-report: login refused (${1%% *})" >&2 ;; esac
}

# measure NAME METHOD BODY: the latency in ms, or an error.
measure() {
	set -- "$1" "$(rpc "$2" "$3")"
	case $2 in
	200\ *)
		put "q.$1" "$(echo "${2#* }" | awk '{printf "%.1f", $1 * 1000}')"
		;;
	*)
		errors=$((errors + 1))
		echo "load-report: $1 answered ${2%% *}: $(head -c 300 "$work/answer" 2>/dev/null)" >&2
		;;
	esac
}

sample_queries() {
	errors=0
	set -- "$(rpc FleetService/GetFleetSummary '{}')"
	case $1 in 401\ *) login && set -- "$(rpc FleetService/GetFleetSummary '{}')" ;; esac
	case $1 in
	200\ *)
		put q.fleet "$(echo "${1#* }" | awk '{printf "%.1f", $1 * 1000}')"
		online=$(grep -o '"online":[0-9]*' "$work/answer" | head -n 1 | cut -d: -f2)
		put fleet.online "${online:-0}"
		;;
	*)
		put q.errors 1
		echo "load-report: fleet answered ${1%% *}" >&2
		return 0
		;;
	esac
	ids=$(grep -o '"nodeId":"[^"]*"' "$work/answer" | cut -d'"' -f4 | head -n 100)
	node=$(echo "$ids" | head -n 1)
	[ -n "$node" ] || {
		put q.errors 0
		return 0
	}
	list=$(echo "$ids" | awk '{printf "%s\"%s\"", (NR > 1 ? "," : ""), $0}')
	to=$(iso "$t")
	cpu='"metric":"sermon_cpu_utilization_ratio"'
	measure sparklines MetricsService/QueryMetrics "{\"selector\":{$cpu,\"nodeIds\":[$list],\"labels\":{\"mode\":\"idle\"}},\"from\":\"$(iso $((t - 900)))\",\"to\":\"$to\",\"step\":\"30s\"}"
	measure node NodeService/GetNode "{\"nodeId\":\"$node\"}"
	measure chart_1h MetricsService/QueryMetrics "{\"selector\":{$cpu,\"nodeIds\":[\"$node\"]},\"from\":\"$(iso $((t - 3600)))\",\"to\":\"$to\"}"
	measure chart_24h MetricsService/QueryMetrics "{\"selector\":{$cpu,\"nodeIds\":[\"$node\"]},\"from\":\"$(iso $((t - 86400)))\",\"to\":\"$to\"}"
	measure chart_30d MetricsService/QueryMetrics "{\"selector\":{$cpu,\"nodeIds\":[\"$node\"]},\"from\":\"$(iso $((t - 2592000)))\",\"to\":\"$to\"}"
	range="\"from\":\"$(iso $((t - 3600)))\",\"to\":\"$to\""
	measure logs LogService/QueryLogs "{\"filter\":{$range},\"limit\":200}"
	measure log_histogram LogService/GetLogHistogram "{\"filter\":{$range}}"
	measure log_histogram_text LogService/GetLogHistogram "{\"filter\":{$range,\"text\":\"nginx\"}}"
	measure log_histogram_2d LogService/GetLogHistogram "{\"filter\":{\"from\":\"$(iso $((t - 172800)))\",\"to\":\"$to\"}}"
	measure log_histogram_14d LogService/GetLogHistogram "{\"filter\":{\"from\":\"$(iso $((t - 1209600)))\",\"to\":\"$to\"}}"
	put q.errors "$errors"
}

sample() {
	[ $# -ge 2 ] || usage
	dir=$1
	duration=$(seconds_of "$2") || usage
	interval=${3:-30}
	mkdir -p "$dir"
	out="$dir/samples.tsv"
	work=$(mktemp -d)
	trap 'rm -rf "$work"' EXIT
	trap 'exit 0' INT TERM
	: >"$work/curlrc"
	: >"$work/jar"
	chmod 0600 "$work/curlrc" "$work/jar"
	if [ -n "${LOAD_COOKIE_FILE:-}" ]; then
		printf 'header = "Cookie: __Host-sermon_session=%s"\n' "$(cat "$LOAD_COOKIE_FILE")" >"$work/curlrc"
	fi
	origin=$(printf '%s' "${LOAD_URL:-}" | sed -E 's#^(https?://[^/]+).*#\1#')
	use_docker=0
	docker_ok && use_docker=1
	{
		printf 'started\t%s\n' "$(date +%s)"
		printf 'host\t%s\n' "$(uname -srm)"
		[ "$use_docker" = 0 ] || printf 'docker\t%s\n' "$(docker info --format '{{.NCPU}} CPUs, {{.MemTotal}} bytes, {{.OperatingSystem}}' 2>/dev/null)"
		printf 'url\t%s\n' "${LOAD_URL:-}"
	} >>"$dir/info.tsv"
	[ -z "${LOAD_URL:-}" ] || login
	start=$(date +%s)
	while :; do
		t=$(date +%s)
		[ -z "${LOAD_METRICS_URL:-}" ] || sample_metrics
		[ -z "${LOAD_LOKI_METRICS_URL:-}" ] || sample_loki
		[ "$use_docker" = 0 ] || sample_docker
		if [ -n "${LOAD_PSQL:-}" ] || [ "$use_docker" = 1 ]; then sample_sql; fi
		[ -z "${LOAD_URL:-}" ] || sample_queries
		[ $(($(date +%s) + interval - start)) -le "$duration" ] || break
		wait=$((t + interval - $(date +%s)))
		[ "$wait" -le 0 ] || sleep "$wait"
	done
}

# --- report ------------------------------------------------------------------------------------

# Per key: n min avg p50 p95 max sum first last delta rate_h rate2_h span_h; rate2 is the growth
# per hour over the second half of the window (after the start-up).
stats() {
	sort -t "$tab" -k2,2 -k3,3g "$1" | awk -F '\t' '
		function flush() { if (k != "") printf "%s\t%s\t%s\n", k, a[int((c + 1) / 2)], a[c - int(c * 0.05)] }
		$2 != k { flush(); k = $2; c = 0 }
		{ a[++c] = $3 }
		END { flush() }' >"$work/pct"
	sort -t "$tab" -k2,2 -k1,1n "$1" | awk -F '\t' '
		function flush(   mid, i, m) {
			if (k == "") return
			mid = (t[1] + t[c]) / 2; m = c
			for (i = 1; i <= c; i++) if (t[i] >= mid) { m = i; break }
			span = t[c] - t[1]
			rate = span > 0 ? (v[c] - v[1]) / span * 3600 : 0
			rate2 = t[c] - t[m] > 0 ? (v[c] - v[m]) / (t[c] - t[m]) * 3600 : rate
			printf "%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", k, c, min, sum / c, max, sum, v[1], v[c], v[c] - v[1], rate, rate2, span / 3600
		}
		$2 != k { flush(); k = $2; c = 0; sum = 0; min = ""; max = "" }
		{ t[++c] = $1; v[c] = $3 + 0; sum += $3; if (min == "" || $3 + 0 < min) min = $3 + 0; if (max == "" || $3 + 0 > max) max = $3 + 0 }
		END { flush() }' >"$work/time"
	# key n min avg p50 p95 max sum first last delta rate_h rate2_h span_h
	awk -F '\t' -v OFS='\t' 'NR == FNR { p[$1] = $2 OFS $3; next } { print $1, $2, $3, $4, p[$1], $5, $6, $7, $8, $9, $10, $11, $12 }' \
		"$work/pct" "$work/time"
}

# value KEY STAT: a statistic of a key, empty if it was not measured.
value() {
	awk -F '\t' -v k="$1" -v s="$2" '
		BEGIN { split("n min avg p50 p95 max sum first last delta rate_h rate2_h span_h", f, " "); for (i in f) col[f[i]] = i + 1 }
		$1 == k && (s in col) { print $col[s]; exit }' "$work/stats"
}

# fmt VALUE [DIVISOR] [DECIMALS]: a number for the tables, n/a without a value.
fmt() {
	if [ -z "$1" ]; then echo n/a; else awk -v v="$1" -v d="${2:-1}" -v p="${3:-1}" 'BEGIN { printf "%." p "f", v / d }'; fi
}

# linear NAME KEY DAYS MODEL [RATE]: a part after 7 days and at the horizon, growing as in the
# second half of the window (or as RATE, e.g. rate_h for the whole window) up to its retention
# (DAYS, 0 = none).
linear() {
	[ -n "$(value "$2" last)" ] || return 0
	awk -v name="$1" -v n="$(value "$2" last)" -v r="$(value "$2" "${5:-rate2_h}")" -v s="$(value "$2" span_h)" \
		-v d="$3" -v h="$horizon" -v model="$4" '
		function at(x,   g) { if (d > 0 && x > d) x = d; g = n + (r > 0 ? r : 0) * (x * 24 - s); return g > n ? g : n }
		BEGIN { printf "%s\t%s\t%.0f\t%.0f\n", name, model, at(7), at(h) }'
}

# compressed KEY: true if the tier KEY has compressed chunks and a growth of its logical size.
compressed() {
	chunks=$(value "$1.compressed_chunks" last)
	[ -n "$chunks" ] && [ "$(fmt "$chunks" 1 0)" -gt 0 ] && [ -n "$(value "$1.logical" rate2_h)" ]
}

# tier NAME KEY DAYS OPEN UPPER [RATIO_KEY]: a metric tier up to its retention (DAYS): the newest
# OPEN hours uncompressed (the columnstore limit plus one chunk: compressed are only chunks that
# ended before the limit), the rest at the ratio of the chunks compressed so far (chunks may be
# partial: their ratio counts, not their size), RATIO_KEY's if the tier has none yet (V1-21). The
# growth per hour is that of the uncompressed data (KEY.logical: as if nothing were compressed).
# Without any compressed chunk, linear up to the retention (UPPER: the model, an upper bound).
tier() {
	ratio=$2
	compressed "$ratio" || ratio=${6:-}
	if [ -z "$ratio" ] || ! compressed "$ratio" || [ -z "$(value "$2.logical" rate2_h)" ]; then
		linear "$1" "$2" "$3" "$5"
		return 0
	fi
	note=""
	[ "$ratio" = "$2" ] || note=" (ratio of ${ratio#db.}, none compressed here yet)"
	awk -v a="$(value "$ratio.compressed_bytes" last)" -v b="$(value "$ratio.uncompressed_bytes" last)" \
		-v r="$(value "$2.logical" rate2_h)" -v d="$3" -v open="$4" -v h="$horizon" -v name="$1" -v note="$note" '
		function at(x,   hours) { hours = (x < d ? x : d) * 24; return r * (hours < open ? hours : open + (hours - open) * q) }
		BEGIN {
			q = b > 0 ? a / b : 1
			printf "%s\t%.0f MiB per hour, compressed to %.1f %%%s: %d h + %d h × %.1f %%\t%.0f\t%.0f\n",
				name, r / 1048576, q * 100, note, open, d * 24 - open, q * 100, at(7), at(h)
		}'
}

projection() {
	{
		# metrics_raw: compressed after 2 h (1-h chunks, counted as 2 h open); metrics_1m after 6 h
		# (10-h chunks); metrics_1h after 1 day (7-day chunks; migrations 00013, 00082).
		tier "metrics_raw (24 h)" db.raw 1 2 "growth per hour up to 24 h (nothing compressed yet: upper bound)"
		tier "metrics_1m (30 d)" db.1m 30 16 "linear up to its retention (nothing compressed yet: upper bound)"
		tier "metrics_1h (365 d)" db.1h 365 192 "linear up to its retention (nothing compressed yet: upper bound)" db.1m
		linear "anomaly_points (30 d)" db.anomaly 30 "linear up to its retention"
		# The counts of the log histogram (V1-24): 14 days, as measured if the run backfilled them.
		backfill=$(awk -F '\t' '$1 == "backfill" { v = $2 } END { print v }' "$dir/info.tsv" 2>/dev/null || true)
		if [ -n "$backfill" ] && [ -n "$(value db.logcounts last)" ]; then
			printf 'log counts (14 d)\tas measured, 14 days backfilled\t%s\t%s\n' "$(value db.logcounts last)" "$(value db.logcounts last)"
		else
			linear "log counts (14 d)" db.logcounts 14 "linear up to its retention"
		fi
		linear "security events and alert history ($event_days d)" db.events "$event_days" "linear up to their retention (--event-days)"
		linear "rest of the database" db.rest 0 "linear, without a limit"
		# pg_wal as measured (a checkpoint frees it); without a measurement at most max_wal_size.
		if [ -n "$(value db.pg_wal_bytes max)" ]; then
			printf 'WAL (pg_wal)\tlargest measured (max_wal_size %.0f GiB)\t%s\t%s\n' "$(fmt "$(value db.max_wal_size_bytes last)" 1073741824 1)" "$(value db.pg_wal_bytes max)" "$(value db.pg_wal_bytes max)"
		elif [ -n "$(value db.max_wal_size_bytes last)" ]; then
			printf 'WAL (pg_wal)\tat most max_wal_size\t%s\t%s\n' "$(value db.max_wal_size_bytes last)" "$(value db.max_wal_size_bytes last)"
		fi
		# Loki writes its chunks in steps (flushes, compaction): the whole window counts.
		linear "Loki (14 d)" disk.loki 14 "linear up to its retention (whole window)" rate_h
	} >"$work/projection"
	if [ -s "$work/projection" ]; then
		awk -F '\t' -v t="$(date +%s)" '{ a += $3; b += $4 } END { printf "%s\tproj.7d_gib\t%s\n%s\tproj.horizon_gib\t%s\n", t, a / 1073741824, t, b / 1073741824 }' \
			"$work/projection" >"$work/proj.tsv"
		stats "$work/proj.tsv" >>"$work/stats"
	fi
}

# check SPEC MEASURED: ok, fail or n/a.
check() {
	if [ -z "$2" ]; then
		echo n/a
		return
	fi
	case $1 in
	*'>='*) awk -v m="$2" -v l="${1#*>=}" 'BEGIN { print (m + 0 >= l + 0) ? "ok" : "fail" }' ;;
	*) awk -v m="$2" -v l="${1#*<=}" 'BEGIN { print (m + 0 <= l + 0) ? "ok" : "fail" }' ;;
	esac
}

# The measured value of a limit's KEY.STAT.
measured() {
	spec=${1%%[<>]=*}
	value "${spec%.*}" "${spec##*.}"
}

report() {
	[ $# -ge 1 ] || usage
	dir=$1
	shift
	[ -s "$dir/samples.tsv" ] || {
		echo "load-report: no samples in $dir" >&2
		exit 2
	}
	work=$(mktemp -d)
	trap 'rm -rf "$work"' EXIT
	horizon=365
	# The retention of the security events (SERMON_EVENT_RETENTION, V1-22).
	event_days=90
	default_limits >"$work/limits"
	while [ $# -gt 0 ]; do
		case $1 in
		--limit)
			[ $# -ge 2 ] || usage
			case $2 in *'<='* | *'>='*) ;; *) usage ;; esac
			# A limit of the same key and statistic replaces the default.
			awk -v k="${2%%[<>]=*}" 'index($0, k "<=") != 1 && index($0, k ">=") != 1' "$work/limits" >"$work/limits.new"
			echo "$2" >>"$work/limits.new"
			mv "$work/limits.new" "$work/limits"
			shift 2
			;;
		--horizon)
			[ $# -ge 2 ] || usage
			horizon=$2
			shift 2
			;;
		--event-days)
			[ $# -ge 2 ] || usage
			event_days=$2
			shift 2
			;;
		*) usage ;;
		esac
	done
	# Derived: each metric tier as if nothing were compressed.
	awk -F '\t' -v OFS='\t' '
		$2 ~ /^db\.(raw|1m|1h)$/ { r[$2, $1] = $3; k[$2, $1] = 1 }
		$2 ~ /^db\.(raw|1m|1h)\.compressed_bytes$/ { a[substr($2, 1, length($2) - 17), $1] = $3 }
		$2 ~ /^db\.(raw|1m|1h)\.uncompressed_bytes$/ { b[substr($2, 1, length($2) - 19), $1] = $3 }
		# The security events (their partitions) and the alert history: kept for their retention.
		$2 ~ /^db\.table\.(security_events_[0-9][0-9][0-9][0-9][0-9][0-9]|alert_history)$/ { e[$1] += $3 }
		$2 == "db.rest" { rest[$1] = $3; next }
		{ print }
		END {
			for (x in k) if ((x in a) && (x in b)) { split(x, p, SUBSEP); print p[2], p[1] ".logical", r[x] + b[x] - a[x] }
			for (t in rest) if (t in e) { print t, "db.rest", rest[t] - e[t]; print t, "db.events", e[t] } else print t, "db.rest", rest[t]
		}' \
		"$dir/samples.tsv" >"$work/samples"
	stats "$work/samples" >"$work/stats"
	projection

	info() { awk -F '\t' -v k="$1" '$1 == k { v = $2 } END { print v }' "$dir/info.tsv" 2>/dev/null; }
	first=$(awk -F '\t' 'NR == 1 || $1 < m { m = $1 } END { print m }' "$dir/samples.tsv")
	last=$(awk -F '\t' '$1 > m { m = $1 } END { print m }' "$dir/samples.tsv")
	rounds=$(cut -f 1 "$dir/samples.tsv" | sort -u | wc -l | tr -d ' ')

	echo "# SerMon load report"
	echo
	echo "- **Window:** $(iso "$first") – $(iso "$last") ($(fmt $((last - first)) 3600 2) h, $rounds samples)"
	echo "- **Nodes online:** median $(fmt "$(value fleet.online p50)" 1 0), minimum $(fmt "$(value fleet.online min)" 1 0); agent sessions median $(fmt "$(value m.sermon_server_agent_sessions p50)" 1 0)"
	[ -z "$(info setup)" ] || echo "- **Load:** $(info setup)"
	[ -z "$(info machine)" ] || echo "- **Machine:** $(info machine)"
	[ -z "$(info compressed)" ] || echo "- **Compression:** $(info compressed)"
	[ -z "$(info backfill)" ] || echo "- **Backfill:** $(info backfill)"
	[ -z "$(info host)" ] || echo "- **Sampled on:** $(info host)"
	[ -z "$(info docker)" ] || echo "- **Docker:** $(info docker)"
	[ -z "$(info url)" ] || echo "- **Server:** $(info url)"
	echo
	echo "## Resources (docker stats; CPU in % of one core, memory in MiB)"
	echo
	echo "| Container | CPU avg | CPU p95 | CPU max | Memory avg | Memory max |"
	echo "|---|---:|---:|---:|---:|---:|"
	cut -f 1 "$work/stats" | sed -n 's/^cpu\.//p' | sort -u >"$work/containers"
	while IFS= read -r name; do
		echo "| $name | $(fmt "$(value "cpu.$name" avg)") | $(fmt "$(value "cpu.$name" p95)") | $(fmt "$(value "cpu.$name" max)") | $(fmt "$(value "mem.$name" avg)" 1 0) | $(fmt "$(value "mem.$name" max)" 1 0) |"
	done <"$work/containers"
	[ -n "$(value cpu.total n)" ] || echo "| n/a (no docker) | | | | | |"
	echo
	if [ -n "$(value loki.go_memstats_heap_inuse_bytes n)" ]; then
		echo "## Memory of Loki (its /metrics, MiB)"
		echo
		echo "| Measurement | avg | max |"
		echo "|---|---:|---:|"
		for row in "go_memstats_heap_inuse_bytes:Go heap in use" "go_memstats_next_gc_bytes:Go heap target of the next GC" \
			"go_memstats_sys_bytes:memory Go took from the system" "process_resident_memory_bytes:resident memory (RSS)"; do
			key=loki.${row%%:*}
			[ -n "$(value "$key" n)" ] || continue
			echo "| ${row#*:} | $(fmt "$(value "$key" avg)" 1048576 0) | $(fmt "$(value "$key" max)" 1048576 0) |"
		done
		[ -z "$(value mem.loki n)" ] || echo "| container (docker stats) | $(fmt "$(value mem.loki avg)" 1 0) | $(fmt "$(value mem.loki max)" 1 0) |"
		[ -z "$(value loki.go_gc_duration_seconds_count n)" ] || echo "| garbage collections per minute | $(fmt "$(value loki.go_gc_duration_seconds_count rate_h)" 60 1) | |"
		echo
	fi
	echo "## Ingest (server /metrics)"
	echo
	m=m.sermon_server_ingest
	echo "| Measurement | Value |"
	echo "|---|---:|"
	echo "| Samples per second | $(fmt "$(value ${m}_samples_total rate_h)" 3600 0) |"
	echo "| Batches per second | $(fmt "$(value ${m}_batches_total rate_h)" 3600 1) |"
	echo "| Log entries per second | $(fmt "$(value ${m}_log_entries_total rate_h)" 3600 1) |"
	echo "| Security events per second | $(fmt "$(value ${m}_security_events_total rate_h)" 3600 2) |"
	echo "| Lag p50 / p95 / max (s) | $(fmt "$(value ${m}_lag_seconds p50)" 1 2) / $(fmt "$(value ${m}_lag_seconds p95)" 1 2) / $(fmt "$(value ${m}_lag_seconds max)" 1 2) |"
	echo "| Dropped or refused in the window | $(fmt "$(value ingest.dropped delta)" 1 0) |"
	for counter in late_samples rejected_samples unknown_samples dropped_series dropped_log_entries dropped_security_events write_errors; do
		d=$(value "${m}_${counter}_total" delta)
		[ -z "$d" ] || [ "$(fmt "$d" 1 0)" = 0 ] || echo "| – of them \`${counter}\` | $(fmt "$d" 1 0) |"
	done
	echo "| Server heap max (MiB) | $(fmt "$(value m.sermon_server_heap_bytes max)" 1048576 0) |"
	echo
	echo "## Reads of the UI (web API, ms)"
	echo
	echo "| Read | p50 | p95 | max | n |"
	echo "|---|---:|---:|---:|---:|"
	for q in fleet sparklines node chart_1h chart_24h chart_30d logs log_histogram log_histogram_text log_histogram_2d log_histogram_14d; do
		echo "| $q | $(fmt "$(value "q.$q" p50)" 1 0) | $(fmt "$(value "q.$q" p95)" 1 0) | $(fmt "$(value "q.$q" max)" 1 0) | $(fmt "$(value "q.$q" n)" 1 0) |"
	done
	echo "| failed reads | $(fmt "$(value q.errors sum)" 1 0) | | | |"
	echo
	echo "## Storage (MiB)"
	echo
	echo "| Part | Start | End | Growth per hour | 2nd half per hour |"
	echo "|---|---:|---:|---:|---:|"
	for row in "db.total:Database" "db.raw:metrics_raw" "db.1m:metrics_1m" "db.1h:metrics_1h" "db.anomaly:anomaly_points" \
		"db.logcounts:log counts (log_counts_1m, log_counts_1h)" \
		"disk.postgres:PostgreSQL volume (with WAL)" "disk.loki:Loki volume"; do
		key=${row%%:*}
		[ -n "$(value "$key" n)" ] || continue
		echo "| ${row#*:} | $(fmt "$(value "$key" first)" 1048576) | $(fmt "$(value "$key" last)" 1048576) | $(fmt "$(value "$key" rate_h)" 1048576) | $(fmt "$(value "$key" rate2_h)" 1048576) |"
	done
	[ -z "$(value db.wal_bytes n)" ] || echo "| WAL written | | | $(fmt "$(value db.wal_bytes rate_h)" 1048576) | $(fmt "$(value db.wal_bytes rate2_h)" 1048576) |"
	[ -z "$(value db.checkpoints_requested n)" ] || echo "| Checkpoints requested by WAL size (count) | | $(fmt "$(value db.checkpoints_requested delta)" 1 0) | | |"
	[ -z "$(value db.rows.security_events n)" ] || echo "| Security events (count) | $(fmt "$(value db.rows.security_events first)" 1 0) | $(fmt "$(value db.rows.security_events last)" 1 0) | $(fmt "$(value db.rows.security_events rate_h)" 1 0) | $(fmt "$(value db.rows.security_events rate2_h)" 1 0) |"
	[ -z "$(value db.series n)" ] || echo "| Series (count) | $(fmt "$(value db.series first)" 1 0) | $(fmt "$(value db.series last)" 1 0) | | |"
	awk -F '\t' '$1 ~ /^db\.table\./ { sub(/^db\.table\./, "", $1); print $10 "\t" $1 }' "$work/stats" | sort -rg | head -n 8 | cut -f 2 >"$work/tables"
	if [ -s "$work/tables" ]; then
		echo
		echo "Largest tables besides the metric hypertables (with indexes and TOAST):"
		echo
		echo "| Table | End | Growth per hour | 2nd half per hour |"
		echo "|---|---:|---:|---:|"
		while IFS= read -r table; do
			echo "| $table | $(fmt "$(value "db.table.$table" last)" 1048576) | $(fmt "$(value "db.table.$table" rate_h)" 1048576) | $(fmt "$(value "db.table.$table" rate2_h)" 1048576) |"
		done <"$work/tables"
	fi
	echo
	echo "## Extrapolation (GiB)"
	echo
	if [ -s "$work/projection" ]; then
		echo "| Part | Model | after 7 days | after $horizon days |"
		echo "|---|---|---:|---:|"
		awk -F '\t' '{ printf "| %s | %s | %.2f | %.2f |\n", $1, $2, $3 / 1073741824, $4 / 1073741824 }' "$work/projection"
		echo "| **Total** | | **$(fmt "$(value proj.7d_gib last)" 1 2)** | **$(fmt "$(value proj.horizon_gib last)" 1 2)** |"
	else
		echo "n/a (no access to the database)"
	fi
	echo
	echo "## Limits"
	echo
	echo "| Limit | Measured | Result |"
	echo "|---|---:|---|"
	failed=0 checked=0 refuted=0
	while IFS= read -r spec; do
		[ -n "$spec" ] || continue
		got=$(measured "$spec")
		result=$(check "$spec" "$got")
		case $result in
		ok) mark='✅ ok' ;;
		fail) mark='❌ exceeded' failed=$((failed + 1)) ;;
		*) mark='– n/a' ;;
		esac
		echo "| \`$spec\` | $(fmt "$got" 1 2) | $mark |"
		# The counter-check: the same measurement against a limit just beyond it must fail.
		if [ "$result" = ok ]; then
			checked=$((checked + 1))
			case $spec in
			*'>='*) lowered="${spec%%>=*}>=$(awk -v m="$got" 'BEGIN { print m + (m < 0 ? -m : m) * 0.1 + 1 }')" ;;
			*) lowered="${spec%%<=*}<=$(awk -v m="$got" 'BEGIN { print m - (m < 0 ? -m : m) * 0.1 - 1 }')" ;;
			esac
			[ "$(check "$lowered" "$got")" != fail ] || refuted=$((refuted + 1))
		fi
	done <"$work/limits"
	echo
	echo "Counter-check: $refuted/$checked of the limits kept fail when set just beyond the measurement."
	echo
	if [ "$failed" -gt 0 ]; then
		echo "**Result: $failed limit(s) exceeded.**"
		[ "$refuted" = "$checked" ] || exit 3
		exit 1
	fi
	echo "**Result: every measured limit kept.**"
	[ "$refuted" = "$checked" ] || exit 3
}

case ${1:-} in
sample)
	shift
	sample "$@"
	;;
report)
	shift
	report "$@"
	;;
limits) default_limits ;;
*) usage ;;
esac
