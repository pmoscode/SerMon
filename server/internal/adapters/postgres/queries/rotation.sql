-- Certificate rotation (M1-11). All queries run inside InTenant.

-- Replaces the current certificate: it stays valid until revoke_at (grace period). No rows
-- means the certificate was already replaced or revoked.
-- name: SupersedeCertificate :execrows
UPDATE agent_certificates SET revoked_at = sqlc.arg(revoke_at)
WHERE serial = sqlc.arg(serial) AND node_id = sqlc.arg(node_id) AND revoked_at IS NULL;

-- name: LockNode :execrows
UPDATE nodes SET locked_at = sqlc.arg(at), lock_reason = sqlc.arg(reason)
WHERE id = sqlc.arg(id) AND locked_at IS NULL;

-- name: RevokeNodeCertificates :exec
UPDATE agent_certificates SET revoked_at = sqlc.arg(at)
WHERE node_id = sqlc.arg(node_id) AND (revoked_at IS NULL OR revoked_at > sqlc.arg(at));

-- Records the source IP of a new session and returns the previous one.
-- name: SwapNodeLastIP :one
UPDATE nodes n SET last_ip = sqlc.arg(ip)
FROM (SELECT o.id, o.last_ip FROM nodes o WHERE o.id = sqlc.arg(node_id) FOR UPDATE) old
WHERE n.id = old.id
RETURNING COALESCE(old.last_ip, '')::text AS previous_ip;

-- Nodes of an on-demand rotation within the scope's groups.
-- name: NodesForRotation :many
SELECT id, group_id FROM nodes
WHERE (sqlc.arg(all_groups)::boolean OR group_id = ANY (sqlc.arg(group_ids)::uuid[]))
  AND (NOT sqlc.arg(by_ids)::boolean OR id = ANY (sqlc.arg(node_ids)::uuid[]))
  AND (sqlc.narg(group_id)::uuid IS NULL OR group_id = sqlc.narg(group_id)::uuid)
ORDER BY id;

-- Node identity in the UI (M2-23).

-- name: NodeLockedAt :one
SELECT locked_at FROM nodes WHERE id = sqlc.arg(id) FOR UPDATE;

-- name: RestoreLockRevokedCertificate :exec
-- The unlock gives the agent its current key back (M2-39): the newest certificate of the node, if
-- the lock revoked it (same time). Older ones – a rotation's grace – stay revoked.
UPDATE agent_certificates c SET revoked_at = NULL
WHERE c.node_id = sqlc.arg(node_id) AND c.revoked_at = sqlc.arg(locked_at)
  AND c.serial = (SELECT n.serial FROM agent_certificates n WHERE n.node_id = sqlc.arg(node_id)
                  ORDER BY n.not_before DESC, n.created_at DESC LIMIT 1);

-- name: UnlockNode :execrows
UPDATE nodes SET locked_at = NULL, lock_reason = NULL
WHERE id = sqlc.arg(id) AND locked_at IS NOT NULL;

-- A node scheduled for deletion can't be taken over (ADR 0041): no rows.
-- name: AllowNodeTakeover :execrows
UPDATE nodes SET takeover_until = sqlc.arg(until) WHERE id = sqlc.arg(id) AND deletion_due_at IS NULL;

-- The node of a group with this machine-id whose takeover window is open.
-- name: NodeForTakeover :one
SELECT id FROM nodes
WHERE machine_id = sqlc.arg(machine_id) AND group_id = sqlc.arg(group_id) AND takeover_until > sqlc.arg(now)
  AND deletion_due_at IS NULL;

-- Takes the node over (in the enrollment transaction): new host facts, the token, window and lock
-- closed, a deactivation lifted (ADR 0041). No rows: the window closed meanwhile, or the node is
-- scheduled for deletion.
-- name: TakeOverNode :execrows
UPDATE nodes SET hostname = sqlc.arg(hostname), os_id = sqlc.arg(os_id), os_version = sqlc.arg(os_version),
                 kernel_release = sqlc.arg(kernel_release), architecture = sqlc.arg(architecture),
                 cpu_count = sqlc.arg(cpu_count), memory_bytes = sqlc.arg(memory_bytes),
                 enrolled_with = sqlc.narg(enrolled_with), takeover_until = NULL, locked_at = NULL, lock_reason = NULL,
                 deactivated_at = NULL
WHERE id = sqlc.arg(id) AND group_id = sqlc.arg(group_id) AND machine_id = sqlc.arg(machine_id)
  AND takeover_until > sqlc.arg(now) AND deletion_due_at IS NULL;
