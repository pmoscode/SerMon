-- The counts of the log lines (V1-24, ADR 0044), only through the SECURITY DEFINER functions and
-- always in the tenant of one organization.

-- name: AddLogCounts :one
SELECT log_counts_add(sqlc.arg(node_id)::uuid, sqlc.arg(seqs)::bigint[], sqlc.arg(ats)::timestamptz[],
    sqlc.arg(sources)::smallint[], sqlc.arg(severities)::smallint[], sqlc.arg(lines)::bigint[])::bigint AS counted;

-- name: LogCountsRange :many
SELECT r.at::timestamptz AS at, r.severity::smallint AS severity, r.lines::bigint AS lines
FROM log_counts_range(sqlc.arg(node_ids)::uuid[], sqlc.arg(after)::timestamptz, sqlc.arg(until)::timestamptz,
    sqlc.arg(step_seconds)::integer, sqlc.arg(sources)::smallint[], sqlc.arg(severities)::smallint[]) r;

-- name: ExpireLogCounts :one
SELECT log_counts_expire(sqlc.arg(before)::timestamptz)::bigint AS deleted;
