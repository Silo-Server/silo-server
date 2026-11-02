-- +goose Up
-- When an admin last chose the item's identity with Identify. A refresh keeps
-- that identity when a local .nfo names a different one, instead of letting
-- the file's <uniqueid> re-match the item. NULL: the matcher chose it.
ALTER TABLE media_items ADD COLUMN IF NOT EXISTS identity_chosen_at timestamptz;

-- +goose Down
ALTER TABLE media_items DROP COLUMN IF EXISTS identity_chosen_at;
