-- Retention per organization, bounded by the database (M5-33, ADR 0029 addendum): what
-- security_events_expire_org deletes follows the organization's own period read here, not the
-- time it is given – whatever the caller's clock or a bug says, nothing younger than the period
-- the organization chose goes, and nothing at all of an organization without its own period
-- (the instance's job keeps those). It runs only within the caller's tenant.

-- +goose Up
GRANT CREATE ON SCHEMA public TO sermon_definer;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION security_events_expire_org(p_org uuid, p_before timestamptz) RETURNS bigint
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
DECLARE
    v_days    smallint;
    v_events  bigint;
    v_history bigint;
BEGIN
    IF current_setting('sermon.org_id', true) IS DISTINCT FROM p_org::text THEN
        RAISE EXCEPTION 'security_events_expire_org outside the tenant of organization %', p_org USING ERRCODE = '42501';
    END IF;
    SELECT event_retention_days INTO v_days FROM public.organizations WHERE id = p_org;
    IF v_days IS NULL THEN
        RETURN 0;
    END IF;
    -- The table's CHECK keeps the period at 7 days at least; the database's clock decides.
    p_before := least(p_before, now() - make_interval(days => v_days));
    DELETE FROM public.security_events WHERE org_id = p_org AND occurred_at < p_before;
    GET DIAGNOSTICS v_events = ROW_COUNT;
    DELETE FROM public.alert_history WHERE org_id = p_org AND at < p_before;
    GET DIAGNOSTICS v_history = ROW_COUNT;
    RETURN v_events + v_history;
END
$$;
-- +goose StatementEnd

REVOKE CREATE ON SCHEMA public FROM sermon_definer;

-- +goose Down
GRANT CREATE ON SCHEMA public TO sermon_definer;
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION security_events_expire_org(p_org uuid, p_before timestamptz) RETURNS bigint
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
DECLARE
    v_events  bigint;
    v_history bigint;
BEGIN
    p_before := least(p_before, now() - interval '7 days');
    PERFORM set_config('sermon.org_id', p_org::text, true);
    DELETE FROM public.security_events WHERE org_id = p_org AND occurred_at < p_before;
    GET DIAGNOSTICS v_events = ROW_COUNT;
    DELETE FROM public.alert_history WHERE org_id = p_org AND at < p_before;
    GET DIAGNOSTICS v_history = ROW_COUNT;
    RETURN v_events + v_history;
END
$$;
-- +goose StatementEnd
REVOKE CREATE ON SCHEMA public FROM sermon_definer;
