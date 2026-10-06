-- +goose Up
-- probe_failed_at records that the most recent local ffprobe ran to completion
-- and rejected the file (zero-byte, corrupt, or truncated), so playback can
-- tell a file that was never probed from one that cannot be read. A successful
-- probe clears it. Nullable with no default, so adding it is a catalog-only
-- change that does not rewrite media_files. Existing rows are not backfilled:
-- the next scan or playback attempt reprobes unprobed files and records the
-- outcome.
ALTER TABLE public.media_files
    ADD COLUMN IF NOT EXISTS probe_failed_at timestamptz NULL;

-- +goose Down
ALTER TABLE public.media_files
    DROP COLUMN IF EXISTS probe_failed_at;
