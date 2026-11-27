-- Counts of the log lines (V1-24, ADR 0044): the histogram of the log explorer over long ranges
-- without counting every line in Loki again. The ingest adds what Loki stored per node, source,
-- severity and minute; a bucket k holds the lines at k − 1 min < at ≤ k (as Loki's count_over_time
-- counts a step), the hourly table the same per hour.
--
-- Like the metrics (ADR 0001, O8) the hypertables have no row-level security (not supported on
-- the columnstore): sermon_app may not touch them, it writes, reads and deletes only through the
-- SECURITY DEFINER functions below, which act in the current organization only.
--
-- log_count_marks holds the highest counted batch per node: a batch resent after a lost
-- acknowledgement is counted once, as Loki keeps it once.
--
-- Nodes and organizations take their counts along (foreign key); the backup leaves them out like
-- the logs in Loki.

-- +goose Up
CREATE TABLE log_counts_1m (
    bucket   timestamptz NOT NULL,
    org_id   uuid        NOT NULL,
    node_id  uuid        NOT NULL,
    source   smallint    NOT NULL CHECK (source BETWEEN 1 AND 3),
    severity smallint    NOT NULL CHECK (severity BETWEEN 0 AND 8),
    lines    bigint      NOT NULL CHECK (lines > 0),
    UNIQUE (node_id, source, severity, bucket),
    FOREIGN KEY (org_id, node_id) REFERENCES nodes (org_id, id) ON DELETE CASCADE
);
CREATE TABLE log_counts_1h (
    bucket   timestamptz NOT NULL,
    org_id   uuid        NOT NULL,
    node_id  uuid        NOT NULL,
    source   smallint    NOT NULL CHECK (source BETWEEN 1 AND 3),
    severity smallint    NOT NULL CHECK (severity BETWEEN 0 AND 8),
    lines    bigint      NOT NULL CHECK (lines > 0),
    UNIQUE (node_id, source, severity, bucket),
    FOREIGN KEY (org_id, node_id) REFERENCES nodes (org_id, id) ON DELETE CASCADE
);
SELECT create_hypertable('log_counts_1m', by_range('bucket', INTERVAL '1 day'));
SELECT create_hypertable('log_counts_1h', by_range('bucket', INTERVAL '1 day'));
ALTER TABLE log_counts_1m SET (
    timescaledb.enable_columnstore = true,
    timescaledb.segmentby = 'node_id',
    timescaledb.orderby = 'bucket, source, severity'
);
ALTER TABLE log_counts_1h SET (
    timescaledb.enable_columnstore = true,
    timescaledb.segmentby = 'node_id',
    timescaledb.orderby = 'bucket, source, severity'
);
-- Lines from an agent's buffer come up to 7 days late: they go into compressed chunks then.
CALL add_columnstore_policy('log_counts_1m', after => INTERVAL '1 day');
CALL add_columnstore_policy('log_counts_1h', after => INTERVAL '1 day');
-- As long as Loki keeps the lines (14 days, D12); whole chunks, so up to a day longer.
SELECT add_retention_policy('log_counts_1m', drop_after => INTERVAL '14 days');
SELECT add_retention_policy('log_counts_1h', drop_after => INTERVAL '14 days');

CREATE TABLE log_count_marks (
    org_id  uuid   NOT NULL,
    node_id uuid   PRIMARY KEY,
    seq     bigint NOT NULL CHECK (seq >= 0),
    FOREIGN KEY (org_id, node_id) REFERENCES nodes (org_id, id) ON DELETE CASCADE
);
ALTER TABLE log_count_marks ENABLE ROW LEVEL SECURITY;
ALTER TABLE log_count_marks FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON log_count_marks
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());

GRANT CREATE ON SCHEMA public TO sermon_definer;
GRANT SELECT, INSERT, UPDATE, DELETE ON log_counts_1m, log_counts_1h TO sermon_definer;
GRANT SELECT, INSERT, UPDATE ON log_count_marks TO sermon_definer;

-- Adds the counts of a node of the current organization; rows of batches at or below the node's
-- mark are skipped. The buckets are rounded up to the minute and the hour here, whatever the
-- caller sends. Returns the lines counted.
-- +goose StatementBegin
CREATE FUNCTION log_counts_add(p_node uuid, p_seq bigint[], p_at timestamptz[], p_source smallint[], p_severity smallint[], p_lines bigint[])
    RETURNS bigint
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
DECLARE
    v_org   uuid := public.current_org_id();
    v_mark  bigint;
    v_lines bigint;
BEGIN
    -- sermon_definer reads every node (definer_read): the organization is checked here.
    IF v_org IS NULL OR NOT EXISTS (SELECT 1 FROM public.nodes WHERE org_id = v_org AND id = p_node) THEN
        RAISE EXCEPTION 'node % is not in the current organization', p_node USING ERRCODE = '42501';
    END IF;
    IF cardinality(p_seq) IS DISTINCT FROM cardinality(p_at) OR cardinality(p_seq) IS DISTINCT FROM cardinality(p_source)
       OR cardinality(p_seq) IS DISTINCT FROM cardinality(p_severity) OR cardinality(p_seq) IS DISTINCT FROM cardinality(p_lines) THEN
        RAISE EXCEPTION 'log counts of different lengths' USING ERRCODE = '22023';
    END IF;
    INSERT INTO public.log_count_marks (org_id, node_id, seq) VALUES (v_org, p_node, 0)
    ON CONFLICT (node_id) DO NOTHING;
    SELECT seq INTO v_mark FROM public.log_count_marks WHERE node_id = p_node FOR UPDATE;

    WITH n AS (
        SELECT public.time_bucket(INTERVAL '1 minute', u.at - INTERVAL '1 microsecond', TIMESTAMPTZ '1970-01-01 00:00:00+00') + INTERVAL '1 minute' AS bucket,
               u.source, u.severity, sum(u.lines)::bigint AS lines
        FROM unnest(p_seq, p_at, p_source, p_severity, p_lines) AS u(seq, at, source, severity, lines)
        WHERE u.seq > v_mark
        GROUP BY 1, 2, 3
    ), minutes AS (
        INSERT INTO public.log_counts_1m AS c (bucket, org_id, node_id, source, severity, lines)
        SELECT n.bucket, v_org, p_node, n.source, n.severity, n.lines FROM n
        ON CONFLICT (node_id, source, severity, bucket) DO UPDATE SET lines = c.lines + EXCLUDED.lines
    ), hours AS (
        INSERT INTO public.log_counts_1h AS c (bucket, org_id, node_id, source, severity, lines)
        SELECT public.time_bucket(INTERVAL '1 hour', n.bucket - INTERVAL '1 microsecond', TIMESTAMPTZ '1970-01-01 00:00:00+00') + INTERVAL '1 hour',
               v_org, p_node, n.source, n.severity, sum(n.lines)
        FROM n
        GROUP BY 1, 4, 5
        ON CONFLICT (node_id, source, severity, bucket) DO UPDATE SET lines = c.lines + EXCLUDED.lines
    )
    SELECT coalesce(sum(n.lines), 0) INTO v_lines FROM n;

    UPDATE public.log_count_marks SET seq = greatest(seq, (SELECT coalesce(max(s), 0) FROM unnest(p_seq) s))
    WHERE node_id = p_node;
    RETURN v_lines;
END
$$;
-- +goose StatementEnd

-- The counts of the given nodes of the current organization per severity at the points
-- p_after < t ≤ p_until, multiples of the step since the epoch: a point holds the buckets in
-- (t − step, t]. Steps of whole hours read the hourly table, other whole minutes the minute table;
-- anything else returns nothing. Empty sources or severities mean all.
CREATE FUNCTION log_counts_range(p_nodes uuid[], p_after timestamptz, p_until timestamptz, p_step_seconds integer, p_sources smallint[], p_severities smallint[])
    RETURNS TABLE (at timestamptz, severity smallint, lines bigint)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT public.time_bucket(make_interval(secs => p_step_seconds), c.bucket - INTERVAL '1 microsecond', TIMESTAMPTZ '1970-01-01 00:00:00+00')
                   + make_interval(secs => p_step_seconds) AS at,
               c.severity, sum(c.lines)::bigint
        FROM (
            SELECT h.bucket, h.org_id, h.node_id, h.source, h.severity, h.lines FROM public.log_counts_1h h
            WHERE p_step_seconds > 0 AND p_step_seconds % 3600 = 0
            UNION ALL
            SELECT m.bucket, m.org_id, m.node_id, m.source, m.severity, m.lines FROM public.log_counts_1m m
            WHERE p_step_seconds > 0 AND p_step_seconds % 60 = 0 AND p_step_seconds % 3600 <> 0
        ) c
        WHERE c.org_id = public.current_org_id()
          AND c.node_id = ANY (p_nodes)
          AND c.bucket > p_after AND c.bucket <= p_until
          AND (cardinality(p_sources) = 0 OR c.source = ANY (p_sources))
          AND (cardinality(p_severities) = 0 OR c.severity = ANY (p_severities))
        GROUP BY 1, 2
        ORDER BY 2, 1
    $$;

-- Deletes the current organization's counts up to p_before (its own retention, with the lines in
-- Loki); returns the rows deleted.
-- +goose StatementBegin
CREATE FUNCTION log_counts_expire(p_before timestamptz) RETURNS bigint
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
DECLARE
    v_org     uuid := public.current_org_id();
    v_minutes bigint;
    v_hours   bigint;
BEGIN
    IF v_org IS NULL THEN
        RAISE EXCEPTION 'log_counts_expire outside a tenant' USING ERRCODE = '42501';
    END IF;
    DELETE FROM public.log_counts_1m WHERE org_id = v_org AND bucket <= p_before;
    GET DIAGNOSTICS v_minutes = ROW_COUNT;
    DELETE FROM public.log_counts_1h WHERE org_id = v_org AND bucket <= p_before;
    GET DIAGNOSTICS v_hours = ROW_COUNT;
    RETURN v_minutes + v_hours;
END
$$;
-- +goose StatementEnd

ALTER FUNCTION log_counts_add(uuid, bigint[], timestamptz[], smallint[], smallint[], bigint[]) OWNER TO sermon_definer;
ALTER FUNCTION log_counts_range(uuid[], timestamptz, timestamptz, integer, smallint[], smallint[]) OWNER TO sermon_definer;
ALTER FUNCTION log_counts_expire(timestamptz) OWNER TO sermon_definer;
REVOKE ALL ON FUNCTION log_counts_add(uuid, bigint[], timestamptz[], smallint[], smallint[], bigint[]) FROM PUBLIC;
REVOKE ALL ON FUNCTION log_counts_range(uuid[], timestamptz, timestamptz, integer, smallint[], smallint[]) FROM PUBLIC;
REVOKE ALL ON FUNCTION log_counts_expire(timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION log_counts_add(uuid, bigint[], timestamptz[], smallint[], smallint[], bigint[]) TO sermon_app;
GRANT EXECUTE ON FUNCTION log_counts_range(uuid[], timestamptz, timestamptz, integer, smallint[], smallint[]) TO sermon_app;
GRANT EXECUTE ON FUNCTION log_counts_expire(timestamptz) TO sermon_app;
REVOKE CREATE ON SCHEMA public FROM sermon_definer;

-- +goose Down
DROP FUNCTION log_counts_expire(timestamptz);
DROP FUNCTION log_counts_range(uuid[], timestamptz, timestamptz, integer, smallint[], smallint[]);
DROP FUNCTION log_counts_add(uuid, bigint[], timestamptz[], smallint[], smallint[], bigint[]);
DROP TABLE log_count_marks;
DROP TABLE log_counts_1h;
DROP TABLE log_counts_1m;
