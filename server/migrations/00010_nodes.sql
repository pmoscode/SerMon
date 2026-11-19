-- Nodes (KONZEPT §6, §7): created by enrollment, one node belongs to exactly one group.

-- +goose Up

CREATE TABLE nodes (
    id             uuid        PRIMARY KEY,
    org_id         uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    group_id       uuid        NOT NULL,
    hostname       text        NOT NULL CHECK (char_length(hostname) BETWEEN 1 AND 253),
    machine_id     text        NOT NULL CHECK (machine_id ~ '^[0-9a-f]{32}$'),
    os_id          text        NOT NULL CHECK (char_length(os_id) BETWEEN 1 AND 64),
    os_version     text        NOT NULL CHECK (char_length(os_version) <= 64),
    kernel_release text        NOT NULL CHECK (char_length(kernel_release) BETWEEN 1 AND 128),
    architecture   text        NOT NULL CHECK (architecture IN ('amd64', 'arm64')),
    cpu_count      integer     NOT NULL CHECK (cpu_count BETWEEN 1 AND 4096),
    memory_bytes   bigint      NOT NULL CHECK (memory_bytes > 0),
    enrolled_with  uuid,       -- enrollment token (kept for the audit trail, token rows may be removed)
    created_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, id),
    -- Re-enrolling the same host needs a confirmed takeover in the UI (ADR 0009), never a silent duplicate.
    UNIQUE (org_id, machine_id),
    -- A group with nodes cannot be deleted (move or delete the nodes first).
    FOREIGN KEY (org_id, group_id) REFERENCES node_groups (org_id, id) ON DELETE RESTRICT
);
CREATE INDEX nodes_group_idx ON nodes (org_id, group_id);
ALTER TABLE nodes ENABLE ROW LEVEL SECURITY;
ALTER TABLE nodes FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON nodes
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON nodes TO sermon_app;

-- Certificates now reference their node (announced in 00002).
ALTER TABLE agent_certificates
    ADD CONSTRAINT agent_certificates_node_fk FOREIGN KEY (org_id, node_id) REFERENCES nodes (org_id, id) ON DELETE CASCADE;

-- +goose Down
ALTER TABLE agent_certificates DROP CONSTRAINT agent_certificates_node_fk;
DROP TABLE nodes;
