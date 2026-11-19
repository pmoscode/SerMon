-- Node groups, role bindings and audit log. All queries run inside InTenant (RLS on org_id).

-- name: CreateNodeGroup :one
INSERT INTO node_groups (id, org_id, name) VALUES (sqlc.arg(id), current_org_id(), sqlc.arg(name))
RETURNING id, org_id, name, created_at;

-- name: ListNodeGroups :many
SELECT id, org_id, name, created_at FROM node_groups ORDER BY lower(name);

-- name: RenameNodeGroup :execrows
UPDATE node_groups SET name = sqlc.arg(name) WHERE id = sqlc.arg(id);

-- name: DeleteNodeGroup :execrows
DELETE FROM node_groups WHERE id = sqlc.arg(id);

-- name: IsMember :one
SELECT EXISTS (SELECT 1 FROM memberships WHERE user_id = sqlc.arg(user_id)) AS member;

-- name: RoleBindingsOfUser :many
SELECT id, group_id, role FROM role_bindings WHERE user_id = sqlc.arg(user_id);

-- name: CreateRoleBinding :exec
INSERT INTO role_bindings (id, org_id, user_id, group_id, role)
VALUES (sqlc.arg(id), current_org_id(), sqlc.arg(user_id), sqlc.narg(group_id), sqlc.arg(role));

-- name: DeleteRoleBinding :execrows
DELETE FROM role_bindings WHERE id = sqlc.arg(id);

-- name: AppendAudit :one
INSERT INTO audit_log (org_id, seq, actor_id, action, target, details, prev_hash, hash)
VALUES (current_org_id(), 0, sqlc.narg(actor_id), sqlc.arg(action), sqlc.arg(target), sqlc.arg(details), '\x', '\x')
RETURNING id, at, hash;

-- name: ListAudit :many
SELECT id, org_id, seq, at, actor_id, action, target, details, prev_hash, hash
FROM audit_log
ORDER BY seq DESC
LIMIT sqlc.arg(max_rows);

-- name: AuditChain :many
SELECT id, org_id, seq, at, actor_id, action, target, details, prev_hash, hash
FROM audit_log
ORDER BY seq;
