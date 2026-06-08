-- +goose Up
-- +goose StatementBegin
ALTER TABLE media_item_provider_ids
    ADD COLUMN IF NOT EXISTS item_type TEXT;

UPDATE media_item_provider_ids mip
SET item_type = mi.type
FROM media_items mi
WHERE mi.content_id = mip.content_id
  AND mip.item_type IS DISTINCT FROM mi.type;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM media_item_provider_ids
        WHERE TRIM(COALESCE(item_type, '')) = ''
    ) THEN
        RAISE EXCEPTION 'media_item_provider_ids contains rows without item_type; resolve before applying migration 088';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM (
            SELECT provider, provider_id, item_type
            FROM media_item_provider_ids
            GROUP BY provider, provider_id, item_type
            HAVING COUNT(*) > 1
        ) duplicate_ids
    ) THEN
        RAISE EXCEPTION 'duplicate provider ids exist within the same item type; resolve duplicates before applying migration 088';
    END IF;
END $$;

DROP INDEX IF EXISTS idx_media_item_provider_ids_provider_id;

ALTER TABLE media_item_provider_ids
    DROP CONSTRAINT IF EXISTS media_item_provider_ids_provider_provider_id_key;

ALTER TABLE media_item_provider_ids
    ALTER COLUMN item_type SET NOT NULL;

ALTER TABLE media_item_provider_ids
    ADD CONSTRAINT media_item_provider_ids_provider_provider_id_item_type_key
    UNIQUE (provider, provider_id, item_type);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM (
            SELECT provider, provider_id
            FROM media_item_provider_ids
            GROUP BY provider, provider_id
            HAVING COUNT(*) > 1
        ) duplicate_ids
    ) THEN
        RAISE EXCEPTION 'cross-type provider ids exist; resolve duplicates before rolling back migration 088';
    END IF;
END $$;

ALTER TABLE media_item_provider_ids
    DROP CONSTRAINT IF EXISTS media_item_provider_ids_provider_provider_id_item_type_key;

ALTER TABLE media_item_provider_ids
    DROP COLUMN IF EXISTS item_type;

ALTER TABLE media_item_provider_ids
    ADD CONSTRAINT media_item_provider_ids_provider_provider_id_key
    UNIQUE (provider, provider_id);

CREATE INDEX IF NOT EXISTS idx_media_item_provider_ids_provider_id
    ON media_item_provider_ids (provider, provider_id);
-- +goose StatementEnd
