-- +goose Up
-- Movie box sets: movies TMDB lists in the same collection (a franchise such
-- as Harry Potter) browse as one card in libraries that opt in. Off by
-- default; see internal/catalog/box_sets.go.
ALTER TABLE public.media_folders
    ADD COLUMN box_sets_enabled boolean NOT NULL DEFAULT false,
    ADD COLUMN box_set_min_movies integer NOT NULL DEFAULT 2,
    ADD COLUMN box_sets_in_search boolean NOT NULL DEFAULT true;

-- One row per TMDB collection a local movie belongs to. member_tmdb_ids is
-- the collection's movies in release order, which box sets list members in.
-- Artwork follows media_items: *_source_path is the provider URL, and
-- *_path is the cached copy's original key, or the provider URL while the
-- image is not cached.
CREATE TABLE public.tmdb_movie_collections (
    tmdb_collection_id bigint PRIMARY KEY,
    name text NOT NULL,
    overview text NOT NULL DEFAULT '',
    poster_path text NOT NULL DEFAULT '',
    poster_source_path text NOT NULL DEFAULT '',
    poster_thumbhash text NOT NULL DEFAULT '',
    backdrop_path text NOT NULL DEFAULT '',
    backdrop_source_path text NOT NULL DEFAULT '',
    backdrop_thumbhash text NOT NULL DEFAULT '',
    member_tmdb_ids bigint[] NOT NULL DEFAULT '{}',
    fetched_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- A replaced or deleted cached image is queued for deletion, as for
-- media_items (20260714120826_artwork_revision_gc.sql).
CREATE TRIGGER tmdb_movie_collections_artwork_revision_gc_update
AFTER UPDATE OF poster_path, backdrop_path ON public.tmdb_movie_collections
FOR EACH ROW
WHEN (OLD.poster_path IS DISTINCT FROM NEW.poster_path
   OR OLD.backdrop_path IS DISTINCT FROM NEW.backdrop_path)
EXECUTE FUNCTION public.queue_displaced_artwork_revision(
    'poster_path', 'poster', 'backdrop_path', 'backdrop'
);

CREATE TRIGGER tmdb_movie_collections_artwork_revision_gc_delete
AFTER DELETE ON public.tmdb_movie_collections
FOR EACH ROW EXECUTE FUNCTION public.queue_displaced_artwork_revision(
    'poster_path', 'poster', 'backdrop_path', 'backdrop'
);

-- The TMDB collection lookup of one movie. checked_tmdb_id is the movie's
-- tmdb_id when it was looked up, so a rematch to another movie is looked up
-- again. tmdb_collection_id is NULL for a movie in no collection.
CREATE TABLE public.media_item_tmdb_collections (
    content_id text PRIMARY KEY REFERENCES public.media_items(content_id) ON UPDATE CASCADE ON DELETE CASCADE,
    checked_tmdb_id bigint NOT NULL,
    tmdb_collection_id bigint,
    checked_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX media_item_tmdb_collections_collection_idx
    ON public.media_item_tmdb_collections (tmdb_collection_id)
    WHERE tmdb_collection_id IS NOT NULL;

-- +goose Down
DROP TABLE IF EXISTS public.media_item_tmdb_collections;
DROP TRIGGER IF EXISTS tmdb_movie_collections_artwork_revision_gc_delete ON public.tmdb_movie_collections;
DROP TRIGGER IF EXISTS tmdb_movie_collections_artwork_revision_gc_update ON public.tmdb_movie_collections;
DROP TABLE IF EXISTS public.tmdb_movie_collections;
ALTER TABLE public.media_folders
    DROP COLUMN IF EXISTS box_sets_in_search,
    DROP COLUMN IF EXISTS box_set_min_movies,
    DROP COLUMN IF EXISTS box_sets_enabled;
