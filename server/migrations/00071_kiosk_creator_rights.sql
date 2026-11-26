-- Rights behind the kiosk tokens and dashboards of members (M5-33, ADR 0040 addendum):
--   * a member who is no longer admin of all groups – role lowered or taken, membership ended,
--     user deleted, by hand or through OIDC – has their open kiosk tokens revoked, in the same
--     transaction. The trigger is deferred to the commit, so that a change that ends with the
--     admin role again (delete and insert in one transaction) revokes nothing. Getting the role
--     back later revives nothing;
--   * a member who leaves the organization takes their private dashboards along; nobody else
--     could see or delete them. Shared ones stay with the organization.
-- Both run as sermon_definer: a cascade (user or organization deleted) runs outside the tenant.

-- +goose Up
GRANT CREATE ON SCHEMA public TO sermon_definer;

GRANT SELECT ON role_bindings TO sermon_definer;
CREATE POLICY definer_read ON role_bindings FOR SELECT TO sermon_definer USING (true);
GRANT UPDATE (revoked_at) ON kiosk_tokens TO sermon_definer;
CREATE POLICY definer_revoke ON kiosk_tokens FOR UPDATE TO sermon_definer USING (true) WITH CHECK (true);
GRANT DELETE ON dashboards TO sermon_definer;
CREATE POLICY definer_delete_private ON dashboards FOR DELETE TO sermon_definer USING (NOT shared);

-- +goose StatementBegin
CREATE FUNCTION kiosk_tokens_revoke_without_admin() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM public.role_bindings b
                   WHERE b.org_id = OLD.org_id AND b.user_id = OLD.user_id AND b.group_id IS NULL AND b.role = 'admin') THEN
        UPDATE public.kiosk_tokens SET revoked_at = now()
        WHERE org_id = OLD.org_id AND created_by = OLD.user_id AND revoked_at IS NULL;
    END IF;
    RETURN NULL;
END
$$;
-- +goose StatementEnd
ALTER FUNCTION kiosk_tokens_revoke_without_admin() OWNER TO sermon_definer;
REVOKE ALL ON FUNCTION kiosk_tokens_revoke_without_admin() FROM PUBLIC;
CREATE CONSTRAINT TRIGGER kiosk_tokens_revoke_without_admin
    AFTER UPDATE OR DELETE ON role_bindings
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW
    WHEN (OLD.group_id IS NULL AND OLD.role = 'admin')
    EXECUTE FUNCTION kiosk_tokens_revoke_without_admin();

-- +goose StatementBegin
CREATE FUNCTION dashboards_delete_private_of_member() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
BEGIN
    DELETE FROM public.dashboards WHERE org_id = OLD.org_id AND owner_id = OLD.user_id AND NOT shared;
    RETURN NULL;
END
$$;
-- +goose StatementEnd
ALTER FUNCTION dashboards_delete_private_of_member() OWNER TO sermon_definer;
REVOKE ALL ON FUNCTION dashboards_delete_private_of_member() FROM PUBLIC;
CREATE TRIGGER dashboards_delete_private_of_member
    AFTER DELETE ON memberships
    FOR EACH ROW
    EXECUTE FUNCTION dashboards_delete_private_of_member();

REVOKE CREATE ON SCHEMA public FROM sermon_definer;

-- +goose Down
DROP TRIGGER dashboards_delete_private_of_member ON memberships;
DROP FUNCTION dashboards_delete_private_of_member();
DROP TRIGGER kiosk_tokens_revoke_without_admin ON role_bindings;
DROP FUNCTION kiosk_tokens_revoke_without_admin();
DROP POLICY definer_delete_private ON dashboards;
REVOKE DELETE ON dashboards FROM sermon_definer;
DROP POLICY definer_revoke ON kiosk_tokens;
REVOKE UPDATE (revoked_at) ON kiosk_tokens FROM sermon_definer;
DROP POLICY definer_read ON role_bindings;
REVOKE SELECT ON role_bindings FROM sermon_definer;
