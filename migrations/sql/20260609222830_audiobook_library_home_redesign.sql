-- +goose Up
-- +goose StatementBegin
-- Audiobook library Home redesign: the Continue Listening section becomes the
-- featured "Now Listening" resume hero, and a next_in_series row surfaces the
-- next unstarted book in series the profile has finished.
--
-- Rows touched here carry a marker key in config so the Down migration can
-- revert exactly what this migration changed and nothing else.
--
-- Mark the first continue-listening section of each audiobook library as
-- featured, but only when the admin has not already featured something else in
-- that library's layout.
WITH target AS (
    SELECT DISTINCT ON (ps.library_id) ps.id
    FROM page_sections ps
    JOIN media_folders mf ON mf.id = ps.library_id
    WHERE ps.scope = 'library'
      AND lower(mf.type) IN ('audiobook', 'audiobooks')
      AND ps.section_type = 'continue_watching'
      AND ps.config->>'continue_type' = 'listening'
      AND NOT ps.featured
      AND NOT EXISTS (
          SELECT 1 FROM page_sections f
          WHERE f.scope = 'library'
            AND f.library_id = ps.library_id
            AND f.featured
      )
    ORDER BY ps.library_id, ps.position ASC
)
UPDATE page_sections ps
SET featured = TRUE,
    config = ps.config || '{"featured_by_migration":"20260609222830"}'::jsonb,
    updated_at = NOW()
FROM target t
WHERE ps.id = t.id;
-- +goose StatementEnd

-- +goose StatementBegin
-- Insert a next_in_series section into each audiobook library that lacks one,
-- directly after its continue-listening section (or at the end of the layout
-- when no continue-listening section exists).
WITH anchors AS (
    SELECT DISTINCT ON (ps.library_id)
        ps.library_id,
        ps.position + 1 AS insert_position
    FROM page_sections ps
    JOIN media_folders mf ON mf.id = ps.library_id
    WHERE ps.scope = 'library'
      AND lower(mf.type) IN ('audiobook', 'audiobooks')
      AND ps.section_type = 'continue_watching'
      AND ps.config->>'continue_type' = 'listening'
    ORDER BY ps.library_id, ps.position ASC
),
targets AS (
    SELECT
        mf.id AS library_id,
        COALESCE(
            a.insert_position,
            (
                SELECT COALESCE(MAX(p2.position) + 1, 0)
                FROM page_sections p2
                WHERE p2.scope = 'library' AND p2.library_id = mf.id
            )
        ) AS insert_position
    FROM media_folders mf
    LEFT JOIN anchors a ON a.library_id = mf.id
    WHERE lower(mf.type) IN ('audiobook', 'audiobooks')
      AND NOT EXISTS (
          SELECT 1 FROM page_sections e
          WHERE e.scope = 'library'
            AND e.library_id = mf.id
            AND e.section_type = 'next_in_series'
      )
),
shifted AS (
    UPDATE page_sections ps
    SET position = ps.position + 1,
        updated_at = NOW()
    FROM targets t
    WHERE ps.scope = 'library'
      AND ps.library_id = t.library_id
      AND ps.position >= t.insert_position
    RETURNING 1
)
INSERT INTO page_sections (
    id, scope, library_id, position, section_type, title, featured,
    item_limit, config, enabled, created_at, updated_at
)
SELECT
    gen_random_uuid()::text,
    'library',
    t.library_id,
    t.insert_position,
    'next_in_series',
    'Next in Your Series',
    false,
    20,
    '{"seeded_by_migration":"20260609222830"}'::jsonb,
    true,
    NOW(),
    NOW()
FROM targets t;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Revert only the rows the Up migration touched, identified by the marker
-- keys it wrote. Admin-created next_in_series sections and sections featured
-- before (or after) the upgrade are left alone.
DELETE FROM page_sections
WHERE scope = 'library'
  AND section_type = 'next_in_series'
  AND config->>'seeded_by_migration' = '20260609222830';

UPDATE page_sections
SET featured = FALSE,
    config = config - 'featured_by_migration',
    updated_at = NOW()
WHERE scope = 'library'
  AND section_type = 'continue_watching'
  AND config->>'featured_by_migration' = '20260609222830';
-- +goose StatementEnd
