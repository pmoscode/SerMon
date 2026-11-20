-- When a series last got data (M1-36): the ingest's series limit counts the active series of a
-- node across sessions and server instances, so that reconnecting doesn't buy a fresh budget,
-- while a series that stopped (a removed disk) frees its place. The store updates the time with
-- the data's time, at most every few hours per series (metrics.SeenResolution); sermon_app may
-- change only this column.

-- +goose Up
-- Databases created before M1-36 still have integer series IDs from the old 00013 (changed in
-- place before the first release): fail with a clear message instead of broken charts.
-- +goose StatementBegin
DO $$
BEGIN
    IF (SELECT data_type FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'series' AND column_name = 'id') <> 'bigint' THEN
        RAISE EXCEPTION 'series.id is not bigint: this database predates M1-36, which changed migration 00013 – recreate it (development: docker compose -f deploy/dev/compose.yaml down -v)';
    END IF;
END
$$;
-- +goose StatementEnd
ALTER TABLE series ADD COLUMN seen_at timestamptz NOT NULL DEFAULT now();
CREATE INDEX series_seen_idx ON series (org_id, seen_at);
GRANT UPDATE (seen_at) ON series TO sermon_app;

-- +goose Down
REVOKE UPDATE (seen_at) ON series FROM sermon_app;
DROP INDEX series_seen_idx;
ALTER TABLE series DROP COLUMN seen_at;
