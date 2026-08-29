-- +goose NO TRANSACTION

-- +goose Up
ALTER TABLE public.media_files
    ADD COLUMN IF NOT EXISTS first_seen_scan_run_id text NULL;

ALTER TABLE public.episode_libraries
    ADD COLUMN IF NOT EXISTS first_seen_scan_run_id text NULL;

-- Add the foreign keys without scanning the populated availability tables
-- under a write-blocking lock. Validation below uses the lighter lock mode.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'public.media_files'::regclass
          AND conname = 'media_files_first_seen_scan_run_id_fkey'
    ) THEN
        ALTER TABLE public.media_files
            ADD CONSTRAINT media_files_first_seen_scan_run_id_fkey
            FOREIGN KEY (first_seen_scan_run_id)
            REFERENCES public.scan_runs(id) ON DELETE SET NULL
            NOT VALID;
    END IF;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'public.episode_libraries'::regclass
          AND conname = 'episode_libraries_first_seen_scan_run_id_fkey'
    ) THEN
        ALTER TABLE public.episode_libraries
            ADD CONSTRAINT episode_libraries_first_seen_scan_run_id_fkey
            FOREIGN KEY (first_seen_scan_run_id)
            REFERENCES public.scan_runs(id) ON DELETE SET NULL
            NOT VALID;
    END IF;
END;
$$;
-- +goose StatementEnd

-- A failed concurrent build can leave an invalid index. Keep retry cleanup
-- outside a transaction so it cannot take a blocking ordinary index-drop lock.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_files_first_seen_scan_run_id;

CREATE INDEX CONCURRENTLY idx_media_files_first_seen_scan_run_id
    ON public.media_files (first_seen_scan_run_id)
    WHERE first_seen_scan_run_id IS NOT NULL;

DROP INDEX CONCURRENTLY IF EXISTS public.idx_episode_libraries_first_seen_scan_run_id;

CREATE INDEX CONCURRENTLY idx_episode_libraries_first_seen_scan_run_id
    ON public.episode_libraries (first_seen_scan_run_id)
    WHERE first_seen_scan_run_id IS NOT NULL;

ALTER TABLE public.media_files
    VALIDATE CONSTRAINT media_files_first_seen_scan_run_id_fkey;

ALTER TABLE public.episode_libraries
    VALIDATE CONSTRAINT episode_libraries_first_seen_scan_run_id_fkey;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_episode_libraries_first_seen_scan_run_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_files_first_seen_scan_run_id;

ALTER TABLE public.episode_libraries
    DROP COLUMN IF EXISTS first_seen_scan_run_id;

ALTER TABLE public.media_files
    DROP COLUMN IF EXISTS first_seen_scan_run_id;
