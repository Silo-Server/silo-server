-- +goose Up
-- Ebook items created by the scanner before status handling was fixed were
-- written with status = '' (the upsert bypasses the column default), which
-- disabled the pending -> matched promotion in enrichment and the curated-
-- metadata guard. Rows enrichment already processed become 'matched'; the
-- rest become 'pending' so the next enrichment sweep promotes them normally.
UPDATE media_items
SET status = CASE
        WHEN matched_at IS NOT NULL OR last_refreshed IS NOT NULL THEN 'matched'
        ELSE 'pending'
    END
WHERE type = 'ebook'
  AND status = '';

-- +goose Down
-- Irreversible data backfill: the pre-migration status ('') carried no
-- information, so there is nothing meaningful to restore.
SELECT 1;
