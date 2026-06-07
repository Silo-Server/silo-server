-- +goose Up
-- +goose StatementBegin
ALTER TABLE metadata_refresh_debt
    ADD COLUMN IF NOT EXISTS target_type TEXT NOT NULL DEFAULT 'item';

ALTER TABLE metadata_refresh_debt
    DROP CONSTRAINT IF EXISTS metadata_refresh_debt_content_id_fkey;

ALTER TABLE metadata_refresh_debt
    DROP CONSTRAINT IF EXISTS metadata_refresh_debt_pkey;

ALTER TABLE metadata_refresh_debt
    ADD CONSTRAINT metadata_refresh_debt_target_type_check
        CHECK (target_type IN ('item', 'season', 'episode'));

ALTER TABLE metadata_refresh_debt
    ADD PRIMARY KEY (target_type, content_id);

CREATE INDEX IF NOT EXISTS idx_metadata_refresh_debt_target_due
    ON metadata_refresh_debt (target_type, next_refresh_at, priority DESC, updated_at);

INSERT INTO metadata_refresh_debt (
    target_type,
    content_id,
    priority,
    reason_mask,
    next_refresh_at,
    updated_at
)
SELECT
    'episode',
    e.content_id,
    300,
    1,
    NOW(),
    NOW()
FROM episodes e
WHERE EXISTS (
    SELECT 1
    FROM episode_libraries el
    JOIN media_folders folders ON folders.id = el.media_folder_id
    WHERE el.episode_id = e.content_id
      AND folders.enabled = TRUE
)
AND (
    COALESCE(BTRIM(e.title), '') = ''
    OR e.title ~* '^(tba|tbd|episode\s+\d+)$'
    OR COALESCE(BTRIM(e.overview), '') = ''
    OR LOWER(COALESCE(BTRIM(e.metadata_source), '')) = 'scanner_fallback'
    OR (
        e.air_date IS NOT NULL
        AND e.air_date >= CURRENT_DATE - INTERVAL '45 days'
        AND COALESCE(BTRIM(e.still_path), '') = ''
    )
)
ON CONFLICT (target_type, content_id) DO NOTHING;

UPDATE metadata_refresh_debt
SET reason_mask = reason_mask & (-2),
    priority = CASE
        WHEN (reason_mask & 2) <> 0 THEN 250
        WHEN (reason_mask & 4) <> 0 THEN 200
        WHEN (reason_mask & 8) <> 0 THEN 150
        ELSE 100
    END,
    updated_at = NOW()
WHERE target_type = 'item'
  AND (reason_mask & 1) <> 0;

DELETE FROM metadata_refresh_debt
WHERE target_type = 'item'
  AND reason_mask = 0;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM metadata_refresh_debt
WHERE target_type <> 'item';

ALTER TABLE metadata_refresh_debt
    DROP CONSTRAINT IF EXISTS metadata_refresh_debt_pkey;

ALTER TABLE metadata_refresh_debt
    DROP CONSTRAINT IF EXISTS metadata_refresh_debt_target_type_check;

ALTER TABLE metadata_refresh_debt
    ADD PRIMARY KEY (content_id);

ALTER TABLE metadata_refresh_debt
    ADD CONSTRAINT metadata_refresh_debt_content_id_fkey
        FOREIGN KEY (content_id) REFERENCES media_items(content_id) ON DELETE CASCADE;

DROP INDEX IF EXISTS idx_metadata_refresh_debt_target_due;

ALTER TABLE metadata_refresh_debt
    DROP COLUMN IF EXISTS target_type;
-- +goose StatementEnd
