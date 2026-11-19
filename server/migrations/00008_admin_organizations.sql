-- Organization list for the superadmin (KONZEPT §7): names and member counts only, no tenant data.

-- +goose Up
GRANT CREATE ON SCHEMA public TO sermon_definer;
CREATE FUNCTION admin_list_organizations()
    RETURNS TABLE (id uuid, name text, created_at timestamptz, members bigint)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT o.id, o.name, o.created_at, (SELECT count(*) FROM public.memberships m WHERE m.org_id = o.id)
        FROM public.organizations o
        ORDER BY lower(o.name), o.id
    $$;
ALTER FUNCTION admin_list_organizations() OWNER TO sermon_definer;
REVOKE CREATE ON SCHEMA public FROM sermon_definer;
REVOKE ALL ON FUNCTION admin_list_organizations() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_list_organizations() TO sermon_app;

-- +goose Down
DROP FUNCTION admin_list_organizations();
