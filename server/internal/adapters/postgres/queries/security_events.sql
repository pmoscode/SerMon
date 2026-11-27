-- Security events (M2-07, ADR 0007). Tenant queries run in the organization's tenant transaction.

-- name: LockNodeEventSeq :one
-- The node's stored event sequence, locked until the batches of this transaction are written.
SELECT event_seq FROM nodes WHERE id = sqlc.arg(node_id) FOR UPDATE;

-- name: SetNodeEventSeq :exec
UPDATE nodes SET event_seq = sqlc.arg(seq) WHERE id = sqlc.arg(node_id) AND event_seq < sqlc.arg(seq);

-- name: InsertSecurityEvent :batchexec
INSERT INTO security_events (org_id, node_id, occurred_at, kind, user_name, remote_ip, remote_host, remote_port, details,
                             unverified, new_source)
VALUES (current_org_id(), sqlc.arg(node_id), sqlc.arg(occurred_at), sqlc.arg(kind), sqlc.narg(user_name),
        sqlc.narg(remote_ip), sqlc.narg(remote_host), sqlc.narg(remote_port), sqlc.arg(details), sqlc.arg(unverified),
        sqlc.arg(new_source));

-- name: InsertServerSecurityEvent :exec
-- An event of the server itself (M2-24), inside the transaction of what it reports.
INSERT INTO security_events (org_id, node_id, occurred_at, kind, user_name, remote_ip, remote_host, remote_port, details,
                             unverified)
VALUES (current_org_id(), sqlc.arg(node_id), sqlc.arg(occurred_at), sqlc.arg(kind), sqlc.narg(user_name),
        sqlc.narg(remote_ip), sqlc.narg(remote_host), sqlc.narg(remote_port), sqlc.arg(details), false);

-- name: ListSecurityEvents :many
-- Newest first; the scope's groups are those of the node now (events of removed nodes only for
-- callers on all groups). Keyset paging below (before_time, before_id).
SELECT e.id, e.node_id, e.occurred_at, e.received_at, e.kind, e.user_name, e.remote_ip, e.remote_host,
       e.remote_port, e.details, e.unverified, e.new_source
FROM security_events e
WHERE e.occurred_at >= sqlc.arg(from_time) AND e.occurred_at < sqlc.arg(to_time)
  AND (sqlc.arg(all_groups)::boolean
       OR e.node_id IN (SELECT n.id FROM nodes n WHERE n.group_id = ANY (sqlc.arg(group_ids)::uuid[])))
  AND (cardinality(sqlc.arg(node_ids)::uuid[]) = 0 OR e.node_id = ANY (sqlc.arg(node_ids)::uuid[]))
  AND (cardinality(sqlc.arg(kinds)::text[]) = 0 OR e.kind = ANY (sqlc.arg(kinds)::text[]))
  AND (sqlc.narg(user_name)::text IS NULL OR e.user_name = sqlc.narg(user_name)::text)
  AND (sqlc.narg(network)::cidr IS NULL OR e.remote_ip <<= sqlc.narg(network)::cidr)
  AND (NOT sqlc.arg(new_sources_only)::boolean OR e.new_source)
  AND (sqlc.narg(before_time)::timestamptz IS NULL
       OR (e.occurred_at, e.id) < (sqlc.narg(before_time)::timestamptz, sqlc.narg(before_id)::bigint))
ORDER BY e.occurred_at DESC, e.id DESC
LIMIT sqlc.arg(max_rows);

-- name: ReceivedSecurityEvents :many
-- The alert engine (M2-08): events of one kind and the given nodes received since a time, newest
-- received first. The bounds on occurred_at only prune partitions: min_occurred lies the oldest
-- age the ingest accepts (secevent.MaxAge) and a margin before since; it takes 5 min ahead.
SELECT e.id, e.node_id, e.occurred_at, e.received_at, e.kind, e.user_name, e.remote_ip, e.remote_host,
       e.remote_port, e.details, e.unverified, e.new_source
FROM security_events e
WHERE e.kind = sqlc.arg(kind) AND e.node_id = ANY (sqlc.arg(node_ids)::uuid[])
  AND e.received_at >= sqlc.arg(since)
  AND e.occurred_at >= sqlc.arg(min_occurred) AND e.occurred_at < now() + interval '1 hour'
ORDER BY e.received_at DESC, e.id DESC
LIMIT sqlc.arg(max_rows);

-- name: SecurityEventHorizon :one
SELECT coalesce(security_events_horizon(), '-infinity'::timestamptz)::timestamptz AS horizon;

-- name: ExpireSecurityEventPartitions :one
SELECT security_events_expire(sqlc.arg(before)::timestamptz, sqlc.arg(default_days)::integer)::integer AS dropped;

-- Known login sources (M3-16).

-- name: LearnLoginSource :many
-- Records a successful login of a user from a source on a node; inserted is true for the first
-- one (the row is new). A node holds at most max_sources: beyond, an unknown source is not
-- recorded and nothing is returned.
INSERT INTO known_login_sources AS k (org_id, node_id, user_name, source, first_seen, last_seen)
SELECT current_org_id(), sqlc.arg(node_id), sqlc.arg(user_name), sqlc.arg(source), sqlc.arg(at), sqlc.arg(at)
WHERE EXISTS (SELECT 1 FROM known_login_sources o
              WHERE o.node_id = sqlc.arg(node_id) AND o.user_name = sqlc.arg(user_name) AND o.source = sqlc.arg(source))
   OR (SELECT count(*) FROM known_login_sources o WHERE o.node_id = sqlc.arg(node_id)) < sqlc.arg(max_sources)::bigint
ON CONFLICT (org_id, node_id, user_name, source) DO UPDATE
    SET first_seen = least(k.first_seen, excluded.first_seen),
        last_seen  = greatest(k.last_seen, excluded.last_seen),
        logins     = k.logins + 1
RETURNING (xmax = 0)::boolean AS inserted;

-- name: ForgetLoginSources :exec
-- Forgets the sources of a node unused since before (a year: personal data like the events).
DELETE FROM known_login_sources WHERE node_id = sqlc.arg(node_id) AND last_seen < sqlc.arg(before);

-- name: NodeHostnames :many
SELECT n.id, n.hostname FROM nodes n
WHERE n.id = ANY (sqlc.arg(node_ids)::uuid[])
  AND (sqlc.arg(all_groups)::boolean OR n.group_id = ANY (sqlc.arg(group_ids)::uuid[]));

-- Evaluations (M3-16). The scope's groups are those of the node now, as in ListSecurityEvents.

-- name: FailedLoginBuckets :many
SELECT e.node_id, date_bin(sqlc.arg(step)::interval, e.occurred_at, sqlc.arg(from_time)::timestamptz)::timestamptz AS bucket,
       count(*) AS events
FROM security_events e
WHERE e.occurred_at >= sqlc.arg(from_time) AND e.occurred_at < sqlc.arg(to_time)
  AND e.kind IN ('ssh.login.failed', 'ssh.login.invalid_user')
  AND (sqlc.arg(all_groups)::boolean
       OR e.node_id IN (SELECT n.id FROM nodes n WHERE n.group_id = ANY (sqlc.arg(group_ids)::uuid[])))
  AND (cardinality(sqlc.arg(node_ids)::uuid[]) = 0 OR e.node_id = ANY (sqlc.arg(node_ids)::uuid[]))
GROUP BY e.node_id, bucket
ORDER BY e.node_id, bucket;

-- name: TopFailedIPs :many
SELECT e.remote_ip::inet AS remote_ip, count(*) AS events, count(DISTINCT e.node_id) AS nodes,
       max(e.occurred_at)::timestamptz AS last_at, count(*) FILTER (WHERE e.unverified) AS unverified
FROM security_events e
WHERE e.occurred_at >= sqlc.arg(from_time) AND e.occurred_at < sqlc.arg(to_time)
  AND e.kind IN ('ssh.login.failed', 'ssh.login.invalid_user') AND e.remote_ip IS NOT NULL
  AND (sqlc.arg(all_groups)::boolean
       OR e.node_id IN (SELECT n.id FROM nodes n WHERE n.group_id = ANY (sqlc.arg(group_ids)::uuid[])))
  AND (cardinality(sqlc.arg(node_ids)::uuid[]) = 0 OR e.node_id = ANY (sqlc.arg(node_ids)::uuid[]))
GROUP BY e.remote_ip
ORDER BY events DESC, e.remote_ip
LIMIT sqlc.arg(max_rows);

-- name: TopFailedUsers :many
SELECT coalesce(e.user_name, '')::text AS user_name, count(*) AS events, count(DISTINCT e.node_id) AS nodes,
       max(e.occurred_at)::timestamptz AS last_at, count(*) FILTER (WHERE e.unverified) AS unverified
FROM security_events e
WHERE e.occurred_at >= sqlc.arg(from_time) AND e.occurred_at < sqlc.arg(to_time)
  AND e.kind IN ('ssh.login.failed', 'ssh.login.invalid_user')
  AND (sqlc.arg(all_groups)::boolean
       OR e.node_id IN (SELECT n.id FROM nodes n WHERE n.group_id = ANY (sqlc.arg(group_ids)::uuid[])))
  AND (cardinality(sqlc.arg(node_ids)::uuid[]) = 0 OR e.node_id = ANY (sqlc.arg(node_ids)::uuid[]))
GROUP BY coalesce(e.user_name, '')
ORDER BY events DESC, user_name
LIMIT sqlc.arg(max_rows);

-- name: SecurityEventTotals :one
SELECT count(*) FILTER (WHERE e.kind IN ('ssh.login.failed', 'ssh.login.invalid_user')) AS failed_logins,
       count(*) FILTER (WHERE e.kind = 'ssh.login.success') AS logins,
       count(*) FILTER (WHERE e.kind = 'ssh.login.success' AND e.new_source) AS new_source_logins,
       count(*) FILTER (WHERE e.kind = 'ssh.login.success' AND e.user_name = 'root') AS root_logins,
       count(*) FILTER (WHERE e.kind IN ('sudo.auth.failed', 'sudo.not_allowed')) AS sudo_failures
FROM security_events e
WHERE e.occurred_at >= sqlc.arg(from_time) AND e.occurred_at < sqlc.arg(to_time)
  AND e.kind IN ('ssh.login.failed', 'ssh.login.invalid_user', 'ssh.login.success', 'sudo.auth.failed', 'sudo.not_allowed')
  AND (sqlc.arg(all_groups)::boolean
       OR e.node_id IN (SELECT n.id FROM nodes n WHERE n.group_id = ANY (sqlc.arg(group_ids)::uuid[])))
  AND (cardinality(sqlc.arg(node_ids)::uuid[]) = 0 OR e.node_id = ANY (sqlc.arg(node_ids)::uuid[]));
