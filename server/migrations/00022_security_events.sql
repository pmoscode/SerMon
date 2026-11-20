-- Security events (KONZEPT §11, M2-07): what the agents parse from the hosts' logs. Partitioned by
-- month of the event time and kept for a year (ADR 0018); append-only for the application.
--
-- No DDL at run time (ADR 0007: sermon_definer has CREATE only during migrations): this
-- migration creates the monthly partitions up to 2036 ahead; a later migration extends them
-- (security_events_horizon tells how far they reach, the server warns a year before). The tables
-- belong to sermon_definer, whose function security_events_expire drops whole months – DROP
-- needs ownership, not CREATE. TimescaleDB was no option: its chunks have no RLS and inherit the
-- grants of the hypertable (tried, M2-07).
--
-- sermon_app may use the parent table only; the partitions have RLS as well and no grants.
--
-- Deduplication: nodes.event_seq is the highest batch sequence number whose events are stored,
-- set in the same transaction as the insert (a batch resent after a reconnect is skipped).

-- +goose Up
ALTER TABLE nodes ADD COLUMN event_seq bigint NOT NULL DEFAULT 0 CHECK (event_seq >= 0);

CREATE SEQUENCE security_events_id_seq AS bigint;

CREATE TABLE security_events (
    org_id      uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    id          bigint      NOT NULL DEFAULT nextval('security_events_id_seq'),
    -- No foreign key: the events of a removed node stay for the retention.
    node_id     uuid        NOT NULL,
    occurred_at timestamptz NOT NULL,
    -- Insert time (not the transaction start): new events for the alert engine (M2-08).
    received_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    kind        text        NOT NULL CHECK (kind IN (
        'ssh.login.failed', 'ssh.login.invalid_user', 'ssh.login.success', 'ssh.preauth.disconnect',
        'sudo.auth.failed', 'sudo.not_allowed', 'sudo.command', 'account.created', 'account.deleted',
        'system.reboot', 'system.oom_kill')),
    user_name   text        CHECK (octet_length(user_name) <= 128),
    remote_ip   inet,
    remote_host text        CHECK (octet_length(remote_host) <= 253),
    remote_port integer     CHECK (remote_port BETWEEN 1 AND 65535),
    details     jsonb       NOT NULL DEFAULT '{}' CHECK (octet_length(details::text) <= 4096),
    PRIMARY KEY (org_id, occurred_at, id),
    CHECK (remote_ip IS NULL OR remote_host IS NULL)
) PARTITION BY RANGE (occurred_at);
CREATE INDEX security_events_node_idx ON security_events (org_id, node_id, occurred_at DESC);
CREATE INDEX security_events_kind_idx ON security_events (org_id, kind, occurred_at DESC);
CREATE INDEX security_events_ip_idx ON security_events (org_id, remote_ip, occurred_at DESC) WHERE remote_ip IS NOT NULL;
CREATE INDEX security_events_received_idx ON security_events (org_id, received_at, id);
ALTER TABLE security_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE security_events FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON security_events
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT ON security_events TO sermon_app;
GRANT USAGE ON SEQUENCE security_events_id_seq TO sermon_app;

-- The monthly partitions, each with RLS.
-- +goose StatementBegin
DO $$
DECLARE
    month timestamp := timestamp '2026-10-01';
    name text;
BEGIN
    WHILE month < timestamp '2037-01-01' LOOP
        name := 'security_events_' || to_char(month, 'YYYYMM');
        EXECUTE format('CREATE TABLE %I PARTITION OF security_events FOR VALUES FROM (%L) TO (%L)',
            name, month AT TIME ZONE 'UTC', (month + interval '1 month') AT TIME ZONE 'UTC');
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', name);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', name);
        month := month + interval '1 month';
    END LOOP;
END
$$;
-- +goose StatementEnd

-- Drops the partitions that end at or before p_before (whole months only); returns how many.
-- +goose StatementBegin
CREATE FUNCTION security_events_expire(p_before timestamptz) RETURNS integer
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
DECLARE
    partition record;
    dropped integer := 0;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtext('security_events_partitions'));
    FOR partition IN
        SELECT c.relname
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE i.inhparent = 'public.security_events'::regclass AND n.nspname = 'public'
          AND c.relname ~ '^security_events_[0-9]{6}$'
    LOOP
        IF (to_timestamp(substring(partition.relname FROM 17), 'YYYYMM') AT TIME ZONE 'UTC')::timestamp
               + interval '1 month' <= (p_before AT TIME ZONE 'UTC') THEN
            EXECUTE format('DROP TABLE public.%I', partition.relname);
            dropped := dropped + 1;
        END IF;
    END LOOP;
    RETURN dropped;
END
$$;
-- +goose StatementEnd

-- The end of the last partition: events after it can't be stored.
CREATE FUNCTION security_events_horizon() RETURNS timestamptz
    LANGUAGE sql STABLE
    SET search_path = pg_catalog, public
    AS $$
        SELECT max((to_timestamp(substring(c.relname FROM 17), 'YYYYMM') AT TIME ZONE 'UTC')::timestamp
                   + interval '1 month') AT TIME ZONE 'UTC'
        FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
        WHERE i.inhparent = 'public.security_events'::regclass AND c.relname ~ '^security_events_[0-9]{6}$'
    $$;

-- Hand the tables over to sermon_definer; it needs CREATE on the schema for that, and only now.
GRANT CREATE ON SCHEMA public TO sermon_definer;
-- +goose StatementBegin
DO $$
DECLARE
    partition record;
BEGIN
    FOR partition IN
        SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
        WHERE i.inhparent = 'public.security_events'::regclass
    LOOP
        EXECUTE format('ALTER TABLE %I OWNER TO sermon_definer', partition.relname);
    END LOOP;
END
$$;
-- +goose StatementEnd
ALTER TABLE security_events OWNER TO sermon_definer;
ALTER FUNCTION security_events_expire(timestamptz) OWNER TO sermon_definer;
REVOKE CREATE ON SCHEMA public FROM sermon_definer;
REVOKE ALL ON FUNCTION security_events_expire(timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION security_events_expire(timestamptz) TO sermon_app;
GRANT EXECUTE ON FUNCTION security_events_horizon() TO sermon_app;

-- +goose Down
DROP FUNCTION security_events_horizon();
DROP FUNCTION security_events_expire(timestamptz);
DROP TABLE security_events;
DROP SEQUENCE security_events_id_seq;
ALTER TABLE nodes DROP COLUMN event_seq;
