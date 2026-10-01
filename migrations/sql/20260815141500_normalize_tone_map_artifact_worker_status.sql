-- +goose NO TRANSACTION

-- +goose Up
-- Follow-up to 20260815135416_fence_tone_map_artifact_workers.sql (already
-- applied): that migration mapped plain statuses forward to tone_map_* only,
-- so a write that clears tone_map_mode left the row stuck in a prefixed
-- status. This migration replaces the fence function with the bidirectional
-- normalization and re-shapes the status constraint for large tables — the
-- expanded CHECK is added NOT VALID, the tone-map status backfill runs before
-- validation, and the partial indexes are rebuilt concurrently outside a
-- transaction.

DROP INDEX CONCURRENTLY IF EXISTS public.download_artifacts_lease_idx;
DROP INDEX CONCURRENTLY IF EXISTS public.download_artifacts_lru_idx;

ALTER TABLE public.download_artifacts
    DROP CONSTRAINT download_artifacts_status_check,
    ADD CONSTRAINT download_artifacts_status_check
        CHECK (status IN ('queued','running','tone_map_queued','tone_map_running','tone_map_ready','ready','failed'))
        NOT VALID;

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

-- The trigger created by 20260815135416 still fires on
-- UPDATE OF status, tone_map_mode; it picks up the replaced function body.
-- Idempotent backfills: rows already normalized by the original migration are
-- untouched, and rows left prefixed with a cleared tone_map_mode are swept
-- back to the plain statuses the new trigger's reverse branch enforces.
UPDATE public.download_artifacts
SET status = CASE status
    WHEN 'queued' THEN 'tone_map_queued'
    WHEN 'running' THEN 'tone_map_running'
    WHEN 'ready' THEN 'tone_map_ready'
    ELSE status
END
WHERE tone_map_mode <> '' AND status IN ('queued', 'running', 'ready');

UPDATE public.download_artifacts
SET status = CASE status
    WHEN 'tone_map_queued' THEN 'queued'
    WHEN 'tone_map_running' THEN 'running'
    WHEN 'tone_map_ready' THEN 'ready'
    ELSE status
END
WHERE tone_map_mode = '' AND status IN ('tone_map_queued', 'tone_map_running', 'tone_map_ready');

ALTER TABLE public.download_artifacts
    VALIDATE CONSTRAINT download_artifacts_status_check;

CREATE INDEX CONCURRENTLY download_artifacts_lease_idx ON public.download_artifacts (lease_expires_at)
    WHERE status IN ('running', 'tone_map_running');
CREATE INDEX CONCURRENTLY download_artifacts_lru_idx ON public.download_artifacts (last_used_at)
    WHERE status IN ('ready', 'tone_map_ready');

-- +goose Down
-- Restore the forward-only fence from 20260815135416; the constraint and
-- indexes are left in the state the original migration established.
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
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
