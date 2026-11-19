-- Metric storage (KONZEPT §4.5, ADR 0001, M1-22): series with row-level security, 10 s windows in
-- a hypertable, continuous aggregates for 1 min and 1 h.
--
-- Tenant isolation without RLS on the TimescaleDB objects (not supported on columnstore and
-- continuous aggregates, spike M0-09): sermon_app may write metrics_raw but never read any metric
-- table. Reading goes through SECURITY DEFINER functions owned by sermon_definer, which has no
-- definer_read policy on series – the tenant policy applies to it, so the functions only return
-- points of series of the current organization.
--
-- Continuous aggregates can't be created inside a transaction.
-- +goose NO TRANSACTION

-- +goose Up
CREATE EXTENSION IF NOT EXISTS timescaledb;

CREATE TABLE series (
    id         integer     GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id     uuid        NOT NULL,
    node_id    uuid        NOT NULL,
    metric     text        NOT NULL CHECK (metric ~ '^sermon_[a-z0-9_]{1,120}$'),
    labels     jsonb       NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(labels) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, node_id, metric, labels),
    FOREIGN KEY (org_id, node_id) REFERENCES nodes (org_id, id) ON DELETE CASCADE
);
CREATE INDEX series_metric_idx ON series (org_id, metric, node_id);
ALTER TABLE series ENABLE ROW LEVEL SECURITY;
ALTER TABLE series FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON series
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT ON series TO sermon_app;

-- One row per series and 10 s window, aggregated by the server from the agent's 1 s values.
CREATE TABLE metrics_raw (
    ts        timestamptz      NOT NULL,
    series_id integer          NOT NULL,
    avg       double precision NOT NULL,
    min       double precision NOT NULL,
    max       double precision NOT NULL
);
SELECT create_hypertable('metrics_raw', by_range('ts', INTERVAL '1 hour'));
-- Without this index a chart query scans every series of the chunk (720 ms instead of 16 ms).
CREATE INDEX metrics_raw_series_ts_idx ON metrics_raw (series_id, ts DESC);
ALTER TABLE metrics_raw SET (
    timescaledb.enable_columnstore = true,
    timescaledb.segmentby = 'series_id',
    timescaledb.orderby = 'ts'
);
CALL add_columnstore_policy('metrics_raw', after => INTERVAL '2 hours');
SELECT add_retention_policy('metrics_raw', drop_after => INTERVAL '24 hours');
GRANT INSERT ON metrics_raw TO sermon_app;

CREATE MATERIALIZED VIEW metrics_1m WITH (timescaledb.continuous, timescaledb.materialized_only = true) AS
SELECT time_bucket(INTERVAL '1 minute', ts) AS bucket, series_id,
       avg(avg) AS avg, min(min) AS min, max(max) AS max, last(avg, ts) AS last
FROM metrics_raw
GROUP BY bucket, series_id
WITH NO DATA;
SELECT add_continuous_aggregate_policy('metrics_1m',
    start_offset => INTERVAL '15 minutes', end_offset => INTERVAL '1 minute',
    schedule_interval => INTERVAL '1 minute');
SELECT add_retention_policy('metrics_1m', drop_after => INTERVAL '30 days');

CREATE MATERIALIZED VIEW metrics_1h WITH (timescaledb.continuous, timescaledb.materialized_only = true) AS
SELECT time_bucket(INTERVAL '1 hour', bucket) AS bucket, series_id,
       avg(avg) AS avg, min(min) AS min, max(max) AS max, last(last, bucket) AS last
FROM metrics_1m
GROUP BY 1, series_id
WITH NO DATA;
SELECT add_continuous_aggregate_policy('metrics_1h',
    start_offset => INTERVAL '3 hours', end_offset => INTERVAL '1 hour',
    schedule_interval => INTERVAL '15 minutes');
SELECT add_retention_policy('metrics_1h', drop_after => INTERVAL '365 days');

-- Ingest state per node: the highest batch sequence number persisted (deduplication across
-- reconnects and server restarts, KONZEPT §5), last contact and agent version.
ALTER TABLE nodes
    ADD COLUMN ingest_seq    bigint      NOT NULL DEFAULT 0 CHECK (ingest_seq >= 0),
    ADD COLUMN last_seen_at  timestamptz,
    ADD COLUMN agent_version text        CHECK (char_length(agent_version) <= 64);

-- Readers: points of the given series, bucketed by p_step. Series of other organizations are
-- filtered out by the tenant policy on series (the owner sermon_definer has no exception there).
GRANT CREATE ON SCHEMA public TO sermon_definer;
GRANT SELECT ON series, metrics_raw, metrics_1m, metrics_1h TO sermon_definer;

CREATE FUNCTION metric_points_raw(p_series integer[], p_from timestamptz, p_to timestamptz, p_step interval)
    RETURNS TABLE (series_id integer, bucket timestamptz, avg double precision, min double precision, max double precision)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT r.series_id, public.time_bucket(p_step, r.ts), avg(r.avg), min(r.min), max(r.max)
        FROM public.metrics_raw r
        WHERE r.series_id IN (SELECT s.id FROM public.series s WHERE s.id = ANY (p_series))
          AND r.ts >= p_from AND r.ts < p_to
        GROUP BY 1, 2
        ORDER BY 1, 2
    $$;

CREATE FUNCTION metric_points_1m(p_series integer[], p_from timestamptz, p_to timestamptz, p_step interval)
    RETURNS TABLE (series_id integer, bucket timestamptz, avg double precision, min double precision, max double precision)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT m.series_id, public.time_bucket(p_step, m.bucket), avg(m.avg), min(m.min), max(m.max)
        FROM public.metrics_1m m
        WHERE m.series_id IN (SELECT s.id FROM public.series s WHERE s.id = ANY (p_series))
          AND m.bucket >= p_from AND m.bucket < p_to
        GROUP BY 1, 2
        ORDER BY 1, 2
    $$;

CREATE FUNCTION metric_points_1h(p_series integer[], p_from timestamptz, p_to timestamptz, p_step interval)
    RETURNS TABLE (series_id integer, bucket timestamptz, avg double precision, min double precision, max double precision)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT m.series_id, public.time_bucket(p_step, m.bucket), avg(m.avg), min(m.min), max(m.max)
        FROM public.metrics_1h m
        WHERE m.series_id IN (SELECT s.id FROM public.series s WHERE s.id = ANY (p_series))
          AND m.bucket >= p_from AND m.bucket < p_to
        GROUP BY 1, 2
        ORDER BY 1, 2
    $$;

ALTER FUNCTION metric_points_raw(integer[], timestamptz, timestamptz, interval) OWNER TO sermon_definer;
ALTER FUNCTION metric_points_1m(integer[], timestamptz, timestamptz, interval) OWNER TO sermon_definer;
ALTER FUNCTION metric_points_1h(integer[], timestamptz, timestamptz, interval) OWNER TO sermon_definer;
REVOKE ALL ON FUNCTION metric_points_raw(integer[], timestamptz, timestamptz, interval) FROM PUBLIC;
REVOKE ALL ON FUNCTION metric_points_1m(integer[], timestamptz, timestamptz, interval) FROM PUBLIC;
REVOKE ALL ON FUNCTION metric_points_1h(integer[], timestamptz, timestamptz, interval) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION metric_points_raw(integer[], timestamptz, timestamptz, interval) TO sermon_app;
GRANT EXECUTE ON FUNCTION metric_points_1m(integer[], timestamptz, timestamptz, interval) TO sermon_app;
GRANT EXECUTE ON FUNCTION metric_points_1h(integer[], timestamptz, timestamptz, interval) TO sermon_app;
REVOKE CREATE ON SCHEMA public FROM sermon_definer;

-- +goose Down
DROP FUNCTION metric_points_1h(integer[], timestamptz, timestamptz, interval);
DROP FUNCTION metric_points_1m(integer[], timestamptz, timestamptz, interval);
DROP FUNCTION metric_points_raw(integer[], timestamptz, timestamptz, interval);
ALTER TABLE nodes DROP COLUMN agent_version, DROP COLUMN last_seen_at, DROP COLUMN ingest_seq;
DROP MATERIALIZED VIEW metrics_1h;
DROP MATERIALIZED VIEW metrics_1m;
DROP TABLE metrics_raw;
DROP TABLE series;
