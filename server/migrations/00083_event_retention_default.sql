-- The instance's default for security events and the alert history (V1-22, ADR 0018 addendum):
-- organizations without a period of their own keep them SERMON_EVENT_RETENTION (90 days unless
-- set, 7 to 365) instead of a year. The server passes the default; the database keeps at least the
-- 7 days of the shortest period and checks everything against its own clock:
--   * security_events_expire_org deletes an organization's rows before its own period, or before
--     the default (at least 7 days) without one;
--   * security_events_expire drops whole months only once no organization keeps them: never one
--     younger than the longest period of any organization or than the default;
--   * organization_retentions lists every organization (the job now expires all of them).

-- +goose Up
GRANT CREATE ON SCHEMA public TO sermon_definer;

CREATE OR REPLACE FUNCTION organization_retentions()
    RETURNS TABLE (id uuid, log_retention_days smallint, event_retention_days smallint, logs_erased_at timestamptz)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT o.id, o.log_retention_days, o.event_retention_days, o.logs_erased_at
        FROM public.organizations o
        ORDER BY o.id
    $$;

DROP FUNCTION security_events_expire_org(uuid, timestamptz);
-- +goose StatementBegin
CREATE FUNCTION security_events_expire_org(p_org uuid, p_before timestamptz, p_default_days integer) RETURNS bigint
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
DECLARE
    v_days    integer;
    v_events  bigint;
    v_history bigint;
BEGIN
    IF current_setting('sermon.org_id', true) IS DISTINCT FROM p_org::text THEN
        RAISE EXCEPTION 'security_events_expire_org outside the tenant of organization %', p_org USING ERRCODE = '42501';
    END IF;
    SELECT event_retention_days INTO v_days FROM public.organizations WHERE id = p_org;
    -- Without a period of its own the instance's default, never under the shortest period.
    v_days := coalesce(v_days, greatest(coalesce(p_default_days, 365), 7));
    p_before := least(p_before, now() - make_interval(days => v_days));
    DELETE FROM public.security_events WHERE org_id = p_org AND occurred_at < p_before;
    GET DIAGNOSTICS v_events = ROW_COUNT;
    DELETE FROM public.alert_history WHERE org_id = p_org AND at < p_before;
    GET DIAGNOSTICS v_history = ROW_COUNT;
    RETURN v_events + v_history;
END
$$;
-- +goose StatementEnd
ALTER FUNCTION security_events_expire_org(uuid, timestamptz, integer) OWNER TO sermon_definer;
REVOKE ALL ON FUNCTION security_events_expire_org(uuid, timestamptz, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION security_events_expire_org(uuid, timestamptz, integer) TO sermon_app;

DROP FUNCTION security_events_expire(timestamptz);
-- Drops the partitions that end at or before p_before (whole months only); returns how many.
-- +goose StatementBegin
CREATE FUNCTION security_events_expire(p_before timestamptz, p_default_days integer) RETURNS integer
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
DECLARE
    partition record;
    dropped integer := 0;
    v_days integer;
BEGIN
    -- Never a month any organization still keeps, whatever the caller's clock says (definer_read
    -- on organizations: all of them).
    v_days := greatest(coalesce(p_default_days, 365), 7,
                       coalesce((SELECT max(event_retention_days) FROM public.organizations), 0));
    p_before := least(p_before, now() - make_interval(days => v_days));
    PERFORM pg_advisory_xact_lock(hashtext('security_events_partitions'));
    FOR partition IN
        SELECT c.relname
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE i.inhparent = 'public.security_events'::regclass AND n.nspname = 'public'
          AND c.relname ~ '^security_events_[0-9]{6}$'
    LOOP
        IF (to_timestamp(substring(partition.relname FROM 17), 'YYYYMM') AT TIME ZONE 'UTC')::timestamp
               + interval '1 month' <= (p_before AT TIME ZONE 'UTC') THEN
            EXECUTE format('DROP TABLE public.%I', partition.relname);
            dropped := dropped + 1;
        END IF;
    END LOOP;
    RETURN dropped;
END
$$;
-- +goose StatementEnd
ALTER FUNCTION security_events_expire(timestamptz, integer) OWNER TO sermon_definer;
REVOKE ALL ON FUNCTION security_events_expire(timestamptz, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION security_events_expire(timestamptz, integer) TO sermon_app;

REVOKE CREATE ON SCHEMA public FROM sermon_definer;

-- +goose Down
GRANT CREATE ON SCHEMA public TO sermon_definer;

DROP FUNCTION security_events_expire(timestamptz, integer);
-- +goose StatementBegin
CREATE FUNCTION security_events_expire(p_before timestamptz) RETURNS integer
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
DECLARE
    partition record;
    dropped integer := 0;
BEGIN
    p_before := least(p_before, now() - interval '365 days');
    PERFORM pg_advisory_xact_lock(hashtext('security_events_partitions'));
    FOR partition IN
        SELECT c.relname
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE i.inhparent = 'public.security_events'::regclass AND n.nspname = 'public'
          AND c.relname ~ '^security_events_[0-9]{6}$'
    LOOP
        IF (to_timestamp(substring(partition.relname FROM 17), 'YYYYMM') AT TIME ZONE 'UTC')::timestamp
               + interval '1 month' <= (p_before AT TIME ZONE 'UTC') THEN
            EXECUTE format('DROP TABLE public.%I', partition.relname);
            dropped := dropped + 1;
        END IF;
    END LOOP;
    RETURN dropped;
END
$$;
-- +goose StatementEnd
ALTER FUNCTION security_events_expire(timestamptz) OWNER TO sermon_definer;
REVOKE ALL ON FUNCTION security_events_expire(timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION security_events_expire(timestamptz) TO sermon_app;

DROP FUNCTION security_events_expire_org(uuid, timestamptz, integer);
-- +goose StatementBegin
CREATE FUNCTION security_events_expire_org(p_org uuid, p_before timestamptz) RETURNS bigint
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
    p_before := least(p_before, now() - make_interval(days => v_days));
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

CREATE OR REPLACE FUNCTION organization_retentions()
    RETURNS TABLE (id uuid, log_retention_days smallint, event_retention_days smallint, logs_erased_at timestamptz)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT o.id, o.log_retention_days, o.event_retention_days, o.logs_erased_at
        FROM public.organizations o
        WHERE o.log_retention_days IS NOT NULL OR o.event_retention_days IS NOT NULL
        ORDER BY o.id
    $$;

REVOKE CREATE ON SCHEMA public FROM sermon_definer;
