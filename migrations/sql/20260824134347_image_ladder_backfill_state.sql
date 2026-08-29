-- +goose Up
-- Records which artwork variant ladder this deployment has already backfilled.
-- Adding a rung (see artworkkey.LadderVersion) means every previously cached
-- image is missing an object, so the ladder backfill runs once per version and
-- then stops. A single row: this is deployment-wide state, not per-item.
CREATE TABLE IF NOT EXISTS image_ladder_backfill_state (
    id                 smallint PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    backfilled_version integer     NOT NULL DEFAULT 0,
    -- When a pass last ran. A deployment holding artwork that cannot be
    -- regenerated never reaches "done", so the sweep must not re-scan the whole
    -- catalog on every scheduler tick; this paces it instead.
    last_attempt_at    timestamptz,
    updated_at         timestamptz NOT NULL DEFAULT NOW()
);

-- Version 0 means "no ladder has been backfilled", which is correct for both a
-- fresh install and an upgrade: a fresh install has no pre-ladder artwork, so
-- its first pass finds nothing and completes immediately.
INSERT INTO image_ladder_backfill_state (id, backfilled_version)
VALUES (1, 0)
ON CONFLICT (id) DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS image_ladder_backfill_state;
