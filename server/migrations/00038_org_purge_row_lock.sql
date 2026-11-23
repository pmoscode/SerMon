-- org_purge without a race (M2-39, from the security review M2-28): it checked the lock with a plain
-- read, so an unlock committed meanwhile went unseen – the deletion then removed an organization
-- just unlocked. Now it locks the row first (FOR UPDATE needs UPDATE rights: on locked_at only),
-- and the accounts to delete are checked again for memberships at the end.

-- +goose Up
GRANT CREATE ON SCHEMA public TO sermon_definer;
GRANT UPDATE (locked_at) ON organizations TO sermon_definer;
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
    IF NOT EXISTS (SELECT 1 FROM public.organizations
                   WHERE id = p_org AND locked_at IS NOT NULL AND deletion_due_at <= p_at) THEN
        RAISE EXCEPTION 'organization % is not locked and due for deletion', p_org USING ERRCODE = 'P0001';
    END IF;
    -- The tenant policies let this transaction see the organization's rows only.
    PERFORM set_config('sermon.org_id', p_org::text, true);
    -- Accounts without another membership go along; superadmins and members of other
    -- organizations keep theirs.
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
    -- Nodes before the organization: their groups refuse to go while nodes point at them.
    DELETE FROM public.nodes WHERE org_id = p_org;
    DELETE FROM public.organizations WHERE id = p_org;
    DELETE FROM public.users WHERE id = ANY (coalesce(v_users, '{}'));
    GET DIAGNOSTICS v_count = ROW_COUNT;
    RETURN v_count;
END
$$;
-- +goose StatementEnd
ALTER FUNCTION org_purge(uuid, timestamptz) OWNER TO sermon_definer;
REVOKE UPDATE (locked_at) ON organizations FROM sermon_definer;
REVOKE CREATE ON SCHEMA public FROM sermon_definer;
