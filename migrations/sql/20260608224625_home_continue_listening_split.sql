-- +goose Up
-- +goose StatementBegin
UPDATE page_sections
SET config = config || '{"continue_type":"listening"}'::jsonb,
    updated_at = NOW()
WHERE section_type = 'continue_watching'
  AND NOT (config ? 'continue_type')
  AND config->>'filter_type' = 'audiobook';

UPDATE page_sections ps
SET config = ps.config || '{"continue_type":"listening"}'::jsonb,
    updated_at = NOW()
FROM media_folders mf
WHERE ps.scope = 'library'
  AND ps.library_id = mf.id
  AND lower(mf.type) IN ('audiobook', 'audiobooks')
  AND ps.section_type = 'continue_watching'
  AND NOT (ps.config ? 'continue_type');

UPDATE page_sections
SET config = config || '{"continue_type":"watching"}'::jsonb,
    updated_at = NOW()
WHERE section_type = 'continue_watching'
  AND NOT (config ? 'continue_type');

WITH insert_position AS (
    SELECT COALESCE(
        (
            SELECT ps.position + 1
            FROM page_sections ps
            WHERE ps.scope = 'home'
              AND ps.library_id IS NULL
              AND ps.section_type = 'continue_watching'
              AND ps.config->>'continue_type' = 'watching'
            ORDER BY ps.position ASC
            LIMIT 1
        ),
        (
            SELECT COALESCE(MAX(position) + 1, 0)
            FROM page_sections
            WHERE scope = 'home'
              AND library_id IS NULL
        )
    ) AS position
    WHERE EXISTS (
        SELECT 1
        FROM media_folders
        WHERE lower(type) IN ('audiobook', 'audiobooks')
    )
      AND NOT EXISTS (
        SELECT 1
        FROM page_sections
        WHERE scope = 'home'
          AND library_id IS NULL
          AND section_type = 'continue_watching'
          AND config->>'continue_type' = 'listening'
    )
),
shifted AS (
    UPDATE page_sections
    SET position = position + 1,
        updated_at = NOW()
    WHERE scope = 'home'
      AND library_id IS NULL
      AND position >= (SELECT position FROM insert_position)
    RETURNING 1
)
INSERT INTO page_sections (
    id, scope, library_id, position, section_type, title, featured,
    item_limit, config, enabled, created_at, updated_at
)
SELECT
    gen_random_uuid()::text,
    'home',
    NULL,
    position,
    'continue_watching',
    'Continue Listening',
    false,
    20,
    '{"continue_type":"listening"}'::jsonb,
    true,
    NOW(),
    NOW()
FROM insert_position;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM page_sections
WHERE scope = 'home'
  AND library_id IS NULL
  AND section_type = 'continue_watching'
  AND title = 'Continue Listening'
  AND config->>'continue_type' = 'listening';

UPDATE page_sections
SET config = config - 'continue_type',
    updated_at = NOW()
WHERE section_type = 'continue_watching'
  AND config ? 'continue_type';
-- +goose StatementEnd
