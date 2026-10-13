-- +goose Up
-- A stored subtitle's timing correction. Bytes stay immutable; delivery maps
-- each original time t to t * timing_scale + timing_offset_ms. Updating these
-- columns goes through the existing revision trigger, so captured validators
-- go stale when the timing changes.
ALTER TABLE downloaded_subtitles
    ADD COLUMN timing_offset_ms INTEGER NOT NULL DEFAULT 0
        CHECK (timing_offset_ms BETWEEN -600000 AND 600000),
    ADD COLUMN timing_scale DOUBLE PRECISION NOT NULL DEFAULT 1
        CHECK (timing_scale BETWEEN 0.9 AND 1.1);

-- One row per sync attempt. Heartbeats live here rather than on the subtitle
-- row, whose every update bumps its revision.
CREATE TABLE subtitle_sync_jobs (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    subtitle_id      INTEGER NOT NULL REFERENCES downloaded_subtitles (id) ON DELETE CASCADE,
    media_file_id    INTEGER NOT NULL,
    requested_by     INTEGER,
    trigger          TEXT NOT NULL CHECK (trigger IN ('auto', 'manual')),
    -- The subtitle revision the job aligned against; the result applies only
    -- while the row still has it.
    base_revision    BIGINT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'running', 'synced', 'already_synced', 'no_match', 'failed')),
    confidence       DOUBLE PRECISION,
    result_offset_ms INTEGER,
    result_scale     DOUBLE PRECISION,
    executed_on      TEXT NOT NULL DEFAULT '',
    error            TEXT NOT NULL DEFAULT '',
    heartbeat_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at      TIMESTAMPTZ
);

-- At most one active job per subtitle.
CREATE UNIQUE INDEX subtitle_sync_jobs_active_idx
    ON subtitle_sync_jobs (subtitle_id)
    WHERE status IN ('pending', 'running');

-- Latest job of a subtitle (projections) and of a file's subtitles (lists).
CREATE INDEX subtitle_sync_jobs_subtitle_idx ON subtitle_sync_jobs (subtitle_id, id DESC);
CREATE INDEX subtitle_sync_jobs_media_file_idx ON subtitle_sync_jobs (media_file_id, id DESC);

-- Stale-job reaping.
CREATE INDEX subtitle_sync_jobs_active_heartbeat_idx
    ON subtitle_sync_jobs (heartbeat_at)
    WHERE status IN ('pending', 'running');

-- +goose Down
DROP TABLE subtitle_sync_jobs;
ALTER TABLE downloaded_subtitles DROP COLUMN timing_scale, DROP COLUMN timing_offset_ms;
