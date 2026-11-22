-- Locking and deleting organizations (M2-33, D29, ADR 0029):
--   * locked_at: the organization is locked – no logins, agent connections, enrollments or
--     invitations; deletion_due_at: it is scheduled for deletion (always locked); the reminder
--     7 days before is sent once (deletion_reminded_at);
--   * the definer functions that admit logins, agents, enrollment tokens and invitations refuse
--     a locked organization; certificates stay valid, so lifting the lock needs no new enrollment;
--   * org_purge deletes a locked organization that is due, with its metrics, events, audit log
--     and the accounts that have no other membership – all in one transaction.

-- +goose Up
ALTER TABLE organizations
    ADD COLUMN locked_at            timestamptz,
    ADD COLUMN deletion_due_at      timestamptz,
    ADD COLUMN deletion_reminded_at timestamptz,
    ADD CONSTRAINT organizations_deletion_locked CHECK (deletion_due_at IS NULL OR locked_at IS NOT NULL);

GRANT CREATE ON SCHEMA public TO sermon_definer;

-- The superadmin's list and the workers (engine, notifications) learn whether it is locked.
DROP FUNCTION admin_organizations();
CREATE FUNCTION admin_organizations()
    RETURNS TABLE (id uuid, name text, created_at timestamptz, members bigint, max_nodes integer,
                   max_members integer, retention_cap_days integer, samples_per_second integer,
                   series_per_node integer, locked boolean)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT o.id, o.name, o.created_at, (SELECT count(*) FROM public.memberships m WHERE m.org_id = o.id),
               o.max_nodes, o.max_members, o.retention_cap_days, o.samples_per_second, o.series_per_node,
               o.locked_at IS NOT NULL
        FROM public.organizations o
        ORDER BY lower(o.name), o.id
    $$;
ALTER FUNCTION admin_organizations() OWNER TO sermon_definer;
REVOKE ALL ON FUNCTION admin_organizations() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_organizations() TO sermon_app;

-- Agents of a locked organization are refused; their certificates are not revoked.
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

CREATE OR REPLACE FUNCTION enrollment_token_by_hash(p_hash bytea, p_at timestamptz)
    RETURNS TABLE (id uuid, org_id uuid, group_id uuid, token_hash bytea)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT t.id, t.org_id, t.group_id, t.token_hash FROM public.enrollment_tokens t
        JOIN public.organizations o ON o.id = t.org_id
        WHERE t.token_hash = p_hash AND t.expires_at > p_at AND t.revoked_at IS NULL AND t.uses < t.max_uses
          AND o.locked_at IS NULL
    $$;

CREATE OR REPLACE FUNCTION invitation_by_token(p_hash bytea, p_at timestamptz)
    RETURNS TABLE (id uuid, org_id uuid, org_name text, email text, group_id uuid, role text, expires_at timestamptz)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT i.id, i.org_id, o.name, i.email, i.group_id, i.role, i.expires_at
        FROM public.invitations i JOIN public.organizations o ON o.id = i.org_id
        WHERE i.token_hash = p_hash AND i.expires_at > p_at AND o.locked_at IS NULL
    $$;

-- A login lands in an organization that is not locked; a locked one is no choice.
CREATE OR REPLACE FUNCTION auth_user_organizations(p_user uuid) RETURNS TABLE (org_id uuid, name text)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT o.id, o.name
        FROM public.memberships m JOIN public.organizations o ON o.id = m.org_id
        WHERE m.user_id = p_user AND o.locked_at IS NULL
        ORDER BY o.name
    $$;

-- Deleting: only what org_purge needs, only for sermon_definer.
-- DELETE with WHERE needs SELECT too; the tenant policies limit both to the organization set below.
GRANT DELETE ON organizations, nodes, audit_log, users, metrics_raw TO sermon_definer;
GRANT SELECT ON nodes, audit_log TO sermon_definer; -- superadmins: since 00032
CREATE POLICY definer_purge ON audit_log FOR DELETE TO sermon_definer USING (org_id = current_org_id());
CREATE POLICY definer_purge ON users FOR DELETE TO sermon_definer USING (true);
-- +goose StatementBegin
DO $$
DECLARE
    agg record;
BEGIN
    FOR agg IN SELECT materialization_hypertable_schema AS s, materialization_hypertable_name AS n
               FROM timescaledb_information.continuous_aggregates LOOP
        EXECUTE format('GRANT SELECT, DELETE ON %I.%I TO sermon_definer', agg.s, agg.n);
    END LOOP;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION org_purge(p_org uuid, p_at timestamptz) RETURNS bigint
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
REVOKE ALL ON FUNCTION org_purge(uuid, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION org_purge(uuid, timestamptz) TO sermon_app;
REVOKE CREATE ON SCHEMA public FROM sermon_definer;

-- +goose Down
DROP FUNCTION org_purge(uuid, timestamptz);
-- +goose StatementBegin
DO $$
DECLARE
    agg record;
BEGIN
    FOR agg IN SELECT materialization_hypertable_schema AS s, materialization_hypertable_name AS n
               FROM timescaledb_information.continuous_aggregates LOOP
        EXECUTE format('REVOKE SELECT, DELETE ON %I.%I FROM sermon_definer', agg.s, agg.n);
    END LOOP;
END
$$;
-- +goose StatementEnd
DROP POLICY definer_purge ON users;
DROP POLICY definer_purge ON audit_log;
REVOKE DELETE ON organizations, nodes, audit_log, users, metrics_raw FROM sermon_definer;
REVOKE SELECT ON nodes, audit_log FROM sermon_definer;
GRANT CREATE ON SCHEMA public TO sermon_definer;
CREATE OR REPLACE FUNCTION auth_user_organizations(p_user uuid) RETURNS TABLE (org_id uuid, name text)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT o.id, o.name
        FROM public.memberships m JOIN public.organizations o ON o.id = m.org_id
        WHERE m.user_id = p_user
        ORDER BY o.name
    $$;
CREATE OR REPLACE FUNCTION invitation_by_token(p_hash bytea, p_at timestamptz)
    RETURNS TABLE (id uuid, org_id uuid, org_name text, email text, group_id uuid, role text, expires_at timestamptz)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT i.id, i.org_id, o.name, i.email, i.group_id, i.role, i.expires_at
        FROM public.invitations i JOIN public.organizations o ON o.id = i.org_id
        WHERE i.token_hash = p_hash AND i.expires_at > p_at
    $$;
CREATE OR REPLACE FUNCTION enrollment_token_by_hash(p_hash bytea, p_at timestamptz)
    RETURNS TABLE (id uuid, org_id uuid, group_id uuid, token_hash bytea)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT t.id, t.org_id, t.group_id, t.token_hash FROM public.enrollment_tokens t
        WHERE t.token_hash = p_hash AND t.expires_at > p_at AND t.revoked_at IS NULL AND t.uses < t.max_uses
    $$;
CREATE OR REPLACE FUNCTION agent_certificate_usable(p_serial text, p_org uuid, p_node uuid, p_at timestamptz) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT EXISTS (
            SELECT 1 FROM public.agent_certificates c
            JOIN public.nodes n ON n.org_id = c.org_id AND n.id = c.node_id
            WHERE c.serial = p_serial AND c.org_id = p_org AND c.node_id = p_node
              AND (c.revoked_at IS NULL OR c.revoked_at > p_at)
              AND p_at >= c.not_before AND p_at < c.not_after
              AND n.locked_at IS NULL
        )
    $$;
DROP FUNCTION admin_organizations();
CREATE FUNCTION admin_organizations()
    RETURNS TABLE (id uuid, name text, created_at timestamptz, members bigint, max_nodes integer,
                   max_members integer, retention_cap_days integer, samples_per_second integer,
                   series_per_node integer)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT o.id, o.name, o.created_at, (SELECT count(*) FROM public.memberships m WHERE m.org_id = o.id),
               o.max_nodes, o.max_members, o.retention_cap_days, o.samples_per_second, o.series_per_node
        FROM public.organizations o
        ORDER BY lower(o.name), o.id
    $$;
ALTER FUNCTION admin_organizations() OWNER TO sermon_definer;
REVOKE ALL ON FUNCTION admin_organizations() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_organizations() TO sermon_app;
REVOKE CREATE ON SCHEMA public FROM sermon_definer;
ALTER TABLE organizations DROP CONSTRAINT organizations_deletion_locked,
    DROP COLUMN deletion_reminded_at, DROP COLUMN deletion_due_at, DROP COLUMN locked_at;
