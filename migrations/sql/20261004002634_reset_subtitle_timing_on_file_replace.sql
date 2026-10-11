-- +goose Up
-- A timing correction is measured against one release's audio. When the file
-- at a path is replaced (a new hash or size; an unknown value on either side
-- proves nothing), its subtitles' corrections and sync history no longer
-- apply, so they are removed. Deleting the jobs also stops one in flight:
-- a job applies its result and finishes in one transaction, which fails when
-- the job row is gone.
-- +goose StatementBegin
CREATE FUNCTION reset_subtitle_timing_on_file_replace() RETURNS trigger
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
CREATE TRIGGER media_files_reset_subtitle_timing
    AFTER UPDATE OF file_hash, file_size ON media_files
    FOR EACH ROW
    WHEN (
        (NULLIF(OLD.file_hash, '') IS NOT NULL AND NULLIF(NEW.file_hash, '') IS NOT NULL
            AND OLD.file_hash <> NEW.file_hash)
        OR (OLD.file_size > 0 AND NEW.file_size > 0 AND OLD.file_size <> NEW.file_size)
    )
    EXECUTE FUNCTION reset_subtitle_timing_on_file_replace();

-- +goose Down
DROP TRIGGER media_files_reset_subtitle_timing ON media_files;
DROP FUNCTION reset_subtitle_timing_on_file_replace();
