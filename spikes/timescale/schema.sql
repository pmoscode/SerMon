-- M0-09 spike schema (ADR 0001): narrow metrics table, columnstore compression, continuous
-- aggregates. Basis for the MetricStore migration in M1-22.

CREATE TABLE series (
    id      integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id  uuid    NOT NULL,
    node_id uuid    NOT NULL,
    metric  text    NOT NULL,
    labels  jsonb   NOT NULL DEFAULT '{}',
    UNIQUE (org_id, node_id, metric, labels)
);
CREATE INDEX series_node_idx ON series (org_id, node_id);

CREATE TABLE metrics_raw (
    ts        timestamptz      NOT NULL,
    series_id integer          NOT NULL,
    value     double precision NOT NULL
);
SELECT create_hypertable('metrics_raw', by_range('ts', INTERVAL '1 hour'));
ALTER TABLE metrics_raw SET (
    timescaledb.enable_columnstore = true,
    timescaledb.segmentby = 'series_id',
    timescaledb.orderby = 'ts'
);
CALL add_columnstore_policy('metrics_raw', after => INTERVAL '2 hours');

CREATE MATERIALIZED VIEW metrics_1m WITH (timescaledb.continuous) AS
SELECT time_bucket(INTERVAL '1 minute', ts) AS bucket, series_id,
       avg(value) AS avg, min(value) AS min, max(value) AS max, last(value, ts) AS last
FROM metrics_raw
GROUP BY bucket, series_id
WITH NO DATA;
SELECT add_continuous_aggregate_policy('metrics_1m',
    start_offset => INTERVAL '15 minutes', end_offset => INTERVAL '1 minute',
    schedule_interval => INTERVAL '1 minute');

CREATE MATERIALIZED VIEW metrics_1h WITH (timescaledb.continuous) AS
SELECT time_bucket(INTERVAL '1 hour', bucket) AS bucket, series_id,
       avg(avg) AS avg, min(min) AS min, max(max) AS max, last(last, bucket) AS last
FROM metrics_1m
GROUP BY 1, series_id
WITH NO DATA;
SELECT add_continuous_aggregate_policy('metrics_1h',
    start_offset => INTERVAL '3 hours', end_offset => INTERVAL '1 hour',
    schedule_interval => INTERVAL '15 minutes');
