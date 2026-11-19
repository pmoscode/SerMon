-- Tenancy foundation: organizations, users, memberships with row-level security (ADR 0007).
--
-- Roles:
--   * the migration role (owner of all objects) runs these migrations only;
--   * sermon-server connects as `sermon_app`: no superuser, not owner, no BYPASSRLS – so RLS
--     always applies. LOGIN and password are set by the operator (sermon-server init / compose).

-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'sermon_app') THEN
        CREATE ROLE sermon_app NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
    END IF;
END
$$;
-- +goose StatementEnd

-- The organization of the current transaction (SET LOCAL sermon.org_id), NULL if unset.
-- With NULL every tenant policy evaluates to false: no tenant set ⇒ no rows.
CREATE FUNCTION current_org_id() RETURNS uuid
    LANGUAGE sql STABLE PARALLEL SAFE
    AS $$ SELECT NULLIF(current_setting('sermon.org_id', true), '')::uuid $$;

CREATE TABLE organizations (
    id         uuid        PRIMARY KEY,
    name       text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE users (
    id           uuid        PRIMARY KEY,
    email        text        NOT NULL CHECK (char_length(email) BETWEEN 3 AND 320),
    display_name text        NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 200),
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_email_key ON users (lower(email));

CREATE TABLE memberships (
    org_id     uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, user_id)
);
CREATE INDEX memberships_user_id_idx ON memberships (user_id);

-- Row-level security. FORCE also applies it to the table owner.
ALTER TABLE organizations ENABLE ROW LEVEL SECURITY;
ALTER TABLE organizations FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON organizations
    USING (id = current_org_id())
    WITH CHECK (id = current_org_id());

ALTER TABLE memberships ENABLE ROW LEVEL SECURITY;
ALTER TABLE memberships FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON memberships
    USING (org_id = current_org_id())
    WITH CHECK (org_id = current_org_id());

-- Users are global identities (one user, many organizations): visible only through a
-- membership in the current organization. Creating users is allowed (invitation flow);
-- reading them back requires the membership.
ALTER TABLE users ENABLE ROW LEVEL SECURITY;
ALTER TABLE users FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_read ON users FOR SELECT
    USING (EXISTS (SELECT 1 FROM memberships m WHERE m.user_id = users.id AND m.org_id = current_org_id()));
CREATE POLICY tenant_write ON users FOR UPDATE
    USING (EXISTS (SELECT 1 FROM memberships m WHERE m.user_id = users.id AND m.org_id = current_org_id()));
CREATE POLICY create_user ON users FOR INSERT
    WITH CHECK (true);

GRANT EXECUTE ON FUNCTION current_org_id() TO sermon_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON organizations, memberships TO sermon_app;
GRANT SELECT, INSERT, UPDATE ON users TO sermon_app;

-- +goose Down
DROP TABLE memberships;
DROP TABLE users;
DROP TABLE organizations;
DROP FUNCTION current_org_id();
-- The role sermon_app is kept: it may own sessions or be managed outside the migrations.
