-- Agent CA rollover (KONZEPT §6, M1-11b): several trusted CAs, one issuing CA, retirement only
-- when no valid certificate of the CA remains.

-- +goose Up
ALTER TABLE pki_authorities
    ADD COLUMN retired_at timestamptz,
    ADD COLUMN started_by uuid,
    ADD COLUMN retired_by uuid,
    ADD CONSTRAINT pki_authorities_issuing_not_retired CHECK (NOT (active AND retired_at IS NOT NULL));
GRANT UPDATE ON pki_authorities TO sermon_app;

-- Every certificate remembers its issuing CA. The backfill must reach all organizations: FORCE
-- is lifted for this statement only (the owner bypasses RLS without FORCE).
ALTER TABLE agent_certificates ADD COLUMN authority_id integer REFERENCES pki_authorities (id);
ALTER TABLE agent_certificates NO FORCE ROW LEVEL SECURITY;
UPDATE agent_certificates SET authority_id = (SELECT id FROM pki_authorities WHERE active) WHERE authority_id IS NULL;
ALTER TABLE agent_certificates FORCE ROW LEVEL SECURITY;
ALTER TABLE agent_certificates ALTER COLUMN authority_id SET NOT NULL;
CREATE INDEX agent_certificates_authority_idx ON agent_certificates (authority_id);

-- Rollover progress without tenant: only a count per CA.
GRANT CREATE ON SCHEMA public TO sermon_definer;
CREATE FUNCTION pki_certificates_in_use(p_authority integer, p_at timestamptz) RETURNS bigint
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT count(*) FROM public.agent_certificates
        WHERE authority_id = p_authority
          AND (revoked_at IS NULL OR revoked_at > p_at)
          AND p_at >= not_before AND p_at < not_after
    $$;
ALTER FUNCTION pki_certificates_in_use(integer, timestamptz) OWNER TO sermon_definer;
REVOKE CREATE ON SCHEMA public FROM sermon_definer;
REVOKE ALL ON FUNCTION pki_certificates_in_use(integer, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION pki_certificates_in_use(integer, timestamptz) TO sermon_app;

-- +goose Down
DROP FUNCTION pki_certificates_in_use(integer, timestamptz);
DROP INDEX agent_certificates_authority_idx;
ALTER TABLE agent_certificates DROP COLUMN authority_id;
REVOKE UPDATE ON pki_authorities FROM sermon_app;
ALTER TABLE pki_authorities DROP CONSTRAINT pki_authorities_issuing_not_retired,
    DROP COLUMN retired_by, DROP COLUMN started_by, DROP COLUMN retired_at;
