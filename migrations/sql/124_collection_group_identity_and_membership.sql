-- +goose Up
-- +goose StatementBegin
-- Upgrade collection groups from label-only buckets to opaque-id groups while
-- moving library collection placement onto the per-library membership table.

ALTER TABLE library_collection_groups
    ADD COLUMN id TEXT,
    ADD COLUMN name TEXT,
    ADD COLUMN slug TEXT,
    ADD COLUMN kind TEXT NOT NULL DEFAULT 'regular',
    ADD COLUMN default_sort_mode TEXT NOT NULL DEFAULT 'manual';

-- Backfill any stale labels referenced by collections but missing a group row.
WITH stale_labels AS (
    SELECT DISTINCT lcl.library_id, lc.group_label AS label
    FROM library_collection_libraries lcl
    JOIN library_collections lc ON lc.id = lcl.collection_id
    WHERE COALESCE(lc.group_label, '') <> ''
      AND NOT EXISTS (
          SELECT 1
          FROM library_collection_groups g
          WHERE g.library_id = lcl.library_id
            AND g.label = lc.group_label
      )
),
numbered AS (
    SELECT
        s.library_id,
        s.label,
        ROW_NUMBER() OVER (PARTITION BY s.library_id ORDER BY s.label) - 1 AS offset
    FROM stale_labels s
),
max_order AS (
    SELECT library_id, COALESCE(MAX(sort_order), -1) AS max_sort_order
    FROM library_collection_groups
    GROUP BY library_id
)
INSERT INTO library_collection_groups (library_id, label, title, sort_order)
SELECT
    n.library_id,
    n.label,
    n.label,
    COALESCE(m.max_sort_order, -1) + 1 + n.offset
FROM numbered n
LEFT JOIN max_order m ON m.library_id = n.library_id
ON CONFLICT (library_id, label) DO NOTHING;

UPDATE library_collection_groups
SET
    id = COALESCE(id, 'lcg_' || md5(library_id::text || ':' || label)),
    name = COALESCE(NULLIF(name, ''), NULLIF(title, ''), label),
    slug = COALESCE(
        NULLIF(slug, ''),
        NULLIF(regexp_replace(lower(trim(label)), '[^a-z0-9]+', '-', 'g'), ''),
        'group-' || substr(md5(library_id::text || ':' || label), 1, 8)
    )
WHERE id IS NULL OR name IS NULL OR slug IS NULL;

-- Seed the viewer-private user-collections placement group for every library.
INSERT INTO library_collection_groups (
    library_id, label, title, sort_order, id, name, slug, kind, default_sort_mode
)
SELECT
    mf.id,
    'user-collections',
    'My collections',
    9998,
    'lcg_user_' || mf.id::text,
    'My collections',
    'user-collections',
    'user_collections',
    'manual'
FROM media_folders mf
ON CONFLICT (library_id, label) DO UPDATE
SET
    id = EXCLUDED.id,
    name = EXCLUDED.name,
    slug = EXCLUDED.slug,
    kind = EXCLUDED.kind,
    default_sort_mode = EXCLUDED.default_sort_mode,
    updated_at = NOW();

ALTER TABLE library_collection_groups
    ALTER COLUMN id SET NOT NULL,
    ALTER COLUMN name SET NOT NULL,
    ALTER COLUMN slug SET NOT NULL,
    ADD CONSTRAINT library_collection_groups_kind_check
        CHECK (kind IN ('regular', 'user_collections')),
    ADD CONSTRAINT library_collection_groups_default_sort_mode_check
        CHECK (default_sort_mode IN ('manual', 'name_asc', 'name_desc', 'recent', 'most_items')),
    ADD CONSTRAINT library_collection_groups_id_unique UNIQUE (id),
    ADD CONSTRAINT library_collection_groups_id_library_unique UNIQUE (id, library_id),
    ADD CONSTRAINT library_collection_groups_library_slug_unique UNIQUE (library_id, slug);

CREATE UNIQUE INDEX idx_library_collection_groups_user_unique
    ON library_collection_groups (library_id)
    WHERE kind = 'user_collections';

CREATE INDEX idx_library_collection_groups_order
    ON library_collection_groups (library_id, sort_order, name);

ALTER TABLE library_collection_libraries
    ADD COLUMN group_id TEXT,
    ADD COLUMN sort_order INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

UPDATE library_collection_libraries lcl
SET
    group_id = (
        SELECT g.id
        FROM library_collection_groups g
        WHERE g.library_id = lcl.library_id
          AND g.label = lc.group_label
        LIMIT 1
    ),
    sort_order = lc.sort_order
FROM library_collections lc
WHERE lc.id = lcl.collection_id;

WITH ranked AS (
    SELECT
        collection_id,
        library_id,
        ROW_NUMBER() OVER (
            PARTITION BY library_id, group_id
            ORDER BY sort_order ASC, collection_id ASC
        ) - 1 AS pos
    FROM library_collection_libraries
)
UPDATE library_collection_libraries lcl
SET sort_order = ranked.pos
FROM ranked
WHERE ranked.collection_id = lcl.collection_id
  AND ranked.library_id = lcl.library_id;

ALTER TABLE library_collection_libraries
    ADD CONSTRAINT library_collection_libraries_group_library_fkey
        FOREIGN KEY (group_id, library_id)
        REFERENCES library_collection_groups (id, library_id)
        ON DELETE SET NULL (group_id);

CREATE INDEX idx_library_collection_libraries_group_order
    ON library_collection_libraries (library_id, group_id, sort_order, collection_id);

ALTER TABLE media_folders
    ADD COLUMN collection_ungrouped_sort_order INTEGER NOT NULL DEFAULT 9999;

ALTER TABLE user_collection_groups
    ADD COLUMN id TEXT,
    ADD COLUMN name TEXT,
    ADD COLUMN slug TEXT,
    ADD COLUMN default_sort_mode TEXT NOT NULL DEFAULT 'manual';

WITH stale_labels AS (
    SELECT DISTINCT user_id, group_label AS label
    FROM user_personal_collections
    WHERE COALESCE(group_label, '') <> ''
      AND NOT EXISTS (
          SELECT 1
          FROM user_collection_groups g
          WHERE g.user_id = user_personal_collections.user_id
            AND g.label = user_personal_collections.group_label
      )
),
numbered AS (
    SELECT
        s.user_id,
        s.label,
        ROW_NUMBER() OVER (PARTITION BY s.user_id ORDER BY s.label) - 1 AS offset
    FROM stale_labels s
),
max_order AS (
    SELECT user_id, COALESCE(MAX(sort_order), -1) AS max_sort_order
    FROM user_collection_groups
    GROUP BY user_id
)
INSERT INTO user_collection_groups (user_id, label, title, sort_order)
SELECT
    n.user_id,
    n.label,
    n.label,
    COALESCE(m.max_sort_order, -1) + 1 + n.offset
FROM numbered n
LEFT JOIN max_order m ON m.user_id = n.user_id
ON CONFLICT (user_id, label) DO NOTHING;

UPDATE user_collection_groups
SET
    id = COALESCE(id, 'ucg_' || md5(user_id::text || ':' || label)),
    name = COALESCE(NULLIF(name, ''), NULLIF(title, ''), label),
    slug = COALESCE(
        NULLIF(slug, ''),
        NULLIF(regexp_replace(lower(trim(label)), '[^a-z0-9]+', '-', 'g'), ''),
        'group-' || substr(md5(user_id::text || ':' || label), 1, 8)
    )
WHERE id IS NULL OR name IS NULL OR slug IS NULL;

ALTER TABLE user_collection_groups
    ALTER COLUMN id SET NOT NULL,
    ALTER COLUMN name SET NOT NULL,
    ALTER COLUMN slug SET NOT NULL,
    ADD CONSTRAINT user_collection_groups_default_sort_mode_check
        CHECK (default_sort_mode IN ('manual', 'name_asc', 'name_desc', 'recent', 'most_items')),
    ADD CONSTRAINT user_collection_groups_user_id_unique UNIQUE (user_id, id),
    ADD CONSTRAINT user_collection_groups_user_slug_unique UNIQUE (user_id, slug);

ALTER TABLE user_personal_collections
    ADD COLUMN group_id TEXT;

UPDATE user_personal_collections upc
SET group_id = g.id
FROM user_collection_groups g
WHERE g.user_id = upc.user_id
  AND g.label = upc.group_label
  AND COALESCE(upc.group_label, '') <> '';

ALTER TABLE user_personal_collections
    ADD CONSTRAINT user_personal_collections_group_fkey
        FOREIGN KEY (user_id, group_id)
        REFERENCES user_collection_groups (user_id, id)
        ON DELETE SET NULL (group_id);

CREATE INDEX idx_user_personal_collections_group_id_order
    ON user_personal_collections (user_id, group_id, sort_order);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_user_personal_collections_group_id_order;

ALTER TABLE user_personal_collections
    DROP CONSTRAINT IF EXISTS user_personal_collections_group_fkey;

UPDATE user_personal_collections upc
SET group_label = COALESCE(g.label, '')
FROM user_collection_groups g
WHERE g.user_id = upc.user_id
  AND g.id = upc.group_id;

ALTER TABLE user_personal_collections
    DROP COLUMN IF EXISTS group_id;

ALTER TABLE user_collection_groups
    DROP CONSTRAINT IF EXISTS user_collection_groups_user_slug_unique,
    DROP CONSTRAINT IF EXISTS user_collection_groups_user_id_unique,
    DROP CONSTRAINT IF EXISTS user_collection_groups_default_sort_mode_check,
    DROP COLUMN IF EXISTS default_sort_mode,
    DROP COLUMN IF EXISTS slug,
    DROP COLUMN IF EXISTS name,
    DROP COLUMN IF EXISTS id;

ALTER TABLE media_folders
    DROP COLUMN IF EXISTS collection_ungrouped_sort_order;

DROP INDEX IF EXISTS idx_library_collection_libraries_group_order;

ALTER TABLE library_collection_libraries
    DROP CONSTRAINT IF EXISTS library_collection_libraries_group_library_fkey;

UPDATE library_collections lc
SET group_label = COALESCE(g.label, '')
FROM library_collection_libraries lcl
LEFT JOIN library_collection_groups g
    ON g.id = lcl.group_id
   AND g.library_id = lcl.library_id
WHERE lcl.collection_id = lc.id
  AND lc.library_id = lcl.library_id;

ALTER TABLE library_collection_libraries
    DROP COLUMN IF EXISTS updated_at,
    DROP COLUMN IF EXISTS created_at,
    DROP COLUMN IF EXISTS sort_order,
    DROP COLUMN IF EXISTS group_id;

DROP INDEX IF EXISTS idx_library_collection_groups_order;
DROP INDEX IF EXISTS idx_library_collection_groups_user_unique;

DELETE FROM library_collection_groups
WHERE kind = 'user_collections'
   OR label = 'user-collections';

ALTER TABLE library_collection_groups
    DROP CONSTRAINT IF EXISTS library_collection_groups_library_slug_unique,
    DROP CONSTRAINT IF EXISTS library_collection_groups_id_library_unique,
    DROP CONSTRAINT IF EXISTS library_collection_groups_id_unique,
    DROP CONSTRAINT IF EXISTS library_collection_groups_default_sort_mode_check,
    DROP CONSTRAINT IF EXISTS library_collection_groups_kind_check,
    DROP COLUMN IF EXISTS default_sort_mode,
    DROP COLUMN IF EXISTS kind,
    DROP COLUMN IF EXISTS slug,
    DROP COLUMN IF EXISTS name,
    DROP COLUMN IF EXISTS id;
-- +goose StatementEnd
