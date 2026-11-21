-- +goose Up
-- One row per scheduled firing of a task that several API servers schedule
-- but that runs once per firing across them (plugin scheduled tasks). The
-- server whose insert wins runs the firing; the others skip it. slot is the
-- firing's cluster-wide instant: the scheduled time of a daily or weekly
-- trigger, the interval-aligned instant of an interval trigger, or the claim
-- time of a startup trigger. Claims are pruned as they are taken.
CREATE TABLE task_firing_claims (
    task_key   TEXT        NOT NULL,
    slot       TIMESTAMPTZ NOT NULL,
    claimed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (task_key, slot)
);
CREATE INDEX task_firing_claims_task_claimed_idx ON task_firing_claims (task_key, claimed_at);

-- +goose Down
DROP TABLE task_firing_claims;
