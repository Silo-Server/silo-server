-- +goose NO TRANSACTION

-- +goose Up
-- Providers accept one submission per account for each item and segment, and
-- offer no way to amend it. Claims were keyed by the full payload hash, so a
-- re-detected marker or a second file of the same episode was submitted again
-- and refused as a conflict. target_key names the item alone.
ALTER TABLE public.marker_contributions
    ADD COLUMN IF NOT EXISTS target_key text;

-- Derive the key from current metadata in the format contributionTargetKey
-- writes: item kind, preferred provider id, season, episode. Only a row whose
-- stored content_hash recomputes from that same metadata was submitted to that
-- target; a row for an item rematched since keeps a NULL key and claims
-- nothing.
UPDATE public.marker_contributions AS contribution
SET target_key = derived.target_key
FROM (
    SELECT mc.id,
           mc.content_hash,
           ids.kind || '|' ||
           CASE
               WHEN ids.tmdb_id <> '' THEN 'tmdb:' || ids.tmdb_id
               WHEN ids.tvdb_id <> '' THEN 'tvdb:' || ids.tvdb_id
               WHEN ids.imdb_id <> '' THEN 'imdb:' || ids.imdb_id
               ELSE ''
           END || '|' || ids.season_number || '|' || ids.episode_number AS target_key,
           -- ContentHash: segment|start|end|duration, then |kind|tmdb|imdb|tvdb|season|episode
           -- with missing bounds as "null" and a zero season or episode as "".
           left(encode(sha256(convert_to(
               mc.segment_kind
               || '|' || COALESCE(mc.submitted_start_ms::text, 'null')
               || '|' || COALESCE(mc.submitted_end_ms::text, 'null')
               || '|' || COALESCE(mc.video_duration_ms::text, 'null')
               || '|' || ids.kind
               || '|' || ids.tmdb_id || '|' || ids.imdb_id || '|' || ids.tvdb_id
               || '|' || CASE WHEN ids.season_number = 0 THEN '' ELSE ids.season_number::text END
               || '|' || CASE WHEN ids.episode_number = 0 THEN '' ELSE ids.episode_number::text END,
               'UTF8')), 'hex'), 32) AS derived_hash
    FROM public.marker_contributions mc
    JOIN public.media_files mf ON mf.id = mc.media_file_id
    LEFT JOIN public.episodes e ON e.content_id = mf.episode_id
    LEFT JOIN public.media_items series ON series.content_id = e.series_id
    LEFT JOIN public.media_items movie
        ON mf.episode_id IS NULL AND movie.content_id = mf.content_id AND movie.type = 'movie'
    CROSS JOIN LATERAL (
        SELECT CASE WHEN mf.episode_id IS NOT NULL THEN 'episode' ELSE 'movie' END AS kind,
               COALESCE(CASE WHEN mf.episode_id IS NOT NULL THEN series.tmdb_id ELSE movie.tmdb_id END, '') AS tmdb_id,
               COALESCE(CASE WHEN mf.episode_id IS NOT NULL THEN series.imdb_id ELSE movie.imdb_id END, '') AS imdb_id,
               COALESCE(CASE WHEN mf.episode_id IS NOT NULL THEN series.tvdb_id ELSE movie.tvdb_id END, '') AS tvdb_id,
               COALESCE(e.season_number, 0) AS season_number,
               COALESCE(e.episode_number, 0) AS episode_number
    ) AS ids
    WHERE mc.target_key IS NULL
      AND (mf.episode_id IS NULL OR e.content_id IS NOT NULL)
) AS derived
WHERE contribution.id = derived.id
  AND derived.derived_hash = derived.content_hash;

-- Upstream 4xx refusals of the item itself were recorded as retryable errors
-- and resubmitted every night. Settle them as invalid so they hold the target.
UPDATE public.marker_contributions
SET status = 'invalid',
    http_status = (regexp_match(error, 'submit HTTP ([0-9]{3})'))[1]::integer,
    updated_at = now()
WHERE status = 'error'
  AND error ~ 'submit HTTP (400|404|422):';

-- Keep one active claim per provider target: the most authoritative outcome,
-- then the newest. Every row stays for audit.
WITH ranked AS (
    SELECT id,
           ROW_NUMBER() OVER (
               PARTITION BY provider, segment_kind, target_key
               ORDER BY CASE status
                            WHEN 'accepted' THEN 0
                            WHEN 'pending' THEN 1
                            WHEN 'rejected' THEN 2
                            WHEN 'conflict' THEN 3
                            WHEN 'invalid' THEN 4
                            ELSE 5
                        END,
                        updated_at DESC, submitted_at DESC, id
           ) AS row_number
    FROM public.marker_contributions
    WHERE status <> 'error'
      AND target_key IS NOT NULL
)
UPDATE public.marker_contributions AS contribution
SET claim_active = (ranked.row_number = 1)
FROM ranked
WHERE contribution.id = ranked.id
  AND contribution.claim_active IS DISTINCT FROM (ranked.row_number = 1);

-- Remove an INVALID remnant before retrying an interrupted concurrent build.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_index i ON i.indexrelid = c.oid
        WHERE n.nspname = 'public'
          AND c.relname = 'marker_contributions_provider_target_active_uidx'
          AND NOT i.indisvalid
    ) THEN
        DROP INDEX public.marker_contributions_provider_target_active_uidx;
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS marker_contributions_provider_target_active_uidx
    ON public.marker_contributions (provider, segment_kind, target_key)
    WHERE claim_active;

-- The payload hash includes the target, so the target index subsumes it.
DROP INDEX CONCURRENTLY IF EXISTS public.marker_contributions_provider_hash_active_uidx;

-- +goose Down
-- Rows that shared a payload hash but not a derived target could both be
-- active; keep the newest per hash so the payload index can be rebuilt.
WITH ranked AS (
    SELECT id,
           ROW_NUMBER() OVER (
               PARTITION BY provider, segment_kind, content_hash
               ORDER BY updated_at DESC, submitted_at DESC, id
           ) AS row_number
    FROM public.marker_contributions
    WHERE claim_active
)
UPDATE public.marker_contributions AS contribution
SET claim_active = false
FROM ranked
WHERE contribution.id = ranked.id
  AND ranked.row_number > 1;
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS marker_contributions_provider_hash_active_uidx
    ON public.marker_contributions (provider, segment_kind, content_hash)
    WHERE claim_active;
DROP INDEX CONCURRENTLY IF EXISTS public.marker_contributions_provider_target_active_uidx;
-- The previous release does not know the invalid status; return those rows to
-- retryable errors so it can read them.
UPDATE public.marker_contributions
SET status = 'error',
    claim_active = false
WHERE status = 'invalid';
ALTER TABLE public.marker_contributions
    DROP COLUMN IF EXISTS target_key;
