-- Node groups, role bindings and audit log. All queries run inside InTenant (RLS on org_id).

-- name: CreateNodeGroup :one
INSERT INTO node_groups (id, org_id, name) VALUES (sqlc.arg(id), current_org_id(), sqlc.arg(name))
RETURNING id, org_id, name, created_at;

-- name: ListNodeGroups :many
SELECT id, org_id, name, created_at FROM node_groups
WHERE sqlc.arg(all_groups)::boolean OR id = ANY (sqlc.arg(group_ids)::uuid[])
ORDER BY lower(name), id;

-- name: RenameNodeGroup :execrows
UPDATE node_groups SET name = sqlc.arg(name) WHERE id = sqlc.arg(id);

-- name: DeleteNodeGroup :execrows
DELETE FROM node_groups WHERE id = sqlc.arg(id);

-- A member of a locked organization (M2-33) is no member until the lock is lifted: every
-- request of its sessions is refused.
-- name: IsMember :one
SELECT EXISTS (
    SELECT 1 FROM memberships m JOIN organizations o ON o.id = m.org_id
    WHERE m.user_id = sqlc.arg(user_id) AND o.locked_at IS NULL
) AS member;

-- name: RoleBindingsOfUser :many
SELECT id, group_id, role FROM role_bindings WHERE user_id = sqlc.arg(user_id);

-- name: CreateRoleBinding :exec
INSERT INTO role_bindings (id, org_id, user_id, group_id, role)
VALUES (sqlc.arg(id), current_org_id(), sqlc.arg(user_id), sqlc.narg(group_id), sqlc.arg(role));

-- Serializes changes of rights within the organization (last-admin check without races).
-- name: LockAccessChanges :exec
SELECT pg_advisory_xact_lock(hashtextextended('access:' || current_org_id()::text, 0));

-- name: UpsertRoleBinding :one
INSERT INTO role_bindings (id, org_id, user_id, group_id, role)
VALUES (sqlc.arg(id), current_org_id(), sqlc.arg(user_id), sqlc.narg(group_id), sqlc.arg(role))
ON CONFLICT (org_id, user_id, COALESCE(group_id, '00000000-0000-0000-0000-000000000000'::uuid))
DO UPDATE SET role = EXCLUDED.role
RETURNING id;

-- name: GetRoleBinding :one
SELECT id, user_id, group_id, role FROM role_bindings WHERE id = sqlc.arg(id);

-- name: CountOrgAdmins :one
SELECT count(*) FROM role_bindings WHERE group_id IS NULL AND role = 'admin';

-- name: ListMembers :many
SELECT u.id, u.email, u.display_name
FROM memberships m JOIN users u ON u.id = m.user_id
ORDER BY lower(u.email), u.id;

-- name: ListRoleBindings :many
SELECT id, user_id, group_id, role FROM role_bindings ORDER BY created_at, id;

-- name: DeleteMembership :execrows
DELETE FROM memberships WHERE user_id = sqlc.arg(user_id);

-- Sessions are instance-level (ADR 0007); only the removed member's sessions in this organization end.
-- name: DeleteOrgSessionsOfUser :exec
DELETE FROM sessions WHERE user_id = sqlc.arg(user_id) AND org_id = current_org_id();

-- name: DeleteRoleBinding :execrows
DELETE FROM role_bindings WHERE id = sqlc.arg(id);

-- name: AppendAudit :one
INSERT INTO audit_log (org_id, seq, actor_id, action, target, details, prev_hash, hash)
VALUES (current_org_id(), 0, sqlc.narg(actor_id), sqlc.arg(action), sqlc.arg(target), sqlc.arg(details), '\x', '\x')
RETURNING id, at, hash;

-- name: ListAudit :many
SELECT id, org_id, seq, at, actor_id, action, target, details, prev_hash, hash
FROM audit_log
WHERE sqlc.arg(before_seq)::bigint = 0 OR seq < sqlc.arg(before_seq)::bigint
ORDER BY seq DESC
LIMIT sqlc.arg(max_rows);

-- name: AuditChain :many
SELECT id, org_id, seq, at, actor_id, action, target, details, prev_hash, hash
FROM audit_log
ORDER BY seq;
