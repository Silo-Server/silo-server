-- +goose Up
-- +goose StatementBegin

-- Per-library real-time monitoring switch. On for existing and new libraries;
-- it takes effect only while the server-wide scanner.realtime_monitoring
-- setting is on and the library is enabled.
ALTER TABLE public.media_folders
    ADD COLUMN realtime_monitoring boolean NOT NULL DEFAULT true;

-- Real-time monitoring status, one row per library per server node that can
-- see at least one of the library's folders. A node upserts its row when the
-- state changes and refreshes it periodically; readers ignore stale rows.
-- Settings-derived states (server switch off, library switch off, library
-- disabled) are computed at read time and never stored here. backend is
-- 'inotify', 'fanotify', or empty.
CREATE TABLE public.library_monitor_status (
    node_id text NOT NULL,
    library_id integer NOT NULL
        REFERENCES public.media_folders(id) ON DELETE CASCADE,
    state text NOT NULL,
    backend text NOT NULL DEFAULT '',
    detail text NOT NULL DEFAULT '',
    directories integer NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (node_id, library_id)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.library_monitor_status;

ALTER TABLE public.media_folders
    DROP COLUMN IF EXISTS realtime_monitoring;
-- +goose StatementEnd
