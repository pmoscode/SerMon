-- Retention per organization (M5-30): how long its logs and security events are kept, within the
-- instance's limits (logs 14 days, events a year) – NULL is the instance's default; when Loki was
-- last asked to delete its older logs. security_events_expire_org deletes an organization's
-- events and alert history before a time, never of the last 7 days.

-- +goose Up
ALTER TABLE organizations
    ADD COLUMN log_retention_days   smallint CHECK (log_retention_days BETWEEN 1 AND 14),
    ADD COLUMN event_retention_days smallint CHECK (event_retention_days BETWEEN 7 AND 365),
    ADD COLUMN logs_erased_at       timestamptz;

GRANT CREATE ON SCHEMA public TO sermon_definer;

-- The job finds the organizations with a retention of their own (definer_read on organizations).
CREATE FUNCTION organization_retentions()
    RETURNS TABLE (id uuid, log_retention_days smallint, event_retention_days smallint, logs_erased_at timestamptz)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT o.id, o.log_retention_days, o.event_retention_days, o.logs_erased_at
        FROM public.organizations o
        WHERE o.log_retention_days IS NOT NULL OR o.event_retention_days IS NOT NULL
        ORDER BY o.id
    $$;
ALTER FUNCTION organization_retentions() OWNER TO sermon_definer;
REVOKE ALL ON FUNCTION organization_retentions() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION organization_retentions() TO sermon_app;

-- +goose StatementBegin
CREATE FUNCTION security_events_expire_org(p_org uuid, p_before timestamptz) RETURNS bigint
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
DECLARE
    v_events  bigint;
    v_history bigint;
BEGIN
    -- Never anything of the last 7 days (the shortest retention), whatever the caller's clock says.
    p_before := least(p_before, now() - interval '7 days');
    -- The tenant policies let this transaction see the organization's rows only.
    PERFORM set_config('sermon.org_id', p_org::text, true);
    DELETE FROM public.security_events WHERE org_id = p_org AND occurred_at < p_before;
    GET DIAGNOSTICS v_events = ROW_COUNT;
    DELETE FROM public.alert_history WHERE org_id = p_org AND at < p_before;
    GET DIAGNOSTICS v_history = ROW_COUNT;
    RETURN v_events + v_history;
END
$$;
-- +goose StatementEnd
ALTER FUNCTION security_events_expire_org(uuid, timestamptz) OWNER TO sermon_definer;
REVOKE ALL ON FUNCTION security_events_expire_org(uuid, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION security_events_expire_org(uuid, timestamptz) TO sermon_app;
REVOKE CREATE ON SCHEMA public FROM sermon_definer;

-- +goose Down
DROP FUNCTION security_events_expire_org(uuid, timestamptz);
DROP FUNCTION organization_retentions();
ALTER TABLE organizations DROP COLUMN logs_erased_at, DROP COLUMN event_retention_days, DROP COLUMN log_retention_days;
