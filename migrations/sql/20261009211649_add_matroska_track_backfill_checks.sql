-- +goose Up
-- One row per media file the Matroska track number backfill has read, keyed
-- to the row revision it read: the file's size and mtime, and probe_md5, a
-- hash of the stored video, audio and subtitle tracks the match reads. A file
-- is read again only when one of those changes, or when matcher_version shows
-- the matching rules have changed since. Files the backfill could not match
-- no longer cost a read on every startup.
CREATE TABLE matroska_track_backfill_checks (
    media_file_id    INTEGER PRIMARY KEY REFERENCES media_files (id) ON DELETE CASCADE,
    matcher_version  INTEGER NOT NULL,
    file_size        BIGINT NOT NULL,
    file_modified_at TIMESTAMPTZ,
    probe_md5        TEXT NOT NULL,
    checked_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE matroska_track_backfill_checks;
