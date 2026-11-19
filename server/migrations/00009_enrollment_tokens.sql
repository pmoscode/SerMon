-- Enrollment tokens for new agents (KONZEPT §6, M1-08): only the SHA-256 of the token is stored.

-- +goose Up

CREATE TABLE enrollment_tokens (
    id         uuid        PRIMARY KEY,
    org_id     uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    group_id   uuid        NOT NULL,
    token_hash bytea       NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    created_by uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    max_uses   integer     NOT NULL CHECK (max_uses BETWEEN 1 AND 1000),
    uses       integer     NOT NULL DEFAULT 0 CHECK (uses >= 0 AND uses <= max_uses),
    revoked_at timestamptz,
    FOREIGN KEY (org_id, group_id) REFERENCES node_groups (org_id, id) ON DELETE CASCADE
);
CREATE INDEX enrollment_tokens_group_idx ON enrollment_tokens (org_id, group_id);
ALTER TABLE enrollment_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE enrollment_tokens FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON enrollment_tokens
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, UPDATE ON enrollment_tokens TO sermon_app;

-- Redemption without tenant: finds one usable token by its hash (no listing possible).
GRANT SELECT ON enrollment_tokens TO sermon_definer;
CREATE POLICY definer_read ON enrollment_tokens FOR SELECT TO sermon_definer USING (true);
GRANT CREATE ON SCHEMA public TO sermon_definer;
CREATE FUNCTION enrollment_token_by_hash(p_hash bytea, p_at timestamptz)
    RETURNS TABLE (id uuid, org_id uuid, group_id uuid, token_hash bytea)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT t.id, t.org_id, t.group_id, t.token_hash FROM public.enrollment_tokens t
        WHERE t.token_hash = p_hash AND t.expires_at > p_at AND t.revoked_at IS NULL AND t.uses < t.max_uses
    $$;
ALTER FUNCTION enrollment_token_by_hash(bytea, timestamptz) OWNER TO sermon_definer;
REVOKE CREATE ON SCHEMA public FROM sermon_definer;
REVOKE ALL ON FUNCTION enrollment_token_by_hash(bytea, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION enrollment_token_by_hash(bytea, timestamptz) TO sermon_app;

-- +goose Down
DROP FUNCTION enrollment_token_by_hash(bytea, timestamptz);
DROP TABLE enrollment_tokens;
