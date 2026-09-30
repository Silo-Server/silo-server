-- +goose NO TRANSACTION

-- +goose Up
-- Keep each active file's badge ordering in the index. Home-page lookups can
-- stop at the best accessible file instead of ranking every episode per card.
-- These expressions must stay identical to internal/sections/overlay_summaries.go.
-- The database plan test exercises the index definitions from this migration.
-- +goose StatementBegin
DO $$
DECLARE
    index_name text;
BEGIN
    FOREACH index_name IN ARRAY ARRAY[
        'idx_media_files_overlay_content', 'idx_media_files_overlay_episode'
    ] LOOP
        IF EXISTS (
            SELECT 1 FROM pg_index i
            JOIN pg_class c ON c.oid = i.indexrelid
            JOIN pg_namespace n ON n.oid = c.relnamespace
            WHERE n.nspname = 'public' AND c.relname = index_name AND NOT i.indisvalid
        ) THEN
            EXECUTE format('DROP INDEX public.%I', index_name);
        END IF;
    END LOOP;
END;
$$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_files_overlay_content
ON public.media_files (
    content_id,
    (CASE
        WHEN lower(btrim(coalesce(resolution, ''), E' \t\n\013\f\r\u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000')) IN ('4k', 'uhd') THEN 2160
        WHEN lower(btrim(coalesce(resolution, ''), E' \t\n\013\f\r\u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000')) ~ '^[+-]?[0-9]{1,18}p$'
            THEN substring(lower(btrim(resolution, E' \t\n\013\f\r\u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000')) from '^([+-]?[0-9]{1,18})p$')::numeric
        ELSE 0
    END) DESC,
    (CASE
        WHEN jsonb_path_exists(coalesce(video_tracks, '[]'::jsonb),
            '$[*] ? ((@.dolby_vision.type() == "string" && @.dolby_vision != "") || @.dv_profile > 0 || @.video_range_type like_regex "^DOVI")') THEN 3
        WHEN jsonb_path_exists(coalesce(video_tracks, '[]'::jsonb),
            '$[*] ? (@.hdr10_plus == true || @.video_range_type like_regex "HDR10Plus" || @.video_range_type like_regex "^[[:space:]]*HDR10[[:space:]]*$" || @.video_range_type like_regex "WithHDR10[[:space:]]*$" || @.video_range_type like_regex "^[[:space:]]*HLG[[:space:]]*$" || @.video_range_type like_regex "WithHLG[[:space:]]*$" || @.color_transfer like_regex "smpte2084" flag "i" || @.color_transfer like_regex "arib-std-b67" flag "i")') THEN 2
        WHEN coalesce(hdr, false) THEN 1
        ELSE 0
    END) DESC,
    episode_id ASC,
    id ASC
)
WHERE missing_since IS NULL AND content_id IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_files_overlay_episode
ON public.media_files (
    episode_id,
    (CASE
        WHEN lower(btrim(coalesce(resolution, ''), E' \t\n\013\f\r\u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000')) IN ('4k', 'uhd') THEN 2160
        WHEN lower(btrim(coalesce(resolution, ''), E' \t\n\013\f\r\u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000')) ~ '^[+-]?[0-9]{1,18}p$'
            THEN substring(lower(btrim(resolution, E' \t\n\013\f\r\u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000')) from '^([+-]?[0-9]{1,18})p$')::numeric
        ELSE 0
    END) DESC,
    (CASE
        WHEN jsonb_path_exists(coalesce(video_tracks, '[]'::jsonb),
            '$[*] ? ((@.dolby_vision.type() == "string" && @.dolby_vision != "") || @.dv_profile > 0 || @.video_range_type like_regex "^DOVI")') THEN 3
        WHEN jsonb_path_exists(coalesce(video_tracks, '[]'::jsonb),
            '$[*] ? (@.hdr10_plus == true || @.video_range_type like_regex "HDR10Plus" || @.video_range_type like_regex "^[[:space:]]*HDR10[[:space:]]*$" || @.video_range_type like_regex "WithHDR10[[:space:]]*$" || @.video_range_type like_regex "^[[:space:]]*HLG[[:space:]]*$" || @.video_range_type like_regex "WithHLG[[:space:]]*$" || @.color_transfer like_regex "smpte2084" flag "i" || @.color_transfer like_regex "arib-std-b67" flag "i")') THEN 2
        WHEN coalesce(hdr, false) THEN 1
        ELSE 0
    END) DESC,
    content_id ASC,
    id ASC
)
WHERE missing_since IS NULL AND episode_id IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_files_overlay_episode;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_files_overlay_content;
