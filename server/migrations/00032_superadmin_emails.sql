-- Self-monitoring (M2-24): the server mails its superadmins. The application role cannot read users
-- without a tenant; this definer function returns only the addresses of the superadmins.

-- +goose Up
GRANT SELECT ON superadmins TO sermon_definer;
GRANT CREATE ON SCHEMA public TO sermon_definer;

-- +goose StatementBegin
CREATE FUNCTION superadmin_emails() RETURNS SETOF text
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT u.email FROM public.superadmins s JOIN public.users u ON u.id = s.user_id ORDER BY u.email
    $$;
-- +goose StatementEnd

ALTER FUNCTION superadmin_emails() OWNER TO sermon_definer;
REVOKE ALL ON FUNCTION superadmin_emails() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION superadmin_emails() TO sermon_app;
REVOKE CREATE ON SCHEMA public FROM sermon_definer;

-- +goose Down
DROP FUNCTION superadmin_emails();
REVOKE SELECT ON superadmins FROM sermon_definer;
