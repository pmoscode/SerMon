-- Hardening of the anomaly detection after the review M4-30 (M4-31, ADR 0037): the notable minutes
-- are written only through a SECURITY DEFINER function (the hypertable has no RLS, O8), with
-- finite scores of at most 1000; "what was unusual?" finds the peak of its top series only instead
-- of collecting every minute in an array; the definer functions bound their arguments.

-- +goose Up
GRANT CREATE ON SCHEMA public TO sermon_definer;

-- The notable minutes of the current organization's series; minutes already stored (a second
-- attempt) stay. Returns how many were written.
-- +goose StatementBegin
CREATE FUNCTION anomaly_points_write(p_times timestamptz[], p_series bigint[], p_scores real[], p_anomalous boolean[])
    RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
DECLARE
    v_count bigint;
BEGIN
    IF coalesce(cardinality(p_times), 0) > 100000
       OR coalesce(cardinality(p_series), 0) <> coalesce(cardinality(p_times), 0)
       OR coalesce(cardinality(p_scores), 0) <> coalesce(cardinality(p_times), 0)
       OR coalesce(cardinality(p_anomalous), 0) <> coalesce(cardinality(p_times), 0) THEN
        RAISE EXCEPTION 'anomaly_points_write: at most 100000 minutes, arrays of equal length' USING ERRCODE = '22023';
    END IF;
    INSERT INTO public.anomaly_points (ts, series_id, score, anomalous)
    SELECT x.ts, x.series_id, x.score, x.anomalous
    FROM unnest(p_times, p_series, p_scores, p_anomalous) AS x(ts, series_id, score, anomalous)
    -- The tenant policy on series leaves the current organization's.
    WHERE x.series_id IN (SELECT s.id FROM public.series s)
      AND x.ts IS NOT NULL AND x.anomalous IS NOT NULL
      -- NaN compares greater than every number in PostgreSQL: 'NaN' is ruled out by the bound.
      AND x.score >= 0 AND x.score <= 1000
    ON CONFLICT DO NOTHING;
    GET DIAGNOSTICS v_count = ROW_COUNT;
    RETURN v_count;
END
$$;
-- +goose StatementEnd

-- At most 1000 series in the answer, at most 20000 asked for; the peak only of those.
CREATE OR REPLACE FUNCTION anomaly_top(p_series bigint[], p_from timestamptz, p_to timestamptz, p_limit integer)
    RETURNS TABLE (series_id bigint, max_score real, peak timestamptz, anomalous_minutes bigint)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        WITH top AS (
            SELECT a.series_id, max(a.score) AS max_score, count(*) FILTER (WHERE a.anomalous) AS minutes
            FROM public.anomaly_points a
            WHERE a.series_id IN (SELECT s.id FROM public.series s WHERE s.id = ANY (p_series[1:20000]))
              AND a.ts >= p_from AND a.ts < p_to
            GROUP BY a.series_id
            ORDER BY 2 DESC, 1
            LIMIT least(greatest(p_limit, 0), 1000)
        )
        SELECT top.series_id, top.max_score, peak.ts, top.minutes
        FROM top CROSS JOIN LATERAL (
            SELECT a.ts FROM public.anomaly_points a
            WHERE a.series_id = top.series_id AND a.ts >= p_from AND a.ts < p_to
            ORDER BY a.score DESC, a.ts
            LIMIT 1
        ) peak
        ORDER BY 2 DESC, 1
    $$;

-- At most 100000 minutes of at most 20000 series.
CREATE OR REPLACE FUNCTION anomaly_points_range(p_series bigint[], p_from timestamptz, p_to timestamptz, p_limit integer)
    RETURNS TABLE (series_id bigint, ts timestamptz, score real, anomalous boolean)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT a.series_id, a.ts, a.score, a.anomalous
        FROM public.anomaly_points a
        WHERE a.series_id IN (SELECT s.id FROM public.series s WHERE s.id = ANY (p_series[1:20000]))
          AND a.ts >= p_from AND a.ts < p_to
        ORDER BY a.series_id, a.ts
        LIMIT least(greatest(p_limit, 0), 100000)
    $$;

ALTER FUNCTION anomaly_points_write(timestamptz[], bigint[], real[], boolean[]) OWNER TO sermon_definer;
REVOKE ALL ON FUNCTION anomaly_points_write(timestamptz[], bigint[], real[], boolean[]) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION anomaly_points_write(timestamptz[], bigint[], real[], boolean[]) TO sermon_app;
GRANT INSERT ON anomaly_points TO sermon_definer;
REVOKE INSERT ON anomaly_points FROM sermon_app;
REVOKE CREATE ON SCHEMA public FROM sermon_definer;

-- +goose Down
GRANT CREATE ON SCHEMA public TO sermon_definer;
GRANT INSERT ON anomaly_points TO sermon_app;
REVOKE INSERT ON anomaly_points FROM sermon_definer;
DROP FUNCTION anomaly_points_write(timestamptz[], bigint[], real[], boolean[]);
CREATE OR REPLACE FUNCTION anomaly_top(p_series bigint[], p_from timestamptz, p_to timestamptz, p_limit integer)
    RETURNS TABLE (series_id bigint, max_score real, peak timestamptz, anomalous_minutes bigint)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT a.series_id, max(a.score), (array_agg(a.ts ORDER BY a.score DESC, a.ts))[1],
               count(*) FILTER (WHERE a.anomalous)
        FROM public.anomaly_points a
        WHERE a.series_id IN (SELECT s.id FROM public.series s WHERE s.id = ANY (p_series))
          AND a.ts >= p_from AND a.ts < p_to
        GROUP BY a.series_id
        ORDER BY 2 DESC, 1
        LIMIT p_limit
    $$;
CREATE OR REPLACE FUNCTION anomaly_points_range(p_series bigint[], p_from timestamptz, p_to timestamptz, p_limit integer)
    RETURNS TABLE (series_id bigint, ts timestamptz, score real, anomalous boolean)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT a.series_id, a.ts, a.score, a.anomalous
        FROM public.anomaly_points a
        WHERE a.series_id IN (SELECT s.id FROM public.series s WHERE s.id = ANY (p_series))
          AND a.ts >= p_from AND a.ts < p_to
        ORDER BY a.series_id, a.ts
        LIMIT p_limit
    $$;
REVOKE CREATE ON SCHEMA public FROM sermon_definer;
