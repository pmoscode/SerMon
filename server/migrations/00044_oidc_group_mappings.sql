-- Groups of the OIDC provider mapped to roles (M3-21, ADR 0033). An organization's admins map a
-- provider group to a node group (NULL: all groups) and a role; every sign-in reconciles the
-- bindings that came from mappings (origin 'oidc'), bindings given by hand (origin 'manual') stay.

-- +goose Up
ALTER TABLE role_bindings ADD COLUMN origin text NOT NULL DEFAULT 'manual' CHECK (origin IN ('manual', 'oidc'));

CREATE TABLE oidc_group_mappings (
    id         uuid        PRIMARY KEY,
    org_id     uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    oidc_group text        NOT NULL CHECK (octet_length(oidc_group) BETWEEN 1 AND 255),
    group_id   uuid,
    role       text        NOT NULL CHECK (role IN ('viewer', 'operator', 'admin')),
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (org_id, group_id) REFERENCES node_groups (org_id, id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX oidc_group_mappings_unique
    ON oidc_group_mappings (org_id, oidc_group, COALESCE(group_id, '00000000-0000-0000-0000-000000000000'::uuid));
CREATE INDEX oidc_group_mappings_group_idx ON oidc_group_mappings (oidc_group);
ALTER TABLE oidc_group_mappings ENABLE ROW LEVEL SECURITY;
ALTER TABLE oidc_group_mappings FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON oidc_group_mappings
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, DELETE ON oidc_group_mappings TO sermon_app;

-- The organizations a sign-in may join: unlocked, with a mapping of one of the groups. Only the
-- IDs leave the function.
GRANT SELECT ON oidc_group_mappings TO sermon_definer;
CREATE POLICY definer_read ON oidc_group_mappings FOR SELECT TO sermon_definer USING (true);
GRANT CREATE ON SCHEMA public TO sermon_definer;
CREATE FUNCTION oidc_mapped_organizations(p_groups text[]) RETURNS TABLE (org_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT DISTINCT m.org_id
        FROM public.oidc_group_mappings m JOIN public.organizations o ON o.id = m.org_id
        WHERE m.oidc_group = ANY (p_groups) AND o.locked_at IS NULL
        ORDER BY m.org_id
    $$;
ALTER FUNCTION oidc_mapped_organizations(text[]) OWNER TO sermon_definer;
REVOKE ALL ON FUNCTION oidc_mapped_organizations(text[]) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION oidc_mapped_organizations(text[]) TO sermon_app;
REVOKE CREATE ON SCHEMA public FROM sermon_definer;

-- +goose Down
DROP FUNCTION oidc_mapped_organizations(text[]);
DROP POLICY definer_read ON oidc_group_mappings;
REVOKE SELECT ON oidc_group_mappings FROM sermon_definer;
DROP TABLE oidc_group_mappings;
ALTER TABLE role_bindings DROP COLUMN origin;
