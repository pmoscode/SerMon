-- Organizations managed by the superadmin (M2-32, ADR 0029):
--   * names are unique regardless of case – existing duplicates get a suffix first;
--   * quotas instead of the instance-wide values, NULL meaning the instance default;
--   * the superadmin's list (admin_organizations, replacing admin_list_organizations) shows the quotas, and the instance's storage totals for the estimate
--     per organization. The counts per organization come from tenant transactions of the app,
--     not from here: series has no definer policy on purpose (the metric readers rely on it).

-- +goose Up
-- The owner does not pass the row-level security of organizations (FORCE) – lifted for the rename.
ALTER TABLE organizations NO FORCE ROW LEVEL SECURITY;
UPDATE organizations o
SET name = left(o.name, 190) || ' (' || d.n || ')'
FROM (SELECT id, row_number() OVER (PARTITION BY lower(name) ORDER BY created_at, id) AS n FROM organizations) d
WHERE d.id = o.id AND d.n > 1;
ALTER TABLE organizations FORCE ROW LEVEL SECURITY;
CREATE UNIQUE INDEX organizations_name_ci_key ON organizations (lower(name));

ALTER TABLE organizations
    ADD COLUMN max_nodes          integer CHECK (max_nodes BETWEEN 1 AND 1000000),
    ADD COLUMN max_members        integer CHECK (max_members BETWEEN 1 AND 100000),
    ADD COLUMN retention_cap_days integer CHECK (retention_cap_days BETWEEN 1 AND 3650),
    ADD COLUMN samples_per_second integer CHECK (samples_per_second BETWEEN 1 AND 1000000),
    ADD COLUMN series_per_node    integer CHECK (series_per_node BETWEEN 1 AND 100000);

GRANT CREATE ON SCHEMA public TO sermon_definer;
DROP FUNCTION admin_list_organizations();
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

-- The storage of the instance: the metric hypertables with their continuous aggregates, and the
-- partitions of the security events. Sizes only, no rows.
CREATE FUNCTION admin_storage_totals()
    RETURNS TABLE (metric_bytes bigint, event_bytes bigint)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT
            (SELECT coalesce(sum(hypertable_size(format('%I.%I', h.hypertable_schema, h.hypertable_name)::regclass)), 0)::bigint
             FROM timescaledb_information.hypertables h)
          + (SELECT coalesce(sum(hypertable_size(format('%I.%I', c.materialization_hypertable_schema, c.materialization_hypertable_name)::regclass)), 0)::bigint
             FROM timescaledb_information.continuous_aggregates c),
            (SELECT coalesce(sum(pg_total_relation_size(i.inhrelid)), 0)::bigint
             FROM pg_inherits i WHERE i.inhparent = 'public.security_events'::regclass)
    $$;
ALTER FUNCTION admin_storage_totals() OWNER TO sermon_definer;
REVOKE CREATE ON SCHEMA public FROM sermon_definer;
REVOKE ALL ON FUNCTION admin_storage_totals() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_storage_totals() TO sermon_app;

-- +goose Down
DROP FUNCTION admin_storage_totals();
GRANT CREATE ON SCHEMA public TO sermon_definer;
DROP FUNCTION admin_organizations();
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
ALTER TABLE organizations DROP COLUMN max_nodes, DROP COLUMN max_members, DROP COLUMN retention_cap_days,
    DROP COLUMN samples_per_second, DROP COLUMN series_per_node;
DROP INDEX organizations_name_ci_key;
