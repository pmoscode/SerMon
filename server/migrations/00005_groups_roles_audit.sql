-- Node groups, role bindings and the audit log (KONZEPT §7, §18.1 Repudiation, ADR 0007).

-- +goose Up

CREATE TABLE node_groups (
    id         uuid        PRIMARY KEY,
    org_id     uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name       text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, name),
    UNIQUE (org_id, id) -- target of composite foreign keys (keeps references inside the tenant)
);
ALTER TABLE node_groups ENABLE ROW LEVEL SECURITY;
ALTER TABLE node_groups FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON node_groups
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON node_groups TO sermon_app;

-- A role on one node group, or on all groups of the organization (group_id IS NULL = "*").
CREATE TABLE role_bindings (
    id       uuid PRIMARY KEY,
    org_id   uuid NOT NULL,
    user_id  uuid NOT NULL,
    group_id uuid,
    role     text NOT NULL CHECK (role IN ('viewer', 'operator', 'admin')),
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (org_id, user_id) REFERENCES memberships (org_id, user_id) ON DELETE CASCADE,
    -- Composite key: a binding can only reference a group of the same organization.
    FOREIGN KEY (org_id, group_id) REFERENCES node_groups (org_id, id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX role_bindings_unique ON role_bindings (org_id, user_id, COALESCE(group_id, '00000000-0000-0000-0000-000000000000'::uuid));
ALTER TABLE role_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE role_bindings FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON role_bindings
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON role_bindings TO sermon_app;

-- Append-only audit log with a hash chain per organization: hash = sha256(prev_hash || entry).
-- sermon_app may only INSERT and SELECT; the trigger computes the chain (no client-supplied hashes).
CREATE TABLE audit_log (
    id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id     uuid        NOT NULL REFERENCES organizations (id) ON DELETE RESTRICT,
    -- Position in the organization's chain, assigned by the trigger under the advisory lock
    -- (the identity id is drawn before the lock and does not reflect the chain order).
    seq        bigint      NOT NULL,
    at         timestamptz NOT NULL DEFAULT clock_timestamp(),
    actor_id   uuid,       -- NULL for system actions
    action     text        NOT NULL CHECK (char_length(action) BETWEEN 1 AND 100),
    target     text        NOT NULL CHECK (char_length(target) <= 300),
    details    jsonb       NOT NULL DEFAULT '{}',
    prev_hash  bytea       NOT NULL,
    hash       bytea       NOT NULL
);
CREATE UNIQUE INDEX audit_log_org_seq ON audit_log (org_id, seq);
ALTER TABLE audit_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_log FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_read ON audit_log FOR SELECT USING (org_id = current_org_id());
CREATE POLICY tenant_append ON audit_log FOR INSERT WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT ON audit_log TO sermon_app;

-- +goose StatementBegin
CREATE FUNCTION audit_log_chain() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, public
    AS $$
DECLARE
    last_hash bytea;
    last_seq  bigint;
BEGIN
    -- Serialize appends per organization so the chain has no forks.
    PERFORM pg_advisory_xact_lock(hashtextextended('audit:' || NEW.org_id::text, 0));
    SELECT hash, seq INTO last_hash, last_seq FROM public.audit_log
        WHERE org_id = NEW.org_id ORDER BY seq DESC LIMIT 1;
    NEW.seq := COALESCE(last_seq, 0) + 1;
    NEW.at := clock_timestamp();
    NEW.prev_hash := COALESCE(last_hash, '\x'::bytea);
    NEW.hash := sha256(NEW.prev_hash || convert_to(concat_ws(E'\x1f',
        NEW.org_id::text, NEW.seq::text, to_char(NEW.at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        COALESCE(NEW.actor_id::text, ''), NEW.action, NEW.target, NEW.details::text), 'UTF8'));
    RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER audit_log_chain BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION audit_log_chain();

-- +goose Down
DROP TABLE audit_log;
DROP FUNCTION audit_log_chain();
DROP TABLE role_bindings;
DROP TABLE node_groups;
