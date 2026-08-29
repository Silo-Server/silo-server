-- +goose NO TRANSACTION

-- +goose Up
-- A prepared stereo-downmix job carries bytes that an older API worker cannot
-- reproduce. Give it a durable recipe discriminator and a status family that
-- the merge-base ClaimNext predicate does not recognize.
DROP INDEX CONCURRENTLY IF EXISTS public.download_artifacts_lease_idx;
DROP INDEX CONCURRENTLY IF EXISTS public.download_artifacts_lru_idx;

ALTER TABLE public.download_artifacts
    ADD COLUMN audio_recipe_version text NOT NULL DEFAULT '',
    DROP CONSTRAINT download_artifacts_status_check,
    ADD CONSTRAINT download_artifacts_status_check
        CHECK (status IN (
            'queued', 'running', 'ready',
            'tone_map_queued', 'tone_map_running', 'tone_map_ready',
            'audio_v2_queued', 'audio_v2_running', 'audio_v2_ready',
            'failed'
        )) NOT VALID;

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

DROP TRIGGER IF EXISTS download_artifacts_tone_map_worker_status ON public.download_artifacts;
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

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.download_artifacts_lease_idx;
DROP INDEX CONCURRENTLY IF EXISTS public.download_artifacts_lru_idx;

DROP TRIGGER IF EXISTS download_artifacts_tone_map_worker_status ON public.download_artifacts;

UPDATE public.download_artifacts
SET status = CASE
    WHEN status = 'audio_v2_queued' AND tone_map_mode <> '' THEN 'tone_map_queued'
    WHEN status = 'audio_v2_running' AND tone_map_mode <> '' THEN 'tone_map_running'
    WHEN status = 'audio_v2_ready' AND tone_map_mode <> '' THEN 'tone_map_ready'
    WHEN status = 'audio_v2_queued' THEN 'queued'
    WHEN status = 'audio_v2_running' THEN 'running'
    WHEN status = 'audio_v2_ready' THEN 'ready'
    ELSE status
END
WHERE status IN ('audio_v2_queued', 'audio_v2_running', 'audio_v2_ready');

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.fence_tone_map_artifact_worker_status()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.tone_map_mode <> '' THEN
        IF NEW.status = 'queued' THEN
            NEW.status := 'tone_map_queued';
        ELSIF NEW.status = 'running' THEN
            NEW.status := 'tone_map_running';
        ELSIF NEW.status = 'ready' THEN
            NEW.status := 'tone_map_ready';
        END IF;
    ELSE
        IF NEW.status = 'tone_map_queued' THEN
            NEW.status := 'queued';
        ELSIF NEW.status = 'tone_map_running' THEN
            NEW.status := 'running';
        ELSIF NEW.status = 'tone_map_ready' THEN
            NEW.status := 'ready';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

ALTER TABLE public.download_artifacts
    DROP COLUMN audio_recipe_version,
    DROP CONSTRAINT download_artifacts_status_check,
    ADD CONSTRAINT download_artifacts_status_check
        CHECK (status IN ('queued', 'running', 'tone_map_queued', 'tone_map_running', 'tone_map_ready', 'ready', 'failed'))
        NOT VALID;

CREATE TRIGGER download_artifacts_tone_map_worker_status
BEFORE INSERT OR UPDATE OF status, tone_map_mode ON public.download_artifacts
FOR EACH ROW
EXECUTE FUNCTION public.fence_tone_map_artifact_worker_status();

ALTER TABLE public.download_artifacts
    VALIDATE CONSTRAINT download_artifacts_status_check;

CREATE INDEX CONCURRENTLY download_artifacts_lease_idx ON public.download_artifacts (lease_expires_at)
    WHERE status IN ('running', 'tone_map_running');
CREATE INDEX CONCURRENTLY download_artifacts_lru_idx ON public.download_artifacts (last_used_at)
    WHERE status IN ('ready', 'tone_map_ready');
