#!/bin/sh
# Integration tests of the database modules against real servers in containers (M5-13, M5-14):
# PostgreSQL 13 to 17, each with a monitoring user (pg_monitor) and one without rights, and some
# load (pgbench); the newest also with a streaming standby. The tests are the ignored ones of agent/crates/sermon-modules/tests/databases.rs.
#
#   scripts/test-db-modules.sh            run the tests
#   FIXTURES=1 scripts/test-db-modules.sh also write the servers' answers into agent/fixtures/modules/
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
postgres=${POSTGRES_VERSIONS:-13 14 15 16 17}
prefix=sermon-dbtest
# Random per run: nothing to leak, nothing to guess.
password=$(od -An -N12 -tx1 /dev/urandom | tr -d ' \n')
admin=$(od -An -N12 -tx1 /dev/urandom | tr -d ' \n')

cleanup() {
	docker ps -aq --filter "name=^$prefix-" | xargs -r docker rm -f >/dev/null 2>&1 || true
	docker network rm "$prefix-net" >/dev/null 2>&1 || true
}
trap cleanup EXIT
cleanup

docker network create "$prefix-net" >/dev/null
for v in $postgres; do
	docker run -d -q --name "$prefix-pg$v" --network "$prefix-net" -e POSTGRES_PASSWORD="$admin" \
		-p 127.0.0.1::5432 "postgres:$v-alpine" >/dev/null
done
# shellcheck disable=SC2086 # the versions are words
newest=$(printf '%s\n' $postgres | sort -n | tail -n 1)

targets=''
for v in $postgres; do
	name="$prefix-pg$v"
	i=0
	# Over TCP: the image's server during initdb listens on its socket only, and stops again.
	until docker exec "$name" pg_isready -q -h 127.0.0.1 -U postgres 2>/dev/null; do
		i=$((i + 1))
		[ $i -lt 60 ] || { echo "✗ $name did not start" >&2; exit 1; }
		sleep 1
	done
	docker exec "$name" psql -q -U postgres -v ON_ERROR_STOP=1 \
		-c "CREATE ROLE sermon LOGIN PASSWORD '$password' IN ROLE pg_monitor" \
		-c "CREATE ROLE plain LOGIN PASSWORD '$password'" \
		-c "CREATE DATABASE app" >/dev/null
	docker exec "$name" pgbench -q -i -s 2 -U postgres app >/dev/null 2>&1
	# Load while the tests read: transactions, cache hits and locks.
	docker exec -d "$name" pgbench -c 2 -T 60 -U postgres app
	port=$(docker port "$name" 5432/tcp | head -n 1 | sed 's/.*://')
	targets="$targets${targets:+,}$v@127.0.0.1:$port"
done

# A standby of the newest, streaming from it.
primary="$prefix-pg$newest"
docker exec "$primary" psql -q -U postgres -v ON_ERROR_STOP=1 \
	-c "CREATE ROLE repl REPLICATION LOGIN PASSWORD '$password'" >/dev/null
docker exec "$primary" sh -c 'echo "host replication repl all scram-sha-256" >>"$PGDATA/pg_hba.conf"'
docker exec "$primary" psql -q -U postgres -c 'SELECT pg_reload_conf()' >/dev/null
docker run -d -q --name "$prefix-standby" --network "$prefix-net" --user postgres -e PGPASSWORD="$password" \
	-p 127.0.0.1::5432 --entrypoint sh "postgres:$newest-alpine" \
	-c "pg_basebackup -h $primary -U repl -D /var/lib/postgresql/data -R -X stream -c fast && chmod 0700 /var/lib/postgresql/data && exec postgres" >/dev/null
i=0
until docker exec "$prefix-standby" psql -q -U postgres -tAc 'SELECT pg_is_in_recovery()' 2>/dev/null | grep -q t; do
	i=$((i + 1))
	[ $i -lt 60 ] || { echo "✗ the standby did not start" >&2; docker logs "$prefix-standby" >&2; exit 1; }
	sleep 1
done
standby="$newest@127.0.0.1:$(docker port "$prefix-standby" 5432/tcp | head -n 1 | sed 's/.*://')"
# Commits to replay while the tests read (without any the standby has no replay time).
docker exec -d "$primary" pgbench -c 1 -T 60 -U postgres app

out=''
[ -z "${FIXTURES:-}" ] || out="$root/agent/fixtures/modules"
cd "$root/agent"
SERMON_TEST_POSTGRES=$targets SERMON_TEST_POSTGRES_STANDBY=$standby SERMON_TEST_DB_PASSWORD=$password SERMON_FIXTURES_OUT=$out \
	cargo test -q --locked -p sermon-modules --test databases -- --ignored --test-threads 1
if [ -n "$out" ]; then
	# The containers' addresses (Docker's networks) become documentation addresses.
	find "$out/postgres" -name '*.json' | while IFS= read -r f; do
		sed -E 's/"172\.[0-9]+\.[0-9]+\.([0-9]+)"/"192.0.2.\1"/g' "$f" >"$f.tmp" && mv "$f.tmp" "$f"
	done
	echo "✓ fixtures written below $out"
fi
