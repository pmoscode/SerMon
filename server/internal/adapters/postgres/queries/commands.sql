-- Commands of the server to the agents (M4-11), always in the tenant of the node's organization.

-- name: InsertNodeCommand :exec
INSERT INTO node_commands (org_id, id, node_id, kind, scope, reason, version, created_at, expires_at, created_by)
VALUES (current_org_id(), sqlc.arg(id), sqlc.arg(node_id), sqlc.arg(kind), sqlc.narg(scope), sqlc.arg(reason),
        sqlc.arg(version), sqlc.arg(created_at), sqlc.arg(expires_at), sqlc.narg(created_by));

-- name: NotifyNodeCommands :exec
-- Wakes the instance holding the node's session; the payload is the node's ID only.
SELECT pg_notify('sermon_node_commands', sqlc.arg(node_id)::uuid::text);

-- name: CountRecentRefreshes :one
-- Refreshes asked for the node since node_since and for the organization since org_since.
SELECT count(*) FILTER (WHERE node_id = sqlc.arg(node_id) AND created_at > sqlc.arg(node_since))::bigint AS node,
       count(*) FILTER (WHERE created_at > sqlc.arg(org_since))::bigint AS org
FROM node_commands WHERE kind = 1 AND created_at > least(sqlc.arg(node_since)::timestamptz, sqlc.arg(org_since)::timestamptz);

-- name: ExpireNodeCommands :exec
UPDATE node_commands SET state = 5
WHERE node_id = sqlc.arg(node_id) AND state IN (1, 2) AND expires_at <= sqlc.arg(now);

-- name: DeliverableNodeCommands :many
-- What the node's session sends: the pending commands, and after a reconnect the sent ones without
-- result as well.
SELECT id, kind, scope, reason, version, state, created_at, expires_at, created_by, sent_at
FROM node_commands
WHERE node_id = sqlc.arg(node_id) AND (state = 1 OR (sqlc.arg(resend)::boolean AND state = 2))
  AND expires_at > sqlc.arg(now)
ORDER BY created_at, id
LIMIT 100;

-- name: MarkNodeCommandsSent :exec
-- A refresh has no result: sent is done.
UPDATE node_commands SET state = CASE WHEN kind = 1 THEN 4 ELSE 2 END, sent_at = sqlc.arg(now)
WHERE node_id = sqlc.arg(node_id) AND id = ANY (sqlc.arg(ids)::uuid[]) AND state IN (1, 2);

-- name: RecordNodeCommandResult :execrows
-- The agent's phase of a run of this node: a start only before any phase, a final phase once.
UPDATE node_commands SET
    phase = sqlc.arg(phase), state = CASE WHEN sqlc.arg(phase)::smallint = 1 THEN 3 ELSE 4 END,
    exit_code = sqlc.narg(exit_code), result_reason = sqlc.arg(result_reason), output = sqlc.arg(output),
    output_truncated = sqlc.arg(output_truncated), started_at = coalesce(sqlc.narg(started_at), started_at),
    ended_at = sqlc.narg(ended_at)
WHERE node_id = sqlc.arg(node_id) AND id = sqlc.arg(id) AND kind <> 1
  AND (phase IS NULL OR (phase = 1 AND sqlc.arg(phase)::smallint <> 1));

-- name: GetNodeCommand :one
SELECT id, node_id, kind, scope, reason, version, state, created_at, expires_at, created_by, sent_at,
       phase, exit_code, result_reason, output, output_truncated, started_at, ended_at
FROM node_commands WHERE id = sqlc.arg(id);
