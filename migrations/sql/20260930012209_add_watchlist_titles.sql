-- +goose Up
-- +goose StatementBegin
-- A movie or series some profile has watchlisted before the server had it.
-- One row per title, shared by every profile that added it, so the title's
-- provider IDs are checked once for all of them. No column stores or is named
-- like a catalog content ID: re-anchoring, merging and deleting catalog items
-- never reach these tables. The id is minted by idgen and never exposed.
CREATE TABLE public.watchlist_titles (
    id bigint PRIMARY KEY,
    media_type text NOT NULL CHECK (media_type IN ('movie', 'series')),
    tmdb_id integer NOT NULL CHECK (tmdb_id > 0),
    imdb_id text NOT NULL DEFAULT '',
    tvdb_id integer,
    title text NOT NULL,
    year integer,
    release_date date,
    poster_path text NOT NULL DEFAULT '',
    -- US certification, the one Discover's rating ceiling filters on.
    certification text NOT NULL DEFAULT '',
    vote_average numeric(3,1),
    state text NOT NULL DEFAULT 'active'
        CHECK (state IN ('active', 'needs_review', 'removed')),
    -- Consecutive TMDB 404s; two at least a day apart confirm a deletion.
    not_found_count integer NOT NULL DEFAULT 0,
    last_not_found_at timestamptz,
    checked_at timestamptz,
    next_check_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Every provider ID a title has had. The key is how a duplicate is
-- recognized: two titles can never hold the same ID.
CREATE TABLE public.watchlist_title_aliases (
    title_id bigint NOT NULL REFERENCES public.watchlist_titles(id) ON DELETE CASCADE,
    media_type text NOT NULL,
    provider text NOT NULL CHECK (provider IN ('tmdb', 'imdb', 'tvdb')),
    provider_id text NOT NULL,
    PRIMARY KEY (media_type, provider, provider_id)
);
CREATE INDEX idx_watchlist_title_aliases_title ON public.watchlist_title_aliases (title_id);

-- A profile's entry. Profiles may live in the SQLite user store, so there is
-- no profile foreign key; deleting a profile removes its rows explicitly, as
-- for user_dropped_series.
CREATE TABLE public.user_watchlist_titles (
    user_id integer NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    profile_id text NOT NULL,
    title_id bigint NOT NULL REFERENCES public.watchlist_titles(id) ON DELETE CASCADE,
    added_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, profile_id, title_id)
);
CREATE INDEX idx_user_watchlist_titles_page
    ON public.user_watchlist_titles (user_id, profile_id, added_at DESC, title_id DESC);
CREATE INDEX idx_user_watchlist_titles_title ON public.user_watchlist_titles (title_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.user_watchlist_titles;
DROP TABLE IF EXISTS public.watchlist_title_aliases;
DROP TABLE IF EXISTS public.watchlist_titles;
-- +goose StatementEnd
