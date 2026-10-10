-- +goose NO TRANSACTION

-- +goose Up
-- A prepared download that carries every audio track and text subtitle has a
-- stream layout an older API worker cannot reproduce. Give it a durable recipe
-- discriminator and a status family that the merge-base ClaimNext predicate
-- does not recognize. The track recipe outranks the audio and tone-map
-- families: its execution fingerprint already covers both.
-- prepared_audio_tracks freezes the audio streams of the delivered file when
-- it becomes ready, so a later re-probe of the source cannot change how the
-- offline manifest describes bytes that were already prepared.
DROP INDEX CONCURRENTLY IF EXISTS public.download_artifacts_lease_idx;
DROP INDEX CONCURRENTLY IF EXISTS public.download_artifacts_lru_idx;

ALTER TABLE public.download_artifacts
    ADD COLUMN IF NOT EXISTS track_recipe_version text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS prepared_audio_tracks jsonb,
    DROP CONSTRAINT download_artifacts_status_check,
    ADD CONSTRAINT download_artifacts_status_check
        CHECK (status IN (
            'queued', 'running', 'ready',
            'tone_map_queued', 'tone_map_running', 'tone_map_ready',
            'audio_v2_queued', 'audio_v2_running', 'audio_v2_ready',
            'tracks_v1_queued', 'tracks_v1_running', 'tracks_v1_ready',
            'failed'
        )) NOT VALID;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.fence_tone_map_artifact_worker_status()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.track_recipe_version <> '' THEN
        IF NEW.status IN ('queued', 'tone_map_queued', 'audio_v2_queued') THEN
            NEW.status := 'tracks_v1_queued';
        ELSIF NEW.status IN ('running', 'tone_map_running', 'audio_v2_running') THEN
            NEW.status := 'tracks_v1_running';
        ELSIF NEW.status IN ('ready', 'tone_map_ready', 'audio_v2_ready') THEN
            NEW.status := 'tracks_v1_ready';
        END IF;
    ELSIF NEW.audio_recipe_version <> '' THEN
        IF NEW.status IN ('queued', 'tone_map_queued', 'tracks_v1_queued') THEN
            NEW.status := 'audio_v2_queued';
        ELSIF NEW.status IN ('running', 'tone_map_running', 'tracks_v1_running') THEN
            NEW.status := 'audio_v2_running';
        ELSIF NEW.status IN ('ready', 'tone_map_ready', 'tracks_v1_ready') THEN
            NEW.status := 'audio_v2_ready';
        END IF;
    ELSIF NEW.tone_map_mode <> '' THEN
        IF NEW.status IN ('queued', 'audio_v2_queued', 'tracks_v1_queued') THEN
            NEW.status := 'tone_map_queued';
        ELSIF NEW.status IN ('running', 'audio_v2_running', 'tracks_v1_running') THEN
            NEW.status := 'tone_map_running';
        ELSIF NEW.status IN ('ready', 'audio_v2_ready', 'tracks_v1_ready') THEN
            NEW.status := 'tone_map_ready';
        END IF;
    ELSE
        IF NEW.status IN ('tone_map_queued', 'audio_v2_queued', 'tracks_v1_queued') THEN
            NEW.status := 'queued';
        ELSIF NEW.status IN ('tone_map_running', 'audio_v2_running', 'tracks_v1_running') THEN
            NEW.status := 'running';
        ELSIF NEW.status IN ('tone_map_ready', 'audio_v2_ready', 'tracks_v1_ready') THEN
            NEW.status := 'ready';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS download_artifacts_tone_map_worker_status ON public.download_artifacts;
CREATE TRIGGER download_artifacts_tone_map_worker_status
BEFORE INSERT OR UPDATE OF status, tone_map_mode, audio_recipe_version, track_recipe_version ON public.download_artifacts
FOR EACH ROW
EXECUTE FUNCTION public.fence_tone_map_artifact_worker_status();

ALTER TABLE public.download_artifacts
    VALIDATE CONSTRAINT download_artifacts_status_check;

CREATE INDEX CONCURRENTLY download_artifacts_lease_idx ON public.download_artifacts (lease_expires_at)
    WHERE status IN ('running', 'tone_map_running', 'audio_v2_running', 'tracks_v1_running');
CREATE INDEX CONCURRENTLY download_artifacts_lru_idx ON public.download_artifacts (last_used_at)
    WHERE status IN ('ready', 'tone_map_ready', 'audio_v2_ready', 'tracks_v1_ready');

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.download_artifacts_lease_idx;
DROP INDEX CONCURRENTLY IF EXISTS public.download_artifacts_lru_idx;

DROP TRIGGER IF EXISTS download_artifacts_tone_map_worker_status ON public.download_artifacts;

-- Older code serves multi-track files as ordinary artifacts; their first audio
-- stream stays valid for the single-track manifest it describes.
UPDATE public.download_artifacts
SET status = CASE
    WHEN status = 'tracks_v1_queued' AND audio_recipe_version <> '' THEN 'audio_v2_queued'
    WHEN status = 'tracks_v1_running' AND audio_recipe_version <> '' THEN 'audio_v2_running'
    WHEN status = 'tracks_v1_ready' AND audio_recipe_version <> '' THEN 'audio_v2_ready'
    WHEN status = 'tracks_v1_queued' AND tone_map_mode <> '' THEN 'tone_map_queued'
    WHEN status = 'tracks_v1_running' AND tone_map_mode <> '' THEN 'tone_map_running'
    WHEN status = 'tracks_v1_ready' AND tone_map_mode <> '' THEN 'tone_map_ready'
    WHEN status = 'tracks_v1_queued' THEN 'queued'
    WHEN status = 'tracks_v1_running' THEN 'running'
    WHEN status = 'tracks_v1_ready' THEN 'ready'
    ELSE status
END
WHERE status IN ('tracks_v1_queued', 'tracks_v1_running', 'tracks_v1_ready');

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.fence_tone_map_artifact_worker_status()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.audio_recipe_version <> '' THEN
        IF NEW.status IN ('queued', 'tone_map_queued') THEN
            NEW.status := 'audio_v2_queued';
        ELSIF NEW.status IN ('running', 'tone_map_running') THEN
            NEW.status := 'audio_v2_running';
        ELSIF NEW.status IN ('ready', 'tone_map_ready') THEN
            NEW.status := 'audio_v2_ready';
        END IF;
    ELSIF NEW.tone_map_mode <> '' THEN
        IF NEW.status IN ('queued', 'audio_v2_queued') THEN
            NEW.status := 'tone_map_queued';
        ELSIF NEW.status IN ('running', 'audio_v2_running') THEN
            NEW.status := 'tone_map_running';
        ELSIF NEW.status IN ('ready', 'audio_v2_ready') THEN
            NEW.status := 'tone_map_ready';
        END IF;
    ELSE
        IF NEW.status IN ('tone_map_queued', 'audio_v2_queued') THEN
            NEW.status := 'queued';
        ELSIF NEW.status IN ('tone_map_running', 'audio_v2_running') THEN
            NEW.status := 'running';
        ELSIF NEW.status IN ('tone_map_ready', 'audio_v2_ready') THEN
            NEW.status := 'ready';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

ALTER TABLE public.download_artifacts
    DROP COLUMN IF EXISTS track_recipe_version,
    DROP COLUMN IF EXISTS prepared_audio_tracks,
    DROP CONSTRAINT download_artifacts_status_check,
    ADD CONSTRAINT download_artifacts_status_check
        CHECK (status IN (
            'queued', 'running', 'ready',
            'tone_map_queued', 'tone_map_running', 'tone_map_ready',
            'audio_v2_queued', 'audio_v2_running', 'audio_v2_ready',
            'failed'
        )) NOT VALID;

CREATE TRIGGER download_artifacts_tone_map_worker_status
BEFORE INSERT OR UPDATE OF status, tone_map_mode, audio_recipe_version ON public.download_artifacts
FOR EACH ROW
EXECUTE FUNCTION public.fence_tone_map_artifact_worker_status();

ALTER TABLE public.download_artifacts
    VALIDATE CONSTRAINT download_artifacts_status_check;

CREATE INDEX CONCURRENTLY download_artifacts_lease_idx ON public.download_artifacts (lease_expires_at)
    WHERE status IN ('running', 'tone_map_running', 'audio_v2_running');
CREATE INDEX CONCURRENTLY download_artifacts_lru_idx ON public.download_artifacts (last_used_at)
    WHERE status IN ('ready', 'tone_map_ready', 'audio_v2_ready');
