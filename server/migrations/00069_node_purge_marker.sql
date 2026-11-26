-- Deleting nodes, checked before the logs go (M5-33, ADR 0041 addendum):
--   * purge_started_at marks a node the job deletes: set under the row lock while the node is
--     deactivated and due, before Loki is asked to delete its logs. A cancellation that came
--     first leaves the node unmarked (its logs stay); one that comes later is refused;
--   * node_purge deletes only a marked node and only within the caller's tenant: the
--     transaction's organization must be p_org (sermon_app can't reach across tenants).

-- +goose Up
ALTER TABLE nodes
    ADD COLUMN purge_started_at timestamptz,
    ADD CONSTRAINT nodes_purge_scheduled CHECK (purge_started_at IS NULL OR deletion_due_at IS NOT NULL);

GRANT CREATE ON SCHEMA public TO sermon_definer;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION node_purge(p_org uuid, p_node uuid, p_at timestamptz) RETURNS text
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
DECLARE
    v_series   bigint[];
    v_agg      record;
    v_hostname text;
BEGIN
    IF current_setting('sermon.org_id', true) IS DISTINCT FROM p_org::text THEN
        RAISE EXCEPTION 'node_purge outside the tenant of organization %', p_org USING ERRCODE = '42501';
    END IF;
    -- Lock the row before checking: a cancellation in flight finishes first and is seen.
    SELECT hostname INTO v_hostname FROM public.nodes
    WHERE org_id = p_org AND id = p_node AND deactivated_at IS NOT NULL AND deletion_due_at <= p_at
      AND purge_started_at IS NOT NULL
    FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'node % is not deactivated and due for deletion', p_node USING ERRCODE = 'P0001';
    END IF;
    SELECT array_agg(id) INTO v_series FROM public.series WHERE org_id = p_org AND node_id = p_node;
    IF v_series IS NOT NULL THEN
        DELETE FROM public.metrics_raw WHERE series_id = ANY (v_series);
        DELETE FROM public.anomaly_points WHERE series_id = ANY (v_series);
        FOR v_agg IN SELECT materialization_hypertable_schema AS s, materialization_hypertable_name AS n
                     FROM timescaledb_information.continuous_aggregates LOOP
            EXECUTE format('DELETE FROM %I.%I WHERE series_id = ANY ($1)', v_agg.s, v_agg.n) USING v_series;
        END LOOP;
    END IF;
    DELETE FROM public.security_events WHERE org_id = p_org AND node_id = p_node;
    DELETE FROM public.alert_history WHERE org_id = p_org AND node_id = p_node;
    -- Certificates, series, alerts, update state, commands, login sources: by foreign key.
    DELETE FROM public.nodes WHERE org_id = p_org AND id = p_node;
    RETURN v_hostname;
END
$$;
-- +goose StatementEnd

REVOKE CREATE ON SCHEMA public FROM sermon_definer;

-- +goose Down
GRANT CREATE ON SCHEMA public TO sermon_definer;
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION node_purge(p_org uuid, p_node uuid, p_at timestamptz) RETURNS text
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
DECLARE
    v_series   bigint[];
    v_agg      record;
    v_hostname text;
BEGIN
    PERFORM set_config('sermon.org_id', p_org::text, true);
    SELECT hostname INTO v_hostname FROM public.nodes
    WHERE org_id = p_org AND id = p_node AND deactivated_at IS NOT NULL AND deletion_due_at <= p_at
    FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'node % is not deactivated and due for deletion', p_node USING ERRCODE = 'P0001';
    END IF;
    SELECT array_agg(id) INTO v_series FROM public.series WHERE org_id = p_org AND node_id = p_node;
    IF v_series IS NOT NULL THEN
        DELETE FROM public.metrics_raw WHERE series_id = ANY (v_series);
        DELETE FROM public.anomaly_points WHERE series_id = ANY (v_series);
        FOR v_agg IN SELECT materialization_hypertable_schema AS s, materialization_hypertable_name AS n
                     FROM timescaledb_information.continuous_aggregates LOOP
            EXECUTE format('DELETE FROM %I.%I WHERE series_id = ANY ($1)', v_agg.s, v_agg.n) USING v_series;
        END LOOP;
    END IF;
    DELETE FROM public.security_events WHERE org_id = p_org AND node_id = p_node;
    DELETE FROM public.alert_history WHERE org_id = p_org AND node_id = p_node;
    DELETE FROM public.nodes WHERE org_id = p_org AND id = p_node;
    RETURN v_hostname;
END
$$;
-- +goose StatementEnd
REVOKE CREATE ON SCHEMA public FROM sermon_definer;
ALTER TABLE nodes DROP CONSTRAINT nodes_purge_scheduled, DROP COLUMN purge_started_at;
