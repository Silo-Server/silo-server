-- +goose Up
-- When an admin marked a provisional (pending, unmatched or ambiguous) item as
-- intentionally unmatched. Such an item leaves the admin unmatched list and
-- automatic matching leaves it alone; Identify still works and a match clears
-- it. NULL: the item is not ignored.
ALTER TABLE media_items ADD COLUMN IF NOT EXISTS match_ignored_at timestamptz;

-- +goose Down
ALTER TABLE media_items DROP COLUMN IF EXISTS match_ignored_at;
