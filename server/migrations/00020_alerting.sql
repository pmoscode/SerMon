-- Alert engine (KONZEPT §12.1, M2-02): instances (rule × node × dimension) with their state,
-- the history of their transitions and the leases that let one server instance evaluate an
-- organization. All three are tenant tables with RLS (ADR 0007); the engine works in the
-- organization's tenant transaction, the lease included.

-- +goose Up
CREATE TABLE alert_instances (
    org_id        uuid             NOT NULL,
    rule_id       text             NOT NULL CHECK (rule_id ~ '^[a-z][a-z0-9_]{0,31}(\.[a-z][a-z0-9_]{0,31}){2,3}$'),
    node_id       uuid             NOT NULL,
    dimension     text             NOT NULL DEFAULT '' CHECK (char_length(dimension) <= 500),
    state         smallint         NOT NULL CHECK (state BETWEEN 0 AND 4),
    since         timestamptz      NOT NULL,
    pending_since timestamptz,
    fired_at      timestamptz,
    ack_by        uuid,
    ack_at        timestamptz,
    flapping      boolean          NOT NULL DEFAULT false,
    value         double precision NOT NULL DEFAULT 0,
    flips         timestamptz[]    NOT NULL DEFAULT '{}' CHECK (cardinality(flips) <= 64),
    updated_at    timestamptz      NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, rule_id, node_id, dimension),
    FOREIGN KEY (org_id, node_id) REFERENCES nodes (org_id, id) ON DELETE CASCADE,
    CHECK ((ack_by IS NULL) = (ack_at IS NULL))
);
ALTER TABLE alert_instances ENABLE ROW LEVEL SECURITY;
ALTER TABLE alert_instances FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON alert_instances
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON alert_instances TO sermon_app;

-- Append-only for the application: the record of what happened (nodes may go, the history stays).
CREATE TABLE alert_history (
    id         bigint           GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id     uuid             NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    rule_id    text             NOT NULL CHECK (char_length(rule_id) <= 100),
    node_id    uuid             NOT NULL,
    dimension  text             NOT NULL DEFAULT '' CHECK (char_length(dimension) <= 500),
    kind       smallint         NOT NULL CHECK (kind BETWEEN 1 AND 4),
    from_state smallint         NOT NULL CHECK (from_state BETWEEN 0 AND 4),
    to_state   smallint         NOT NULL CHECK (to_state BETWEEN 0 AND 4),
    at         timestamptz      NOT NULL,
    value      double precision NOT NULL,
    by_user    uuid
);
CREATE INDEX alert_history_org_at_idx ON alert_history (org_id, at DESC);
CREATE INDEX alert_history_instance_idx ON alert_history (org_id, rule_id, node_id, dimension, at DESC);
ALTER TABLE alert_history ENABLE ROW LEVEL SECURITY;
ALTER TABLE alert_history FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON alert_history
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT ON alert_history TO sermon_app;

-- One server instance evaluates an organization; another takes over when the lease expires.
CREATE TABLE alert_leases (
    org_id     uuid        PRIMARY KEY REFERENCES organizations (id) ON DELETE CASCADE,
    holder     text        NOT NULL CHECK (char_length(holder) BETWEEN 1 AND 64),
    expires_at timestamptz NOT NULL
);
ALTER TABLE alert_leases ENABLE ROW LEVEL SECURITY;
ALTER TABLE alert_leases FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON alert_leases
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, UPDATE ON alert_leases TO sermon_app;

-- +goose Down
DROP TABLE alert_leases;
DROP TABLE alert_history;
DROP TABLE alert_instances;
