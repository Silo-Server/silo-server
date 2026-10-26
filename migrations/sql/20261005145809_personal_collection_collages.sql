-- +goose Up
-- A personal collection without an uploaded or imported poster shows a collage
-- of its titles' posters, chosen per viewer as a server collection's is
-- (library_collection_poster_variants): the first titles the viewer can see,
-- and for another profile's shared collection only those its owner can see
-- too. One collection can have several collages, one per distinct set of
-- source posters, named by a hash of those posters. Rows are touched at most
-- daily while in use and deleted with their collection or after going unused.
CREATE TABLE public.user_personal_collection_poster_variants (
    user_id integer NOT NULL,
    collection_id text NOT NULL,
    variant_key text NOT NULL,
    poster_path text NOT NULL,
    poster_thumbhash text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT NOW(),
    last_used_at timestamptz NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, collection_id, variant_key),
    FOREIGN KEY (user_id, collection_id)
        REFERENCES public.user_personal_collections (user_id, id) ON DELETE CASCADE
);

-- Every deleted collage row hands its objects to the artwork revision
-- collector in the same transaction, as server collection collages do: an
-- unused collage retired by the server, and every collage of a deleted
-- collection or account (ON DELETE CASCADE).
CREATE TRIGGER user_personal_collection_poster_variants_queue_deleted
AFTER DELETE ON public.user_personal_collection_poster_variants
FOR EACH ROW EXECUTE FUNCTION public.queue_deleted_collection_collage();

-- +goose Down
-- Dropping the table fires no row trigger, so queue its objects first.
SELECT public.queue_collection_poster_objects(poster_path)
FROM public.user_personal_collection_poster_variants;

DROP TABLE IF EXISTS public.user_personal_collection_poster_variants;
