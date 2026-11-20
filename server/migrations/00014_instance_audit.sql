-- Instance-wide audit log (KONZEPT §18.1 Repudiation, IMPLEMENTIERUNGSPLAN M1-11b): actions of the
-- superadmin and of the system outside any organization, e.g. agent CA rollover. Same hash chain
-- as audit_log, one chain for the whole instance. No tenant data, so no RLS; only the superadmin
-- reads it through the API.

-- +goose Up
CREATE TABLE instance_audit_log (
    id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    seq        bigint      NOT NULL UNIQUE,
    at         timestamptz NOT NULL DEFAULT clock_timestamp(),
    actor_id   uuid,       -- NULL for system actions
    action     text        NOT NULL CHECK (char_length(action) BETWEEN 1 AND 100),
    target     text        NOT NULL CHECK (char_length(target) <= 300),
    details    jsonb       NOT NULL DEFAULT '{}',
    prev_hash  bytea       NOT NULL,
    hash       bytea       NOT NULL
);
GRANT SELECT, INSERT ON instance_audit_log TO sermon_app;

-- +goose StatementBegin
CREATE FUNCTION instance_audit_log_chain() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, public
    AS $$
DECLARE
    last_hash bytea;
    last_seq  bigint;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended('audit:instance', 0));
    SELECT hash, seq INTO last_hash, last_seq FROM public.instance_audit_log ORDER BY seq DESC LIMIT 1;
    NEW.seq := COALESCE(last_seq, 0) + 1;
    NEW.at := clock_timestamp();
    NEW.prev_hash := COALESCE(last_hash, '\x'::bytea);
    NEW.hash := sha256(NEW.prev_hash || convert_to(concat_ws(E'\x1f',
        NEW.seq::text, to_char(NEW.at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        COALESCE(NEW.actor_id::text, ''), NEW.action, NEW.target, NEW.details::text), 'UTF8'));
    RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER instance_audit_log_chain BEFORE INSERT ON instance_audit_log
    FOR EACH ROW EXECUTE FUNCTION instance_audit_log_chain();

-- +goose Down
DROP TABLE instance_audit_log;
DROP FUNCTION instance_audit_log_chain();
