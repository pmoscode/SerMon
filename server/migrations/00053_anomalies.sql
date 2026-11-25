-- Anomaly detection (M4-23, ADR 0037): seasonal baselines per series and hour of the week (with
-- row-level security), the notable minutes in a hypertable without it (read only through SECURITY
-- DEFINER functions, like the metrics, O8), and the worker's lease per organization.

-- +goose Up
CREATE TABLE anomaly_baselines (
    org_id      uuid             NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    series_id   bigint           NOT NULL REFERENCES series (id) ON DELETE CASCADE,
    hour        smallint         NOT NULL CHECK (hour BETWEEN 0 AND 167),
    median      double precision NOT NULL,
    mad         double precision NOT NULL CHECK (mad >= 0),
    low         double precision NOT NULL,
    high        double precision NOT NULL CHECK (high >= low),
    points      integer          NOT NULL CHECK (points >= 0),
    computed_at timestamptz      NOT NULL,
    PRIMARY KEY (series_id, hour)
);
CREATE INDEX anomaly_baselines_org_hour_idx ON anomaly_baselines (org_id, hour);
ALTER TABLE anomaly_baselines ENABLE ROW LEVEL SECURITY;
ALTER TABLE anomaly_baselines FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON anomaly_baselines
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON anomaly_baselines TO sermon_app;

CREATE TABLE anomaly_leases (
    org_id     uuid        PRIMARY KEY REFERENCES organizations (id) ON DELETE CASCADE,
    holder     text        NOT NULL CHECK (char_length(holder) BETWEEN 1 AND 64),
    expires_at timestamptz NOT NULL
);
ALTER TABLE anomaly_leases ENABLE ROW LEVEL SECURITY;
ALTER TABLE anomaly_leases FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON anomaly_leases
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, UPDATE ON anomaly_leases TO sermon_app;

-- Minutes with a score of at least 3 or anomalous; the rest is unremarkable.
CREATE TABLE anomaly_points (
    ts        timestamptz NOT NULL,
    series_id bigint      NOT NULL,
    score     real        NOT NULL CHECK (score >= 0),
    anomalous boolean     NOT NULL,
    UNIQUE (series_id, ts)
);
SELECT create_hypertable('anomaly_points', by_range('ts', INTERVAL '1 day'));
ALTER TABLE anomaly_points SET (
    timescaledb.enable_columnstore = true,
    timescaledb.segmentby = 'series_id',
    timescaledb.orderby = 'ts'
);
CALL add_columnstore_policy('anomaly_points', after => INTERVAL '2 hours');
SELECT add_retention_policy('anomaly_points', drop_after => INTERVAL '30 days');
GRANT INSERT ON anomaly_points TO sermon_app;

GRANT CREATE ON SCHEMA public TO sermon_definer;
GRANT SELECT, DELETE ON anomaly_points TO sermon_definer;

-- The notable minutes of the given series in [p_from, p_to), at most p_limit; series of other
-- organizations are filtered out by the tenant policy on series.
CREATE FUNCTION anomaly_points_range(p_series bigint[], p_from timestamptz, p_to timestamptz, p_limit integer)
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

-- Per series in [p_from, p_to): the highest score, when it was and the anomalous minutes; the
-- p_limit highest first ("what was unusual?").
CREATE FUNCTION anomaly_top(p_series bigint[], p_from timestamptz, p_to timestamptz, p_limit integer)
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

ALTER FUNCTION anomaly_points_range(bigint[], timestamptz, timestamptz, integer) OWNER TO sermon_definer;
ALTER FUNCTION anomaly_top(bigint[], timestamptz, timestamptz, integer) OWNER TO sermon_definer;
REVOKE ALL ON FUNCTION anomaly_points_range(bigint[], timestamptz, timestamptz, integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION anomaly_top(bigint[], timestamptz, timestamptz, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION anomaly_points_range(bigint[], timestamptz, timestamptz, integer) TO sermon_app;
GRANT EXECUTE ON FUNCTION anomaly_top(bigint[], timestamptz, timestamptz, integer) TO sermon_app;

-- org_purge removes the anomaly points of the organization's series as well.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION org_purge(p_org uuid, p_at timestamptz) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
DECLARE
    v_users  uuid[];
    v_series bigint[];
    v_agg    record;
    v_count  bigint;
BEGIN
    -- The tenant policies let this transaction see the organization's rows only.
    PERFORM set_config('sermon.org_id', p_org::text, true);
    -- Lock the row before checking (M2-39): an unlock in flight finishes first and is seen, or it
    -- waits for this deletion and finds nothing.
    PERFORM 1 FROM public.organizations
    WHERE id = p_org AND locked_at IS NOT NULL AND deletion_due_at <= p_at
    FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'organization % is not locked and due for deletion', p_org USING ERRCODE = 'P0001';
    END IF;
    -- Accounts without another membership go along; superadmins and members of other
    -- organizations keep theirs.
    SELECT array_agg(m.user_id) INTO v_users FROM public.memberships m
    WHERE m.org_id = p_org
      AND NOT EXISTS (SELECT 1 FROM public.memberships other WHERE other.user_id = m.user_id AND other.org_id <> p_org)
      AND NOT EXISTS (SELECT 1 FROM public.superadmins s WHERE s.user_id = m.user_id);
    SELECT array_agg(id) INTO v_series FROM public.series WHERE org_id = p_org;
    IF v_series IS NOT NULL THEN
        DELETE FROM public.metrics_raw WHERE series_id = ANY (v_series);
        DELETE FROM public.anomaly_points WHERE series_id = ANY (v_series);
        FOR v_agg IN SELECT materialization_hypertable_schema AS s, materialization_hypertable_name AS n
                     FROM timescaledb_information.continuous_aggregates LOOP
            EXECUTE format('DELETE FROM %I.%I WHERE series_id = ANY ($1)', v_agg.s, v_agg.n) USING v_series;
        END LOOP;
    END IF;
    DELETE FROM public.security_events WHERE org_id = p_org;
    DELETE FROM public.audit_log WHERE org_id = p_org;
    -- Nodes before the organization: their groups refuse to go while nodes point at them.
    DELETE FROM public.nodes WHERE org_id = p_org;
    DELETE FROM public.organizations WHERE id = p_org;
    -- Checked again: someone may have joined another organization meanwhile.
    DELETE FROM public.users u WHERE u.id = ANY (coalesce(v_users, '{}'))
      AND NOT EXISTS (SELECT 1 FROM public.memberships m WHERE m.user_id = u.id);
    GET DIAGNOSTICS v_count = ROW_COUNT;
    RETURN v_count;
END
$$;
-- +goose StatementEnd
ALTER FUNCTION org_purge(uuid, timestamptz) OWNER TO sermon_definer;
REVOKE CREATE ON SCHEMA public FROM sermon_definer;

-- +goose Down
GRANT CREATE ON SCHEMA public TO sermon_definer;
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION org_purge(p_org uuid, p_at timestamptz) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
DECLARE
    v_users  uuid[];
    v_series bigint[];
    v_agg    record;
    v_count  bigint;
BEGIN
    PERFORM set_config('sermon.org_id', p_org::text, true);
    PERFORM 1 FROM public.organizations
    WHERE id = p_org AND locked_at IS NOT NULL AND deletion_due_at <= p_at
    FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'organization % is not locked and due for deletion', p_org USING ERRCODE = 'P0001';
    END IF;
    SELECT array_agg(m.user_id) INTO v_users FROM public.memberships m
    WHERE m.org_id = p_org
      AND NOT EXISTS (SELECT 1 FROM public.memberships other WHERE other.user_id = m.user_id AND other.org_id <> p_org)
      AND NOT EXISTS (SELECT 1 FROM public.superadmins s WHERE s.user_id = m.user_id);
    SELECT array_agg(id) INTO v_series FROM public.series WHERE org_id = p_org;
    IF v_series IS NOT NULL THEN
        DELETE FROM public.metrics_raw WHERE series_id = ANY (v_series);
        FOR v_agg IN SELECT materialization_hypertable_schema AS s, materialization_hypertable_name AS n
                     FROM timescaledb_information.continuous_aggregates LOOP
            EXECUTE format('DELETE FROM %I.%I WHERE series_id = ANY ($1)', v_agg.s, v_agg.n) USING v_series;
        END LOOP;
    END IF;
    DELETE FROM public.security_events WHERE org_id = p_org;
    DELETE FROM public.audit_log WHERE org_id = p_org;
    DELETE FROM public.nodes WHERE org_id = p_org;
    DELETE FROM public.organizations WHERE id = p_org;
    DELETE FROM public.users u WHERE u.id = ANY (coalesce(v_users, '{}'))
      AND NOT EXISTS (SELECT 1 FROM public.memberships m WHERE m.user_id = u.id);
    GET DIAGNOSTICS v_count = ROW_COUNT;
    RETURN v_count;
END
$$;
-- +goose StatementEnd
ALTER FUNCTION org_purge(uuid, timestamptz) OWNER TO sermon_definer;
DROP FUNCTION anomaly_top(bigint[], timestamptz, timestamptz, integer);
DROP FUNCTION anomaly_points_range(bigint[], timestamptz, timestamptz, integer);
REVOKE SELECT, DELETE ON anomaly_points FROM sermon_definer;
REVOKE CREATE ON SCHEMA public FROM sermon_definer;
DROP TABLE anomaly_points;
DROP TABLE anomaly_leases;
DROP TABLE anomaly_baselines;
