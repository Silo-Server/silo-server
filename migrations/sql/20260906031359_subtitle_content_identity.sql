-- +goose Up
-- Existing object keys contain only a truncated hash. Do not invent the full
-- digest from those keys; legacy content is verified lazily when encountered.
ALTER TABLE downloaded_subtitles
    ADD COLUMN content_sha256 TEXT CHECK (content_sha256 ~ '^[0-9a-f]{64}$'),
    ADD COLUMN revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0);
CREATE UNIQUE INDEX downloaded_subtitles_content_identity
    ON downloaded_subtitles (media_file_id, provider, language, format, content_sha256)
    WHERE content_sha256 IS NOT NULL;

-- Every writer invalidates captured revisions, including bridge and direct SQL updates.
-- +goose StatementBegin
CREATE FUNCTION bump_downloaded_subtitle_revision() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.revision := OLD.revision + 1;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER downloaded_subtitles_revision
    BEFORE UPDATE ON downloaded_subtitles
    FOR EACH ROW EXECUTE FUNCTION bump_downloaded_subtitle_revision();

-- +goose Down
DROP TRIGGER downloaded_subtitles_revision ON downloaded_subtitles;
DROP FUNCTION bump_downloaded_subtitle_revision();
DROP INDEX downloaded_subtitles_content_identity;
ALTER TABLE downloaded_subtitles DROP COLUMN revision, DROP COLUMN content_sha256;
