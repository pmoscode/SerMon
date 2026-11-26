-- The anomaly detection (M4-23, ADR 0037), always in the tenant of one organization.

-- name: LeaseAnomalyOrganization :execrows
-- Compare-and-swap: the holder renews, anybody else only takes over an expired lease. With the
-- database's time (M4-31): the instances' clocks may differ.
INSERT INTO anomaly_leases (org_id, holder, expires_at)
VALUES (current_org_id(), sqlc.arg(holder), now() + make_interval(secs => sqlc.arg(lease_seconds)::float8))
ON CONFLICT (org_id) DO UPDATE SET holder = EXCLUDED.holder, expires_at = EXCLUDED.expires_at
WHERE anomaly_leases.holder = EXCLUDED.holder OR anomaly_leases.expires_at <= now();

-- name: ReleaseAnomalyOrganization :exec
-- The holder gives the organization up: another instance takes it in its next round.
UPDATE anomaly_leases SET expires_at = now()
WHERE org_id = current_org_id() AND holder = sqlc.arg(holder) AND expires_at > now();

-- name: HoldAnomalyLease :many
-- The holder's lease, if still valid; locked until the transaction ends, so no takeover comes
-- between this check and the writes after it (M4-31).
SELECT holder FROM anomaly_leases
WHERE org_id = current_org_id() AND holder = sqlc.arg(holder) AND expires_at > now()
FOR SHARE;

-- name: AnomalyCandidateSeries :many
-- The series of the candidate metrics with data since `since`, with when their node began to learn:
-- at most per_node of a node (the metrics' order first), so a node with many disks or interfaces
-- can't crowd the others out (M4-31).
SELECT c.id, c.node_id, c.metric, c.labels, c.created_at, c.learning_since
FROM (
    SELECT s.id, s.node_id, s.metric, s.labels, s.created_at, n.learning_since,
           row_number() OVER (PARTITION BY s.node_id ORDER BY array_position(sqlc.arg(metrics)::text[], s.metric), s.id) AS nth
    FROM series s JOIN nodes n ON n.org_id = s.org_id AND n.id = s.node_id
    WHERE s.metric = ANY (sqlc.arg(metrics)::text[]) AND s.seen_at >= sqlc.arg(since)
) c
WHERE c.nth <= sqlc.arg(per_node)::bigint
ORDER BY c.node_id, c.id
LIMIT sqlc.arg(max_series);

-- name: AnomalyBaselinesOfHour :many
SELECT series_id, median, mad, low, high, points
FROM anomaly_baselines WHERE hour = sqlc.arg(hour);

-- name: UpsertAnomalyBaselines :exec
-- One row per position of the arrays.
INSERT INTO anomaly_baselines (org_id, series_id, hour, median, mad, low, high, points, computed_at)
SELECT current_org_id(), id.v, sqlc.arg(hour), med.v, mad.v, low.v, high.v, cnt.v, sqlc.arg(computed_at)
FROM unnest(sqlc.arg(series_ids)::bigint[]) WITH ORDINALITY AS id(v, i)
JOIN unnest(sqlc.arg(medians)::float8[]) WITH ORDINALITY AS med(v, i) ON med.i = id.i
JOIN unnest(sqlc.arg(mads)::float8[]) WITH ORDINALITY AS mad(v, i) ON mad.i = id.i
JOIN unnest(sqlc.arg(lows)::float8[]) WITH ORDINALITY AS low(v, i) ON low.i = id.i
JOIN unnest(sqlc.arg(highs)::float8[]) WITH ORDINALITY AS high(v, i) ON high.i = id.i
JOIN unnest(sqlc.arg(counts)::int[]) WITH ORDINALITY AS cnt(v, i) ON cnt.i = id.i
ON CONFLICT (series_id, hour) DO UPDATE SET
    median = excluded.median, mad = excluded.mad, low = excluded.low, high = excluded.high,
    points = excluded.points, computed_at = excluded.computed_at;

-- name: DeleteOtherAnomalyBaselines :exec
-- The rows of the hour of series no longer scored (review M4-30): a series that stopped would
-- keep its old row and make the hour look stale for good.
DELETE FROM anomaly_baselines WHERE hour = sqlc.arg(hour) AND series_id <> ALL(coalesce(sqlc.arg(series_ids)::bigint[], '{}'));

-- name: AnomalyBaselineAges :many
-- When each hour of the week was last computed (hours without a row are missing).
SELECT hour, min(computed_at)::timestamptz AS computed_at FROM anomaly_baselines GROUP BY hour;

-- name: InsertAnomalyPoints :one
-- The notable minutes; a minute already stored (a second attempt) stays. Only through the
-- function (the hypertable has no RLS, M4-31): series of the current organization, finite scores.
SELECT anomaly_points_write(sqlc.arg(times)::timestamptz[], sqlc.arg(series_ids)::bigint[], sqlc.arg(scores)::float4[], sqlc.arg(anomalous)::bool[])::bigint AS written;

-- name: AnomalyPointsRange :many
SELECT series_id::bigint AS series_id, ts::timestamptz AS ts, score::real AS score, anomalous::boolean AS anomalous
FROM anomaly_points_range(sqlc.arg(series_ids)::bigint[], sqlc.arg(from_ts)::timestamptz, sqlc.arg(to_ts)::timestamptz, sqlc.arg(max_points)::int);

-- name: AnomalyTop :many
SELECT series_id::bigint AS series_id, max_score::real AS max_score, peak::timestamptz AS peak, anomalous_minutes::bigint AS anomalous_minutes
FROM anomaly_top(sqlc.arg(series_ids)::bigint[], sqlc.arg(from_ts)::timestamptz, sqlc.arg(to_ts)::timestamptz, sqlc.arg(max_series)::int);

-- name: ScopedAnomalySeries :many
-- The candidate series of the scope's nodes (all nodes or the given ones), for the readers.
SELECT s.id, s.node_id, s.metric, s.labels
FROM series s JOIN nodes n ON n.org_id = s.org_id AND n.id = s.node_id
WHERE s.metric = ANY (sqlc.arg(metrics)::text[])
  AND (coalesce(cardinality(sqlc.arg(node_ids)::uuid[]), 0) = 0 OR s.node_id = ANY (sqlc.arg(node_ids)::uuid[]))
  AND (sqlc.arg(all_groups)::boolean OR n.group_id = ANY (sqlc.arg(group_ids)::uuid[]))
ORDER BY s.node_id, s.id
LIMIT sqlc.arg(max_series);

-- name: NodeLearning :one
-- When the node of the scope began to learn (no row: not visible).
SELECT n.learning_since FROM nodes n
WHERE n.id = sqlc.arg(node_id)
  AND (sqlc.arg(all_groups)::boolean OR n.group_id = ANY (sqlc.arg(group_ids)::uuid[]));
