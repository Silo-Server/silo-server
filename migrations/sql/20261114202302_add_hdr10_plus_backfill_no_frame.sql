-- +goose Up
-- One row per media file whose HDR10+ backfill frame read last decoded no
-- frame, keyed to the file revision it read: its size and mtime. The backfill
-- skips such a file for a day instead of reading it again on every pass. A
-- changed file no longer matches its row, so it is read on the next pass.
-- The row holds only when the read happened, never an HDR10+ status, and it
-- lives outside media_files.video_tracks so the bookkeeping does not count as
-- a change to the file.
CREATE TABLE hdr10_plus_backfill_no_frame (
    media_file_id    INTEGER PRIMARY KEY REFERENCES media_files (id) ON DELETE CASCADE,
    file_size        BIGINT NOT NULL,
    file_modified_at TIMESTAMPTZ,
    read_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE hdr10_plus_backfill_no_frame;
