-- +goose Up
ALTER TABLE playback_v3_replans
    ADD COLUMN lease_owner TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE playback_v3_replans
    DROP COLUMN IF EXISTS lease_owner;
