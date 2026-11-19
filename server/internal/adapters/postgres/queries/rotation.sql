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
