-- +goose NO TRANSACTION

-- +goose Up
-- A prepared file is a cache: once no download is still waiting on it and its
-- cache period has passed, its bytes are deleted. The row stays as 'expired'
-- so devices that already finished keep their offline manifest (which reads
-- the frozen recipe), and so a re-download can prepare the same recipe again.
-- 'expired' is outside every recipe family, so no worker claims it and an
-- older replica's readiness checks treat it as not ready.
ALTER TABLE public.download_artifacts
    DROP CONSTRAINT download_artifacts_status_check,
    ADD CONSTRAINT download_artifacts_status_check
        CHECK (status IN (
            'queued', 'running', 'ready',
            'tone_map_queued', 'tone_map_running', 'tone_map_ready',
            'audio_v2_queued', 'audio_v2_running', 'audio_v2_ready',
            'tracks_v1_queued', 'tracks_v1_running', 'tracks_v1_ready',
            'failed', 'expired'
        )) NOT VALID;
ALTER TABLE public.download_artifacts VALIDATE CONSTRAINT download_artifacts_status_check;

CREATE INDEX CONCURRENTLY IF NOT EXISTS download_artifacts_expired_idx
    ON public.download_artifacts (last_used_at) WHERE status = 'expired';

-- The preparation list, its counts, and queue positions read rows that are
-- still being prepared. Expired rows are not, so they leave that index along
-- with the ready ones.
DROP INDEX CONCURRENTLY IF EXISTS public.download_artifacts_unready_idx;
CREATE INDEX CONCURRENTLY IF NOT EXISTS download_artifacts_unready_idx
    ON public.download_artifacts (created_at)
    WHERE status NOT IN ('ready', 'tone_map_ready', 'audio_v2_ready', 'tracks_v1_ready', 'expired');

-- What storage clean-up and revocation removed, and why. Rows are grouped by
-- batch_id: one clean-up pass per location and reason, or one admin action.
-- Kept 90 days.
CREATE TABLE IF NOT EXISTS public.download_storage_events (
    id            bigserial   PRIMARY KEY,
    batch_id      text        NOT NULL,
    occurred_at   timestamptz NOT NULL DEFAULT now(),
    reason        text        NOT NULL,
    location_key  text        NOT NULL,
    location_name text        NOT NULL DEFAULT '',
    artifact_id   text        NOT NULL DEFAULT '',
    download_id   text        NOT NULL DEFAULT '',
    user_id       integer,
    content_id    text        NOT NULL DEFAULT '',
    episode_id    text        NOT NULL DEFAULT '',
    title         text        NOT NULL DEFAULT '',
    bytes         bigint      NOT NULL DEFAULT 0,
    actor_user_id integer,
    detail        text        NOT NULL DEFAULT '',
    CONSTRAINT download_storage_events_reason_check CHECK (reason IN (
        'cache_expired', 'budget', 'disk_ceiling', 'admin_delete',
        'untracked', 'missing', 'revoked', 'device_removed'
    )),
    CONSTRAINT download_storage_events_location_check CHECK (
        location_key IN ('server', 'device') OR location_key ~ '^node:[1-9][0-9]*$'
    ),
    CONSTRAINT download_storage_events_bytes_check CHECK (bytes >= 0)
);
CREATE INDEX CONCURRENTLY IF NOT EXISTS download_storage_events_batch_idx
    ON public.download_storage_events (batch_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS download_storage_events_occurred_idx
    ON public.download_storage_events (occurred_at DESC, batch_id);

-- +goose Down
DROP TABLE IF EXISTS public.download_storage_events;
DROP INDEX CONCURRENTLY IF EXISTS public.download_artifacts_expired_idx;
DROP INDEX CONCURRENTLY IF EXISTS public.download_artifacts_unready_idx;
CREATE INDEX CONCURRENTLY IF NOT EXISTS download_artifacts_unready_idx
    ON public.download_artifacts (created_at)
    WHERE status NOT IN ('ready', 'tone_map_ready', 'audio_v2_ready', 'tracks_v1_ready');
-- Expired rows have no bytes; returning them to the queue lets a worker
-- rebuild any a download still needs before the status is narrowed again.
UPDATE public.download_artifacts
SET status = CASE
                 WHEN track_recipe_version <> '' THEN 'tracks_v1_queued'
                 WHEN audio_recipe_version <> '' THEN 'audio_v2_queued'
                 WHEN tone_map_mode <> '' THEN 'tone_map_queued'
                 ELSE 'queued'
             END,
    attempts = 0, completed_at = NULL
WHERE status = 'expired';
ALTER TABLE public.download_artifacts
    DROP CONSTRAINT download_artifacts_status_check,
    ADD CONSTRAINT download_artifacts_status_check
        CHECK (status IN (
            'queued', 'running', 'ready',
            'tone_map_queued', 'tone_map_running', 'tone_map_ready',
            'audio_v2_queued', 'audio_v2_running', 'audio_v2_ready',
            'tracks_v1_queued', 'tracks_v1_running', 'tracks_v1_ready',
            'failed'
        )) NOT VALID;
