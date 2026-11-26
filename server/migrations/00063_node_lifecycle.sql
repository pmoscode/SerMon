-- Deactivating and deleting nodes (M5-29, ADR 0041):
--   * deactivated_at: the node is deactivated – its certificates are revoked and refused, the
--     alert engine skips it; deletion_due_at: it is deleted after the grace (always deactivated);
--   * agent_certificate_usable refuses a deactivated node, also with a certificate not revoked;
--   * node_purge deletes a deactivated node that is due with its metrics, anomaly points,
--     security events and alert history, then the node and what hangs on it by foreign key – in
--     one transaction, the row locked first;
--   * nodes_due_for_deletion lists them across organizations for the hourly job.

-- +goose Up
ALTER TABLE nodes
    ADD COLUMN deactivated_at  timestamptz,
    ADD COLUMN deletion_due_at timestamptz,
    ADD CONSTRAINT nodes_deletion_deactivated CHECK (deletion_due_at IS NULL OR deactivated_at IS NOT NULL);
CREATE INDEX nodes_deletion_due_idx ON nodes (deletion_due_at) WHERE deletion_due_at IS NOT NULL;

GRANT CREATE ON SCHEMA public TO sermon_definer;

CREATE OR REPLACE FUNCTION agent_certificate_usable(p_serial text, p_org uuid, p_node uuid, p_at timestamptz) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT EXISTS (
            SELECT 1 FROM public.agent_certificates c
            JOIN public.nodes n ON n.org_id = c.org_id AND n.id = c.node_id
            JOIN public.organizations o ON o.id = c.org_id
            WHERE c.serial = p_serial AND c.org_id = p_org AND c.node_id = p_node
              AND (c.revoked_at IS NULL OR c.revoked_at > p_at)
              AND p_at >= c.not_before AND p_at < c.not_after
              AND n.locked_at IS NULL AND n.deactivated_at IS NULL AND o.locked_at IS NULL
        )
    $$;

-- The job finds the due nodes of all organizations (definer_read on nodes since 00011).
CREATE FUNCTION nodes_due_for_deletion(p_at timestamptz, p_limit integer)
    RETURNS TABLE (org_id uuid, node_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT n.org_id, n.id FROM public.nodes n
        WHERE n.deactivated_at IS NOT NULL AND n.deletion_due_at <= p_at
        ORDER BY n.deletion_due_at, n.id
        LIMIT p_limit
    $$;
ALTER FUNCTION nodes_due_for_deletion(timestamptz, integer) OWNER TO sermon_definer;
REVOKE ALL ON FUNCTION nodes_due_for_deletion(timestamptz, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION nodes_due_for_deletion(timestamptz, integer) TO sermon_app;

-- The alert history stays append-only for sermon_app (00027); node_purge may delete a node's.
GRANT SELECT, DELETE ON alert_history TO sermon_definer;
ALTER POLICY delete_only_expired ON alert_history TO sermon_app;
CREATE POLICY definer_purge ON alert_history FOR DELETE TO sermon_definer USING (org_id = current_org_id());
-- FOR UPDATE on the node needs UPDATE rights: on deactivated_at only.
GRANT UPDATE (deactivated_at) ON nodes TO sermon_definer;

-- +goose StatementBegin
CREATE FUNCTION node_purge(p_org uuid, p_node uuid, p_at timestamptz) RETURNS text
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
DECLARE
    v_series   bigint[];
    v_agg      record;
    v_hostname text;
BEGIN
    -- The tenant policies let this transaction see the organization's rows only.
    PERFORM set_config('sermon.org_id', p_org::text, true);
    -- Lock the row before checking: a cancellation in flight finishes first and is seen.
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
    -- Certificates, series, alerts, update state, commands, login sources: by foreign key.
    DELETE FROM public.nodes WHERE org_id = p_org AND id = p_node;
    RETURN v_hostname;
END
$$;
-- +goose StatementEnd
ALTER FUNCTION node_purge(uuid, uuid, timestamptz) OWNER TO sermon_definer;
REVOKE ALL ON FUNCTION node_purge(uuid, uuid, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION node_purge(uuid, uuid, timestamptz) TO sermon_app;
REVOKE CREATE ON SCHEMA public FROM sermon_definer;

-- +goose Down
GRANT CREATE ON SCHEMA public TO sermon_definer;
DROP FUNCTION node_purge(uuid, uuid, timestamptz);
REVOKE UPDATE (deactivated_at) ON nodes FROM sermon_definer;
DROP POLICY definer_purge ON alert_history;
ALTER POLICY delete_only_expired ON alert_history TO PUBLIC;
REVOKE SELECT, DELETE ON alert_history FROM sermon_definer;
DROP FUNCTION nodes_due_for_deletion(timestamptz, integer);
CREATE OR REPLACE FUNCTION agent_certificate_usable(p_serial text, p_org uuid, p_node uuid, p_at timestamptz) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT EXISTS (
            SELECT 1 FROM public.agent_certificates c
            JOIN public.nodes n ON n.org_id = c.org_id AND n.id = c.node_id
            JOIN public.organizations o ON o.id = c.org_id
            WHERE c.serial = p_serial AND c.org_id = p_org AND c.node_id = p_node
              AND (c.revoked_at IS NULL OR c.revoked_at > p_at)
              AND p_at >= c.not_before AND p_at < c.not_after
              AND n.locked_at IS NULL AND o.locked_at IS NULL
        )
    $$;
REVOKE CREATE ON SCHEMA public FROM sermon_definer;
DROP INDEX nodes_deletion_due_idx;
ALTER TABLE nodes DROP CONSTRAINT nodes_deletion_deactivated, DROP COLUMN deletion_due_at, DROP COLUMN deactivated_at;
