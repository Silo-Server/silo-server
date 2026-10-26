-- +goose Up
-- The titles an AniDB-based anime list names, by the IDs TMDB also reports:
-- TVDB series IDs and IMDb IDs. Request routing counts a listed title as
-- anime. A scheduled task replaces the rows from the published list; the
-- request path only reads them.
CREATE TABLE anime_ids (
    source text NOT NULL CONSTRAINT anime_ids_source_check CHECK (source IN ('tvdb', 'imdb')),
    external_id text NOT NULL,
    PRIMARY KEY (source, external_id)
);

-- One row: when the list was last replaced, and the claim a refresh holds so
-- only one server downloads it at a time. A failed refresh keeps the rows.
CREATE TABLE anime_ids_refresh (
    id boolean PRIMARY KEY DEFAULT true CONSTRAINT anime_ids_refresh_one_row CHECK (id),
    refreshed_at timestamp with time zone,
    last_attempt_at timestamp with time zone,
    last_status text NOT NULL DEFAULT '',
    last_error text NOT NULL DEFAULT '',
    etag text NOT NULL DEFAULT '',
    entry_count integer NOT NULL DEFAULT 0
);

-- +goose Down
DROP TABLE anime_ids_refresh;
DROP TABLE anime_ids;
