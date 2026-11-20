-- Metric series and points (M1-22, ADR 0001). Every query runs in a tenant transaction.

-- name: EnsureSeries :many
-- Registers series of the current organization (one row per position of the arrays) and returns
-- the IDs of new and existing ones. A row inserted concurrently by another transaction may be
-- missing from the result; the caller asks again for missing keys.
WITH wanted AS (
    SELECT n.node_id, m.metric, l.labels
    FROM unnest(sqlc.arg(node_ids)::uuid[]) WITH ORDINALITY AS n(node_id, i)
    JOIN unnest(sqlc.arg(metrics)::text[]) WITH ORDINALITY AS m(metric, i) ON m.i = n.i
    JOIN unnest(sqlc.arg(labels)::jsonb[]) WITH ORDINALITY AS l(labels, i) ON l.i = n.i
), inserted AS (
    INSERT INTO series (org_id, node_id, metric, labels, seen_at)
    SELECT current_org_id(), node_id, metric, labels, sqlc.arg(seen_at)::timestamptz FROM wanted
    ON CONFLICT (org_id, node_id, metric, labels) DO NOTHING
    RETURNING id, node_id, metric, labels
)
SELECT inserted.id, inserted.node_id, inserted.metric, inserted.labels FROM inserted
UNION ALL
SELECT s.id, s.node_id, s.metric, s.labels
FROM series s JOIN wanted w ON s.node_id = w.node_id AND s.metric = w.metric AND s.labels = w.labels;

-- name: CopyMetricWindows :copyfrom
INSERT INTO metrics_raw (ts, series_id, avg, min, max) VALUES ($1, $2, $3, $4, $5);

-- name: AdvanceIngestSeq :execrows
UPDATE nodes SET ingest_seq = GREATEST(ingest_seq, sqlc.arg(seq)::bigint) WHERE id = sqlc.arg(node_id);

-- name: GetIngestSeq :one
SELECT ingest_seq FROM nodes WHERE id = sqlc.arg(node_id);

-- name: FindSeries :many
-- Series of a metric on the given nodes, restricted to the node groups of the scope and to series
-- whose labels contain the filter.
SELECT s.id, s.node_id, s.labels
FROM series s JOIN nodes n ON n.org_id = s.org_id AND n.id = s.node_id
WHERE s.metric = sqlc.arg(metric)
  AND s.node_id = ANY (sqlc.arg(node_ids)::uuid[])
  AND s.labels @> sqlc.arg(label_filter)::jsonb
  AND (sqlc.arg(all_groups)::boolean OR n.group_id = ANY (sqlc.arg(group_ids)::uuid[]))
ORDER BY s.node_id, s.labels::text
LIMIT sqlc.arg(max_series);

-- name: PointsRaw :many
SELECT series_id::bigint AS series_id, bucket::timestamptz AS bucket, avg::float8 AS avg, min::float8 AS min, max::float8 AS max
FROM metric_points_raw(sqlc.arg(series_ids)::bigint[], sqlc.arg(from_ts)::timestamptz, sqlc.arg(to_ts)::timestamptz, sqlc.arg(step)::interval);

-- name: Points1m :many
SELECT series_id::bigint AS series_id, bucket::timestamptz AS bucket, avg::float8 AS avg, min::float8 AS min, max::float8 AS max
FROM metric_points_1m(sqlc.arg(series_ids)::bigint[], sqlc.arg(from_ts)::timestamptz, sqlc.arg(to_ts)::timestamptz, sqlc.arg(step)::interval);

-- name: Points1h :many
SELECT series_id::bigint AS series_id, bucket::timestamptz AS bucket, avg::float8 AS avg, min::float8 AS min, max::float8 AS max
FROM metric_points_1h(sqlc.arg(series_ids)::bigint[], sqlc.arg(from_ts)::timestamptz, sqlc.arg(to_ts)::timestamptz, sqlc.arg(step)::interval);

-- name: MarkSeriesSeen :exec
-- Moves seen_at forward to the newest data time of a write (never back).
UPDATE series SET seen_at = sqlc.arg(seen_at)
WHERE id = ANY (sqlc.arg(ids)::bigint[]) AND seen_at < sqlc.arg(seen_at);

-- name: NodeSeries :many
-- Series of the node with data from p_since on (the node lies in the current organization by RLS).
SELECT metric, labels FROM series
WHERE node_id = sqlc.arg(node_id) AND seen_at >= sqlc.arg(since)
ORDER BY metric, labels::text
LIMIT sqlc.arg(max_series);

-- name: OrgSeriesCount :one
SELECT count(*) FROM series WHERE seen_at >= sqlc.arg(since);
