#!/bin/sh
# shellcheck disable=SC2016 # the backticks are Markdown in the expected report
# Tests for load-report.sh (V1-08): the report of a synthetic window keeps the default limits and
# fails a lowered one (the counter-check of the plan), its statistics and extrapolation are right,
# and sampling reads /metrics, SQL and the web API (curl replaced by a fake) and logs in again when
# the session ends. Run: sh scripts/load-report_test.sh
set -eu

dir=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
fail() {
	echo "FAIL: $*"
	exit 1
}
report() { sh "$dir/load-report.sh" report "$@"; }
has() {
	case $1 in *"$2"*) ;; *) fail "missing \"$2\" in:
$1" ;; esac
}

# Two hours, one sample a minute: steady resources, the database growing 1 GiB per hour.
mkdir "$work/run"
awk -v t0=1795000000 'BEGIN {
	gib = 1073741824; mib = 1048576
	for (i = 0; i <= 120; i++) {
		t = t0 + i * 60; h = i / 60
		printf "%d\tcpu.server\t%d\n%d\tcpu.postgres\t20\n%d\tcpu.loki\t5\n%d\tcpu.caddy\t1\n", t, 10 + i % 3, t, t, t
		printf "%d\tcpu.total\t%d\n%d\tmem.server\t300\n%d\tmem.total\t2000\n", t, 36 + i % 3, t, t
		printf "%d\tq.chart_1h\t%d\n%d\tq.fleet\t50\n%d\tq.errors\t0\n", t, i + 1, t, t
		printf "%d\tfleet.online\t50\n%d\tm.sermon_server_ingest_lag_seconds\t0.3\n", t, t
		printf "%d\tm.sermon_server_ingest_samples_total\t%d\n%d\tingest.dropped\t0\n", t, i * 60 * 2000, t
		printf "%d\tdb.total\t%d\n%d\tdb.raw\t%d\n%d\tdb.1m\t%d\n", t, 100 * mib + h * gib, t, h * gib / 2, t, h * 10 * mib
		printf "%d\tdb.rest\t%d\n%d\tdisk.loki\t%d\n", t, 50 * mib, t, h * 100 * mib
		printf "%d\tdb.table.security_events\t%d\n%d\tdb.table.nodes\t%d\n", t, 40 * mib, t, 2 * mib
		# Anomalies only from the second hour on: the second half grows twice as fast.
		printf "%d\tdb.anomaly\t%d\n", t, (h > 1 ? (h - 1) * 60 * mib : 0)
		printf "%d\tdb.raw.compressed_chunks\t0\n%d\tdb.jobs_failed\t0\n%d\tdb.max_wal_size_bytes\t%d\n", t, t, t, gib
	}
}' >"$work/run/samples.tsv"
printf 'host\tLinux 6.12 x86_64\n' >"$work/run/info.tsv"

out=$(report "$work/run") || fail "the default limits failed: $out"
has "$out" "**Result: every measured limit kept.**"
has "$out" "| chart_1h | 61 | 115 | 121 | 121 |"
has "$out" "| total | 37.0 | 38.0 | 38.0 | 2000 | 2000 |"
has "$out" "| Samples per second | 2000 |"
has "$out" "| Database | 100.0 | 2148.0 | 1024.0 | 1024.0 |"
has "$out" "| anomaly_points | 0.0 | 60.0 | 30.0 | 60.0 |"
has "$out" "| anomaly_points (30 d) | linear up to its retention | 9.79 | 42.13 |"
has "$out" "| security_events | 40.0 | 0.0 | 0.0 |
| nodes | 2.0 | 0.0 | 0.0 |"
# metrics_raw grows 0.5 GiB per hour up to its 24 h: 12 GiB; metrics_1m 10 MiB per hour up to 30
# days; the rest stays; Loki up to 14 days; the WAL at most max_wal_size.
has "$out" "| metrics_raw (24 h) | growth per hour up to 24 h (nothing compressed yet: upper bound) | 12.00 | 12.00 |"
has "$out" "| metrics_1m (30 d) | linear up to its retention (nothing compressed yet: upper bound) | 1.64 | 7.03 |"
has "$out" "| rest of the database | linear, without a limit | 0.05 | 0.05 |"
has "$out" "| Loki (14 d) | linear up to its retention (whole window) | 16.41 | 32.81 |"
has "$out" "| **Total** | | **40.88** | **95.02** |"
has "$out" '| `q.chart_1h.p95<=200` | 115.00 | ✅ ok |'
has "$out" '| `q.logs.p95<=1000` | n/a | – n/a |'
has "$out" "Counter-check: 12/12 of the limits kept fail when set just beyond the measurement."

# The counter-check of the plan: a limit lowered under the measurement fails the report.
if out=$(report "$work/run" --limit 'q.chart_1h.p95<=100'); then fail "a lowered limit passed: $out"; fi
has "$out" '| `q.chart_1h.p95<=100` | 115.00 | ❌ exceeded |'
has "$out" "**Result: 1 limit(s) exceeded.**"
case $out in *'q.chart_1h.p95<=200'*) fail "the default stayed next to the replacing limit" ;; esac
if out=$(report "$work/run" --limit 'fleet.online.p50>=51'); then fail "too few nodes passed"; fi
report "$work/run" --limit 'fleet.online.p50>=50' >/dev/null || fail "enough nodes failed"
if out=$(report "$work/run" --limit 'proj.7d_gib.last<=30'); then fail "the extrapolation passed 30 GiB"; fi
has "$out" '| `proj.7d_gib.last<=30` | 40.88 | ❌ exceeded |'
if out=$(report "$work/run" --limit 'cpu.total.p95<=37'); then fail "CPU passed under its p95"; fi
report "$work/run" --limit 'cpu.total.p95<=38' >/dev/null || fail "CPU at its p95 failed"
out=$(report "$work/run" --horizon 30) || fail "horizon: $out"
has "$out" "| metrics_1m (30 d) | linear up to its retention (nothing compressed yet: upper bound) | 1.64 | 7.03 |"
has "$out" "after 30 days"

# Once chunks are compressed, the raw tier is 2 h uncompressed and 22 h at their ratio. Here 512
# MiB per hour arrive, from the second hour on what is older than an hour shrinks to 10 %: the
# table grows 51 MiB per hour then, the data 512 → 512 × (2 + 22 × 0.1) MiB.
mkdir "$work/compressed"
awk -F '\t' -v OFS='\t' -v t0=1795000000 '
	$2 == "db.raw" || $2 ~ /^db\.raw\.compressed/ { next }
	{ print }
	$2 == "cpu.server" {
		h = ($1 - t0) / 3600; b = (h > 1 ? (h - 1) * 536870912 : 0); a = b * 0.1
		print $1, "db.raw", h * 536870912 - b + a; print $1, "db.raw.compressed_chunks", (h > 1 ? 2 : 0)
		print $1, "db.raw.compressed_bytes", a; print $1, "db.raw.uncompressed_bytes", b
	}' "$work/run/samples.tsv" >"$work/compressed/samples.tsv"
out=$(report "$work/compressed") || fail "compressed: $out"
has "$out" "| metrics_raw (24 h) | 512 MiB per hour, compressed to 10.0 %: 2 h + 22 h × 10.0 % | 2.10 | 2.10 |"

# Security events growing 10 MiB per hour: up to their retention (--event-days), the rest
# without them; pg_wal as measured instead of max_wal_size.
mkdir "$work/events"
awk -F '\t' -v OFS='\t' -v t0=1795000000 '
	$2 == "db.table.security_events" { next }
	{ print }
	$2 == "cpu.server" {
		h = ($1 - t0) / 3600
		print $1, "db.table.security_events_202611", h * 10485760; print $1, "db.table.alert_history", 1048576
		print $1, "db.pg_wal_bytes", 268435456 * (1 + h / 2)
	}' "$work/run/samples.tsv" >"$work/events/samples.tsv"
out=$(report "$work/events" --event-days 30) || fail "events: $out"
# 21 MiB + 10 MiB × (7 × 24 − 2) and × (30 × 24 − 2).
has "$out" "| security events and alert history (30 d) | linear up to their retention (--event-days) | 1.64 | 7.03 |"
has "$out" "| WAL (pg_wal) | largest measured (max_wal_size 1 GiB) | 0.50 | 0.50 |"
# The rest (50 MiB) without the events (21 MiB at the end).
has "$out" "| rest of the database | linear, without a limit | 0.03 | 0.03 |"

# The aggregates (V1-21): metrics_1m 16 h open and the rest of its 30 days at its ratio; metrics_1h
# without a compressed chunk of its own at the ratio of metrics_1m, 192 h open. Here metrics_1m
# grows 60 MiB per hour, compressed to 5 %; metrics_1h 6 MiB per hour.
mkdir "$work/aggregates"
awk -F '\t' -v OFS='\t' -v t0=1795000000 '
	$2 == "db.1m" { next }
	{ print }
	$2 == "cpu.server" {
		h = ($1 - t0) / 3600; b = (h > 1 ? (h - 1) * 62914560 : 0); a = b * 0.05
		print $1, "db.1m", h * 62914560 - b + a; print $1, "db.1m.compressed_chunks", (h > 1 ? 1 : 0)
		print $1, "db.1m.compressed_bytes", a; print $1, "db.1m.uncompressed_bytes", b
		print $1, "db.1h", h * 6291456; print $1, "db.1h.compressed_chunks", 0
		print $1, "db.1h.compressed_bytes", 0; print $1, "db.1h.uncompressed_bytes", 0
	}' "$work/run/samples.tsv" >"$work/aggregates/samples.tsv"
out=$(report "$work/aggregates") || fail "aggregates: $out"
# 60 MiB × (16 + 152 × 0.05) after 7 days, × (16 + 704 × 0.05) after 30; 6 MiB × 168 h and
# 6 MiB × (192 + 8568 × 0.05).
has "$out" "| metrics_1m (30 d) | 60 MiB per hour, compressed to 5.0 %: 16 h + 704 h × 5.0 % | 1.38 | 3.00 |"
has "$out" "| metrics_1h (365 d) | 6 MiB per hour, compressed to 5.0 % (ratio of 1m, none compressed here yet): 192 h + 8568 h × 5.0 % | 0.98 | 3.64 |"
# Without a compressed chunk anywhere: linear, an upper bound.
grep -v -e '	db\.1m\.' "$work/aggregates/samples.tsv" >"$work/aggregates/plain.tsv"
mkdir "$work/plain" && mv "$work/aggregates/plain.tsv" "$work/plain/samples.tsv"
out=$(report "$work/plain") || fail "plain: $out"
has "$out" "| metrics_1h (365 d) | linear up to its retention (nothing compressed yet: upper bound) |"

# A real server without docker access: no resources, limits not evaluated, nothing fails.
mkdir "$work/remote"
grep -v -e '	cpu\.' -e '	mem\.' -e '	disk\.' "$work/run/samples.tsv" >"$work/remote/samples.tsv"
out=$(report "$work/remote") || fail "missing parts failed: $out"
has "$out" "| n/a (no docker) | | | | | |"
has "$out" '| `cpu.total.p95<=200` | n/a | – n/a |'

if out=$(report "$work/empty" 2>&1); then fail "a report without samples: $out"; fi
if sh "$dir/load-report.sh" report "$work/run" --limit 'q.fleet.p95' >/dev/null 2>&1; then fail "a limit without bound"; fi
if sh "$dir/load-report.sh" sample "$work/x" 2 >/dev/null 2>&1; then fail "a duration without unit"; fi

# Sampling against fakes: /metrics as a file, SQL from a command, the web API from a fake curl
# that wants a login first.
cat >"$work/metrics.txt" <<-'EOF'
	# HELP sermon_server_ingest_samples_total Stored samples.
	# TYPE sermon_server_ingest_samples_total counter
	sermon_server_ingest_samples_total 1200
	sermon_server_ingest_late_samples_total 1
	sermon_server_ingest_dropped_log_entries_total{reason="rate"} 3
	sermon_server_ingest_dropped_log_entries_total{reason="age"} 2
	sermon_server_ingest_lag_seconds 0.25
	sermon_server_build_info{version="1.0.0",go_version="go1.26"} 1
EOF
cat >"$work/loki.txt" <<-'END'
	# TYPE go_memstats_heap_inuse_bytes gauge
	go_memstats_heap_inuse_bytes 3.145728e+08
	go_memstats_next_gc_bytes 4.194304e+08
	process_resident_memory_bytes 6.291456e+08
	go_gc_duration_seconds_count 120
	go_gc_duration_seconds{quantile="0.5"} 0.001
	loki_ingester_memory_streams{tenant="a"} 30
	loki_ingester_memory_streams{tenant="b"} 20
	loki_request_duration_seconds_count{route="push"} 5
END
printf 'db.total\t4096\ndb.raw\t1024\n' >"$work/sql.out"
mkdir "$work/bin"
cat >"$work/bin/curl" <<-'EOF'
	#!/bin/sh
	# Fake curl: file:// URLs, the session in the cookie jar, answers by method.
	out=/dev/stdout jar= body= url=
	while [ $# -gt 0 ]; do
		case $1 in
		-o) out=$2; shift ;;
		-c) jar=$2; shift ;;
		--data-binary) body=${2#@}; shift ;;
		-w | -K | -b | -H | -X | --max-time) shift ;;
		-*) ;;
		*) url=$1 ;;
		esac
		shift
	done
	case $url in file://*) cat "${url#file://}"; exit ;; esac
	echo "$url" >>"$FAKE_LOG"
	[ -z "$body" ] || { cat "$body"; echo; } >>"$FAKE_LOG.bodies"
	case $url in
	*.AuthService/Login)
		cp "$body" "$FAKE_LOG.login"
		echo session >"$jar"
		echo '{}' >"$out"
		printf '200 0.100'
		;;
	*)
		if ! grep -q session "$jar" 2>/dev/null; then echo '{"code":"unauthenticated"}' >"$out"; printf '401 0.001'; exit; fi
		case $url in
		*.FleetService/*)
			# The session ends once: the sampler logs in again.
			if [ -f "$FAKE_LOG.expire" ]; then rm "$FAKE_LOG.expire"; : >"$jar"; echo '{}' >"$out"; printf '401 0.001'; exit; fi
			printf '{"total":2,"online":2,"nodes":[{"nodeId":"0b0e8b6c-0000-4000-8000-000000000001"},{"nodeId":"0b0e8b6c-0000-4000-8000-000000000002"}]}' >"$out"; printf '200 0.012' ;;
		*.LogService/QueryLogs) echo '{"code":"unavailable"}' >"$out"; printf '503 0.002' ;;
		*) echo '{}' >"$out"; printf '200 0.0405' ;;
		esac
		;;
	esac
EOF
chmod 0755 "$work/bin/curl"
printf 'correct "horse"\n' >"$work/password"
: >"$work/curl.log.expire"
PATH="$work/bin:$PATH" FAKE_LOG="$work/curl.log" LOAD_DOCKER=0 LOAD_METRICS_URL="file://$work/metrics.txt" \
	LOAD_LOKI_METRICS_URL="file://$work/loki.txt" \
	LOAD_PSQL="cat '$work/sql.out'" LOAD_URL=https://sermon.example.com LOAD_EMAIL=alice@example.org \
	LOAD_PASSWORD_FILE="$work/password" \
	sh "$dir/load-report.sh" sample "$work/sampled" 1s 1 2>"$work/sample.err" || fail "sampling: $(cat "$work/sample.err")"
samples=$(cat "$work/sampled/samples.tsv")
has "$samples" "	m.sermon_server_ingest_samples_total	1200"
has "$samples" "	m.sermon_server_ingest_dropped_log_entries_total	5"
has "$samples" "	ingest.dropped	6"
has "$samples" "	m.sermon_server_ingest_lag_seconds	0.25"
has "$samples" "	db.total	4096"
has "$samples" "	q.fleet	12.0"
has "$samples" "	fleet.online	2"
has "$samples" "	q.chart_1h	40.5"
has "$samples" "	q.log_histogram_14d	40.5"
has "$samples" "	loki.go_memstats_heap_inuse_bytes	314572800"
has "$samples" "	loki.loki_ingester_memory_streams	50"
has "$samples" "	loki.go_gc_duration_seconds_count	120"
case $samples in *"loki_request_duration"* | *"loki.go_gc_duration_seconds	"*) fail "Loki: a metric beyond the memory sampled" ;; esac
out=$(report "$work/sampled" 2>&1) || true
has "$out" "| Go heap in use | 300 | 300 |"
has "$out" "| resident memory (RSS) | 600 | 600 |"
has "$samples" "	q.errors	1"
has "$(cat "$work/sample.err")" "logs answered 503"
has "$(cat "$work/curl.log.login")" '"email":"alice@example.org","password":"correct \"horse\""'
bodies=$(cat "$work/curl.log.bodies")
has "$bodies" '{"selector":{"metric":"sermon_cpu_utilization_ratio","nodeIds":["0b0e8b6c-0000-4000-8000-000000000001","0b0e8b6c-0000-4000-8000-000000000002"],"labels":{"mode":"idle"}},"from":"'
has "$bodies" '{"nodeId":"0b0e8b6c-0000-4000-8000-000000000001"}'
has "$bodies" '{"filter":{"from":"'
case $samples in *"	q.logs	"*) fail "a failed read counted as a latency" ;; esac
case $samples in *"	cpu."*) fail "docker sampled with LOAD_DOCKER=0" ;; esac
[ "$(grep -c 'AuthService/Login' "$work/curl.log")" = 2 ] || fail "logins: $(cat "$work/curl.log")"
# Docker on the server host: docker stats of the services and of a named container, the volumes
# measured with du in the image of PostgreSQL (fake docker).
cat >"$work/bin/docker" <<-'EOF'
	#!/bin/sh
	case "$1 $2" in
	"compose ps") echo "${4}00000000000000000000000000000000000000000000000000000000" | cut -c 1-64 ;;
	"stats --no-stream")
		shift 4
		for target in "$@"; do
			short=$(echo "$target" | cut -c 1-12)
			case $target in
			server*) printf '%s\tsermon-server-1\t12.50%%\t1.5GiB / 7.7GiB\n' "$short" ;;
			postgres*) printf '%s\tsermon-postgres-1\t30.25%%\t512MiB / 7.7GiB\n' "$short" ;;
			loki*) printf '%s\tsermon-loki-1\t5.00%%\t800KiB / 1GiB\n' "$short" ;;
			caddy*) printf '%s\tsermon-caddy-1\t0.25%%\t1048576B / 7.7GiB\n' "$short" ;;
			sermon-load-simulator) printf 'ffffffffffff\tsermon-load-simulator\t3.00%%\t25MiB / 7.7GiB\n' ;;
			esac
		done
		;;
	"inspect -f")
		case $3 in *Mounts*) echo "vol-$(echo "$4" | cut -c 1-4)" ;; *) echo sha256:image ;; esac
		;;
	"run --rm") case "$*" in *vol-post*) printf '2048\t/v\n' ;; *vol-loki*) printf '512\t/v\n' ;; esac ;;
	"info --format") echo "4 CPUs, 8000000000 bytes, Linux" ;;
	*) exit 1 ;;
	esac
EOF
chmod 0755 "$work/bin/docker"
PATH="$work/bin:$PATH" LOAD_PSQL="cat '$work/sql.out'" LOAD_CONTAINERS="simulator=sermon-load-simulator" \
	sh "$dir/load-report.sh" sample "$work/docker" 1s 5 2>"$work/docker.err" || fail "docker: $(cat "$work/docker.err")"
samples=$(cut -f 2,3 "$work/docker/samples.tsv")
for line in "cpu.server	12.5" "mem.server	1536.0" "cpu.postgres	30.25" "mem.postgres	512.0" "mem.loki	0.8" "mem.caddy	1.0" \
	"cpu.simulator	3" "mem.simulator	25.0" "cpu.total	48.00" "mem.total	2049.8" "disk.postgres	2097152" "disk.loki	524288"; do
	has "$samples" "$line"
done
has "$(cat "$work/docker/info.tsv")" "docker	4 CPUs, 8000000000 bytes, Linux"

out=$(report "$work/sampled") && fail "a failed read passed the report"
echo "$out" | grep -q '^| `q.errors.sum<=0` | [12].00 | ❌ exceeded |$' || fail "failed reads: $out"
echo "load-report: ok"
