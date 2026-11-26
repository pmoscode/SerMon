#!/bin/sh
# Captures `INFO` of Redis and Valkey (M5-15) from the official images: a primary with a replica
# attached, after some writes, reads, misses, expiries and an eviction, and the replica's own
# INFO. The addresses of the Docker network become documentation addresses (192.0.2.0/24).
# Usage: agent/fixtures/modules/redis/capture.sh [image:tag …]  (Docker)
set -eu
cd "$(dirname "$0")"
images=${*:-redis:6.2 redis:7.2 redis:7.4 redis:8.0 valkey/valkey:8.1}

for image in $images; do
	name=$(printf '%s' "$image" | sed 's|.*/||; s|:|-|')
	net="capture-redis-$name"
	docker rm -f "$net-primary" "$net-replica" >/dev/null 2>&1 || true
	docker network rm "$net" >/dev/null 2>&1 || true
	docker network create "$net" >/dev/null
	docker run -d --name "$net-primary" --network "$net" "$image" \
		--maxmemory 64mb --maxmemory-policy allkeys-lru >/dev/null
	docker run -d --name "$net-replica" --network "$net" "$image" --replicaof "$net-primary" 6379 >/dev/null
	sleep 3
	cli=redis-cli
	case $image in valkey*) cli=valkey-cli ;; esac
	docker exec "$net-primary" sh -c "
		for i in \$(seq 1 50); do $cli SET key:\$i value-\$i >/dev/null; done
		for i in \$(seq 1 30); do $cli GET key:\$i >/dev/null; $cli GET missing:\$i >/dev/null; done
		$cli SET short x PX 100 >/dev/null; $cli -n 1 SET other y >/dev/null; sleep 1; $cli GET short >/dev/null"
	sleep 1
	mkdir -p "$name"
	for role in primary replica; do
		docker exec "$net-$role" "$cli" INFO >"$name/info-$role.txt"
		# The Docker network's addresses are no fixture data.
		sed -i.bak -E 's/(172|10)\.[0-9]+\.[0-9]+\.([0-9]+)/192.0.2.\2/g; s/capture-redis-[a-z0-9.-]+-primary/primary.example/g' "$name/info-$role.txt"
		rm "$name/info-$role.txt.bak"
	done
	docker rm -f "$net-primary" "$net-replica" >/dev/null
	docker network rm "$net" >/dev/null
	echo "✓ $image → agent/fixtures/modules/redis/$name/"
done
