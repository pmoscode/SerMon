-- Cancelling and pausing updates (M4-31, threat T97): a run cancelled before it went out (state 6),
-- one cancelled after it was sent (cancelled_at, withdrawn from the agent: cancel_sent_at), a
-- rollout cancelled by hand, the organization's switch that pauses all updates, and the count of
-- the runs under way on the whole instance for its limit.

-- +goose Up
ALTER TABLE node_commands
    DROP CONSTRAINT node_commands_state_check,
    ADD CONSTRAINT node_commands_state_check CHECK (state BETWEEN 1 AND 6),
    ADD COLUMN cancelled_at   timestamptz,
    ADD COLUMN cancelled_by   uuid REFERENCES users (id) ON DELETE SET NULL,
    ADD COLUMN cancel_sent_at timestamptz;

ALTER TABLE update_rollouts
    ADD COLUMN cancelled_at timestamptz,
    ADD COLUMN cancelled_by uuid REFERENCES users (id) ON DELETE SET NULL;

ALTER TABLE organizations
    ADD COLUMN updates_paused_at timestamptz,
    ADD COLUMN updates_paused_by uuid REFERENCES users (id) ON DELETE SET NULL;

-- The runs under way of all organizations (pending, sent, running; refreshes left out, a day at
-- most): a number only, for the instance's limit of the runs by hand.
CREATE POLICY definer_count ON node_commands FOR SELECT TO sermon_definer USING (true);
GRANT SELECT ON node_commands TO sermon_definer;
GRANT CREATE ON SCHEMA public TO sermon_definer;
CREATE FUNCTION open_node_runs() RETURNS bigint
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT count(*) FROM public.node_commands
        WHERE kind <> 1 AND state IN (1, 2, 3) AND created_at > now() - interval '1 day'
    $$;
ALTER FUNCTION open_node_runs() OWNER TO sermon_definer;
REVOKE ALL ON FUNCTION open_node_runs() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION open_node_runs() TO sermon_app;
REVOKE CREATE ON SCHEMA public FROM sermon_definer;

-- +goose Down
DROP FUNCTION open_node_runs();
REVOKE SELECT ON node_commands FROM sermon_definer;
DROP POLICY definer_count ON node_commands;
ALTER TABLE organizations DROP COLUMN updates_paused_by, DROP COLUMN updates_paused_at;
ALTER TABLE update_rollouts DROP COLUMN cancelled_by, DROP COLUMN cancelled_at;
UPDATE node_commands SET state = 5 WHERE state = 6;
ALTER TABLE node_commands
    DROP COLUMN cancel_sent_at,
    DROP COLUMN cancelled_by,
    DROP COLUMN cancelled_at,
    DROP CONSTRAINT node_commands_state_check,
    ADD CONSTRAINT node_commands_state_check CHECK (state BETWEEN 1 AND 5);
