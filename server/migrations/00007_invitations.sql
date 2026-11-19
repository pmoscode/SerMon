-- Invitations of new members (ADR 0008): single-use link token, only its SHA-256 is stored.

-- +goose Up

CREATE TABLE invitations (
    id         uuid        PRIMARY KEY,
    org_id     uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    email      text        NOT NULL CHECK (email = lower(email) AND char_length(email) BETWEEN 3 AND 320),
    group_id   uuid,       -- NULL: the role applies to all groups
    role       text        NOT NULL CHECK (role IN ('viewer', 'operator', 'admin')),
    token_hash bytea       NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    invited_by uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    UNIQUE (org_id, email), -- one open invitation per address; inviting again replaces it
    FOREIGN KEY (org_id, group_id) REFERENCES node_groups (org_id, id) ON DELETE CASCADE
);
ALTER TABLE invitations ENABLE ROW LEVEL SECURITY;
ALTER TABLE invitations FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON invitations
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON invitations TO sermon_app;

-- Acceptance without tenant: finds a valid invitation by token hash only (no listing possible).
GRANT SELECT ON invitations TO sermon_definer;
CREATE POLICY definer_read ON invitations FOR SELECT TO sermon_definer USING (true);
GRANT CREATE ON SCHEMA public TO sermon_definer;
CREATE FUNCTION invitation_by_token(p_hash bytea, p_at timestamptz)
    RETURNS TABLE (id uuid, org_id uuid, org_name text, email text, group_id uuid, role text, expires_at timestamptz)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT i.id, i.org_id, o.name, i.email, i.group_id, i.role, i.expires_at
        FROM public.invitations i JOIN public.organizations o ON o.id = i.org_id
        WHERE i.token_hash = p_hash AND i.expires_at > p_at
    $$;
ALTER FUNCTION invitation_by_token(bytea, timestamptz) OWNER TO sermon_definer;
REVOKE CREATE ON SCHEMA public FROM sermon_definer;
REVOKE ALL ON FUNCTION invitation_by_token(bytea, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION invitation_by_token(bytea, timestamptz) TO sermon_app;

-- +goose Down
DROP FUNCTION invitation_by_token(bytea, timestamptz);
DROP TABLE invitations;
