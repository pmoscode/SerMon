-- Deactivating and deleting nodes (M5-29, ADR 0041). All queries run inside InTenant, except
-- the due nodes of all organizations (a definer function).

-- The node within the scope's groups, locked for the change.
-- name: NodeLifecycleState :one
SELECT group_id, hostname, deactivated_at, deletion_due_at FROM nodes
WHERE id = sqlc.arg(id) AND (sqlc.arg(all_groups)::boolean OR group_id = ANY (sqlc.arg(group_ids)::uuid[]))
FOR UPDATE;

-- Deactivates the node (if it is not) and closes its takeover window; with a deadline it is
-- scheduled for deletion as well.
-- name: DeactivateNode :execrows
UPDATE nodes SET deactivated_at = COALESCE(deactivated_at, sqlc.arg(at)), takeover_until = NULL,
                 deletion_due_at = COALESCE(sqlc.narg(due), deletion_due_at)
WHERE id = sqlc.arg(id);

-- name: CancelNodeDeletion :execrows
UPDATE nodes SET deletion_due_at = NULL WHERE id = sqlc.arg(id) AND deletion_due_at IS NOT NULL;

-- name: NodesDueForDeletion :many
SELECT d.org_id::uuid AS org_id, d.node_id::uuid AS node_id FROM nodes_due_for_deletion(sqlc.arg(at), sqlc.arg(max_nodes)::integer) d;

-- name: PurgeNode :one
SELECT node_purge(sqlc.arg(org_id)::uuid, sqlc.arg(node_id)::uuid, sqlc.arg(at))::text AS hostname;
