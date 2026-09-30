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

-- library_collections.poster_url now holds only uploaded and template posters.
-- The single collage it held was built from members regardless of viewer and
-- may show titles a restricted profile can't access, so it is cleared rather
-- than served. Its objects go to the artwork revision collector, which deletes
-- them after the grace period once nothing references them. Collection
-- collages are stored as original, w500 and w300 under the same revision.
INSERT INTO public.artwork_revision_gc_candidates (
    original_path, image_type, object_keys, not_before, next_attempt_at
)
SELECT poster_url,
       'poster',
       ARRAY[
           poster_url,
           regexp_replace(poster_url, '/original(\.[^/]+)$', '/w500\1'),
           regexp_replace(poster_url, '/original(\.[^/]+)$', '/w300\1')
       ],
       NOW() + interval '24 hours',
       NOW() + interval '24 hours'
FROM public.library_collections
WHERE poster_auto_generated
  AND poster_url NOT LIKE '%://%'
  AND poster_url ~ '/original\.[^/]+$'
ON CONFLICT (original_path) DO NOTHING;

UPDATE public.library_collections
SET poster_url = '',
    poster_thumbhash = '',
    poster_auto_generated = FALSE,
    updated_at = NOW()
WHERE poster_auto_generated;

-- +goose Down
DROP TABLE IF EXISTS public.library_collection_poster_variants;
