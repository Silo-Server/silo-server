-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS metadata_refresh_debt (
    content_id TEXT PRIMARY KEY REFERENCES media_items(content_id) ON DELETE CASCADE,
    priority INTEGER NOT NULL DEFAULT 0,
    reason_mask BIGINT NOT NULL DEFAULT 0,
    next_refresh_at TIMESTAMPTZ NOT NULL,
    claimed_at TIMESTAMPTZ NULL,
    lease_expires_at TIMESTAMPTZ NULL,
    last_attempt_at TIMESTAMPTZ NULL,
    last_success_at TIMESTAMPTZ NULL,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_metadata_refresh_debt_due
    ON metadata_refresh_debt (next_refresh_at, priority DESC, updated_at);

CREATE INDEX IF NOT EXISTS idx_metadata_refresh_debt_lease
    ON metadata_refresh_debt (lease_expires_at);

INSERT INTO metadata_refresh_debt (
    content_id,
    priority,
    reason_mask,
    next_refresh_at,
    updated_at
)
SELECT
    mi.content_id,
    CASE
        WHEN (reason_mask & 1) <> 0 THEN 300
        WHEN (reason_mask & 2) <> 0 THEN 250
        WHEN (reason_mask & 4) <> 0 THEN 200
        ELSE 150
    END AS priority,
    reason_mask,
    LEAST(
        CASE
            WHEN (reason_mask & 1) <> 0 THEN
                CASE
                    WHEN mi.episode_metadata_last_checked_at IS NULL THEN NOW()
                    ELSE GREATEST(NOW(), mi.episode_metadata_last_checked_at + INTERVAL '72 hours')
                END
            ELSE NOW() + INTERVAL '100 years'
        END,
        CASE
            WHEN (reason_mask & 2) <> 0 THEN
                GREATEST(
                    NOW(),
                    COALESCE(
                        (
                            SELECT MAX(smi.last_seen_at) + INTERVAL '24 hours'
                            FROM stale_media_ids smi
                            WHERE smi.content_id = mi.content_id
                        ),
                        NOW()
                    )
                )
            ELSE NOW() + INTERVAL '100 years'
        END,
        CASE
            WHEN (reason_mask & 4) <> 0 OR (reason_mask & 8) <> 0 THEN
                GREATEST(NOW(), COALESCE(mi.last_refreshed, mi.updated_at) + INTERVAL '24 hours')
            ELSE NOW() + INTERVAL '100 years'
        END
    ) AS next_refresh_at,
    NOW()
FROM (
    SELECT
        mi.content_id,
        mi.episode_metadata_last_checked_at,
        mi.last_refreshed,
        mi.updated_at,
        (
            CASE
                WHEN mi.episode_metadata_incomplete = TRUE THEN 1
                ELSE 0
            END +
            CASE
                WHEN EXISTS (
                    SELECT 1
                    FROM stale_media_ids smi
                    WHERE smi.content_id = mi.content_id
                ) THEN 2
                ELSE 0
            END +
            CASE
                WHEN mi.status = 'matched'
                 AND mi.refresh_failures > 0 THEN 4
                ELSE 0
            END +
            CASE
                WHEN mi.status = 'matched'
                 AND (
                    COALESCE(mi.overview, '') = ''
                    OR COALESCE(mi.poster_path, '') = ''
                    OR COALESCE(mi.backdrop_path, '') = ''
                    OR (
                        mi.rating_imdb IS NULL
                        AND mi.rating_tmdb IS NULL
                        AND mi.rating_rt_critic IS NULL
                        AND mi.rating_rt_audience IS NULL
                    )
                 ) THEN 8
                ELSE 0
            END
        )::bigint AS reason_mask
    FROM media_items mi
    WHERE EXISTS (
        SELECT 1
        FROM media_item_libraries mil
        JOIN media_folders folders ON folders.id = mil.media_folder_id
        WHERE mil.content_id = mi.content_id
          AND folders.enabled = TRUE
    )
) mi
WHERE reason_mask <> 0
ON CONFLICT (content_id) DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS metadata_refresh_debt;
-- +goose StatementEnd
