-- +goose Up
-- What a running sync job is doing and how far it got, and why a failed one
-- failed, for clients to show. Progress writes touch only the job row, so they
-- never change a subtitle's revision.
ALTER TABLE subtitle_sync_jobs
    ADD COLUMN phase TEXT NOT NULL DEFAULT ''
        CHECK (phase IN ('', 'analyzing', 'matching')),
    ADD COLUMN progress DOUBLE PRECISION
        CHECK (progress BETWEEN 0 AND 1),
    ADD COLUMN failure TEXT NOT NULL DEFAULT ''
        CHECK (failure IN ('', 'subtitle_changed', 'no_audio', 'unavailable', 'error'));

-- +goose Down
ALTER TABLE subtitle_sync_jobs DROP COLUMN failure, DROP COLUMN progress, DROP COLUMN phase;
