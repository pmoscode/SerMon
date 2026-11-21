-- Notification policies and the routing state (KONZEPT §12.1, M2-14). All are tenant tables with
-- RLS (ADR 0007).
--
-- A policy matches alerts (matchers as JSON: severities, groups, rule patterns, labels) and
-- names its channels and, optionally, the channels of its escalation. Deleting a channel removes
-- it from the policies. alert_routing_state keeps, per firing instance and policy, when it last
-- notified (repeats) and whether it escalated; it goes with the instance and with the policy.

-- +goose Up
CREATE TABLE notification_policies (
    org_id             uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    id                 uuid        NOT NULL,
    name               text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    enabled            boolean     NOT NULL,
    matchers           jsonb       NOT NULL DEFAULT '{}'
        CHECK (jsonb_typeof(matchers) = 'object' AND octet_length(matchers::text) <= 16384),
    grouping           text        NOT NULL CHECK (grouping IN ('node', 'digest')),
    repeat_seconds     integer     NOT NULL DEFAULT 0 CHECK (repeat_seconds = 0 OR repeat_seconds BETWEEN 300 AND 604800),
    escalation_seconds integer     CHECK (escalation_seconds BETWEEN 60 AND 86400),
    created_at         timestamptz NOT NULL,
    updated_at         timestamptz NOT NULL,
    created_by         uuid,
    PRIMARY KEY (org_id, id)
);
ALTER TABLE notification_policies ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_policies FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON notification_policies
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON notification_policies TO sermon_app;

CREATE TABLE notification_policy_channels (
    org_id     uuid     NOT NULL,
    policy_id  uuid     NOT NULL,
    channel_id uuid     NOT NULL,
    escalation boolean  NOT NULL,
    position   smallint NOT NULL CHECK (position BETWEEN 0 AND 99),
    PRIMARY KEY (org_id, policy_id, escalation, channel_id),
    FOREIGN KEY (org_id, policy_id) REFERENCES notification_policies (org_id, id) ON DELETE CASCADE,
    FOREIGN KEY (org_id, channel_id) REFERENCES notification_channels (org_id, id) ON DELETE CASCADE
);
ALTER TABLE notification_policy_channels ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_policy_channels FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON notification_policy_channels
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON notification_policy_channels TO sermon_app;

CREATE TABLE alert_routing_state (
    org_id       uuid        NOT NULL,
    rule_id      text        NOT NULL,
    node_id      uuid        NOT NULL,
    dimension    text        NOT NULL,
    policy_id    uuid        NOT NULL,
    last_sent_at timestamptz NOT NULL,
    escalated    boolean     NOT NULL,
    PRIMARY KEY (org_id, rule_id, node_id, dimension, policy_id),
    FOREIGN KEY (org_id, rule_id, node_id, dimension)
        REFERENCES alert_instances (org_id, rule_id, node_id, dimension) ON DELETE CASCADE,
    FOREIGN KEY (org_id, policy_id) REFERENCES notification_policies (org_id, id) ON DELETE CASCADE
);
ALTER TABLE alert_routing_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE alert_routing_state FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON alert_routing_state
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON alert_routing_state TO sermon_app;

-- +goose Down
DROP TABLE alert_routing_state;
DROP TABLE notification_policy_channels;
DROP TABLE notification_policies;
