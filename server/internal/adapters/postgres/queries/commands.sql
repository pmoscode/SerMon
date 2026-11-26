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
-- Pending commands at their expiry, sent ones without an answer only before sent_before – the
-- expiry plus command.SentGrace (M4-31): the agent's answer may still be on its way.
UPDATE node_commands SET state = 5
WHERE node_id = sqlc.arg(node_id)
  AND ((state = 1 AND expires_at <= sqlc.arg(now)) OR (state = 2 AND expires_at <= sqlc.arg(sent_before)));

-- name: DeliverableNodeCommands :many
-- What the node's session sends: the pending commands, and after a reconnect the sent ones without
-- result as well; none cancelled, and while the organization's updates are paused no run (M4-31).
SELECT c.id, c.kind, c.scope, c.reason, c.version, c.state, c.created_at, c.expires_at, c.created_by, c.sent_at
FROM node_commands c
WHERE c.node_id = sqlc.arg(node_id) AND (c.state = 1 OR (sqlc.arg(resend)::boolean AND c.state = 2))
  AND c.expires_at > sqlc.arg(now) AND c.cancelled_at IS NULL
  AND (c.kind = 1 OR NOT EXISTS (SELECT 1 FROM organizations o WHERE o.id = c.org_id AND o.updates_paused_at IS NOT NULL))
ORDER BY c.created_at, c.id
LIMIT 100;

-- name: CancelNodeCommand :one
-- Cancels a run the node hasn't started (M4-31): a pending one at once, a sent one is withdrawn
-- from the agent, which answers. Nothing for a run that started, ended or expired.
UPDATE node_commands SET
    state = CASE WHEN state = 1 THEN 6 ELSE state END, cancelled_at = sqlc.arg(at), cancelled_by = sqlc.narg(cancelled_by)
WHERE id = sqlc.arg(id) AND kind <> 1 AND cancelled_at IS NULL AND phase IS NULL
  AND ((state = 1 AND expires_at > sqlc.arg(at)) OR (state = 2 AND expires_at > sqlc.arg(sent_before)))
RETURNING node_id, state;

-- name: CancelsToSend :many
-- The node's sent runs withdrawn without an answer of the agent: their cancel goes out once per
-- session (M4-31).
SELECT id FROM node_commands
WHERE node_id = sqlc.arg(node_id) AND state = 2 AND cancelled_at IS NOT NULL AND phase IS NULL
  AND (cancel_sent_at IS NULL OR sqlc.arg(resend)::boolean)
ORDER BY created_at, id
LIMIT 100;

-- name: MarkCancelsSent :exec
UPDATE node_commands SET cancel_sent_at = sqlc.arg(now)
WHERE node_id = sqlc.arg(node_id) AND id = ANY (sqlc.arg(ids)::uuid[]) AND state = 2;

-- name: OpenRunsOfOrganization :one
-- The organization's runs under way (pending, sent, running; a day at most) and the instance's.
SELECT count(*)::bigint AS org, open_node_runs()::bigint AS instance
FROM node_commands WHERE kind <> 1 AND state IN (1, 2, 3) AND created_at > now() - interval '1 day';

-- name: NodesUnderWay :many
-- Of the nodes those with a reboot under way or a run in a rollout (M4-31): no second reboot or
-- self-update by hand beside it.
SELECT DISTINCT c.node_id, 'reboot'::text AS what FROM node_commands c
WHERE c.node_id = ANY (sqlc.arg(node_ids)::uuid[]) AND c.kind = 3 AND c.state IN (1, 2, 3)
  AND c.cancelled_at IS NULL AND c.created_at > now() - interval '1 day'
UNION
SELECT DISTINCT n.node_id, 'rollout'::text AS what FROM update_rollout_nodes n
WHERE n.node_id = ANY (sqlc.arg(node_ids)::uuid[]) AND n.state IN (2, 3, 4, 8);

-- name: UpdatesPausedAt :one
SELECT updates_paused_at FROM organizations WHERE id = current_org_id();

-- name: MarkNodeCommandsSent :exec
-- A refresh has no result: sent is done.
UPDATE node_commands SET state = CASE WHEN kind = 1 THEN 4 ELSE 2 END, sent_at = sqlc.arg(now)
WHERE node_id = sqlc.arg(node_id) AND id = ANY (sqlc.arg(ids)::uuid[]) AND state IN (1, 2);

-- name: RecordNodeCommandResult :one
-- The agent's phase of a run of this node: a start only before any phase, a final phase once.
-- A withdrawn run the agent refused is cancelled (M4-31).
UPDATE node_commands SET
    phase = sqlc.arg(phase),
    state = CASE WHEN sqlc.arg(phase)::smallint = 1 THEN 3 WHEN sqlc.arg(phase)::smallint = 4 AND cancelled_at IS NOT NULL THEN 6 ELSE 4 END,
    exit_code = sqlc.narg(exit_code), result_reason = sqlc.arg(result_reason), output = sqlc.arg(output),
    output_truncated = sqlc.arg(output_truncated), started_at = coalesce(sqlc.narg(started_at), started_at),
    ended_at = sqlc.narg(ended_at)
WHERE node_id = sqlc.arg(node_id) AND id = sqlc.arg(id) AND kind <> 1
  AND (phase IS NULL OR (phase = 1 AND sqlc.arg(phase)::smallint <> 1))
RETURNING kind;

-- name: GetNodeCommand :one
SELECT id, node_id, kind, scope, reason, version, state, created_at, expires_at, created_by, sent_at,
       phase, exit_code, result_reason, output, output_truncated, started_at, ended_at, cancelled_at
FROM node_commands WHERE id = sqlc.arg(id);

-- name: ListNodeRuns :many
-- The runs of a node (no refreshes), newest first, without their output (M4-15).
SELECT id, kind, scope, reason, version, state, created_at, expires_at, created_by, sent_at,
       phase, exit_code, result_reason, output_truncated, started_at, ended_at, cancelled_at
FROM node_commands WHERE node_id = sqlc.arg(node_id) AND kind <> 1
ORDER BY created_at DESC, id DESC LIMIT sqlc.arg(max_rows);
