#!/bin/sh
# Restore in CI (V1-07): the backup of the demo environment restored into a new one, with the
# checks of V1-05 against the real production compose. After scripts/e2e-demo.sh (left running)
# the one-shot service "backup" writes the backup of the demo's instance; the server side is then
# thrown away – database, server state with the master keys, Loki – and built anew: an empty
# database, "restore" with the saved master keys and the secret backup key, "docker compose up".
# The agent hosts keep running and must come back with their certificates.
#
# Checked: the backup file holds nothing in plain text; a wrong backup key and a second restore
# are refused (exit 3); afterwards the same rows in every table of the backup, no sessions, FORCE
# ROW LEVEL SECURITY everywhere; the admin's password is accepted; every node that was live
# reports again; the modules with credentials (PostgreSQL, MariaDB on web-01) work again after
# its agent restarted (credentials only from the restored server); a kiosk
# token made before the backup opens its dashboard.
#
#   scripts/e2e-restore.sh                run the demo, then the restore, and tear down
#   SKIP_DEMO=1 scripts/e2e-restore.sh    against an environment left running (KEEP=1 e2e-demo.sh)
#   KEEP=1 …                              leave the restored environment running
#   FULL=1, SKIP_BUILD=1                  as for scripts/e2e-demo.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
e2e="$root/deploy/e2e"
build="$e2e/.build"
secrets="$build/secrets"
port=${SERMON_E2E_PORT:-18443}
export COMPOSE_FILE="$root/deploy/compose/compose.yaml:$root/deploy/compose/source.yaml:$e2e/compose.yaml"
export COMPOSE_ENV_FILES="$e2e/e2e.env"
compose() { docker compose "$@"; }
sql() { compose exec -T postgres psql -v ON_ERROR_STOP=1 -qtA -U postgres -d sermon "$@"; }
fail() {
	echo "e2e-restore: $*" >&2
	exit 1
}

if [ "${SKIP_DEMO:-}" != 1 ]; then
	KEEP=1 "$root/scripts/e2e-demo.sh"
fi

cleanup() {
	status=$?
	if [ "$status" -ne 0 ]; then compose logs --no-color server >"$build/server-restore.log" 2>&1 || true; fi
	if [ "${KEEP:-}" = 1 ]; then
		echo "▸ environment left running: https://localhost:$port (stop: COMPOSE_FILE=$COMPOSE_FILE docker compose down -v)"
	else
		compose down -v --remove-orphans >/dev/null 2>&1 || true
	fi
	rm -f "$secrets/backup_identity" "$secrets/master_keys"
	exit "$status"
}
trap cleanup EXIT INT TERM

# The admin of the demo (web/tests/demo/stack.ts); a fixture of a throwaway installation.
admin_password="correct horse battery staple" # betterleaks:allow
login() { printf '{"email":"ann@example.org","password":"%s"}' "$1"; }
SERMON_RELEASE_KEY=$(cat "$build/release.pub")
export SERMON_RELEASE_KEY
project=$(compose config --format json | python3 -c 'import json, sys; print(json.load(sys.stdin)["name"])')

# The secrets of the backup (an older .build lacks them) and, as for an installation from before
# V1-05, the role sermon_backup if its volume was initialized without it (docs/admin/backup.md).
"$root/deploy/compose/secrets.sh" "$secrets" >/dev/null
compose up -d postgres >/dev/null 2>&1
# shellcheck disable=SC2016 # expanded in the container, where the secret is
compose exec -T postgres sh -c 'psql -v ON_ERROR_STOP=1 -qtA -U postgres -d sermon -v pw="$(cat /run/secrets/postgres_backup_password)"' <<'SQL'
SELECT format('CREATE ROLE sermon_backup LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION BYPASSRLS IN ROLE pg_read_all_data PASSWORD %L', :'pw')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'sermon_backup') \gexec
SQL

echo "▸ backup keys: sermon-server backup-keygen"
compose run --rm --no-deps -T backup backup-keygen >"$build/backup-identity" 2>/dev/null
sed -n 's/^# public key: //p' "$build/backup-identity" >"$secrets/backup_recipients"
[ -s "$secrets/backup_recipients" ] || fail "backup-keygen wrote no public key"

# A kiosk token of this run on a shared dashboard of the demo, stored as the UI does (its hash).
kiosk="smk_$(openssl rand -base64 32 | tr '+/' '-_' | tr -d '=\n')"
[ "$(sql -c "INSERT INTO kiosk_tokens (org_id, id, dashboard_id, name, token_hash, created_by, created_at, expires_at)
	SELECT org_id, gen_random_uuid(), id, 'restore check', sha256(convert_to('$kiosk', 'UTF8')), owner_id, now(), now() + interval '1 day'
	FROM dashboards WHERE shared ORDER BY created_at LIMIT 1 RETURNING 1")" = 1 ] || fail "no shared dashboard for a kiosk token"

# What has to be there again: the rows of the backed-up tables, the nodes that report, the
# modules with credentials that work.
tables="organizations users memberships user_credentials user_totp webauthn_credentials superadmins instance_setup
	node_groups nodes agent_certificates pki_authorities enrollment_tokens role_bindings invitations audit_log
	alert_instances alert_history alert_rule_overrides notification_channels notification_policies
	module_settings module_secrets dashboards kiosk_tokens security_events log_settings update_policies series"
counts() {
	for t in $tables; do printf '%s %s\n' "$t" "$(sql -c "SELECT count(*) FROM $t")"; done
}
counts >"$build/restore-before.txt"
instance_audit=$(sql -c "SELECT count(*) FROM instance_audit_log")
sql -c "SELECT id FROM nodes WHERE deactivated_at IS NULL AND last_seen_at > now() - interval '3 minutes' ORDER BY id" >"$build/restore-nodes.txt"
sql -c "SELECT DISTINCT h.node_id || ' ' || h.module_id FROM module_health h JOIN nodes n ON n.id = h.node_id
	JOIN module_secrets s ON s.org_id = h.org_id AND s.group_id = n.group_id AND s.module_id = h.module_id
	WHERE h.state = 1 ORDER BY 1" >"$build/restore-modules.txt"
[ -s "$build/restore-nodes.txt" ] || fail "no live nodes before the backup"
[ -s "$build/restore-modules.txt" ] || fail "no module with a credential works before the backup"
echo "  $(wc -l <"$build/restore-nodes.txt" | tr -d ' ') live nodes, $(wc -l <"$build/restore-modules.txt" | tr -d ' ') modules with credentials"

echo "▸ backup: docker compose run --rm -T backup"
compose run --rm -T backup >"$build/sermon.backup"
[ -s "$build/sermon.backup" ] || fail "empty backup"
echo "  $(wc -c <"$build/sermon.backup" | tr -d ' ') bytes"
# Encrypted to the backup key (what is inside is checked by the integration test of V1-05).
for plain in "ann@example.org" "$admin_password" "$kiosk" "BEGIN CERTIFICATE"; do
	if grep -qF -- "$plain" "$build/sermon.backup"; then fail "plain text in the backup: $plain"; fi
done
head -c 22 "$build/sermon.backup" | grep -q '^age-encryption.org/v1' || fail "the backup is no age file"

# The master keys are not in the backup: saved apart, as docs/admin/backup.md says.
compose cp server:/var/lib/sermon/master_keys "$secrets/master_keys" >/dev/null
chmod 0644 "$secrets/master_keys"

echo "▸ a new environment: database, server state and Loki thrown away"
compose stop server >/dev/null 2>&1
compose rm -fsv server migrate postgres loki >/dev/null 2>&1
for volume in postgres-data server-state loki-data; do docker volume rm "${project}_$volume" >/dev/null; done
compose up -d --wait postgres >/dev/null 2>&1

echo "▸ a wrong backup key is refused"
compose run --rm --no-deps -T backup backup-keygen >"$secrets/backup_identity" 2>/dev/null
chmod 0644 "$secrets/backup_identity"
status=0
compose run --rm -T restore <"$build/sermon.backup" >"$build/restore-wrong-key.log" 2>&1 || status=$?
[ "$status" = 3 ] || fail "restore with another backup key: exit $status, want 3"

echo "▸ restore: docker compose run --rm -T restore"
cp "$build/backup-identity" "$secrets/backup_identity"
chmod 0644 "$secrets/backup_identity"
restored_at=$(sql -c "SELECT now()")
compose run --rm -T restore <"$build/sermon.backup" >"$build/restore.log" 2>&1 || {
	cat "$build/restore.log" >&2
	fail "restore failed"
}
status=0
compose run --rm -T restore <"$build/sermon.backup" >"$build/restore-twice.log" 2>&1 || status=$?
[ "$status" = 3 ] || fail "a second restore: exit $status, want 3"
grep -q "not empty" "$build/restore-twice.log" || fail "a second restore without the reason"
rm -f "$secrets/backup_identity" "$secrets/master_keys"

echo "▸ docker compose up"
compose up -d --quiet-pull >/dev/null 2>&1
for _ in $(seq 1 90); do
	curl -fsSk "https://localhost:$port/readyz" >/dev/null 2>&1 && break
	sleep 1
done
curl -fsSk "https://localhost:$port/readyz" >/dev/null || fail "the restored server is not ready"

echo "▸ checks"
counts >"$build/restore-after.txt"
diff "$build/restore-before.txt" "$build/restore-after.txt" || fail "other rows after the restore"
[ "$(sql -c "SELECT count(*) FROM sessions")" = 0 ] || fail "sessions were restored"
# The instance's audit log has the restore in addition, at the end of its chain.
[ "$(sql -c "SELECT count(*) - 1 FROM instance_audit_log")" = "$instance_audit" ] || fail "instance audit log: other rows after the restore"
[ "$(sql -c "SELECT action FROM instance_audit_log ORDER BY seq DESC LIMIT 1")" = instance.restored ] || fail "the restore is not audited"
unforced=$(sql -c "SELECT string_agg(relname, ',') FROM pg_class WHERE relnamespace = 'public'::regnamespace AND relrowsecurity AND NOT relforcerowsecurity")
[ -z "$unforced" ] || fail "without FORCE ROW LEVEL SECURITY: $unforced"

api() { # METHOD BODY [HEADER]
	curl -sk -o "$build/api.json" -w '%{http_code}' -X POST "https://localhost:$port/sermon.web.v1.$1" \
		-H "Origin: https://localhost:$port" -H 'Content-Type: application/json' ${3:+-H "$3"} -d "$2"
}
[ "$(api AuthService/Login "$(login "$admin_password")")" = 200 ] ||
	fail "the admin's password is refused: $(cat "$build/api.json")"
[ "$(api AuthService/Login "$(login "wrong $admin_password")")" = 401 ] ||
	fail "a wrong password is accepted"
[ "$(api KioskService/GetKioskDashboard '{}' "Authorization: Kiosk $kiosk")" = 200 ] ||
	fail "the kiosk token is refused: $(cat "$build/api.json")"

# The agents reconnect with their certificates (backoff). web-01's agent starts anew: it keeps the
# modules' credentials in memory only, so they must come from the restored server and open with
# the master keys; its modules report their health at the start.
compose exec -T agent-debian-12 systemctl restart sermon-agent
deadline=$(($(date +%s) + 300))
while :; do
	nodes=$(sql -c "SELECT id FROM nodes WHERE last_seen_at > '$restored_at' ORDER BY id")
	modules=$(sql -c "SELECT node_id || ' ' || module_id FROM module_health WHERE state = 1 AND received_at > '$restored_at' ORDER BY 1")
	missing_nodes=$(printf '%s\n' "$nodes" | comm -23 "$build/restore-nodes.txt" -)
	missing_modules=$(printf '%s\n' "$modules" | comm -23 "$build/restore-modules.txt" -)
	[ -z "$missing_nodes" ] && [ -z "$missing_modules" ] && break
	[ "$(date +%s)" -lt "$deadline" ] || fail "after the restore: nodes not back: ${missing_nodes:-none}; modules not working: ${missing_modules:-none}"
	sleep 5
done
if compose logs --no-color server | grep -q "a credential of a module does not open"; then fail "a module credential does not open"; fi
echo "✓ restored: the same rows, the admin signs in, $(wc -l <"$build/restore-nodes.txt" | tr -d ' ') nodes back, the modules' credentials and the kiosk token work"
