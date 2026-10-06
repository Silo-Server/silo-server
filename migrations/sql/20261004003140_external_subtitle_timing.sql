-- +goose Up
-- The timing correction of a sidecar subtitle: a subtitle file next to the
-- media, which Silo never writes. A correction belongs to the sidecar's bytes,
-- so an edited or replaced sidecar loses it and a renamed one keeps it.
-- Delivery maps each original time t to t * timing_scale + timing_offset_ms.
CREATE TABLE external_subtitle_timings (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    media_file_id    INTEGER NOT NULL REFERENCES media_files (id) ON DELETE CASCADE,
    content_sha256   TEXT NOT NULL CHECK (content_sha256 ~ '^[0-9a-f]{64}$'),
    -- Where the sidecar was last seen; a sync job reads it from there.
    path             TEXT NOT NULL,
    format           TEXT NOT NULL,
    timing_offset_ms INTEGER NOT NULL DEFAULT 0
        CHECK (timing_offset_ms BETWEEN -600000 AND 600000),
    timing_scale     DOUBLE PRECISION NOT NULL DEFAULT 1
        CHECK (timing_scale BETWEEN 0.9 AND 1.1),
    revision         BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (media_file_id, content_sha256)
);

-- Only a timing write changes the revision that guards jobs and validators;
-- recording a new path for the same bytes does not.
-- +goose StatementBegin
CREATE FUNCTION bump_external_subtitle_timing_revision() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.revision := OLD.revision + 1;
    NEW.updated_at := now();
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER external_subtitle_timings_revision
    BEFORE UPDATE OF timing_offset_ms, timing_scale ON external_subtitle_timings
    FOR EACH ROW EXECUTE FUNCTION bump_external_subtitle_timing_revision();

-- A sync job aligns either a stored subtitle or a sidecar.
ALTER TABLE subtitle_sync_jobs
    ALTER COLUMN subtitle_id DROP NOT NULL,
    ADD COLUMN external_timing_id BIGINT REFERENCES external_subtitle_timings (id) ON DELETE CASCADE,
    ADD CONSTRAINT subtitle_sync_jobs_one_subject CHECK (num_nonnulls(subtitle_id, external_timing_id) = 1);

-- At most one active job per sidecar, and its latest job.
CREATE UNIQUE INDEX subtitle_sync_jobs_active_external_idx
    ON subtitle_sync_jobs (external_timing_id)
    WHERE status IN ('pending', 'running');
CREATE INDEX subtitle_sync_jobs_external_idx
    ON subtitle_sync_jobs (external_timing_id, id DESC)
    WHERE external_timing_id IS NOT NULL;

-- A replaced media file also loses its sidecar corrections; their jobs
-- cascade.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION reset_subtitle_timing_on_file_replace() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE downloaded_subtitles
    SET timing_offset_ms = 0, timing_scale = 1
    WHERE media_file_id = NEW.id AND (timing_offset_ms <> 0 OR timing_scale <> 1);
    DELETE FROM external_subtitle_timings WHERE media_file_id = NEW.id;
    DELETE FROM subtitle_sync_jobs WHERE media_file_id = NEW.id;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION reset_subtitle_timing_on_file_replace() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE downloaded_subtitles
    SET timing_offset_ms = 0, timing_scale = 1
    WHERE media_file_id = NEW.id AND (timing_offset_ms <> 0 OR timing_scale <> 1);
    DELETE FROM subtitle_sync_jobs WHERE media_file_id = NEW.id;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
DELETE FROM subtitle_sync_jobs WHERE external_timing_id IS NOT NULL;
DROP INDEX subtitle_sync_jobs_external_idx;
DROP INDEX subtitle_sync_jobs_active_external_idx;
ALTER TABLE subtitle_sync_jobs
    DROP CONSTRAINT subtitle_sync_jobs_one_subject,
    DROP COLUMN external_timing_id,
    ALTER COLUMN subtitle_id SET NOT NULL;
DROP TABLE external_subtitle_timings;
DROP FUNCTION bump_external_subtitle_timing_revision();
