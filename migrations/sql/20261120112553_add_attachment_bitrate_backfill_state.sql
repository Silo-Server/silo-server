-- +goose Up
-- Progress of the one-off pass that recomputes the stored bitrate of MKV
-- files scanned while Matroska attachments (embedded fonts) still counted
-- toward it. Scans since store the corrected rate, so the pass covers only
-- rows that existed before, once. A single row: last_media_file_id is the
-- resume cursor after a restart or a stalled-storage stop,
-- retry_media_file_ids lists the files the pass could not read, which later
-- runs read again starting after retry_after_id, and completed_at marks the
-- cursor's walk finished. The pass is done once the walk is finished and the
-- retry list is empty.
CREATE TABLE attachment_bitrate_backfill_state (
    id                   SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    last_media_file_id   BIGINT      NOT NULL DEFAULT 0,
    retry_media_file_ids BIGINT[]    NOT NULL DEFAULT '{}',
    retry_after_id       BIGINT      NOT NULL DEFAULT 0,
    completed_at         TIMESTAMPTZ,
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO attachment_bitrate_backfill_state (id) VALUES (1);

-- +goose Down
DROP TABLE attachment_bitrate_backfill_state;
