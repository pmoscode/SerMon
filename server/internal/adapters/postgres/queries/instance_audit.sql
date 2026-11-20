-- name: AppendInstanceAudit :exec
-- The trigger instance_audit_log_chain assigns seq, time and the hashes.
INSERT INTO instance_audit_log (seq, actor_id, action, target, details, prev_hash, hash)
VALUES (0, sqlc.narg(actor_id), sqlc.arg(action), sqlc.arg(target), sqlc.arg(details), '\x', '\x');

-- name: ListInstanceAudit :many
SELECT id, seq, at, actor_id, action, target, details, prev_hash, hash
FROM instance_audit_log
WHERE sqlc.arg(before_seq)::bigint = 0 OR seq < sqlc.arg(before_seq)::bigint
ORDER BY seq DESC
LIMIT sqlc.arg(max_rows);

-- name: InstanceAuditChain :many
SELECT id, seq, at, actor_id, action, target, details, prev_hash, hash
FROM instance_audit_log
ORDER BY seq;
