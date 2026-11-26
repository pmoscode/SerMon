-- Kiosk tokens (M5-26, ADR 0040): a read-only credential for a display, bound to one shared
-- dashboard and its organization. Only the SHA-256 of the secret is stored. A tenant table with
-- RLS (ADR 0007); the display's token is looked up before any tenant is known through a SECURITY
-- DEFINER function that returns only usable tokens: not revoked, not expired, of an unlocked
-- organization and of a dashboard that is still shared. Deleting the dashboard deletes its tokens.

-- +goose Up
CREATE TABLE kiosk_tokens (
    org_id       uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    id           uuid        NOT NULL,
    dashboard_id uuid        NOT NULL,
    name         text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    token_hash   bytea       NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    created_by   uuid        NOT NULL,
    created_at   timestamptz NOT NULL,
    expires_at   timestamptz NOT NULL,
    last_used_at timestamptz,
    revoked_at   timestamptz,
    PRIMARY KEY (org_id, id),
    FOREIGN KEY (org_id, dashboard_id) REFERENCES dashboards (org_id, id) ON DELETE CASCADE,
    CHECK (expires_at > created_at)
);
CREATE INDEX kiosk_tokens_dashboard_idx ON kiosk_tokens (org_id, dashboard_id);
ALTER TABLE kiosk_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE kiosk_tokens FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON kiosk_tokens
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, UPDATE ON kiosk_tokens TO sermon_app;

GRANT SELECT ON kiosk_tokens, dashboards TO sermon_definer;
CREATE POLICY definer_read ON kiosk_tokens FOR SELECT TO sermon_definer USING (true);
CREATE POLICY definer_read ON dashboards FOR SELECT TO sermon_definer USING (true);
GRANT CREATE ON SCHEMA public TO sermon_definer;
CREATE FUNCTION kiosk_token_by_hash(p_hash bytea, p_at timestamptz)
    RETURNS TABLE (id uuid, org_id uuid, dashboard_id uuid, created_by uuid, token_hash bytea)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT t.id, t.org_id, t.dashboard_id, t.created_by, t.token_hash
        FROM public.kiosk_tokens t
        JOIN public.dashboards d ON d.org_id = t.org_id AND d.id = t.dashboard_id
        JOIN public.organizations o ON o.id = t.org_id
        WHERE t.token_hash = p_hash AND t.revoked_at IS NULL AND t.expires_at > p_at
          AND d.shared AND o.locked_at IS NULL
    $$;
ALTER FUNCTION kiosk_token_by_hash(bytea, timestamptz) OWNER TO sermon_definer;
REVOKE CREATE ON SCHEMA public FROM sermon_definer;
REVOKE ALL ON FUNCTION kiosk_token_by_hash(bytea, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION kiosk_token_by_hash(bytea, timestamptz) TO sermon_app;

-- +goose Down
DROP FUNCTION kiosk_token_by_hash(bytea, timestamptz);
DROP POLICY definer_read ON dashboards;
REVOKE SELECT ON dashboards FROM sermon_definer;
DROP TABLE kiosk_tokens;
