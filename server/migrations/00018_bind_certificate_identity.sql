-- The revocation check binds the serial to the identity in the certificate (M1-38): a forged
-- agent certificate – possible only with a compromised intermediate key – can't reuse a known
-- valid serial (serials appear in audit details) for another organization or node. The old
-- function without org and node is dropped, so no caller can use the unbound check.

-- +goose Up
GRANT CREATE ON SCHEMA public TO sermon_definer;
CREATE FUNCTION agent_certificate_usable(p_serial text, p_org uuid, p_node uuid, p_at timestamptz) RETURNS boolean
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
ALTER FUNCTION agent_certificate_usable(text, uuid, uuid, timestamptz) OWNER TO sermon_definer;
REVOKE ALL ON FUNCTION agent_certificate_usable(text, uuid, uuid, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION agent_certificate_usable(text, uuid, uuid, timestamptz) TO sermon_app;
REVOKE CREATE ON SCHEMA public FROM sermon_definer;
DROP FUNCTION agent_certificate_usable(text, timestamptz);

-- +goose Down
GRANT CREATE ON SCHEMA public TO sermon_definer;
CREATE FUNCTION agent_certificate_usable(p_serial text, p_at timestamptz) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT EXISTS (
            SELECT 1 FROM public.agent_certificates c
            JOIN public.nodes n ON n.org_id = c.org_id AND n.id = c.node_id
            WHERE c.serial = p_serial
              AND (c.revoked_at IS NULL OR c.revoked_at > p_at)
              AND p_at >= c.not_before AND p_at < c.not_after
              AND n.locked_at IS NULL
        )
    $$;
ALTER FUNCTION agent_certificate_usable(text, timestamptz) OWNER TO sermon_definer;
REVOKE ALL ON FUNCTION agent_certificate_usable(text, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION agent_certificate_usable(text, timestamptz) TO sermon_app;
REVOKE CREATE ON SCHEMA public FROM sermon_definer;
DROP FUNCTION agent_certificate_usable(text, uuid, uuid, timestamptz);
