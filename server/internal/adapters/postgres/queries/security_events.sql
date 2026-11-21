-- Security events (M2-07, ADR 0007). Tenant queries run in the organization's tenant transaction.

-- name: LockNodeEventSeq :one
-- The node's stored event sequence, locked until the batches of this transaction are written.
SELECT event_seq FROM nodes WHERE id = sqlc.arg(node_id) FOR UPDATE;

-- name: SetNodeEventSeq :exec
UPDATE nodes SET event_seq = sqlc.arg(seq) WHERE id = sqlc.arg(node_id) AND event_seq < sqlc.arg(seq);

-- name: InsertSecurityEvent :batchexec
INSERT INTO security_events (org_id, node_id, occurred_at, kind, user_name, remote_ip, remote_host, remote_port, details,
                             unverified)
VALUES (current_org_id(), sqlc.arg(node_id), sqlc.arg(occurred_at), sqlc.arg(kind), sqlc.narg(user_name),
        sqlc.narg(remote_ip), sqlc.narg(remote_host), sqlc.narg(remote_port), sqlc.arg(details), sqlc.arg(unverified));

-- name: ListSecurityEvents :many
-- Newest first; the scope's groups are those of the node now (events of removed nodes only for
-- callers on all groups). Keyset paging below (before_time, before_id).
SELECT e.id, e.node_id, e.occurred_at, e.received_at, e.kind, e.user_name, e.remote_ip, e.remote_host,
       e.remote_port, e.details, e.unverified
FROM security_events e
WHERE e.occurred_at >= sqlc.arg(from_time) AND e.occurred_at < sqlc.arg(to_time)
  AND (sqlc.arg(all_groups)::boolean
       OR e.node_id IN (SELECT n.id FROM nodes n WHERE n.group_id = ANY (sqlc.arg(group_ids)::uuid[])))
  AND (cardinality(sqlc.arg(node_ids)::uuid[]) = 0 OR e.node_id = ANY (sqlc.arg(node_ids)::uuid[]))
  AND (cardinality(sqlc.arg(kinds)::text[]) = 0 OR e.kind = ANY (sqlc.arg(kinds)::text[]))
  AND (sqlc.narg(user_name)::text IS NULL OR e.user_name = sqlc.narg(user_name)::text)
  AND (sqlc.narg(network)::cidr IS NULL OR e.remote_ip <<= sqlc.narg(network)::cidr)
  AND (sqlc.narg(before_time)::timestamptz IS NULL
       OR (e.occurred_at, e.id) < (sqlc.narg(before_time)::timestamptz, sqlc.narg(before_id)::bigint))
ORDER BY e.occurred_at DESC, e.id DESC
LIMIT sqlc.arg(max_rows);

-- name: ReceivedSecurityEvents :many
-- The alert engine (M2-08): events of one kind and the given nodes received since a time, newest
-- received first. The bounds on occurred_at only prune partitions: min_occurred lies the oldest
-- age the ingest accepts (secevent.MaxAge) and a margin before since; it takes 5 min ahead.
SELECT e.id, e.node_id, e.occurred_at, e.received_at, e.kind, e.user_name, e.remote_ip, e.remote_host,
       e.remote_port, e.details, e.unverified
FROM security_events e
WHERE e.kind = sqlc.arg(kind) AND e.node_id = ANY (sqlc.arg(node_ids)::uuid[])
  AND e.received_at >= sqlc.arg(since)
  AND e.occurred_at >= sqlc.arg(min_occurred) AND e.occurred_at < now() + interval '1 hour'
ORDER BY e.received_at DESC, e.id DESC
LIMIT sqlc.arg(max_rows);

-- name: SecurityEventHorizon :one
SELECT coalesce(security_events_horizon(), '-infinity'::timestamptz)::timestamptz AS horizon;

-- name: ExpireSecurityEventPartitions :one
SELECT security_events_expire(sqlc.arg(before)::timestamptz)::integer AS dropped;
