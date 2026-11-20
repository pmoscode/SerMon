-- Users are global identities (one user, many organizations): an admin of one organization must
-- not change the e-mail (the login) or the name of a user who is also a member elsewhere (M1-35).
-- No use case updates users; profile changes by the users themselves will get their own path.

-- +goose Up
DROP POLICY tenant_write ON users;
REVOKE UPDATE ON users FROM sermon_app;

-- +goose Down
GRANT UPDATE ON users TO sermon_app;
CREATE POLICY tenant_write ON users FOR UPDATE
    USING (EXISTS (SELECT 1 FROM memberships m WHERE m.user_id = users.id AND m.org_id = current_org_id()));
