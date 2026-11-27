-- Columnstore for the metric aggregates (V1-21, ADR 0001 addendum): without it metrics_1m was the
-- largest part of the database after 30 days (load test V1-08).
--
-- Segmented by series and ordered by bucket like metrics_raw: the readers (metric_points_1m,
-- metric_points_1h) filter by series and time range, so a chart decompresses only the segments of
-- its series. The limits leave the refresh windows (15 min and 3 h) in uncompressed chunks;
-- refreshes and the purge of nodes and organizations still work on compressed ones.
--
-- metrics_1h gets 7-day chunks instead of 10 h (ten times the chunk of metrics_1m): with 10 h a
-- segment held ten rows per series and compressed badly. Only new chunks get the new interval.

-- +goose Up
SELECT set_chunk_time_interval('metrics_1h', INTERVAL '7 days');
ALTER MATERIALIZED VIEW metrics_1m SET (
    timescaledb.enable_columnstore = true,
    timescaledb.segmentby = 'series_id',
    timescaledb.orderby = 'bucket'
);
ALTER MATERIALIZED VIEW metrics_1h SET (
    timescaledb.enable_columnstore = true,
    timescaledb.segmentby = 'series_id',
    timescaledb.orderby = 'bucket'
);
CALL add_columnstore_policy('metrics_1m', after => INTERVAL '6 hours');
CALL add_columnstore_policy('metrics_1h', after => INTERVAL '1 day');

-- +goose Down
CALL remove_columnstore_policy('metrics_1h');
CALL remove_columnstore_policy('metrics_1m');
SELECT decompress_chunk(c, if_compressed => true) FROM show_chunks('metrics_1h') c;
SELECT decompress_chunk(c, if_compressed => true) FROM show_chunks('metrics_1m') c;
ALTER MATERIALIZED VIEW metrics_1h SET (timescaledb.enable_columnstore = false);
ALTER MATERIALIZED VIEW metrics_1m SET (timescaledb.enable_columnstore = false);
SELECT set_chunk_time_interval('metrics_1h', INTERVAL '10 hours');
