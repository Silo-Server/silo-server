-- +goose Up
-- A server collection without an uploaded poster shows a collage of its
-- members' posters. The collage is chosen per viewer from the first members
-- that viewer can access (#1618), so one collection can have several: one per
-- distinct set of source posters, named by a hash of those posters. Rows are
-- touched at most daily while in use and deleted with their collection or
-- after going unused.
CREATE TABLE public.library_collection_poster_variants (
    collection_id text NOT NULL REFERENCES public.library_collections(id) ON DELETE CASCADE,
    variant_key text NOT NULL,
    poster_path text NOT NULL,
    poster_thumbhash text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT NOW(),
    last_used_at timestamptz NOT NULL DEFAULT NOW(),
    PRIMARY KEY (collection_id, variant_key)
);

-- Queue a stored collection poster's objects for the artwork revision
-- collector, as the artwork displacement trigger does for catalog artwork. The
-- collector deletes them after the grace period only while no artwork surface
-- names the path. A path queued again starts over, including one whose objects
-- the collector already deleted: a collage build reserves its path this way
-- before uploading, so the objects are deleted again if the build fails.
-- Collection posters are stored as original, w500 and w300 under one revision
-- (catalog.CollectionPosterWidths); change both together.
-- +goose StatementBegin
CREATE FUNCTION public.queue_collection_poster_objects(p_path text)
RETURNS void
LANGUAGE sql
AS $$
    INSERT INTO public.artwork_revision_gc_candidates (
        original_path, image_type, object_keys, not_before, next_attempt_at
    )
    SELECT p_path,
           'poster',
           ARRAY[
               p_path,
               regexp_replace(p_path, '/original(\.[^/]+)$', '/w500\1'),
               regexp_replace(p_path, '/original(\.[^/]+)$', '/w300\1')
           ],
           NOW() + interval '24 hours',
           NOW() + interval '24 hours'
    WHERE p_path NOT LIKE '%://%'
      AND p_path ~ '/original\.[^/]+$'
    ON CONFLICT (original_path) DO UPDATE SET
        object_keys = EXCLUDED.object_keys,
        not_before = EXCLUDED.not_before,
        next_attempt_at = EXCLUDED.next_attempt_at,
        attempt_count = 0,
        locked_at = NULL,
        locked_by = '',
        last_error = '',
        deleted_at = NULL,
        updated_at = NOW();
$$;
-- +goose StatementEnd

-- Every deleted collage row hands its objects to the collector in the same
-- transaction: an unused collage retired by the server and every collage of a
-- deleted collection (ON DELETE CASCADE) alike, so no failure between the
-- delete and a storage call can orphan them.
-- +goose StatementBegin
CREATE FUNCTION public.queue_deleted_collection_collage()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM public.queue_collection_poster_objects(OLD.poster_path);
    RETURN OLD;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER library_collection_poster_variants_queue_deleted
AFTER DELETE ON public.library_collection_poster_variants
FOR EACH ROW EXECUTE FUNCTION public.queue_deleted_collection_collage();

-- library_collections.poster_url now holds only uploaded and template posters.
-- The single collage it held was built from members regardless of viewer and
-- may show titles a restricted profile can't access, so it is cleared rather
-- than served, and its objects go to the collector.
SELECT public.queue_collection_poster_objects(poster_url)
FROM public.library_collections
WHERE poster_auto_generated;

UPDATE public.library_collections
SET poster_url = '',
    poster_thumbhash = '',
    poster_auto_generated = FALSE,
    updated_at = NOW()
WHERE poster_auto_generated;

-- +goose Down
DROP TABLE IF EXISTS public.library_collection_poster_variants;
DROP FUNCTION IF EXISTS public.queue_deleted_collection_collage();
DROP FUNCTION IF EXISTS public.queue_collection_poster_objects(text);
