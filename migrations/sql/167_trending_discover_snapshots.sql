-- +goose Up
-- +goose StatementBegin
-- Persisted snapshot of external global trending (TMDB / Trakt) for the
-- trending_discover home section. One row per canonical (source, time_window):
-- content_ids are already resolved to library catalog content IDs and ordered
-- by trending rank. A background task refreshes these rows; the section read
-- path only reads them, so a slow or down provider never blocks the home page.
CREATE TABLE public.trending_discover_snapshots (
    source          text        NOT NULL,            -- 'tmdb' | 'trakt'
    time_window     text        NOT NULL,            -- 'day' | 'week' (trakt pinned to 'week')
    content_ids     text[]      NOT NULL DEFAULT '{}'::text[],
    entry_count     integer     NOT NULL DEFAULT 0,  -- raw provider entries fetched
    refreshed_at    timestamptz,                     -- last successful refresh
    last_attempt_at timestamptz,                     -- last attempt (success or failure)
    last_status     text        NOT NULL DEFAULT '', -- 'ok' | 'empty' | 'error'
    last_error      text        NOT NULL DEFAULT '',
    PRIMARY KEY (source, time_window)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.trending_discover_snapshots;
-- +goose StatementEnd
