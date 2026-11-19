-- +goose Up
-- +goose StatementBegin
-- Outside titles: movies and series the library does not have, which
-- personal recommendations on Requests Discover can name. They are keyed by
-- TMDB ID and media type ('movie' or 'series', Silo's names) and hold the
-- card fields a read serves without calling TMDB. A title gets a row only
-- after its TMDB detail call succeeded; titles the library has never get one.
CREATE TABLE public.external_titles (
    media_type text NOT NULL CHECK (media_type IN ('movie', 'series')),
    tmdb_id integer NOT NULL,
    -- The title's other IDs from its TMDB detail, so a read can tell that the
    -- library has it under one of them without asking TMDB. Empty or 0 when
    -- TMDB has none.
    imdb_id text NOT NULL DEFAULT '',
    tvdb_id integer NOT NULL DEFAULT 0,
    title text NOT NULL,
    overview text NOT NULL DEFAULT '',
    poster_path text NOT NULL DEFAULT '',
    backdrop_path text NOT NULL DEFAULT '',
    -- As TMDB gives it: YYYY-MM-DD, or empty.
    release_date text NOT NULL DEFAULT '',
    original_language text NOT NULL DEFAULT '',
    popularity double precision NOT NULL DEFAULT 0,
    vote_average double precision NOT NULL DEFAULT 0,
    vote_count integer NOT NULL DEFAULT 0,
    -- Empty when TMDB has no US certification; reads fail closed on it.
    us_certification text NOT NULL DEFAULT '',
    detail_fetched_at timestamptz,
    last_referenced_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (media_type, tmdb_id)
);

-- Vectors of outside titles, in the same embedding space as
-- media_item_embeddings (zero-padded to 3072, same lock). No ANN index: the
-- pool is a few thousand rows and ranking scans it exactly.
CREATE TABLE public.external_title_embeddings (
    media_type text NOT NULL,
    tmdb_id integer NOT NULL,
    embedding public.vector(3072) NOT NULL,
    model text NOT NULL,
    canonical_text text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (media_type, tmdb_id),
    FOREIGN KEY (media_type, tmdb_id)
        REFERENCES public.external_titles (media_type, tmdb_id) ON DELETE CASCADE
);

-- One row per thing the pool is built from: a seed title's TMDB lists
-- ('seed:movie:603') or a shared set ('set:recent:movie',
-- 'set:lang:hi:top:series'). An 'empty' result is kept so it is not fetched
-- again before it ages out; a 'failed' one keeps the links it had.
CREATE TABLE public.external_title_sources (
    source_key text PRIMARY KEY,
    status text NOT NULL CHECK (status IN ('ok', 'empty', 'failed')),
    error text NOT NULL DEFAULT '',
    fetched_at timestamptz,
    last_referenced_at timestamptz NOT NULL DEFAULT now()
);

-- The titles a source names, in TMDB's order. A link may name a title the
-- library has, or one without a detail yet, so there is no foreign key to
-- external_titles.
CREATE TABLE public.external_title_links (
    source_key text NOT NULL
        REFERENCES public.external_title_sources (source_key) ON DELETE CASCADE,
    list text NOT NULL CHECK (list IN ('recommendations', 'similar', 'set')),
    rank integer NOT NULL,
    media_type text NOT NULL CHECK (media_type IN ('movie', 'series')),
    tmdb_id integer NOT NULL,
    PRIMARY KEY (source_key, list, rank)
);
CREATE INDEX idx_external_title_links_title
    ON public.external_title_links (media_type, tmdb_id);

-- A profile's ranked outside titles per media type, written whole by the
-- background build: items is [{tmdb_id, score, reasons: [content_id, ...]}].
-- Profiles may live in the SQLite user store, so there is no profile foreign
-- key; deleting a profile removes its rows explicitly, as for
-- recommendation_cache.
CREATE TABLE public.external_recommendation_rows (
    user_id integer NOT NULL REFERENCES public.users (id) ON DELETE CASCADE,
    profile_id text NOT NULL,
    media_type text NOT NULL CHECK (media_type IN ('movie', 'series')),
    items jsonb NOT NULL,
    embedding_model text NOT NULL,
    built_at timestamptz NOT NULL,
    PRIMARY KEY (user_id, profile_id, media_type)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.external_recommendation_rows;
DROP TABLE IF EXISTS public.external_title_links;
DROP TABLE IF EXISTS public.external_title_sources;
DROP TABLE IF EXISTS public.external_title_embeddings;
DROP TABLE IF EXISTS public.external_titles;
-- +goose StatementEnd
