-- Kiosk tokens (M5-26, ADR 0040). All but KioskTokenByHash run inside InTenant.

-- name: ListKioskTokens :many
SELECT t.id, t.dashboard_id, t.name, coalesce(u.display_name, '')::text AS created_by_name, t.created_by,
       t.created_at, t.expires_at, t.last_used_at, t.revoked_at
FROM kiosk_tokens t LEFT JOIN users u ON u.id = t.created_by
WHERE t.dashboard_id = sqlc.arg(dashboard_id)
ORDER BY t.created_at DESC, t.id;

-- name: LockSharedDashboard :one
-- The dashboard of a new token, locked against a concurrent unsharing.
SELECT shared FROM dashboards WHERE id = sqlc.arg(id) FOR UPDATE;

-- name: CountOpenKioskTokens :one
SELECT count(*) FROM kiosk_tokens
WHERE dashboard_id = sqlc.arg(dashboard_id) AND revoked_at IS NULL AND expires_at > sqlc.arg(at);

-- name: CreateKioskToken :exec
INSERT INTO kiosk_tokens (org_id, id, dashboard_id, name, token_hash, created_by, created_at, expires_at)
VALUES (current_org_id(), sqlc.arg(id), sqlc.arg(dashboard_id), sqlc.arg(name), sqlc.arg(token_hash),
        sqlc.arg(created_by), sqlc.arg(created_at), sqlc.arg(expires_at));

-- name: RevokeKioskToken :one
UPDATE kiosk_tokens SET revoked_at = coalesce(revoked_at, sqlc.arg(at))
WHERE id = sqlc.arg(id)
RETURNING dashboard_id, name;

-- name: RevokeKioskTokensOfDashboard :execrows
UPDATE kiosk_tokens SET revoked_at = sqlc.arg(at) WHERE dashboard_id = sqlc.arg(dashboard_id) AND revoked_at IS NULL;

-- name: TouchKioskToken :exec
-- At most once a minute: a display asks every few seconds.
UPDATE kiosk_tokens SET last_used_at = sqlc.arg(at)
WHERE id = sqlc.arg(id) AND (last_used_at IS NULL OR last_used_at < sqlc.arg(at)::timestamptz - interval '1 minute');

-- name: KioskTokenByHash :many
SELECT id::uuid AS id, org_id::uuid AS org_id, dashboard_id::uuid AS dashboard_id, created_by::uuid AS created_by,
       token_hash::bytea AS token_hash
FROM kiosk_token_by_hash(sqlc.arg(hash)::bytea, sqlc.arg(at)::timestamptz);
