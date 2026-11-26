#!/bin/sh
# Integration tests of the database modules against real servers in containers (M5-13, M5-14):
# PostgreSQL 13 to 17, each with a monitoring user (pg_monitor) and one without rights, and some
# load (pgbench); the newest also with a streaming standby. MySQL 8.4 and 9, MariaDB 10.11, 11.4
# and 11.8 with a monitoring user, one without rights and load; MySQL 8.4 and MariaDB 11.4 each
# with a replica. The tests are the ignored ones of agent/crates/sermon-modules/tests/databases.rs.
#
#   scripts/test-db-modules.sh            run the tests
#   FIXTURES=1 scripts/test-db-modules.sh also write the servers' answers into agent/fixtures/modules/
#   KEEP=1 scripts/test-db-modules.sh     leave the servers running
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
postgres=${POSTGRES_VERSIONS:-13 14 15 16 17}
mysql=${MYSQL_VERSIONS:-mysql:8.4 mysql:9 mariadb:10.11 mariadb:11.4 mariadb:11.8}
# The versions that get a replica.
replicated=${MYSQL_REPLICATED-mysql:8.4 mariadb:11.4}
prefix=sermon-dbtest
# Random per run: nothing to leak, nothing to guess.
password=$(od -An -N12 -tx1 /dev/urandom | tr -d ' \n')
[ -z "${KEEP:-}" ] || echo "password: $password"
admin=$(od -An -N12 -tx1 /dev/urandom | tr -d ' \n')
# A CA and a certificate for 127.0.0.1 of the MySQL and MariaDB servers: the module verifies them
# with ca_file (the full login of caching_sha2_password needs verified TLS, M5-33).
certs=$(mktemp -d)

cleanup() {
	docker ps -aq --filter "name=^$prefix-" | xargs -r docker rm -f >/dev/null 2>&1 || true
	docker network rm "$prefix-net" >/dev/null 2>&1 || true
}
# KEEP=1 leaves the servers running (debugging); the next run removes them.
[ -n "${KEEP:-}" ] || trap 'cleanup; rm -rf "$certs"' EXIT
cleanup

openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj /CN=sermon-dbtest-ca \
	-addext basicConstraints=critical,CA:TRUE -addext keyUsage=critical,keyCertSign \
	-keyout "$certs/ca-key.pem" -out "$certs/ca.pem" 2>/dev/null
openssl req -newkey rsa:2048 -nodes -subj /CN=localhost -keyout "$certs/server-key.pem" \
	-out "$certs/server.csr" 2>/dev/null
printf 'subjectAltName=IP:127.0.0.1,DNS:localhost\nbasicConstraints=CA:FALSE\n' >"$certs/ext"
openssl x509 -req -in "$certs/server.csr" -CA "$certs/ca.pem" -CAkey "$certs/ca-key.pem" \
	-CAcreateserial -days 2 -extfile "$certs/ext" -out "$certs/server.pem" 2>/dev/null
# The servers run as their own user; the key is a throwaway of this run.
chmod 0644 "$certs/server-key.pem"
ssl="--ssl-ca=/certs/ca.pem --ssl-cert=/certs/server.pem --ssl-key=/certs/server-key.pem"

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

# MySQL and MariaDB: the name of a version below the fixtures (mysql-8.4), its client, its SQL.
my_name() { printf '%s' "$1" | tr ':' '-'; }
my_client() { case $1 in mariadb:*) echo mariadb ;; *) echo mysql ;; esac; }
my_sql() { docker exec "$1" "$(my_client "$2")" -uroot -p"$admin" -N -e "$3" 2>/dev/null; }
my_port() { docker port "$1" 3306/tcp | head -n 1 | sed 's/.*://'; }
my_wait() {
	i=0
	until my_sql "$1" "$2" 'SELECT 1' >/dev/null; do
		i=$((i + 1))
		[ $i -lt 90 ] || { echo "✗ $1 did not start" >&2; docker logs "$1" 2>&1 | tail -20 >&2; exit 1; }
		sleep 1
	done
}
for image in $mysql; do
	name="$prefix-$(my_name "$image")"
	args="--server-id=1 --log-bin=bin $ssl"
	case $image in mysql:*) args="$args --gtid-mode=ON --enforce-gtid-consistency=ON" ;; esac
	# shellcheck disable=SC2086 # the server's arguments are words
	docker run -d -q --name "$name" --network "$prefix-net" -e MYSQL_ROOT_PASSWORD="$admin" \
		-e MARIADB_ROOT_PASSWORD="$admin" -v "$certs:/certs:ro" -p 127.0.0.1::3306 "$image" $args >/dev/null
done
mytargets='' myreplicas=''
for image in $mysql; do
	name="$prefix-$(my_name "$image")"
	my_wait "$name" "$image"
	case $image in
	mariadb:*) monitor='PROCESS, REPLICA MONITOR' ;;
	*) monitor='PROCESS, REPLICATION CLIENT' ;;
	esac
	my_sql "$name" "$image" "CREATE USER 'sermon'@'%' IDENTIFIED BY '$password'; GRANT $monitor ON *.* TO 'sermon'@'%';
		CREATE USER 'plain'@'%' IDENTIFIED BY '$password'; CREATE USER 'fresh'@'%' IDENTIFIED BY '$password';
		GRANT $monitor ON *.* TO 'fresh'@'%'; CREATE USER 'repl'@'%' IDENTIFIED BY '$password';
		GRANT REPLICATION SLAVE ON *.* TO 'repl'@'%'; SET GLOBAL long_query_time = 0.2;
		CREATE DATABASE app; CREATE TABLE app.t (id INT AUTO_INCREMENT PRIMARY KEY, v TEXT);"
	# Load: inserts, reads and slow statements while the tests read.
	docker exec -d "$name" sh -c "while :; do $(my_client "$image") -uroot -p'$admin' -e \"INSERT INTO app.t (v) VALUES (REPEAT('x', 1000)); SELECT COUNT(*) FROM app.t; SELECT SLEEP(0.3);\" >/dev/null 2>&1; done"
	mytargets="$mytargets${mytargets:+,}$(my_name "$image")@127.0.0.1:$(my_port "$name")"
done
for image in $replicated; do
	source="$prefix-$(my_name "$image")"
	name="$source-replica"
	case $image in
	mysql:*)
		args="--server-id=2 --gtid-mode=ON --enforce-gtid-consistency=ON --read-only=ON $ssl"
		change="CHANGE REPLICATION SOURCE TO SOURCE_HOST='$source', SOURCE_USER='repl', SOURCE_PASSWORD='$password', SOURCE_AUTO_POSITION=1, GET_SOURCE_PUBLIC_KEY=1; START REPLICA;"
		;;
	*)
		args="--server-id=2 --read-only=ON $ssl"
		change="CHANGE MASTER TO MASTER_HOST='$source', MASTER_USER='repl', MASTER_PASSWORD='$password', MASTER_USE_GTID=slave_pos; START SLAVE;"
		;;
	esac
	# shellcheck disable=SC2086
	docker run -d -q --name "$name" --network "$prefix-net" -e MYSQL_ROOT_PASSWORD="$admin" \
		-e MARIADB_ROOT_PASSWORD="$admin" -v "$certs:/certs:ro" -p 127.0.0.1::3306 "$image" $args >/dev/null
	my_wait "$name" "$image"
	my_sql "$name" "$image" "$change"
	i=0
	until my_sql "$name" "$image" "SELECT 1 FROM mysql.user WHERE user = 'sermon'" | grep -q 1; do
		i=$((i + 1))
		[ $i -lt 60 ] || { echo "✗ $name does not replicate" >&2; my_sql "$name" "$image" 'SHOW SLAVE STATUS\G' >&2; exit 1; }
		sleep 1
	done
	myreplicas="$myreplicas${myreplicas:+,}$(my_name "$image")@127.0.0.1:$(my_port "$name")"
done

out=''
[ -z "${FIXTURES:-}" ] || out="$root/agent/fixtures/modules"
cd "$root/agent"
SERMON_TEST_POSTGRES=$targets SERMON_TEST_POSTGRES_STANDBY=$standby SERMON_TEST_MYSQL=$mytargets \
	SERMON_TEST_MYSQL_REPLICAS=$myreplicas SERMON_TEST_DB_PASSWORD=$password SERMON_FIXTURES_OUT=$out \
	SERMON_TEST_DB_CA=$certs/ca.pem \
	cargo test -q --locked -p sermon-modules --test databases -- --ignored --test-threads 1
if [ -n "$out" ]; then
	# The containers' addresses (Docker's networks) become documentation addresses.
	find "$out/postgres" "$out/mysql" -name '*.json' | while IFS= read -r f; do
		sed -E 's/"172\.[0-9]+\.[0-9]+\.([0-9]+)"/"192.0.2.\1"/g' "$f" >"$f.tmp" && mv "$f.tmp" "$f"
	done
	echo "✓ fixtures written below $out"
fi
