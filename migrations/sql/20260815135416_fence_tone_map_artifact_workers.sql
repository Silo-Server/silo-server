-- +goose Up
DROP INDEX IF EXISTS public.download_artifacts_lease_idx;
DROP INDEX IF EXISTS public.download_artifacts_lru_idx;

ALTER TABLE public.download_artifacts
    DROP CONSTRAINT download_artifacts_status_check,
    ADD CONSTRAINT download_artifacts_status_check
        CHECK (status IN ('queued','running','tone_map_queued','tone_map_running','tone_map_ready','ready','failed'));

-- +goose StatementBegin
CREATE FUNCTION public.fence_tone_map_artifact_worker_status()
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
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER download_artifacts_tone_map_worker_status
BEFORE INSERT OR UPDATE OF status, tone_map_mode ON public.download_artifacts
FOR EACH ROW
EXECUTE FUNCTION public.fence_tone_map_artifact_worker_status();

UPDATE public.download_artifacts
SET status = CASE status
    WHEN 'queued' THEN 'tone_map_queued'
    WHEN 'running' THEN 'tone_map_running'
    WHEN 'ready' THEN 'tone_map_ready'
    ELSE status
END
WHERE tone_map_mode <> '' AND status IN ('queued', 'running', 'ready');

CREATE INDEX download_artifacts_lease_idx ON public.download_artifacts (lease_expires_at)
    WHERE status IN ('running', 'tone_map_running');
CREATE INDEX download_artifacts_lru_idx ON public.download_artifacts (last_used_at)
    WHERE status IN ('ready', 'tone_map_ready');

-- +goose Down
DROP TRIGGER IF EXISTS download_artifacts_tone_map_worker_status ON public.download_artifacts;
DROP FUNCTION IF EXISTS public.fence_tone_map_artifact_worker_status();

UPDATE public.download_artifacts
SET status = CASE status
    WHEN 'tone_map_queued' THEN 'queued'
    WHEN 'tone_map_running' THEN 'running'
    WHEN 'tone_map_ready' THEN 'ready'
    ELSE status
END
WHERE status IN ('tone_map_queued', 'tone_map_running', 'tone_map_ready');

DROP INDEX IF EXISTS public.download_artifacts_lease_idx;
DROP INDEX IF EXISTS public.download_artifacts_lru_idx;

ALTER TABLE public.download_artifacts
    DROP CONSTRAINT download_artifacts_status_check,
    ADD CONSTRAINT download_artifacts_status_check
        CHECK (status IN ('queued','running','ready','failed'));

CREATE INDEX download_artifacts_lease_idx ON public.download_artifacts (lease_expires_at)
    WHERE status = 'running';
CREATE INDEX download_artifacts_lru_idx ON public.download_artifacts (last_used_at)
    WHERE status = 'ready';
