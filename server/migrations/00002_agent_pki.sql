-- Agent PKI (KONZEPT §6, ADR 0007): CA material and issued agent certificates.

-- +goose Up

-- Instance-level CA material (no tenant). Private keys are sealed with the master key
-- (secrets.Box, associated data "pki:<id>:root|intermediate"); the database never sees them in clear.
CREATE TABLE pki_authorities (
    id                      integer     GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    root_cert               bytea       NOT NULL,
    root_key_sealed         bytea       NOT NULL,
    intermediate_cert       bytea       NOT NULL,
    intermediate_key_sealed bytea       NOT NULL,
    active                  boolean     NOT NULL DEFAULT true,
    created_at              timestamptz NOT NULL DEFAULT now()
);
-- At most one active authority; CA rollover (M1-11b) deactivates the old one.
CREATE UNIQUE INDEX pki_authorities_single_active ON pki_authorities (active) WHERE active;
GRANT SELECT, INSERT ON pki_authorities TO sermon_app;
GRANT USAGE ON SEQUENCE pki_authorities_id_seq TO sermon_app;

-- Issued agent certificates (serial = lowercase hex of the 127-bit random serial number).
CREATE TABLE agent_certificates (
    serial     text        PRIMARY KEY CHECK (serial ~ '^[0-9a-f]{32}$'),
    org_id     uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    node_id    uuid        NOT NULL, -- FK to nodes follows with M1-09
    not_before timestamptz NOT NULL,
    not_after  timestamptz NOT NULL,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (not_after > not_before)
);
CREATE INDEX agent_certificates_node_idx ON agent_certificates (org_id, node_id);

-- RLS without FORCE (deliberate exception, ADR 0007): the table owner – and thereby the
-- SECURITY DEFINER function below – must see all rows for the revocation check during the TLS
-- handshake, when no tenant is known yet. sermon_app is not the owner, so RLS applies to it.
ALTER TABLE agent_certificates ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON agent_certificates
    USING (org_id = current_org_id())
    WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, UPDATE ON agent_certificates TO sermon_app;

-- Revocation check without tenant: answers only "usable yes/no" for one serial.
-- Unknown serials are unusable (fail closed).
CREATE FUNCTION agent_certificate_usable(p_serial text, p_at timestamptz) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT EXISTS (
            SELECT 1 FROM public.agent_certificates
            WHERE serial = p_serial AND revoked_at IS NULL AND p_at >= not_before AND p_at < not_after
        )
    $$;
REVOKE ALL ON FUNCTION agent_certificate_usable(text, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION agent_certificate_usable(text, timestamptz) TO sermon_app;

-- +goose Down
DROP FUNCTION agent_certificate_usable(text, timestamptz);
DROP TABLE agent_certificates;
DROP TABLE pki_authorities;
