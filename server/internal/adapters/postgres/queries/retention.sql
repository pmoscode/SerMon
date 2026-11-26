-- Retention per organization (M5-30). Get, set and mark run inside InTenant.

-- name: GetRetention :one
SELECT log_retention_days, event_retention_days, retention_cap_days FROM organizations WHERE id = current_org_id();

-- name: SetRetention :execrows
UPDATE organizations SET log_retention_days = sqlc.narg(log_days), event_retention_days = sqlc.narg(event_days)
WHERE id = current_org_id();

-- name: MarkLogsErased :exec
UPDATE organizations SET logs_erased_at = sqlc.arg(at) WHERE id = current_org_id();

-- name: OrganizationRetentions :many
-- Unset is 0; never erased is the epoch.
SELECT r.id::uuid AS org_id, COALESCE(r.log_retention_days, 0)::smallint AS log_days,
       COALESCE(r.event_retention_days, 0)::smallint AS event_days,
       COALESCE(r.logs_erased_at, 'epoch')::timestamptz AS logs_erased_at
FROM organization_retentions() r;

-- name: ExpireOrganizationEvents :one
SELECT security_events_expire_org(sqlc.arg(org_id)::uuid, sqlc.arg(before)::timestamptz)::bigint AS deleted;
