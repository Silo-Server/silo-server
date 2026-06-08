-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS scan_runs (
    id TEXT PRIMARY KEY,
    media_folder_id INTEGER NOT NULL REFERENCES media_folders(id) ON DELETE CASCADE,
    mode TEXT NOT NULL,
    path TEXT NOT NULL DEFAULT '',
    trigger TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    result_payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    error_message TEXT NOT NULL DEFAULT '',
    requested_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at TIMESTAMPTZ NULL,
    completed_at TIMESTAMPTZ NULL,
    heartbeat_at TIMESTAMPTZ NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT scan_runs_mode_check CHECK (mode = ANY (ARRAY['library'::text, 'subtree'::text, 'file'::text])),
    CONSTRAINT scan_runs_status_check CHECK (status = ANY (ARRAY['accepted'::text, 'running'::text, 'completed'::text, 'failed'::text, 'cancelled'::text]))
);

CREATE INDEX IF NOT EXISTS idx_scan_runs_active_lookup
    ON scan_runs (status, requested_at, media_folder_id, mode, path);

CREATE INDEX IF NOT EXISTS idx_scan_runs_folder_requested
    ON scan_runs (media_folder_id, requested_at DESC);

CREATE UNIQUE INDEX IF NOT EXISTS idx_scan_runs_active_scope
    ON scan_runs (media_folder_id, mode, path)
    WHERE status = ANY (ARRAY['accepted'::text, 'running'::text]);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS scan_runs;
-- +goose StatementEnd
