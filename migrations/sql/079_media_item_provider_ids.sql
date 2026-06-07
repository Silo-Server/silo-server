-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS media_item_provider_ids (
    content_id TEXT NOT NULL REFERENCES media_items(content_id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    provider_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (content_id, provider),
    UNIQUE (provider, provider_id)
);

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM (
            SELECT provider, provider_id
            FROM (
                SELECT 'tmdb' AS provider, tmdb_id AS provider_id
                FROM media_items
                WHERE tmdb_id <> ''
                UNION ALL
                SELECT 'tvdb' AS provider, tvdb_id AS provider_id
                FROM media_items
                WHERE tvdb_id <> ''
                UNION ALL
                SELECT 'imdb' AS provider, imdb_id AS provider_id
                FROM media_items
                WHERE imdb_id <> ''
            ) seeded_ids
            GROUP BY provider, provider_id
            HAVING COUNT(*) > 1
        ) duplicate_ids
    ) THEN
        RAISE EXCEPTION 'duplicate provider ids exist in media_items; resolve duplicates before applying migration 079';
    END IF;
END $$;

INSERT INTO media_item_provider_ids (content_id, provider, provider_id)
SELECT content_id, provider, provider_id
FROM (
    SELECT content_id, 'tmdb' AS provider, tmdb_id AS provider_id
    FROM media_items
    WHERE tmdb_id <> ''
    UNION ALL
    SELECT content_id, 'tvdb' AS provider, tvdb_id AS provider_id
    FROM media_items
    WHERE tvdb_id <> ''
    UNION ALL
    SELECT content_id, 'imdb' AS provider, imdb_id AS provider_id
    FROM media_items
    WHERE imdb_id <> ''
) seeded
ORDER BY content_id ASC, provider ASC;

CREATE INDEX IF NOT EXISTS idx_media_item_provider_ids_provider_id
    ON media_item_provider_ids (provider, provider_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_media_item_provider_ids_provider_id;
DROP TABLE IF EXISTS media_item_provider_ids;
-- +goose StatementEnd
