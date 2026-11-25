-- Updates by hand (M4-14): "update now" for a node or a selection is a rollout of its own per
-- group, run by the orchestrator with the group's maxParallel; the caller chose the scope (its
-- mode) and whether to reboot. Its window is the next two hours.

-- +goose Up
ALTER TABLE update_rollouts
    ADD COLUMN manual      boolean  NOT NULL DEFAULT false,
    ADD COLUMN mode        smallint CHECK (mode BETWEEN 2 AND 3),
    ADD COLUMN auto_reboot boolean  NOT NULL DEFAULT false,
    ADD COLUMN created_by  uuid     REFERENCES users (id) ON DELETE SET NULL,
    ADD CONSTRAINT update_rollouts_manual_check CHECK (manual = (mode IS NOT NULL));

-- +goose Down
ALTER TABLE update_rollouts
    DROP CONSTRAINT update_rollouts_manual_check,
    DROP COLUMN created_by, DROP COLUMN auto_reboot, DROP COLUMN mode, DROP COLUMN manual;
