-- +goose Up
-- A smart server collection's collage comes from its query's first matches,
-- which are too costly to read on every list read. A background refresh reads
-- them and records here which stored collage each viewer access sees, so a
-- list read finds it by key alone. variant_key is NULL when that viewer can
-- see no match with a poster. A row goes with its collection, and with its
-- collage when the server retires that collage as unused.
CREATE TABLE public.library_collection_smart_collages (
    collection_id text NOT NULL REFERENCES public.library_collections (id) ON DELETE CASCADE,
    -- A hash of the viewer's access filter.
    access_key text NOT NULL,
    -- A hash of what decides the collection's matches and their order.
    definition_key text NOT NULL,
    variant_key text,
    refreshed_at timestamptz NOT NULL DEFAULT NOW(),
    PRIMARY KEY (collection_id, access_key),
    FOREIGN KEY (collection_id, variant_key)
        REFERENCES public.library_collection_poster_variants (collection_id, variant_key) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE IF EXISTS public.library_collection_smart_collages;
