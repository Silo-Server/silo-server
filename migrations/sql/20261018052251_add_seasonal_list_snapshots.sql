-- +goose Up
-- +goose StatementBegin
-- Persisted copies of the MDBList lists behind seasonal_themed home rows, one
-- row per canonical list URL. entries keeps the list's external IDs in IMDb
-- popularity order; the section read path resolves them against the catalog,
-- so titles added to the library show up without waiting for a refresh and no
-- content_id is stored here. A background task refreshes the lists a row will
-- need soon; a failed refresh keeps the last good entries.
CREATE TABLE public.seasonal_list_snapshots (
    list_url        text        PRIMARY KEY,
    entries         jsonb       NOT NULL DEFAULT '[]'::jsonb,
    entry_count     integer     NOT NULL DEFAULT 0,
    refreshed_at    timestamptz,
    last_attempt_at timestamptz,
    last_status     text        NOT NULL DEFAULT '',  -- 'refreshing' | 'ok' | 'empty' | 'error'
    last_error      text        NOT NULL DEFAULT ''
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.seasonal_list_snapshots;
-- +goose StatementEnd
