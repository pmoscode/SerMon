-- Custom dashboards (M5-23, ADR 0040). All queries run inside InTenant; the owner's or the
-- shared ones only.

-- name: ListDashboards :many
SELECT d.id, d.owner_id, coalesce(u.display_name, '')::text AS owner_name, d.shared, d.revision, d.spec,
       d.created_at, d.updated_at
FROM dashboards d LEFT JOIN users u ON u.id = d.owner_id
WHERE d.owner_id = sqlc.arg(user_id) OR d.shared
ORDER BY lower(d.title), d.id;

-- name: GetDashboard :one
SELECT d.id, d.owner_id, coalesce(u.display_name, '')::text AS owner_name, d.shared, d.revision, d.spec,
       d.created_at, d.updated_at
FROM dashboards d LEFT JOIN users u ON u.id = d.owner_id
WHERE d.id = sqlc.arg(id) AND (d.owner_id = sqlc.arg(user_id) OR d.shared);

-- name: LockDashboard :one
SELECT d.id, d.owner_id, coalesce(u.display_name, '')::text AS owner_name, d.shared, d.revision, d.spec,
       d.created_at, d.updated_at
FROM dashboards d LEFT JOIN users u ON u.id = d.owner_id
WHERE d.id = sqlc.arg(id) AND (d.owner_id = sqlc.arg(user_id) OR d.shared)
FOR UPDATE OF d;

-- name: LockDashboardCount :exec
-- Serializes the creations of an organization against its limit.
SELECT pg_advisory_xact_lock(hashtextextended('dashboards:' || current_org_id()::text, 0));

-- name: CountDashboards :one
SELECT count(*) FROM dashboards;

-- name: CreateDashboard :exec
INSERT INTO dashboards (org_id, id, owner_id, title, shared, revision, spec, created_at, updated_at)
VALUES (current_org_id(), sqlc.arg(id), sqlc.arg(owner_id), sqlc.arg(title), false, 1, sqlc.arg(spec),
        sqlc.arg(created_at), sqlc.arg(created_at));

-- name: UpdateDashboard :exec
UPDATE dashboards SET title = sqlc.arg(title), shared = sqlc.arg(shared), revision = sqlc.arg(revision),
    spec = sqlc.arg(spec), updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id);

-- name: DeleteDashboard :exec
DELETE FROM dashboards WHERE id = sqlc.arg(id);

-- name: VisibleNodeIDs :many
SELECT id FROM nodes
WHERE id = ANY (sqlc.arg(ids)::uuid[]) AND (sqlc.arg(all_groups)::boolean OR group_id = ANY (sqlc.arg(group_ids)::uuid[]));

-- name: VisibleGroupIDs :many
SELECT id FROM node_groups
WHERE id = ANY (sqlc.arg(ids)::uuid[]) AND (sqlc.arg(all_groups)::boolean OR id = ANY (sqlc.arg(group_ids)::uuid[]));
