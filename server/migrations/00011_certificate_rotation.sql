-- Certificate rotation (KONZEPT §6 "Schlüsselrotation", M1-11): grace period for replaced
-- certificates, precautionary lock of nodes and the source IP of the last session.

-- +goose Up
ALTER TABLE nodes
    ADD COLUMN locked_at   timestamptz,
    ADD COLUMN lock_reason text CHECK (char_length(lock_reason) <= 200),
    ADD COLUMN last_ip     text CHECK (char_length(last_ip) <= 45);

-- The revocation check also honors revocations scheduled for later (grace period after a
-- rotation) and rejects every certificate of a locked node.
GRANT SELECT ON nodes TO sermon_definer;
CREATE POLICY definer_read ON nodes FOR SELECT TO sermon_definer USING (true);
CREATE OR REPLACE FUNCTION agent_certificate_usable(p_serial text, p_at timestamptz) RETURNS boolean
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

-- +goose Down
CREATE OR REPLACE FUNCTION agent_certificate_usable(p_serial text, p_at timestamptz) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT EXISTS (
            SELECT 1 FROM public.agent_certificates
            WHERE serial = p_serial AND revoked_at IS NULL AND p_at >= not_before AND p_at < not_after
        )
    $$;
DROP POLICY definer_read ON nodes;
REVOKE SELECT ON nodes FROM sermon_definer;
ALTER TABLE nodes DROP COLUMN last_ip, DROP COLUMN lock_reason, DROP COLUMN locked_at;
